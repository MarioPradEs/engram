package triage

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// TriageStore is the subset of store.Store used by triage read handlers.
// Declaring a narrow interface keeps the handlers testable without a real SQLite file.
type TriageStore interface {
	ListProjectsWithStats() ([]store.ProjectStats, error)
	RecentObservations(project, scope string, limit int) ([]store.Observation, error)
}

// MutableTriageStore extends TriageStore with the mutation methods required by
// WU-5 handlers (toggle, bulk set-scope) and the tag-query methods added in
// PR#3 (E2b: bulk-by-tag). All methods proxy to store.Store without re-implementing
// store logic.
type MutableTriageStore interface {
	TriageStore
	// UpdateObservationScope sets the internal scope of a single observation.
	// internalScope must be one of the store's recognised values ("team", "personal", …).
	// Callers should convert via ToInternalScope before calling this method.
	UpdateObservationScope(id int64, internalScope string) error
	// ObservationsByTag returns observations in the given project whose tags_json
	// field matches the given facet/value pair. facet must be in {"juego","tipo"}.
	ObservationsByTag(project, facet, value string, limit int) ([]store.Observation, error)
	// DistinctTagValues returns the sorted, de-duplicated set of non-empty values
	// for the given facet across all observations in the project.
	// facet must be in {"juego","tipo"}.
	DistinctTagValues(project, facet string) ([]string, error)
}

const (
	// obsPerProjectLimit is the maximum observations loaded per project page (read view).
	obsPerProjectLimit = 200

	// obsBulkScopeLimit is a practical sentinel passed to RecentObservations by
	// handleSetProjectScope to bypass the 200-row read-view cap (obsPerProjectLimit)
	// and materialize ALL project rows into memory before bulk-updating them.
	// 10 million is far beyond any realistic local observation count, so the
	// store effectively returns every row. This is acceptable for a loopback
	// tool where the entire dataset lives on localhost; it is NOT a streaming
	// solution and should not be reused in contexts where dataset size is
	// unbounded or latency is user-facing.
	obsBulkScopeLimit = 10_000_000
)

// handleIndex renders the landing page grouped by project.
// GET /
//
// For each project it shows: name, observation count, and a per-project link.
// Only the cwd launch project shows a resolved default-scope badge (Option A,
// decision #939/#940: the store has no project filesystem paths, so other
// projects cannot resolve their config.json default_scope).
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	ts := s.triageStore
	if ts == nil {
		// No store injected (unit test or pre-init) — render empty page gracefully.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := ProjectsPage(nil, "", "personal", false)
		if err := TriageLayout("Projects", page).Render(context.Background(), w); err != nil {
			log.Printf("[triage] handleIndex render error: %v", err)
		}
		return
	}

	projects, err := ts.ListProjectsWithStats()
	if err != nil {
		log.Printf("[triage] handleIndex: ListProjectsWithStats error: %v", err)
		http.Error(w, "failed to load projects", http.StatusInternalServerError)
		return
	}

	// Stable ordering: by observation count desc, then by name asc for ties.
	// ListProjectsWithStats already sorts by count desc, but ties need name sort for golden determinism.
	sort.SliceStable(projects, func(i, j int) bool {
		if projects[i].ObservationCount != projects[j].ObservationCount {
			return projects[i].ObservationCount > projects[j].ObservationCount
		}
		return projects[i].Name < projects[j].Name
	})

	cwdProject := s.cwdProject
	cwdDefault := "personal"
	cwdHasExplicit := false
	if s.cwdDir != "" && cwdProject != "" {
		cwdDefault, cwdHasExplicit = ResolveDefaultScopeWithPresence(s.cwdDir)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := ProjectsPage(projects, cwdProject, cwdDefault, cwdHasExplicit)
	if err := TriageLayout("Projects", page).Render(context.Background(), w); err != nil {
		log.Printf("[triage] handleIndex render error: %v", err)
	}
}

// handleProject renders the per-project observation list.
// GET /project/{name}
//
// Observations are fetched via RecentObservations and displayed with
// their UI scope badges. Legacy "project"/"department" scope rows surface
// as "needs triage" badges.
func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	// Extract the project name from the URL path segment.
	// Go 1.22 net/http routing passes {name} via r.PathValue.
	rawName := r.PathValue("name")
	if rawName == "" {
		http.NotFound(w, r)
		return
	}
	projectName, err := url.PathUnescape(rawName)
	if err != nil || projectName == "" {
		http.NotFound(w, r)
		return
	}

	ts := s.triageStore
	if ts == nil {
		// No store — render empty project page for tests.
		page := ProjectListPage(projectName, nil, s.cwdProject)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := TriageLayout(projectName, page).Render(context.Background(), w); err != nil {
			log.Printf("[triage] handleProject render error: %v", err)
		}
		return
	}

	observations, err := ts.RecentObservations(projectName, "", obsPerProjectLimit)
	if err != nil {
		log.Printf("[triage] handleProject %q: RecentObservations error: %v", projectName, err)
		http.Error(w, "failed to load observations", http.StatusInternalServerError)
		return
	}

	// Build view models: apply UIScopeOf to each observation.
	rows := make([]ObservationRow, len(observations))
	for i, obs := range observations {
		uiScope, needsTriage := UIScopeOf(obs.Scope)
		rows[i] = ObservationRow{
			Obs:         obs,
			UIScope:     uiScope,
			NeedsTriage: needsTriage,
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Pass cwdProject so the template gates classify controls to the cwd project (W-1).
	page := ProjectListPage(projectName, rows, s.cwdProject)
	if err := TriageLayout(projectName, page).Render(context.Background(), w); err != nil {
		log.Printf("[triage] handleProject render error: %v", err)
	}
}

// handleToggleScope sets the scope of a single observation.
// POST /observations/{id}/scope
//
// Form fields:
//
//	scope — UI vocabulary: "shared" or "personal"
//
// Semantics (REQ-31): per-item toggle sets the chosen scope directly on the row.
// The value is converted via ToInternalScope before being passed to the store.
// The handler redirects back to the referrer (or project page) on success.
func (s *Server) handleToggleScope(w http.ResponseWriter, r *http.Request) {
	rawID := r.PathValue("id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid observation id", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	uiScope := r.FormValue("scope")
	if uiScope != "shared" && uiScope != "personal" {
		http.Error(w, fmt.Sprintf("invalid scope %q: must be 'shared' or 'personal'", uiScope), http.StatusBadRequest)
		return
	}

	ms := s.mutableStore
	if ms == nil {
		http.Error(w, "store not available", http.StatusServiceUnavailable)
		return
	}

	internalScope := ToInternalScope(uiScope)
	if err := ms.UpdateObservationScope(id, internalScope); err != nil {
		log.Printf("[triage] handleToggleScope id=%d: %v", id, err)
		http.Error(w, "failed to update scope", http.StatusInternalServerError)
		return
	}

	// Redirect back to referrer or the root page.
	ref := r.Referer()
	if ref == "" {
		ref = "/"
	}
	http.Redirect(w, r, ref, http.StatusSeeOther)
}

// handleSetProjectScope bulk-sets the scope of all observations in a project.
// POST /project/{name}/set-scope
//
// Form fields:
//
//	scope   — UI vocabulary: "shared" or "personal" (required)
//	confirm — "1" to execute; absent to show confirmation page first
//
// Without confirm=1: returns a direction-sensitive confirmation page (D7).
//   - → shared: shows sync-risk warning (REQ-36).
//   - → personal: lighter copy, no sync-risk warning (REQ-36).
//
// With confirm=1: updates every observation in the project to the chosen scope,
// then writes default_scope to config.json for the cwd project in both directions (D4, REQ-42).
//
// Decision #937.3: the bulk action MUST require a confirmation step.
// Decision #939/#940 (Option A): config.json is only written for the cwd project.
// REQ-35: bidirectional (→ shared AND → personal).
func (s *Server) handleSetProjectScope(w http.ResponseWriter, r *http.Request) {
	rawName := r.PathValue("name")
	if rawName == "" {
		http.NotFound(w, r)
		return
	}
	projectName, err := url.PathUnescape(rawName)
	if err != nil || projectName == "" {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	// Validate scope — only "shared" and "personal" are accepted (REQ-35).
	uiScope := r.FormValue("scope")
	if uiScope != "shared" && uiScope != "personal" {
		http.Error(w, fmt.Sprintf("invalid scope %q: must be 'shared' or 'personal'", uiScope), http.StatusBadRequest)
		return
	}

	ms := s.mutableStore
	if ms == nil {
		http.Error(w, "store not available", http.StatusServiceUnavailable)
		return
	}

	// Fetch ALL observations for the project — no cap (W-2).
	// obsBulkScopeLimit is a very large sentinel so the bulk action never
	// silently truncates projects with more than obsPerProjectLimit (200) items.
	observations, err := ms.RecentObservations(projectName, "", obsBulkScopeLimit)
	if err != nil {
		log.Printf("[triage] handleSetProjectScope %q: RecentObservations: %v", projectName, err)
		http.Error(w, "failed to load observations", http.StatusInternalServerError)
		return
	}

	confirmed := r.FormValue("confirm") == "1"
	if !confirmed {
		// Return a direction-sensitive confirmation page (D7, REQ-36).
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := SetScopeConfirmPage(projectName, len(observations), uiScope)
		title := "Confirm bulk scope"
		if err := TriageLayout(title, page).Render(context.Background(), w); err != nil {
			log.Printf("[triage] handleSetProjectScope confirm render: %v", err)
		}
		return
	}

	// Execute the bulk update, skipping observations already at the target scope.
	// Skipping avoids spurious sync mutations and revision bumps (store.UpdateObservation
	// always increments revision_count + updated_at, even when the value is unchanged).
	// Scenario C-06: rows already at internalScope must be left untouched.
	internalScope := ToInternalScope(uiScope)
	updatedCount := 0
	for _, obs := range observations {
		if obs.Scope == internalScope {
			// Already at target — do not re-write; do not enqueue a sync mutation.
			continue
		}
		if err := ms.UpdateObservationScope(obs.ID, internalScope); err != nil {
			log.Printf("[triage] handleSetProjectScope %q: UpdateObservationScope id=%d: %v", projectName, obs.ID, err)
			http.Error(w, "partial update — some items may not have been updated", http.StatusInternalServerError)
			return
		}
		updatedCount++
	}
	_ = updatedCount // available for future C-08 count reporting

	// Option A (D4, REQ-42): write config.json for both directions, cwd project only.
	if s.cwdDir != "" && s.cwdProject == projectName {
		if err := WriteProjectDefaultScope(s.cwdDir, uiScope); err != nil {
			log.Printf("[triage] handleSetProjectScope %q: WriteProjectDefaultScope: %v", projectName, err)
			// Non-fatal — the rows are updated; only the default badge is affected.
		}
	}

	http.Redirect(w, r, "/project/"+url.PathEscape(projectName), http.StatusSeeOther)
}

// handleClassify sets the default_scope for the cwd project in config.json.
// POST /project/{name}/classify
//
// Option A boundary (#939/#940): only the cwd project (s.cwdProject) can have
// its default set from the UI. Requests for other projects are rejected with 400.
//
// Form fields:
//
//	scope — "shared" or "personal"
func (s *Server) handleClassify(w http.ResponseWriter, r *http.Request) {
	rawName := r.PathValue("name")
	if rawName == "" {
		http.NotFound(w, r)
		return
	}
	projectName, err := url.PathUnescape(rawName)
	if err != nil || projectName == "" {
		http.NotFound(w, r)
		return
	}

	// Option A: only cwd project can be classified.
	if projectName != s.cwdProject {
		http.Error(w,
			fmt.Sprintf("can only set default scope for the launch project (%q); got %q (Option A, decision #939/#940)",
				s.cwdProject, projectName),
			http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	uiScope := r.FormValue("scope")
	if uiScope != "shared" && uiScope != "personal" {
		http.Error(w, fmt.Sprintf("invalid scope %q: must be 'shared' or 'personal'", uiScope), http.StatusBadRequest)
		return
	}

	if s.cwdDir == "" {
		http.Error(w, "launch directory not set; cannot write config.json", http.StatusInternalServerError)
		return
	}

	if err := WriteProjectDefaultScope(s.cwdDir, uiScope); err != nil {
		log.Printf("[triage] handleClassify %q: WriteProjectDefaultScope: %v", projectName, err)
		http.Error(w, "failed to write config.json", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}
