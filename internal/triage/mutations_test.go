package triage_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/triage"
)

// ─── Mutation-capable fake store ────────────────────────────────────────────

// fakeMutableStore implements triage.MutableTriageStore with mutation tracking.
type fakeMutableStore struct {
	projects     []store.ProjectStats
	observations []store.Observation
	projectsErr  error
	obsErr       error
	updateCalls  []updateCall // records every UpdateObservationScope call
	updateErr    error        // returned by UpdateObservationScope when non-nil
}

type updateCall struct {
	ID    int64
	Scope string
}

func (f *fakeMutableStore) ListProjectsWithStats() ([]store.ProjectStats, error) {
	return f.projects, f.projectsErr
}

func (f *fakeMutableStore) RecentObservations(project, scope string, limit int) ([]store.Observation, error) {
	return f.observations, f.obsErr
}

func (f *fakeMutableStore) UpdateObservationScope(id int64, internalScope string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updateCalls = append(f.updateCalls, updateCall{ID: id, Scope: internalScope})
	return nil
}

// ─── Per-item toggle: POST /observations/{id}/scope ──────────────────────────

// TestHandleToggleScope_SetsShared verifies that POSTing scope=shared to
// /observations/{id}/scope calls UpdateObservationScope with "team".
func TestHandleToggleScope_SetsShared(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/observations/42/scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("want redirect or 2xx, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if len(fs.updateCalls) != 1 {
		t.Fatalf("want 1 UpdateObservationScope call, got %d", len(fs.updateCalls))
	}
	if fs.updateCalls[0].ID != 42 {
		t.Errorf("want id=42, got %d", fs.updateCalls[0].ID)
	}
	if fs.updateCalls[0].Scope != "team" {
		t.Errorf("want internal scope=team, got %q", fs.updateCalls[0].Scope)
	}
}

// TestHandleToggleScope_SetsPersonal verifies that POSTing scope=personal calls
// UpdateObservationScope with "personal".
func TestHandleToggleScope_SetsPersonal(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{"scope": {"personal"}}
	req := httptest.NewRequest(http.MethodPost, "/observations/7/scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("unexpected 500; body: %s", rec.Body.String())
	}
	if len(fs.updateCalls) != 1 {
		t.Fatalf("want 1 update call, got %d", len(fs.updateCalls))
	}
	if fs.updateCalls[0].Scope != "personal" {
		t.Errorf("want internal scope=personal, got %q", fs.updateCalls[0].Scope)
	}
}

// TestHandleToggleScope_RejectsBadID verifies that a non-numeric id returns 400.
func TestHandleToggleScope_RejectsBadID(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/observations/notanumber/scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad id, got %d", rec.Code)
	}
	if len(fs.updateCalls) != 0 {
		t.Error("want no store calls for bad id")
	}
}

// TestHandleToggleScope_RejectsBadScope verifies that an unknown scope value
// returns 400 without calling the store.
func TestHandleToggleScope_RejectsBadScope(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{"scope": {"team"}} // internal value, not UI vocab
	req := httptest.NewRequest(http.MethodPost, "/observations/1/scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad scope, got %d", rec.Code)
	}
	if len(fs.updateCalls) != 0 {
		t.Error("want no store calls for bad scope")
	}
}

// ─── Bulk: POST /project/{name}/share-all ────────────────────────────────────

// TestHandleShareAll_RequiresConfirm verifies that without the confirm param,
// the handler returns a confirmation page (200) but does NOT mutate the store.
func TestHandleShareAll_RequiresConfirm(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "A", Scope: "personal", Project: ptrStr("proj")},
		{ID: 2, Title: "B", Scope: "personal", Project: ptrStr("proj")},
	}
	fs := &fakeMutableStore{observations: obs}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	// POST without confirm field.
	form := url.Values{}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/share-all",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200 confirmation page, got %d", rec.Code)
	}
	body := rec.Body.String()
	// Must show item count in the confirm prompt.
	if !strings.Contains(body, "2") {
		t.Errorf("want item count (2) in confirmation page; body: %s", body[:min(300, len(body))])
	}
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 store mutations before confirm, got %d", len(fs.updateCalls))
	}
}

// TestHandleShareAll_WithConfirmUpdatesAll verifies that confirming the bulk
// share-all action updates all project observations to scope=team.
func TestHandleShareAll_WithConfirmUpdatesAll(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 10, Title: "A", Scope: "personal", Project: ptrStr("proj")},
		{ID: 20, Title: "B", Scope: "personal", Project: ptrStr("proj")},
		{ID: 30, Title: "C", Scope: "personal", Project: ptrStr("proj")},
	}
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fs := &fakeMutableStore{observations: obs}
	srv := triage.NewWithMutableStore(nil, fs, 0, dir)
	srv.SetCwdProject("proj")
	h := srv.Handler()

	form := url.Values{"confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/share-all",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Should redirect or return 2xx after bulk action.
	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("unexpected 500; body: %s", rec.Body.String())
	}
	if len(fs.updateCalls) != 3 {
		t.Fatalf("want 3 update calls, got %d", len(fs.updateCalls))
	}
	for _, c := range fs.updateCalls {
		if c.Scope != "team" {
			t.Errorf("want scope=team for id=%d, got %q", c.ID, c.Scope)
		}
	}
}

// TestHandleShareAll_ReportsCount verifies the confirmation page shows the
// correct item count.
func TestHandleShareAll_ReportsCount(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "X", Scope: "personal", Project: ptrStr("p")},
		{ID: 2, Title: "Y", Scope: "personal", Project: ptrStr("p")},
		{ID: 3, Title: "Z", Scope: "personal", Project: ptrStr("p")},
		{ID: 4, Title: "W", Scope: "personal", Project: ptrStr("p")},
		{ID: 5, Title: "V", Scope: "personal", Project: ptrStr("p")},
	}
	fs := &fakeMutableStore{observations: obs}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{}
	req := httptest.NewRequest(http.MethodPost, "/project/p/share-all",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "5") {
		t.Errorf("want count 5 in body; got: %s", body[:min(400, len(body))])
	}
}

// limitEnforcingStore is a MutableTriageStore that enforces a limit parameter,
// unlike fakeMutableStore which ignores it. Used to verify W-2: share-all must
// not be capped at obsPerProjectLimit.
type limitEnforcingStore struct {
	fakeMutableStore
	lastLimit int // records the limit passed to the most recent RecentObservations call
}

func (f *limitEnforcingStore) RecentObservations(project, scope string, limit int) ([]store.Observation, error) {
	f.lastLimit = limit
	return f.fakeMutableStore.RecentObservations(project, scope, limit)
}

// TestHandleShareAll_SharesAllBeyondLimit verifies that share-all operates on
// ALL observations in a project, not just the first obsPerProjectLimit (200).
// W-2: the handler must pass a limit > obsPerProjectLimit (200) to the store so
// that projects with >200 observations are not silently truncated.
func TestHandleShareAll_SharesAllBeyondLimit(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	const total = 250
	obs := make([]store.Observation, total)
	for i := range obs {
		obs[i] = store.Observation{ID: int64(i + 1), Title: "obs", Scope: "personal", Project: ptrStr("bigproject")}
	}

	// Confirm page: the store must be queried with a limit > 200.
	ls := &limitEnforcingStore{fakeMutableStore: fakeMutableStore{observations: obs}}
	srv := triage.NewWithMutableStore(nil, ls, 0, "")
	h := srv.Handler()

	formNoConfirm := url.Values{}
	req := httptest.NewRequest(http.MethodPost, "/project/bigproject/share-all",
		strings.NewReader(formNoConfirm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 confirm page, got %d", rec.Code)
	}
	// The limit passed to RecentObservations must be well above 200 (W-2: no cap).
	const oldCap = 200
	if ls.lastLimit <= oldCap {
		t.Errorf("W-2: share-all passed limit=%d to store (must be > %d to avoid silent truncation)", ls.lastLimit, oldCap)
	}
}

// ─── Classify (set default scope): POST /project/{name}/classify ─────────────

// TestHandleClassify_SetsCwdProjectDefault verifies that POSTing to
// /project/{name}/classify for the cwd project writes default_scope to config.json.
func TestHandleClassify_SetsCwdProjectDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, dir)
	srv.SetCwdProject("myproject")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/myproject/classify",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("500 from classify; body: %s", rec.Body.String())
	}

	// Read config.json and verify default_scope was written.
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

// TestHandleClassify_RefusesNonCwdProject verifies Option A boundary:
// classify is refused (400 or 403) when the URL project does not match cwdProject.
func TestHandleClassify_RefusesNonCwdProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, dir)
	srv.SetCwdProject("myproject") // cwd project is "myproject"
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/otherproject/classify",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
		t.Errorf("want 400 or 403 for non-cwd project classify, got %d", rec.Code)
	}
}

// TestHandleClassify_RefusesBadScope verifies that an invalid scope value
// (not "shared" or "personal") is rejected with 400.
func TestHandleClassify_RefusesBadScope(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/.engram", 0o755); err != nil {
		t.Fatal(err)
	}

	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, dir)
	srv.SetCwdProject("proj")
	h := srv.Handler()

	form := url.Values{"scope": {"team"}} // internal value — not valid UI vocab
	req := httptest.NewRequest(http.MethodPost, "/project/proj/classify",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad scope, got %d", rec.Code)
	}
}
