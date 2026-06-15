package triage

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"sort"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// TriageStore is the subset of store.Store used by triage handlers.
// Declaring a narrow interface keeps the handlers testable without a real SQLite file.
type TriageStore interface {
	ListProjectsWithStats() ([]store.ProjectStats, error)
	RecentObservations(project, scope string, limit int) ([]store.Observation, error)
}

const (
	// obsPerProjectLimit is the maximum observations loaded per project page.
	obsPerProjectLimit = 200
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
		page := ProjectListPage(projectName, nil)
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
	page := ProjectListPage(projectName, rows)
	if err := TriageLayout(projectName, page).Render(context.Background(), w); err != nil {
		log.Printf("[triage] handleProject render error: %v", err)
	}
}
