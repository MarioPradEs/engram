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

// EnrollmentStore is the narrow interface that handleShareProject and
// handleUnshareProject use to client-enroll or client-unenroll a project.
// Declared here so triage handlers do not depend directly on *store.Store,
// keeping them independently testable via fake implementations.
//
// *triage.StoreAdapter satisfies this interface (methods added in Phase 5).
// *store.Store also satisfies it directly (EnrollProject/UnenrollProject exist).
type EnrollmentStore interface {
	EnrollProject(project string) error
	UnenrollProject(project string) error
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
		page := ProjectListPage(projectName, nil, s.cwdProject, nil)
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

	// Pre-load initial tag values for the juego facet (tag-picker, PR#3 / E2b).
	// If the mutable store is available, fetch distinct juego values for the picker.
	// On failure, fall back to nil (picker shows "No values" hint — non-fatal).
	var tagInitialValues []string
	if ms := s.mutableStore; ms != nil {
		if vals, err := ms.DistinctTagValues(projectName, "juego"); err == nil {
			tagInitialValues = vals
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Pass cwdProject so the template gates classify controls to the cwd project (W-1).
	page := ProjectListPage(projectName, rows, s.cwdProject, tagInitialValues)
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

// tagScopeFacetAllowList is the closed set of facets accepted by handleTagScope
// and handleTagValues. Mirrors the store-level allow-list (tagFacetAllowList in store.go)
// so that invalid facets are rejected at the handler boundary before any store call.
var tagScopeFacetAllowList = map[string]bool{
	"juego": true,
	"tipo":  true,
}

// handleTagScope bulk-sets the scope of all observations in a project that match
// a given tag facet/value pair.
// POST /project/{name}/tag-scope
//
// Form fields:
//
//	facet   — "juego" or "tipo" (required; 400 otherwise — REQ-51, D3)
//	value   — tag value to match (required)
//	scope   — "shared" or "personal" (required; 400 otherwise)
//	confirm — "1" to execute; absent to show confirmation page first
//
// Without confirm=1:
//   - Zero matches → renders TagScopeEmptyPage (inline message; no confirm button — D5, REQ-53).
//   - One or more matches → renders TagScopeConfirmPage (D7, REQ-55):
//   - → shared: sync-risk warning.
//   - → personal: lighter copy.
//
// With confirm=1: updates matching observations to the chosen scope, skipping those
// already at the target (same guard as handleSetProjectScope). Does NOT write
// default_scope to config.json — tag-scope is a subset action, not a project-wide
// default change (D4; only handleSetProjectScope writes config.json).
//
// CSRF: must be wrapped in originCheckMiddleware (see routes()).
// Per-project only (D2): the project name comes from the URL segment {name}.
func (s *Server) handleTagScope(w http.ResponseWriter, r *http.Request) {
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

	// Validate facet (allow-list: juego, tipo only — REQ-51, D3).
	facet := r.FormValue("facet")
	if !tagScopeFacetAllowList[facet] {
		http.Error(w,
			fmt.Sprintf("invalid facet %q: must be one of {juego, tipo}", facet),
			http.StatusBadRequest)
		return
	}

	// Validate scope.
	uiScope := r.FormValue("scope")
	if uiScope != "shared" && uiScope != "personal" {
		http.Error(w,
			fmt.Sprintf("invalid scope %q: must be 'shared' or 'personal'", uiScope),
			http.StatusBadRequest)
		return
	}

	value := r.FormValue("value")
	if value == "" {
		http.Error(w, "value is required", http.StatusBadRequest)
		return
	}

	ms := s.mutableStore
	if ms == nil {
		http.Error(w, "store not available", http.StatusServiceUnavailable)
		return
	}

	// Fetch matching observations via the tag-index method (PR#2 / E2a).
	obs, err := ms.ObservationsByTag(projectName, facet, value, obsBulkScopeLimit)
	if err != nil {
		log.Printf("[triage] handleTagScope %q %s=%s: ObservationsByTag: %v", projectName, facet, value, err)
		http.Error(w, "failed to query observations by tag", http.StatusInternalServerError)
		return
	}

	// D5 zero-match guard: block confirmation and show inline message.
	if len(obs) == 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := TagScopeEmptyPage(projectName, facet, value)
		if err := TriageLayout("No observations found", page).Render(context.Background(), w); err != nil {
			log.Printf("[triage] handleTagScope empty render: %v", err)
		}
		return
	}

	confirmed := r.FormValue("confirm") == "1"
	if !confirmed {
		// Return direction-sensitive confirmation page (D7, REQ-55).
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := TagScopeConfirmPage(projectName, facet, value, len(obs), uiScope)
		title := "Confirm bulk tag-scope"
		if err := TriageLayout(title, page).Render(context.Background(), w); err != nil {
			log.Printf("[triage] handleTagScope confirm render: %v", err)
		}
		return
	}

	// Execute the bulk update, skipping observations already at the target scope.
	// Same guard as handleSetProjectScope — avoids spurious store mutations.
	internalScope := ToInternalScope(uiScope)
	for _, o := range obs {
		if o.Scope == internalScope {
			continue
		}
		if err := ms.UpdateObservationScope(o.ID, internalScope); err != nil {
			log.Printf("[triage] handleTagScope %q %s=%s: UpdateObservationScope id=%d: %v",
				projectName, facet, value, o.ID, err)
			http.Error(w, "partial update — some items may not have been updated", http.StatusInternalServerError)
			return
		}
	}

	// DO NOT write config.json here (D4: only handleSetProjectScope writes default_scope;
	// tag-scope is a per-subset action and must not change the whole-project default).

	http.Redirect(w, r, "/project/"+url.PathEscape(projectName), http.StatusSeeOther)
}

// handleTagValues returns an htmx fragment of <option> elements for the value
// <select> in the tag-picker form. Called when the facet select changes.
// GET /project/{name}/tag-values?facet=X
//
// Query params:
//
//	facet — "juego" or "tipo" (required; 400 otherwise — REQ-51, D3)
//
// Response: an HTML fragment (no full HTML shell) containing <option> elements
// suitable for htmx swap into #tag-values. Empty values → single disabled hint.
func (s *Server) handleTagValues(w http.ResponseWriter, r *http.Request) {
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

	facet := r.URL.Query().Get("facet")
	if !tagScopeFacetAllowList[facet] {
		http.Error(w,
			fmt.Sprintf("invalid facet %q: must be one of {juego, tipo}", facet),
			http.StatusBadRequest)
		return
	}

	ms := s.mutableStore
	if ms == nil {
		// Graceful degradation: no store → empty fragment.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := TagValuesFragment(nil).Render(context.Background(), w); err != nil {
			log.Printf("[triage] handleTagValues no-store render: %v", err)
		}
		return
	}

	values, err := ms.DistinctTagValues(projectName, facet)
	if err != nil {
		log.Printf("[triage] handleTagValues %q %s: DistinctTagValues: %v", projectName, facet, err)
		http.Error(w, "failed to fetch tag values", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := TagValuesFragment(values).Render(context.Background(), w); err != nil {
		log.Printf("[triage] handleTagValues render: %v", err)
	}
}

// handleShareProject atomically enrolls the cwd project for cloud sync.
// POST /project/{name}/share
//
// Atomicity order (D9): server enroll FIRST so that a network failure does not
// leave the project partially enrolled locally.
//
//  1. Option A gate: name MUST equal s.cwdProject → 400 if mismatch.
//  2. Call s.serverEnrollFn(project, bearerToken) — server enroll FIRST.
//     On failure: return 4xx "share failed: <error>", NO local state change.
//  3. Call s.enrollStore.EnrollProject(project) — client SQLite.
//     On failure: s.enrollStore.UnenrollProject (no-op, nothing enrolled), return 500.
//  4. Call WriteProjectDefaultScope(cwdDir, "shared") — write config.json.
//     On failure: call s.enrollStore.UnenrollProject to roll back step 3, return 500.
//  5. Return HTTP 200 OK.
//
// CSRF: must be wrapped in originCheckMiddleware (see routes()).
func (s *Server) handleShareProject(w http.ResponseWriter, r *http.Request) {
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

	// Option A boundary: only the cwd project can be shared from this server instance.
	if projectName != s.cwdProject {
		http.Error(w,
			fmt.Sprintf("project mismatch — share only applies to the current folder's project (%q); got %q",
				s.cwdProject, projectName),
			http.StatusBadRequest)
		return
	}

	// Step 2: server-side enroll FIRST (D9). A nil fn is a safe-default that fails.
	if s.serverEnrollFn == nil {
		http.Error(w, "share not available — server enroll function not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.serverEnrollFn(projectName, s.bearerToken); err != nil {
		http.Error(w, "share failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Step 3: client-side enrollment in local SQLite.
	if s.enrollStore == nil {
		http.Error(w, "share not available — enrollment store not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.enrollStore.EnrollProject(projectName); err != nil {
		log.Printf("[triage] handleShareProject %q: EnrollProject: %v", projectName, err)
		// Rollback: attempt to remove from local enrollment (no-op if not enrolled).
		_ = s.enrollStore.UnenrollProject(projectName)
		http.Error(w, "share failed: local enroll: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Step 4: write default_scope="shared" to config.json.
	if s.cwdDir != "" {
		if err := WriteProjectDefaultScope(s.cwdDir, "shared"); err != nil {
			log.Printf("[triage] handleShareProject %q: WriteProjectDefaultScope: %v", projectName, err)
			// Rollback: remove from local enrollment (undo step 3).
			_ = s.enrollStore.UnenrollProject(projectName)
			http.Error(w, "share failed: write config: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Project %q shared successfully", projectName)
}

// handleUnshareProject atomically un-enrolls the cwd project from cloud sync.
// POST /project/{name}/unshare
//
// Atomicity order (reverse of D9 share): client unenroll FIRST so that new
// observations stop syncing immediately, then server un-enroll, then scope reset.
// Rollback: if the server un-enroll call fails after a successful client unenroll,
// the client enrollment is restored (EnrollProject called). This prevents a state
// where the server still thinks the project is shared but the client has already
// stopped syncing. Already-synced observations are NOT deleted from the cloud.
//
//  1. Option A gate: name MUST equal s.cwdProject → 400 if mismatch.
//  2. Call s.enrollStore.UnenrollProject(project) — client SQLite unenroll FIRST.
//     On failure: return 500.
//  3. Call s.serverUnenrollFn(project, bearerToken) — server-side un-enroll.
//     On failure: rollback by calling s.enrollStore.EnrollProject, return 4xx.
//  4. Call WriteProjectDefaultScope(cwdDir, "personal") — revert config.json.
//     On failure: log only (non-fatal; enrollment changes already committed).
//  5. Return HTTP 200.
//
// CSRF: must be wrapped in originCheckMiddleware (see routes()).
func (s *Server) handleUnshareProject(w http.ResponseWriter, r *http.Request) {
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

	// Option A boundary: only the cwd project can be unshared from this instance.
	if projectName != s.cwdProject {
		http.Error(w,
			fmt.Sprintf("project mismatch — unshare only applies to the current folder's project (%q); got %q",
				s.cwdProject, projectName),
			http.StatusBadRequest)
		return
	}

	// Step 2: client-side unenroll FIRST (stops local sync immediately).
	if s.enrollStore == nil {
		http.Error(w, "unshare not available — enrollment store not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.enrollStore.UnenrollProject(projectName); err != nil {
		log.Printf("[triage] handleUnshareProject %q: UnenrollProject: %v", projectName, err)
		http.Error(w, "unshare failed: local unenroll: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Step 3: server-side un-enroll. A nil fn is a safe-default that fails.
	if s.serverUnenrollFn == nil {
		// Rollback client unenroll before returning error.
		_ = s.enrollStore.EnrollProject(projectName)
		http.Error(w, "unshare not available — server unenroll function not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.serverUnenrollFn(projectName, s.bearerToken); err != nil {
		// Rollback: restore client enrollment so both sides stay in sync.
		_ = s.enrollStore.EnrollProject(projectName)
		http.Error(w, "unshare failed: server unenroll: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Step 4: revert default_scope to "personal" in config.json (non-fatal).
	if s.cwdDir != "" {
		if err := WriteProjectDefaultScope(s.cwdDir, "personal"); err != nil {
			log.Printf("[triage] handleUnshareProject %q: WriteProjectDefaultScope: %v", projectName, err)
			// Non-fatal — the enrollments are already reverted; only the default badge is affected.
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Project %q unshared — already-synced data remains in the cloud", projectName)
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
