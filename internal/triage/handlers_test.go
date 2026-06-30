package triage_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

// TestSetScopeConfirmPage_SharedGolden renders the SetScopeConfirmPage for the
// → shared direction and compares to a golden file.
// Update with: go test ./internal/triage/... -run TestSetScopeConfirmPage_SharedGolden -update
func TestSetScopeConfirmPage_SharedGolden(t *testing.T) {
	fs := &fakeMutableStore{
		observations: []store.Observation{
			{ID: 1, Scope: "personal"},
		},
	}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/alpha/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	got, _ := io.ReadAll(rec.Body)
	goldenCheck(t, "set_scope_confirm_shared", got)
}

// TestSetScopeConfirmPage_PersonalGolden renders the SetScopeConfirmPage for the
// → personal direction and compares to a golden file.
// Update with: go test ./internal/triage/... -run TestSetScopeConfirmPage_PersonalGolden -update
func TestSetScopeConfirmPage_PersonalGolden(t *testing.T) {
	fs := &fakeMutableStore{
		observations: []store.Observation{
			{ID: 1, Scope: "team"},
			{ID: 2, Scope: "team"},
		},
	}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{"scope": {"personal"}}
	req := httptest.NewRequest(http.MethodPost, "/project/alpha/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	got, _ := io.ReadAll(rec.Body)
	goldenCheck(t, "set_scope_confirm_personal", got)
}

// ─── Bulk set-scope: POST /project/{name}/set-scope ──────────────────────────

// TestSetProjectScope_SharedNoConfirm verifies that POSTing scope=shared without
// confirm=1 returns a confirmation page with the sync-risk warning and no store writes.
// Scenario C-11.
func TestSetProjectScope_SharedNoConfirm(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "A", Scope: "personal", Project: ptrStr("proj")},
		{ID: 2, Title: "B", Scope: "personal", Project: ptrStr("proj")},
	}
	fs := &fakeMutableStore{observations: obs}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200 confirmation page, got %d", rec.Code)
	}
	body := rec.Body.String()
	// Must show the canonical sync-risk phrase for → shared direction (D7, REQ-36).
	// The golden (set_scope_confirm_shared.html) uses the exact phrase below.
	// Both conditions must hold independently — the previous && allowed a single
	// keyword to satisfy the check; we now require the precise phrase.
	if !strings.Contains(body, "cannot be recalled") {
		t.Errorf("want canonical sync-risk phrase 'cannot be recalled' in shared confirm page; body excerpt: %s", body[:min(500, len(body))])
	}
	// Must show observation count with contextual phrasing — a bare "2" is too weak
	// (any "2" anywhere would pass). Require the template's exact phrasing so a wrong
	// count (e.g. 0 or 1) fails this assertion.
	if !strings.Contains(body, "2 observation(s)") {
		t.Errorf("want '2 observation(s)' in confirmation page; body excerpt: %s", body[:min(300, len(body))])
	}
	// Zero writes before confirm.
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 store mutations before confirm, got %d", len(fs.updateCalls))
	}
}

// TestSetProjectScope_PersonalNoConfirm verifies that POSTing scope=personal without
// confirm=1 returns a confirmation page WITHOUT the sync-risk warning and no store
// writes. Scenario C-12.
func TestSetProjectScope_PersonalNoConfirm(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "A", Scope: "team", Project: ptrStr("proj")},
	}
	fs := &fakeMutableStore{observations: obs}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{"scope": {"personal"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200 confirmation page, got %d", rec.Code)
	}
	body := rec.Body.String()
	// Must NOT show sync-risk warning for → personal direction (D7, REQ-36).
	if strings.Contains(body, "cannot be recalled") {
		t.Errorf("want NO sync-risk warning in personal confirm page; body excerpt: %s", body[:min(500, len(body))])
	}
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 store mutations before confirm, got %d", len(fs.updateCalls))
	}
}

// TestSetProjectScope_SharedConfirm verifies that confirming scope=shared updates all
// observations to internal scope "team" and writes default_scope="shared" to config.json
// for the cwd project. Scenarios C-06, C-14.
func TestSetProjectScope_SharedConfirm(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 10, Title: "A", Scope: "personal", Project: ptrStr("proj")},
		{ID: 20, Title: "B", Scope: "personal", Project: ptrStr("proj")},
	}
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fs := &fakeMutableStore{observations: obs}
	srv := triage.NewWithMutableStore(nil, fs, 0, dir)
	srv.SetCwdProject("proj")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Fix #1: assert real 303 redirect with Location on success.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 SeeOther on success, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/project/proj" {
		t.Errorf("want Location=/project/proj, got %q", loc)
	}
	// All observations must be updated to internal scope "team".
	if len(fs.updateCalls) != 2 {
		t.Fatalf("want 2 UpdateObservationScope calls, got %d", len(fs.updateCalls))
	}
	for _, c := range fs.updateCalls {
		if c.Scope != "team" {
			t.Errorf("want scope=team, got %q for id=%d", c.Scope, c.ID)
		}
	}
	// WriteProjectDefaultScope must have written default_scope="shared".
	configPath := dir + "/.engram/config.json"
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.json not written: %v", err)
	}
	var cfg map[string]string
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg["default_scope"] != "shared" {
		t.Errorf("want default_scope=shared, got %q", cfg["default_scope"])
	}
}

// TestSetProjectScope_PersonalConfirm verifies that confirming scope=personal updates
// all observations to "personal" and writes default_scope="personal" to config.json.
// Scenarios C-06b, C-13.
func TestSetProjectScope_PersonalConfirm(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 10, Title: "A", Scope: "team", Project: ptrStr("proj")},
		{ID: 20, Title: "B", Scope: "team", Project: ptrStr("proj")},
		{ID: 30, Title: "C", Scope: "team", Project: ptrStr("proj")},
	}
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fs := &fakeMutableStore{observations: obs}
	srv := triage.NewWithMutableStore(nil, fs, 0, dir)
	srv.SetCwdProject("proj")
	h := srv.Handler()

	form := url.Values{"scope": {"personal"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Fix #1: assert real 303 redirect with Location on success.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 SeeOther on success, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/project/proj" {
		t.Errorf("want Location=/project/proj, got %q", loc)
	}
	if len(fs.updateCalls) != 3 {
		t.Fatalf("want 3 UpdateObservationScope calls, got %d", len(fs.updateCalls))
	}
	for _, c := range fs.updateCalls {
		if c.Scope != "personal" {
			t.Errorf("want scope=personal, got %q for id=%d", c.Scope, c.ID)
		}
	}
	// WriteProjectDefaultScope must have written default_scope="personal".
	configPath := dir + "/.engram/config.json"
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.json not written: %v", err)
	}
	var cfg map[string]string
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg["default_scope"] != "personal" {
		t.Errorf("want default_scope=personal, got %q", cfg["default_scope"])
	}
}

// TestSetProjectScope_NonCwdProjectNoConfigWrite verifies that confirming set-scope
// for a project that is NOT the cwd project does NOT write config.json. Scenario C-15.
func TestSetProjectScope_NonCwdProjectNoConfigWrite(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 5, Title: "A", Scope: "personal", Project: ptrStr("other")},
	}
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fs := &fakeMutableStore{observations: obs}
	srv := triage.NewWithMutableStore(nil, fs, 0, dir)
	srv.SetCwdProject("mine") // cwd is "mine" — posting for "other"
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/other/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Fix #1: assert real 303 redirect with Location on success.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 SeeOther on success, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/project/other" {
		t.Errorf("want Location=/project/other, got %q", loc)
	}
	// Observations MUST still be updated.
	if len(fs.updateCalls) != 1 {
		t.Fatalf("want 1 UpdateObservationScope call, got %d", len(fs.updateCalls))
	}
	// config.json must NOT be written (Option A — only cwd project).
	configPath := dir + "/.engram/config.json"
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("want config.json NOT written for non-cwd project, but file exists")
	}
}

// TestSetProjectScope_InvalidScope has been replaced by TestSetProjectScope_InvalidScope_TableDriven
// which covers the same "department" case plus empty string and wrong-case "SHARED".

// TestSetProjectScope_PartialFailure verifies that a store error on a single
// UpdateObservationScope call causes the handler to report a failure AND does
// NOT write config.json (partial failure must abort before WriteProjectDefaultScope).
// Scenario C-08.
func TestSetProjectScope_PartialFailure(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "A", Scope: "personal", Project: ptrStr("proj")},
	}
	// Fix #5: set up temp cwdDir + SetCwdProject so config.json write path is active.
	cwdDir := t.TempDir()
	if err := os.MkdirAll(cwdDir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}
	fs := &fakeMutableStore{
		observations: obs,
		updateErr:    fmt.Errorf("simulated store error"),
	}
	srv := triage.NewWithMutableStore(nil, fs, 0, cwdDir)
	srv.SetCwdProject("proj")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The handler returns 500 on a store error (partial update).
	// Use Fatalf so downstream body/config checks only run in the expected (500) path.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 on partial failure, got %d; body: %s",
			rec.Code, rec.Body.String()[:min(300, len(rec.Body.String()))])
	}
	// Secondary: body should contain a hint about the failure.
	if !strings.Contains(rec.Body.String(), "partial") && !strings.Contains(rec.Body.String(), "update") {
		t.Errorf("want partial-update hint in body on failure, got: %s", rec.Body.String()[:min(300, len(rec.Body.String()))])
	}
	// Fix #5: config.json must NOT be written when a store error aborts mid-loop.
	configPath := cwdDir + "/.engram/config.json"
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("want config.json NOT written on partial failure, but file exists at %s", configPath)
	}
}

// ─── WU-A6: Landing bulk buttons (REQ-07 / Scenario A-04) ───────────────────

// TestHandleIndex_BulkButtons_AllCards verifies that GET / renders bulk
// set-scope forms ("Mark all as shared" / "Mark all as personal") on EVERY
// project card — both the cwd project and non-cwd projects.
// REQ-07 / Scenario A-04.
// RED: added before projects.templ is changed (will fail until template updated).
func TestHandleIndex_BulkButtons_AllCards(t *testing.T) {
	projects := []store.ProjectStats{
		{Name: "alpha", ObservationCount: 10, SessionCount: 2},
		{Name: "beta", ObservationCount: 5, SessionCount: 1},
	}
	s := &fakeTriageStore{projects: projects}
	cwdDir := t.TempDir()
	srv := triage.NewWithStore(nil, s, 0, cwdDir)
	srv.SetCwdProject("alpha") // "alpha" is cwd; "beta" is non-cwd
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()

	// Fix #3: per-project action-specific assertions.
	// Both bulk forms (→ shared and → personal) must appear for cwd project alpha.
	alphaSharedCount := strings.Count(body, `action="/project/alpha/set-scope"`)
	if alphaSharedCount < 2 {
		t.Errorf("want at least 2 forms with action=/project/alpha/set-scope (one per scope direction), got %d", alphaSharedCount)
	}
	// Both bulk forms must appear for non-cwd project beta — this proves non-cwd
	// cards also get bulk buttons. A count < 2 means only one direction is rendered.
	betaCount := strings.Count(body, `action="/project/beta/set-scope"`)
	if betaCount < 2 {
		t.Errorf("want at least 2 forms with action=/project/beta/set-scope (one per scope direction), got %d; non-cwd cards must have both bulk buttons", betaCount)
	}
	// The button labels must be present at least twice (once per project card).
	sharedCount := strings.Count(body, "Mark all as shared")
	personalCount := strings.Count(body, "Mark all as personal")
	if sharedCount < 2 {
		t.Errorf("want 'Mark all as shared' at least 2 times (once per card), got %d", sharedCount)
	}
	if personalCount < 2 {
		t.Errorf("want 'Mark all as personal' at least 2 times (once per card), got %d", personalCount)
	}
	// The cwd project must STILL show classify controls (cwd-only classify
	// stays on top of the new bulk block).
	if !strings.Contains(body, "Set default: shared") {
		t.Errorf("want cwd classify button 'Set default: shared' to remain for cwd project 'alpha'")
	}
}

// TestHandleIndex_BulkButtons_FormStructure verifies that the bulk forms are
// placed OUTSIDE the <a> anchor link so the HTML is valid (no nested interactive
// elements). We detect this by confirming that the bulk form action appears in
// the rendered output without being inside an anchor href.
// This is a structural sanity check — it does not parse HTML but confirms the
// template does not embed the form inside the anchor.
func TestHandleIndex_BulkButtons_FormStructure(t *testing.T) {
	projects := []store.ProjectStats{
		{Name: "gamma", ObservationCount: 7, SessionCount: 3},
	}
	s := &fakeTriageStore{projects: projects}
	srv := triage.NewWithStore(nil, s, 0, t.TempDir())
	srv.SetCwdProject("other") // gamma is non-cwd; still must get bulk buttons
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	// The bulk action form must target /project/gamma/set-scope.
	if !strings.Contains(body, `/project/gamma/set-scope`) {
		t.Errorf("want bulk form for non-cwd project 'gamma' in landing body; not found")
	}
	// Confirm "Mark all as shared" and "Mark all as personal" are both present.
	if !strings.Contains(body, "Mark all as shared") {
		t.Errorf("want 'Mark all as shared' button in body")
	}
	if !strings.Contains(body, "Mark all as personal") {
		t.Errorf("want 'Mark all as personal' button in body")
	}
	// Fix #2: anchor-nesting structural assertion — the template must render the
	// anchor closing tag immediately before the bulk-actions div:
	//   <a href="/project/gamma">...</a><div class="bulk-actions">...
	// If a regression nests the form inside the <a>, that exact sequence disappears.
	// This is robust to nav links that add </a> earlier in the page.
	if !strings.Contains(body, `</a><div class="bulk-actions">`) {
		t.Errorf(`anchor nesting regression: expected "</a><div class=\"bulk-actions\">" sequence proving bulk form is outside the anchor; not found in body excerpt: %s`, body[:min(500, len(body))])
	}
	// Additional guard: verify gammaFormIdx for the specific project.
	gammaFormIdx := strings.Index(body, `action="/project/gamma/set-scope"`)
	if gammaFormIdx < 0 {
		t.Errorf("want action=/project/gamma/set-scope in rendered body; not found")
	} else {
		// The last </a> before the gamma form must exist and must not be followed
		// by an opening <a before the form (proves no anchor wraps the form).
		beforeForm := body[:gammaFormIdx]
		lastAnchorClose := strings.LastIndex(beforeForm, "</a>")
		if lastAnchorClose < 0 {
			t.Errorf("want </a> before gamma form action; not found")
		} else if strings.Contains(beforeForm[lastAnchorClose:], "<a ") {
			t.Errorf("anchor nesting regression: found <a after the last </a> and before gamma form action; bulk form is nested inside an anchor")
		}
	}
}

// ─── Fix 6a: empty project coverage ─────────────────────────────────────────

// TestSetProjectScope_EmptyProject verifies that when the store returns 0 observations
// and confirm=1 is sent, the handler makes 0 UpdateObservationScope calls, redirects
// 303, and (for the cwd project) still writes config.json.
func TestSetProjectScope_EmptyProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fs := &fakeMutableStore{observations: nil} // zero observations
	srv := triage.NewWithMutableStore(nil, fs, 0, dir)
	srv.SetCwdProject("emptyproj")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/emptyproj/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 SeeOther for empty project, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/project/emptyproj" {
		t.Errorf("want Location=/project/emptyproj, got %q", loc)
	}
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 UpdateObservationScope calls for empty project, got %d", len(fs.updateCalls))
	}
	// config.json IS written for the cwd project even when 0 rows are updated.
	configPath := dir + "/.engram/config.json"
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.json must be written for cwd project even with 0 rows; error: %v", err)
	}
	var cfg map[string]string
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config.json: %v", err)
	}
	if cfg["default_scope"] != "shared" {
		t.Errorf("want default_scope=shared in config.json, got %q", cfg["default_scope"])
	}
}

// ─── Fix 6b: all already at target coverage ──────────────────────────────────

// TestSetProjectScope_AllAlreadyAtTarget verifies that when all observations are
// already at the target scope, 0 UpdateObservationScope calls are made, the handler
// still redirects 303, and config.json is written for the cwd project.
func TestSetProjectScope_AllAlreadyAtTarget(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	// All observations already at internal scope "team" (UI scope "shared").
	obs := []store.Observation{
		{ID: 1, Scope: "team", Project: ptrStr("proj")},
		{ID: 2, Scope: "team", Project: ptrStr("proj")},
	}
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fs := &fakeMutableStore{observations: obs}
	srv := triage.NewWithMutableStore(nil, fs, 0, dir)
	srv.SetCwdProject("proj")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/set-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 SeeOther when all at target, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/project/proj" {
		t.Errorf("want Location=/project/proj, got %q", loc)
	}
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 update calls when all already at target, got %d: %+v", len(fs.updateCalls), fs.updateCalls)
	}
	// config.json must still be written for the cwd project.
	configPath := dir + "/.engram/config.json"
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.json must be written for cwd project even when 0 rows updated; error: %v", err)
	}
	var cfg map[string]string
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config.json: %v", err)
	}
	if cfg["default_scope"] != "shared" {
		t.Errorf("want default_scope=shared in config.json, got %q", cfg["default_scope"])
	}
}

// ─── Fix 6c: table-driven invalid scope ──────────────────────────────────────

// TestSetProjectScope_InvalidScope_TableDriven replaces (and extends) the single
// invalid-scope test with a table-driven test covering empty string, "department"
// (which was the original test case), and "SHARED" (wrong case).
// Each case must return 400 and make ZERO store calls.
func TestSetProjectScope_InvalidScope_TableDriven(t *testing.T) {
	cases := []struct {
		name  string
		scope string
	}{
		{"empty", ""},
		{"department", "department"},
		{"uppercase SHARED", "SHARED"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeMutableStore{}
			srv := triage.NewWithMutableStore(nil, fs, 0, "")
			h := srv.Handler()

			form := url.Values{"scope": {tc.scope}}
			req := httptest.NewRequest(http.MethodPost, "/project/proj/set-scope",
				strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("scope=%q: want 400, got %d; body: %s", tc.scope, rec.Code, rec.Body.String()[:min(200, len(rec.Body.String()))])
			}
			if len(fs.updateCalls) != 0 {
				t.Errorf("scope=%q: want 0 store calls, got %d", tc.scope, len(fs.updateCalls))
			}
		})
	}
}

// ─── Phase 7 RED: handleShareProject tests ───────────────────────────────────

// fakeEnrollStore records calls to EnrollProject and UnenrollProject.
// It is used to test handleShareProject without a real SQLite store.
type fakeEnrollStore struct {
	enrollCalls   []string
	unenrollCalls []string
	enrollErr     error
	unenrollErr   error
}

func (f *fakeEnrollStore) EnrollProject(project string) error {
	f.enrollCalls = append(f.enrollCalls, project)
	return f.enrollErr
}

func (f *fakeEnrollStore) UnenrollProject(project string) error {
	f.unenrollCalls = append(f.unenrollCalls, project)
	return f.unenrollErr
}

// TestHandleShareProject_HappyPath verifies that a successful share:
//  1. Calls serverEnrollFn with the project name (server FIRST, D9).
//  2. Calls EnrollProject on the enrollment store.
//  3. Writes default_scope="shared" to config.json.
//  4. Returns HTTP 200.
func TestHandleShareProject_HappyPath(t *testing.T) {
	cwdDir := t.TempDir()
	if err := os.MkdirAll(cwdDir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	var serverEnrollCalls []string
	fakeFn := func(project, bearerToken string) error {
		serverEnrollCalls = append(serverEnrollCalls, project)
		return nil
	}

	es := &fakeEnrollStore{}
	srv := triage.NewWithMutableStore(nil, &fakeMutableStore{}, 0, cwdDir)
	srv.SetCwdProject("myproject")
	srv.WithEnrollmentStore(es)
	srv.WithServerEnrollFn(fakeFn)
	srv.WithBearerToken("my-jwt-token")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/project/myproject/share", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if len(serverEnrollCalls) != 1 || serverEnrollCalls[0] != "myproject" {
		t.Errorf("want serverEnrollFn called once with 'myproject', got %v", serverEnrollCalls)
	}
	if len(es.enrollCalls) != 1 || es.enrollCalls[0] != "myproject" {
		t.Errorf("want EnrollProject called once with 'myproject', got %v", es.enrollCalls)
	}
	// WriteProjectDefaultScope must have set default_scope="shared".
	configPath := cwdDir + "/.engram/config.json"
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.json not written: %v", err)
	}
	var cfg map[string]string
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config.json: %v", err)
	}
	if cfg["default_scope"] != "shared" {
		t.Errorf("want default_scope=shared, got %q", cfg["default_scope"])
	}
}

// TestHandleShareProject_ServerEnrollFailure verifies that when serverEnrollFn
// returns an error, the handler returns 4xx, EnrollProject is NOT called, and
// default_scope is not written (no local state change per D9).
func TestHandleShareProject_ServerEnrollFailure(t *testing.T) {
	cwdDir := t.TempDir()
	if err := os.MkdirAll(cwdDir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fakeFn := func(project, bearerToken string) error {
		return fmt.Errorf("server enrollment failed: network error")
	}

	es := &fakeEnrollStore{}
	srv := triage.NewWithMutableStore(nil, &fakeMutableStore{}, 0, cwdDir)
	srv.SetCwdProject("myproject")
	srv.WithEnrollmentStore(es)
	srv.WithServerEnrollFn(fakeFn)
	srv.WithBearerToken("my-jwt-token")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/project/myproject/share", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code < 400 {
		t.Fatalf("want 4xx on server enroll failure, got %d", rec.Code)
	}
	// EnrollProject must NOT be called (server FIRST: local change only after server succeeds).
	if len(es.enrollCalls) != 0 {
		t.Errorf("want EnrollProject NOT called on server failure, got %d calls", len(es.enrollCalls))
	}
	// config.json must NOT be written.
	configPath := cwdDir + "/.engram/config.json"
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("want config.json NOT written on server failure")
	}
}

// TestHandleShareProject_CwdBoundary verifies Option A: requests for a project
// other than cwdProject are rejected with HTTP 400 and nothing is called.
func TestHandleShareProject_CwdBoundary(t *testing.T) {
	var serverCalls, enrollCalls int
	fakeFn := func(project, bearerToken string) error {
		serverCalls++
		return nil
	}
	es := &fakeEnrollStore{}

	srv := triage.NewWithMutableStore(nil, &fakeMutableStore{}, 0, t.TempDir())
	srv.SetCwdProject("android-game-perf-tool-desktop")
	srv.WithEnrollmentStore(es)
	srv.WithServerEnrollFn(fakeFn)
	srv.WithBearerToken("tok")
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/project/other-project/share", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on project mismatch, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mismatch") {
		t.Errorf("want 'mismatch' in body, got: %q", rec.Body.String())
	}
	if serverCalls != 0 {
		t.Errorf("want serverEnrollFn NOT called on mismatch, got %d", serverCalls)
	}
	_ = enrollCalls // fakeEnrollStore.enrollCalls covers this
	if len(es.enrollCalls) != 0 {
		t.Errorf("want EnrollProject NOT called on mismatch, got %d", len(es.enrollCalls))
	}
}

// TestHandleShareProject_NoJWT verifies that when the server's bearer token is
// empty, serverEnrollFn receives an empty string and (if it returns "not logged in")
// the handler propagates the error as HTTP 4xx. No local enrollment must occur.
func TestHandleShareProject_NoJWT(t *testing.T) {
	cwdDir := t.TempDir()
	if err := os.MkdirAll(cwdDir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	var capturedToken string
	fakeFn := func(project, bearerToken string) error {
		capturedToken = bearerToken
		if bearerToken == "" {
			return fmt.Errorf("not logged in — please run engram auth login first")
		}
		return nil
	}

	es := &fakeEnrollStore{}
	srv := triage.NewWithMutableStore(nil, &fakeMutableStore{}, 0, cwdDir)
	srv.SetCwdProject("myproject")
	srv.WithEnrollmentStore(es)
	srv.WithServerEnrollFn(fakeFn)
	// WithBearerToken NOT called → s.bearerToken is empty string
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/project/myproject/share", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code < 400 {
		t.Fatalf("want 4xx on empty JWT, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if capturedToken != "" {
		t.Errorf("want empty bearer token passed to serverEnrollFn, got %q", capturedToken)
	}
	if !strings.Contains(rec.Body.String(), "not logged in") {
		t.Errorf("want 'not logged in' in response body, got: %q", rec.Body.String())
	}
	if len(es.enrollCalls) != 0 {
		t.Errorf("want no EnrollProject calls on JWT failure, got %d", len(es.enrollCalls))
	}
}

// ─── Phase 5.1 RED: EnrollmentStore interface compile-time check ─────────────

// TestEnrollmentStoreInterface is a compile-time assertion that
// *triage.StoreAdapter satisfies triage.EnrollmentStore.
// The test body is intentionally empty; the var declaration at package scope
// fails compilation if the interface or methods are absent.
func TestEnrollmentStoreInterface(t *testing.T) {
	// Compile-time assertion — fails if EnrollmentStore is not declared or
	// StoreAdapter does not implement EnrollProject / UnenrollProject.
	var _ triage.EnrollmentStore = (*triage.StoreAdapter)(nil)
}

// min is a small helper (Go 1.21+ has min built-in but kept explicit for clarity).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
