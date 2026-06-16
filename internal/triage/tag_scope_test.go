package triage_test

import (
	"errors"
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

// errFakeStore is the sentinel error returned by tag-query fakes on failure tests.
var errFakeStore = errors.New("fake store error")

// ─── Helpers ─────────────────────────────────────────────────────────────────

// newMutableSrv creates a triage.Server with a fakeMutableStore, port 0, no cwdDir.
func newMutableSrv(fs *fakeMutableStore) *triage.Server {
	return triage.NewWithMutableStore(nil, fs, 0, "")
}

// newMutableSrvWithCwd creates a triage.Server with cwdDir and cwdProject set.
func newMutableSrvWithCwd(fs *fakeMutableStore, cwdDir, cwdProject string) *triage.Server {
	srv := triage.NewWithMutableStore(nil, fs, 0, cwdDir)
	srv.SetCwdProject(cwdProject)
	return srv
}

// ─── handleTagScope: POST /project/{name}/tag-scope ──────────────────────────

// TestTagScope_SharedNoConfirm verifies that POSTing scope=shared without confirm=1
// returns a confirmation page WITH the sync-risk warning and makes no store writes.
// Scenario B-10; REQ-55, D7.
func TestTagScope_SharedNoConfirm(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "A", Scope: "personal", Project: ptrStr("proj")},
		{ID: 2, Title: "B", Scope: "personal", Project: ptrStr("proj")},
	}
	fs := &fakeMutableStore{tagObs: obs}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"game-x"}, "scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 confirmation page, got %d; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must show sync-risk warning for → shared direction (D7, REQ-55).
	if !strings.Contains(body, "cannot be recalled") {
		t.Errorf("want sync-risk phrase 'cannot be recalled' in shared confirm; body: %s", body[:min(500, len(body))])
	}
	// Must show observation count.
	if !strings.Contains(body, "2 observation(s)") {
		t.Errorf("want '2 observation(s)' in confirm page; body: %s", body[:min(300, len(body))])
	}
	// Zero writes before confirm.
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 store mutations before confirm, got %d", len(fs.updateCalls))
	}
}

// TestTagScope_PersonalNoConfirm verifies that POSTing scope=personal without confirm=1
// returns a confirmation page WITHOUT the sync-risk warning and makes no store writes.
// Scenario B-11; REQ-55, D7.
func TestTagScope_PersonalNoConfirm(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Title: "A", Scope: "team", Project: ptrStr("proj")},
	}
	fs := &fakeMutableStore{tagObs: obs}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"tipo"}, "value": {"decision"}, "scope": {"personal"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 confirmation page, got %d; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must NOT show sync-risk warning for → personal direction.
	if strings.Contains(body, "cannot be recalled") {
		t.Errorf("want NO sync-risk warning in personal confirm; body: %s", body[:min(500, len(body))])
	}
	// Must show match count (mirrors the shared no-confirm test assertion).
	if !strings.Contains(body, "1 observation(s)") {
		t.Errorf("want '1 observation(s)' in personal confirm page; body: %s", body[:min(300, len(body))])
	}
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 store mutations before confirm, got %d", len(fs.updateCalls))
	}
}

// TestTagScope_SharedConfirm verifies that confirm=1 + scope=shared updates all
// matching observations to internal scope "team". Scenario B-01; REQ-50.
// Also asserts that the correct facet and value were passed to ObservationsByTag.
func TestTagScope_SharedConfirm(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 10, Scope: "personal", Project: ptrStr("proj")},
		{ID: 20, Scope: "personal", Project: ptrStr("proj")},
	}
	fs := &fakeMutableStore{tagObs: obs}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"game-x"}, "scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 SeeOther on success, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/project/proj" {
		t.Errorf("want Location=/project/proj, got %q", loc)
	}
	if len(fs.updateCalls) != 2 {
		t.Fatalf("want 2 UpdateObservationScope calls, got %d", len(fs.updateCalls))
	}
	for _, c := range fs.updateCalls {
		if c.Scope != "team" {
			t.Errorf("want scope=team for id=%d, got %q", c.ID, c.Scope)
		}
	}
	// Assert the handler forwarded the correct facet and value to the store.
	if fs.lastTagFacet != "juego" {
		t.Errorf("want facet=juego passed to ObservationsByTag, got %q", fs.lastTagFacet)
	}
	if fs.lastTagValue != "game-x" {
		t.Errorf("want value=game-x passed to ObservationsByTag, got %q", fs.lastTagValue)
	}
}

// TestTagScope_PersonalConfirm verifies that confirm=1 + scope=personal updates all
// matching observations to "personal". Scenario B-02; REQ-50.
// Also asserts that the correct facet and value were passed to ObservationsByTag.
func TestTagScope_PersonalConfirm(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 10, Scope: "team", Project: ptrStr("proj")},
		{ID: 20, Scope: "team", Project: ptrStr("proj")},
		{ID: 30, Scope: "team", Project: ptrStr("proj")},
	}
	fs := &fakeMutableStore{tagObs: obs}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"tipo"}, "value": {"decision"}, "scope": {"personal"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 SeeOther on success, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if len(fs.updateCalls) != 3 {
		t.Fatalf("want 3 UpdateObservationScope calls, got %d", len(fs.updateCalls))
	}
	for _, c := range fs.updateCalls {
		if c.Scope != "personal" {
			t.Errorf("want scope=personal for id=%d, got %q", c.ID, c.Scope)
		}
	}
	// Assert the handler forwarded the correct facet and value to the store.
	if fs.lastTagFacet != "tipo" {
		t.Errorf("want facet=tipo passed to ObservationsByTag, got %q", fs.lastTagFacet)
	}
	if fs.lastTagValue != "decision" {
		t.Errorf("want value=decision passed to ObservationsByTag, got %q", fs.lastTagValue)
	}
}

// TestTagScope_ZeroMatch verifies that when ObservationsByTag returns 0 results,
// the response blocks confirmation (no confirm button) and shows an inline
// "no observations" message. Zero writes must occur. REQ-53, D5; Scenario B-07.
func TestTagScope_ZeroMatch(t *testing.T) {
	fs := &fakeMutableStore{tagObs: []store.Observation{}} // empty result
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"nonexistent"}, "scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for zero-match (inline message page), got %d; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must show the no-match message.
	if !strings.Contains(body, "No observations") {
		t.Errorf("want 'No observations' inline message; body: %s", body[:min(500, len(body))])
	}
	// Must NOT show a confirm button (no confirm=1 hidden input for tag-scope).
	if strings.Contains(body, `name="confirm"`) {
		t.Errorf("D5: zero-match MUST NOT render a confirm button; found one in body: %s", body[:min(500, len(body))])
	}
	// Zero writes.
	if len(fs.updateCalls) != 0 {
		t.Errorf("D5: want 0 store mutations for zero-match, got %d", len(fs.updateCalls))
	}
}

// TestTagScope_InvalidFacet_Departamento verifies that an invalid facet "departamento"
// returns HTTP 400 (allow-list guard, REQ-51, D3).
func TestTagScope_InvalidFacet_Departamento(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"departamento"}, "value": {"x"}, "scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400 for invalid facet 'departamento', got %d", rec.Code)
	}
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 store calls for invalid facet, got %d", len(fs.updateCalls))
	}
}

// TestTagScope_InvalidFacet_Proyecto verifies that facet "proyecto" is also rejected.
func TestTagScope_InvalidFacet_Proyecto(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"proyecto"}, "value": {"x"}, "scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400 for invalid facet 'proyecto', got %d", rec.Code)
	}
}

// TestTagScope_InvalidScope verifies that scope values other than "shared"/"personal"
// return HTTP 400.
func TestTagScope_InvalidScope(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"x"}, "scope": {"department"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400 for invalid scope 'department', got %d", rec.Code)
	}
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 store calls for invalid scope, got %d", len(fs.updateCalls))
	}
}

// TestTagScope_StoreError verifies that a store error on ObservationsByTag returns 500.
func TestTagScope_StoreError(t *testing.T) {
	fs := &fakeMutableStore{tagErr: errFakeStore}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"x"}, "scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("want 500 on store error, got %d", rec.Code)
	}
}

// TestTagScope_SkipAlreadyAtTarget verifies that the bulk loop skips observations
// already at the target scope (same guard as handleSetProjectScope, Scenario C-06).
func TestTagScope_SkipAlreadyAtTarget(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	// obs[0] is already "team" (target for "shared"); obs[1] is "personal" and must be updated.
	obs := []store.Observation{
		{ID: 1, Scope: "team", Project: ptrStr("proj")},     // already at target
		{ID: 2, Scope: "personal", Project: ptrStr("proj")}, // needs update
	}
	fs := &fakeMutableStore{tagObs: obs}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"game-x"}, "scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", rec.Code, rec.Body.String())
	}
	// Only the non-target obs should be updated.
	if len(fs.updateCalls) != 1 {
		t.Errorf("want 1 UpdateObservationScope call (skip already-at-target), got %d: %+v", len(fs.updateCalls), fs.updateCalls)
	}
	if len(fs.updateCalls) == 1 && fs.updateCalls[0].ID != 2 {
		t.Errorf("want update for id=2, got id=%d", fs.updateCalls[0].ID)
	}
}

// TestTagScope_NoConfigWrite verifies that handleTagScope does NOT call
// WriteProjectDefaultScope (per-tag action must not update whole-project default, D4/design).
func TestTagScope_NoConfigWrite(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Scope: "personal", Project: ptrStr("proj")},
	}
	dir := t.TempDir()
	fs := &fakeMutableStore{tagObs: obs}
	srv := newMutableSrvWithCwd(fs, dir, "proj")
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"x"}, "scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d; body: %s", rec.Code, rec.Body.String())
	}
	// config.json must NOT be written by tag-scope (D4: only set-scope writes config).
	configPath := dir + "/.engram/config.json"
	if _, err := os.Stat(configPath); err == nil {
		t.Errorf("D4: config.json MUST NOT be written by handleTagScope, but file exists at %s", configPath)
	}
}

// TestTagScope_EmptyValue verifies that POSTing an empty value="" returns 400
// without touching the store. Fix 1; RED before the empty-value guard was added
// (the handler would proceed to ObservationsByTag with an empty string).
func TestTagScope_EmptyValue(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {""}, "scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty value, got %d; body: %s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) == "" {
		t.Error("want non-empty error message in response body")
	}
	// Zero store calls — the guard must fire before any store access.
	if fs.lastTagFacet != "" || fs.lastTagValue != "" {
		t.Errorf("want zero store calls for empty value, but ObservationsByTag was called with facet=%q value=%q",
			fs.lastTagFacet, fs.lastTagValue)
	}
	if len(fs.updateCalls) != 0 {
		t.Errorf("want 0 update calls for empty value, got %d", len(fs.updateCalls))
	}
}

// TestTagScope_ZeroMatchWithConfirm verifies that the D5 zero-match guard fires
// BEFORE the confirm check: even with confirm=1, an empty tagObs set must block
// the update and show the empty/"No observations" page. Scenario B-07 + confirm edge.
func TestTagScope_ZeroMatchWithConfirm(t *testing.T) {
	fs := &fakeMutableStore{tagObs: []store.Observation{}} // empty result
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"nonexistent"}, "scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Zero-match guard must produce a 200 empty page, NOT proceed to bulk update.
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (zero-match page) even with confirm=1, got %d; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No observations") {
		t.Errorf("want 'No observations' in body; body: %s", body[:min(500, len(body))])
	}
	// Zero writes — the bulk loop must never run when there are no matches.
	if len(fs.updateCalls) != 0 {
		t.Errorf("D5: zero-match guard must block confirm; got %d update call(s)", len(fs.updateCalls))
	}
}

// TestTagScope_UpdateError verifies that a store error during bulk update returns 500.
// One matching obs with a mismatched scope triggers UpdateObservationScope, which fails.
func TestTagScope_UpdateError(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := []store.Observation{
		{ID: 1, Scope: "personal", Project: ptrStr("proj")},
	}
	fs := &fakeMutableStore{tagObs: obs, updateErr: errFakeStore}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"game-x"}, "scope": {"shared"}, "confirm": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/project/proj/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("want 500 on update error, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

// ─── handleTagValues: GET /project/{name}/tag-values?facet=X ─────────────────

// TestTagValues_StoreError verifies that a DistinctTagValues store error returns 500.
// Scenario: error path in handleTagValues when the store is available but fails.
func TestTagValues_StoreError(t *testing.T) {
	fs := &fakeMutableStore{tagValErr: errFakeStore}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/proj/tag-values?facet=juego", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("want 500 on DistinctTagValues store error, got %d; body: %s", rec.Code, rec.Body.String())
	}
}

// TestTagValues_JuegoReturnsOptions verifies that GET tag-values?facet=juego
// returns an HTML <option> list for the given values.
func TestTagValues_JuegoReturnsOptions(t *testing.T) {
	fs := &fakeMutableStore{tagValues: []string{"game-x", "game-y"}}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/proj/tag-values?facet=juego", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "game-x") {
		t.Errorf("want 'game-x' in tag-values response; body: %s", body)
	}
	if !strings.Contains(body, "game-y") {
		t.Errorf("want 'game-y' in tag-values response; body: %s", body)
	}
	// Must include <option> tags for htmx swap.
	if !strings.Contains(body, "<option") {
		t.Errorf("want <option> elements in tag-values fragment; body: %s", body)
	}
}

// TestTagValues_EmptyReturnsHint verifies that when DistinctTagValues returns an
// empty slice, the fragment still renders with an <option> element and a hint text.
// Scenario B-06.
func TestTagValues_EmptyReturnsHint(t *testing.T) {
	fs := &fakeMutableStore{tagValues: []string{}}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/project/proj/tag-values?facet=tipo", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for empty values, got %d; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must contain an <option> element for htmx swap compatibility.
	if !strings.Contains(body, "<option") {
		t.Errorf("want <option> element in empty tag-values fragment; body: %s", body)
	}
	// Must show a hint that no values exist.
	if !strings.Contains(body, "No values") {
		t.Errorf("want 'No values' hint text in empty tag-values fragment; body: %s", body)
	}
}

// TestTagValues_InvalidFacet verifies that a missing or invalid facet returns 400.
func TestTagValues_InvalidFacet(t *testing.T) {
	cases := []struct {
		name  string
		facet string
	}{
		{"missing", ""},
		{"invalid proyecto", "proyecto"},
		{"invalid departamento", "departamento"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeMutableStore{}
			srv := newMutableSrv(fs)
			h := srv.Handler()

			target := "/project/proj/tag-values"
			if tc.facet != "" {
				target += "?facet=" + tc.facet
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("facet=%q: want 400, got %d", tc.facet, rec.Code)
			}
		})
	}
}

// ─── Golden tests for tag-scope pages ────────────────────────────────────────

// TestTagScopeConfirmPage_SharedGolden renders the tag-scope confirm page for → shared
// and compares to a golden file.
// Update with: go test ./internal/triage/... -run TestTagScopeConfirmPage_SharedGolden -update
func TestTagScopeConfirmPage_SharedGolden(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := make([]store.Observation, 30)
	for i := range obs {
		obs[i] = store.Observation{ID: int64(i + 1), Scope: "personal", Project: ptrStr("alpha")}
	}
	fs := &fakeMutableStore{tagObs: obs}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"juego"}, "value": {"game-x"}, "scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/alpha/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	got, _ := io.ReadAll(rec.Body)
	goldenCheck(t, "tag_scope_confirm_shared", got)
}

// TestTagScopeConfirmPage_PersonalGolden renders the tag-scope confirm page for → personal
// and compares to a golden file.
// Update with: go test ./internal/triage/... -run TestTagScopeConfirmPage_PersonalGolden -update
func TestTagScopeConfirmPage_PersonalGolden(t *testing.T) {
	ptrStr := func(s string) *string { return &s }
	obs := make([]store.Observation, 15)
	for i := range obs {
		obs[i] = store.Observation{ID: int64(i + 1), Scope: "team", Project: ptrStr("alpha")}
	}
	fs := &fakeMutableStore{tagObs: obs}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"tipo"}, "value": {"decision"}, "scope": {"personal"}}
	req := httptest.NewRequest(http.MethodPost, "/project/alpha/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	got, _ := io.ReadAll(rec.Body)
	goldenCheck(t, "tag_scope_confirm_personal", got)
}

// TestTagScopeEmptyPage_Golden renders the zero-match empty page and compares to golden.
// Update with: go test ./internal/triage/... -run TestTagScopeEmptyPage_Golden -update
func TestTagScopeEmptyPage_Golden(t *testing.T) {
	fs := &fakeMutableStore{tagObs: []store.Observation{}}
	srv := newMutableSrv(fs)
	h := srv.Handler()

	form := url.Values{"facet": {"tipo"}, "value": {"scratch"}, "scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/project/alpha/tag-scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	got, _ := io.ReadAll(rec.Body)
	goldenCheck(t, "tag_scope_empty", got)
}
