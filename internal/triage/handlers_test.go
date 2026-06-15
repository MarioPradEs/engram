package triage_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/triage"
)

// ─── Fake store ──────────────────────────────────────────────────────────────

// fakeTriageStore implements triage.TriageStore with canned responses.
// All methods are safe to call from handler tests without a real SQLite file.
type fakeTriageStore struct {
	projects     []store.ProjectStats
	observations []store.Observation
	projectsErr  error
	obsErr       error
}

func (f *fakeTriageStore) ListProjectsWithStats() ([]store.ProjectStats, error) {
	return f.projects, f.projectsErr
}

func (f *fakeTriageStore) RecentObservations(project, scope string, limit int) ([]store.Observation, error) {
	return f.observations, f.obsErr
}

// ─── Index handler tests ─────────────────────────────────────────────────────

// TestHandleIndex_EmptyStore verifies that the index page renders when there
// are no projects in the store (empty state, no crash).
func TestHandleIndex_EmptyStore(t *testing.T) {
	srv := triage.NewWithStore(nil, &fakeTriageStore{}, 0, "")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Triage") {
		t.Errorf("want body to mention 'Triage', got: %q", body[:min(200, len(body))])
	}
}

// TestHandleIndex_GroupedByProject verifies that the index page renders each
// project by name when the store has multiple projects.
func TestHandleIndex_GroupedByProject(t *testing.T) {
	projects := []store.ProjectStats{
		{Name: "alpha", ObservationCount: 10},
		{Name: "beta", ObservationCount: 5},
	}
	s := &fakeTriageStore{projects: projects}
	srv := triage.NewWithStore(nil, s, 0, "")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alpha") {
		t.Errorf("want project 'alpha' in body")
	}
	if !strings.Contains(body, "beta") {
		t.Errorf("want project 'beta' in body")
	}
}

// TestHandleIndex_CwdDefaultBadge verifies that the cwd project shows a
// default-scope badge when the cwdProject matches a listed project.
func TestHandleIndex_CwdDefaultBadge(t *testing.T) {
	projects := []store.ProjectStats{
		{Name: "myproject", ObservationCount: 3},
		{Name: "other", ObservationCount: 1},
	}
	// Use a temp dir as cwdDir so ResolveDefaultScope returns personal (no config.json).
	cwdDir := t.TempDir()
	s := &fakeTriageStore{projects: projects}
	// cwdProject "myproject" is the launch project — it gets the default badge.
	srv := triage.NewWithStore(nil, s, 0, cwdDir)
	srv.SetCwdProject("myproject")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The cwd project card should show the default-scope badge.
	// "default: personal" is the fail-safe output when no config.json is present.
	if !strings.Contains(body, "myproject") {
		t.Errorf("want 'myproject' in body")
	}
	// The badge text should appear somewhere in the page for the cwd project.
	if !strings.Contains(body, "personal") && !strings.Contains(body, "shared") {
		t.Errorf("want default-scope badge (personal or shared) for cwd project, body: %s", body[:min(500, len(body))])
	}
}

// TestHandleIndex_NoCwdBadgeForOtherProjects verifies that non-cwd projects
// do NOT get a default-scope resolution badge (Option A: cwd-only).
func TestHandleIndex_NoCwdBadgeForOtherProjects(t *testing.T) {
	projects := []store.ProjectStats{
		{Name: "alpha", ObservationCount: 3},
		{Name: "beta", ObservationCount: 1},
	}
	cwdDir := t.TempDir()
	s := &fakeTriageStore{projects: projects}
	// cwdProject is "alpha" — "beta" should NOT show a resolved default badge.
	srv := triage.NewWithStore(nil, s, 0, cwdDir)
	srv.SetCwdProject("alpha")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// The resolved default-scope badge uses class "badge-default".
	// Only the cwd project (alpha) gets this badge; beta must not have it.
	// Note: classify buttons say "Set default: ..." but use class "btn-classify",
	// not "badge-default" — so counting badge-default is the precise check.
	count := strings.Count(body, "badge-default")
	if count > 1 {
		t.Errorf("want badge-default only for cwd project, got %d occurrences", count)
	}
}

// ─── Project handler tests ───────────────────────────────────────────────────

// TestHandleProject_Returns200 verifies the per-project page returns 200.
func TestHandleProject_Returns200(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "First obs", Scope: "team", Project: ptrStr("myproject")},
		{ID: 2, Title: "Second obs", Scope: "personal", Project: ptrStr("myproject")},
	}
	s := &fakeTriageStore{observations: obs}
	srv := triage.NewWithStore(nil, s, 0, "")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/myproject", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

// TestHandleProject_ShowsObservations verifies observation titles appear in
// the per-project list page.
func TestHandleProject_ShowsObservations(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "Alpha memory", Scope: "team", Project: ptrStr("p")},
		{ID: 2, Title: "Beta memory", Scope: "personal", Project: ptrStr("p")},
	}
	s := &fakeTriageStore{observations: obs}
	srv := triage.NewWithStore(nil, s, 0, "")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/p", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "Alpha memory") {
		t.Errorf("want 'Alpha memory' in project page")
	}
	if !strings.Contains(body, "Beta memory") {
		t.Errorf("want 'Beta memory' in project page")
	}
}

// TestHandleProject_NeedsTriageBadge verifies that legacy-scope observations
// ("project" or "department") render a "needs triage" badge, not "shared" or "personal".
func TestHandleProject_NeedsTriageBadge(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "Legacy obs", Scope: "project", Project: ptrStr("p")},
	}
	s := &fakeTriageStore{observations: obs}
	srv := triage.NewWithStore(nil, s, 0, "")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/p", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "needs triage") {
		t.Errorf("want 'needs triage' badge for legacy-scope obs, got body: %s", body[:min(500, len(body))])
	}
}

// TestHandleProject_ScopeBadges verifies team→"shared" and personal→"personal"
// badges appear for explicitly-scoped observations.
func TestHandleProject_ScopeBadges(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "Team obs", Scope: "team", Project: ptrStr("p")},
		{ID: 2, Title: "Personal obs", Scope: "personal", Project: ptrStr("p")},
	}
	s := &fakeTriageStore{observations: obs}
	srv := triage.NewWithStore(nil, s, 0, "")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/p", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "shared") {
		t.Errorf("want 'shared' badge for team-scope obs")
	}
	if !strings.Contains(body, "personal") {
		t.Errorf("want 'personal' badge for personal-scope obs")
	}
}

// TestHandleProject_RouteNotMatched verifies that /project/ (no name segment)
// does NOT match the GET /project/{name} route and instead falls through to
// the index handler (200) — i.e., the mux does not panic or 500.
//
// The pattern GET /project/{name} in Go 1.22+ requires a non-empty segment,
// so /project/ routes to GET / (the index handler).
func TestHandleProject_RouteNotMatched(t *testing.T) {
	s := &fakeTriageStore{}
	srv := triage.NewWithStore(nil, s, 0, "")
	h := srv.Handler()

	// /project/ (trailing slash, no name) hits the index handler → 200.
	req := httptest.NewRequest(http.MethodGet, "/project/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The server must not 500; index page or redirect is fine.
	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("unexpected 500 for /project/ request")
	}
}

// ─── Per-project classify controls gate (W-1) ────────────────────────────────

// TestHandleProject_ClassifyControlsAbsentForNonCwdProject verifies that the
// classify ("Set default") buttons are NOT rendered on the per-project page
// when the visited project is not the cwd project. W-1: the classify controls
// must be gated by cwd, matching the gate that already exists on projects.templ.
func TestHandleProject_ClassifyControlsAbsentForNonCwdProject(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "Some obs", Scope: "team", Project: ptrStr("other")},
	}
	s := &fakeTriageStore{observations: obs}
	// cwd project is "mine" — visiting "other" must not show classify controls.
	srv := triage.NewWithStore(nil, s, 0, "")
	srv.SetCwdProject("mine")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/other", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "btn-classify") {
		t.Errorf("W-1: classify buttons must NOT appear for non-cwd project; body excerpt: %s",
			body[:min(500, len(body))])
	}
}

// TestHandleProject_ClassifyControlsPresentForCwdProject verifies that the
// classify ("Set default") buttons ARE rendered when visiting the cwd project page.
func TestHandleProject_ClassifyControlsPresentForCwdProject(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "My obs", Scope: "team", Project: ptrStr("mine")},
	}
	s := &fakeTriageStore{observations: obs}
	srv := triage.NewWithStore(nil, s, 0, "")
	srv.SetCwdProject("mine")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/mine", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "btn-classify") {
		t.Errorf("W-1: classify buttons must appear for cwd project; body excerpt: %s",
			body[:min(500, len(body))])
	}
}

// ─── Golden tests ─────────────────────────────────────────────────────────────

// TestHandleIndex_Golden renders the index page with a fixed dataset and
// compares the output to a golden file for determinism.
// Update goldens with: go test ./internal/triage/... -run TestHandleIndex_Golden -update
func TestHandleIndex_Golden(t *testing.T) {
	projects := []store.ProjectStats{
		{Name: "alpha", ObservationCount: 10, SessionCount: 2},
		{Name: "beta", ObservationCount: 3, SessionCount: 1},
	}
	s := &fakeTriageStore{projects: projects}
	cwdDir := t.TempDir()
	srv := triage.NewWithStore(nil, s, 0, cwdDir)
	srv.SetCwdProject("alpha")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	got, _ := io.ReadAll(rec.Body)
	goldenCheck(t, "index", got)
}

// TestHandleProject_Golden renders the per-project page with a fixed dataset
// and compares to a golden file.
// Update goldens with: go test ./internal/triage/... -run TestHandleProject_Golden -update
func TestHandleProject_Golden(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "Alpha obs", Scope: "team", Project: ptrStr("alpha"), Type: "decision"},
		{ID: 2, Title: "Beta obs", Scope: "personal", Project: ptrStr("alpha"), Type: "bugfix"},
		{ID: 3, Title: "Legacy obs", Scope: "project", Project: ptrStr("alpha"), Type: "discovery"},
	}
	s := &fakeTriageStore{observations: obs}
	srv := triage.NewWithStore(nil, s, 0, "")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/alpha", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	got, _ := io.ReadAll(rec.Body)
	goldenCheck(t, "project", got)
}

// min is a small helper (Go 1.21+ has min built-in but kept explicit for clarity).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
