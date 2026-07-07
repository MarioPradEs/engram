package dashboard

// Tests for S6: Brain nav restructure.
// Covers:
//   1. Nav structure: member sees Brain/Dashboard/Browser/Projects (no Contributors/Admin).
//   2. Nav structure: admin sees Brain/Dashboard/Browser/Projects/Contributors/Admin.
//   3. GET /dashboard/brain serves 200 with iframe when BrainURL is set.
//   4. GET /dashboard/graph redirects to /dashboard/brain (301/302).
//   5. dashboardHomePath() returns /dashboard/brain when BrainURL is set.
//   6. Browser tab scope (S6 reversal 2026-07-07): admin now sees ALL data; non-admin sees only own.
//   7. Contributors: admin → 200 + sees other user's data (global scope).
//   8. Contributors: non-admin → 403.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// newMuxWithEmailAndAdmin builds a test mux that gates auth via ?auth=ok,
// exposes GetUserEmail (for scope enforcement), and has a configurable isAdmin.
// brainURL can be empty or a valid https URL.
func newMuxWithEmailAndAdmin(store DashboardStore, userEmail string, isAdmin bool, brainURL string) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:      func(_ *http.Request) bool { return isAdmin },
		GetUserEmail: func(_ *http.Request) string { return userEmail },
		BrainURL:     brainURL,
		Store:        store,
	})
	return mux
}

// ─── Nav structure tests ─────────────────────────────────────────────────────

// TestNavTabsMemberSeesOnlyPublicTabs verifies that a non-admin user sees exactly
// Brain, Dashboard, Browser, Projects — in that order — and NOT Contributors or Admin.
func TestNavTabsMemberSeesOnlyPublicTabs(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()

	// Must contain the four public tab hrefs.
	for _, href := range []string{
		`href="/dashboard/brain"`,
		`href="/dashboard/"`,
		`href="/dashboard/browser"`,
		`href="/dashboard/projects"`,
	} {
		if !strings.Contains(body, href) {
			t.Errorf("member nav: expected %q in body", href)
		}
	}

	// Must NOT contain Contributors or Admin tabs.
	for _, href := range []string{
		`href="/dashboard/contributors"`,
		`href="/dashboard/admin"`,
	} {
		if strings.Contains(body, href) {
			t.Errorf("member nav: must NOT contain %q in body", href)
		}
	}

	// Graph href must NOT be present (renamed to brain).
	if strings.Contains(body, `href="/dashboard/graph"`) {
		t.Errorf("member nav: legacy /dashboard/graph href must not appear in nav")
	}
}

// TestNavTabsAdminSeesContributorsAndAdmin verifies that an admin user sees all
// six tabs: Brain, Dashboard, Browser, Projects, Contributors, Admin.
func TestNavTabsAdminSeesContributorsAndAdmin(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, true)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()

	for _, href := range []string{
		`href="/dashboard/brain"`,
		`href="/dashboard/"`,
		`href="/dashboard/browser"`,
		`href="/dashboard/projects"`,
		`href="/dashboard/contributors"`,
		`href="/dashboard/admin"`,
	} {
		if !strings.Contains(body, href) {
			t.Errorf("admin nav: expected %q in body", href)
		}
	}

	// Legacy graph href must not appear.
	if strings.Contains(body, `href="/dashboard/graph"`) {
		t.Errorf("admin nav: legacy /dashboard/graph href must not appear in nav")
	}
}

// ─── Brain route tests ───────────────────────────────────────────────────────

// TestBrainRouteServes200WithIframe verifies that GET /dashboard/brain returns 200
// and contains an iframe when BrainURL is configured.
func TestBrainRouteServes200WithIframe(t *testing.T) {
	mux := newMuxWithEmailAndAdmin(parityStoreStub{}, "user@example.com", false, "https://brain.example.com/")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/brain?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<iframe") {
		t.Errorf("expected <iframe> element in brain page body when BrainURL is set, got %q", body)
	}
	if !strings.Contains(body, "brain.example.com") {
		t.Errorf("expected BrainURL in iframe src, got body=%q", body)
	}
}

// TestBrainRouteFallbackWhenNoBrainURL verifies that /dashboard/brain shows a placeholder
// when BrainURL is empty.
func TestBrainRouteFallbackWhenNoBrainURL(t *testing.T) {
	mux := newMuxWithEmailAndAdmin(parityStoreStub{}, "user@example.com", false, "")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/brain?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<iframe") {
		t.Errorf("expected NO iframe when BrainURL is empty, got %q", body)
	}
	if !strings.Contains(body, "ENGRAM_BRAIN_URL") {
		t.Errorf("expected fallback text mentioning ENGRAM_BRAIN_URL, got %q", body)
	}
}

// TestGraphRouteRedirectsToBrain verifies that GET /dashboard/graph redirects
// to /dashboard/brain with a 301 or 302 status code.
func TestGraphRouteRedirectsToBrain(t *testing.T) {
	mux := newAuthedMux(parityStoreStub{}, false)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/graph?auth=ok", nil))
	if rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusFound {
		t.Fatalf("expected 301 or 302 redirect for /dashboard/graph, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "/dashboard/brain" {
		t.Errorf("expected redirect Location to be /dashboard/brain, got %q", loc)
	}
}

// ─── dashboardHomePath with BrainURL ────────────────────────────────────────

// TestPostLoginLandingWithBrainURLIsBrain updates the S5 test: with BrainURL set,
// post-login must redirect to /dashboard/brain (not /dashboard/graph).
func TestPostLoginLandingWithBrainURLIsBrain(t *testing.T) {
	mux := newMuxWithBrainURL("https://brain.example.com/")
	rec := httptest.NewRecorder()
	form := strings.NewReader("token=any-valid-token")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 SeeOther, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "/dashboard/brain" {
		t.Errorf("post-login with BrainURL = %q, want /dashboard/brain", loc)
	}
}

// ─── Browser scope: admin sees ALL data (S6 reversal 2026-07-07) ───────────

// scopedObsStore is a store stub that honours ReadScope: returns only email-matched
// rows when scope.IsAdmin is false, and returns all rows when scope.IsAdmin is true.
// After the S6 reversal, admin requests carry IsAdmin: true so all rows appear.
type scopedObsStore struct {
	parityStoreStub
}

func (s scopedObsStore) ListRecentObservationsPaginated(scope *cloudstore.ReadScope, _, _, _ string, _, _ int) ([]cloudstore.DashboardObservationRow, int, error) {
	all := s.observations
	if scope == nil || scope.IsAdmin {
		return all, len(all), nil
	}
	var filtered []cloudstore.DashboardObservationRow
	for _, o := range all {
		if strings.EqualFold(o.UserEmail, scope.Email) {
			filtered = append(filtered, o)
		}
	}
	return filtered, len(filtered), nil
}

// TestBrowserObservationsAdminSeesAllData verifies that an admin requesting
// /dashboard/browser/observations sees ALL users' observations (S6 reversal 2026-07-07).
// The seeded data has alice's and bob's observations; alice (admin) must see both.
//
// Renamed and inverted from TestBrowserObservationsAdminSeesOnlyOwnData: the original
// test asserted admin only sees own data. Mario reversed that decision on 2026-07-07,
// so this test now asserts the new behavior (admin global scope in Browser tab).
func TestBrowserObservationsAdminSeesAllData(t *testing.T) {
	store := scopedObsStore{
		parityStoreStub: parityStoreStub{
			observations: []cloudstore.DashboardObservationRow{
				{Project: "proj-a", SessionID: "s1", SyncID: "obs-alice", Type: "decision",
					Title: "Alice observation", UserEmail: "alice@example.com", CreatedAt: "2026-06-01T10:00:00Z"},
				{Project: "proj-a", SessionID: "s2", SyncID: "obs-bob", Type: "bugfix",
					Title: "Bob observation", UserEmail: "bob@example.com", CreatedAt: "2026-06-01T11:00:00Z"},
			},
		},
	}
	// Alice is admin; Browser must now return ALL observations (global scope).
	mux := newMuxWithEmailAndAdmin(store, "alice@example.com", true, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Alice observation") {
		t.Errorf("admin browser observations: expected alice's own observation, got body=%q", body)
	}
	// After S6 reversal: admin MUST see other users' observations too.
	if !strings.Contains(body, "Bob observation") {
		t.Errorf("admin browser observations: expected bob's observation (admin global scope), got body=%q", body)
	}
}

// ─── Contributors route: admin-gated, global scope ──────────────────────────

// TestContributorsRouteNonAdminGets403 verifies that a non-admin user hitting
// /dashboard/contributors gets a 403 response.
func TestContributorsRouteNonAdminGets403(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "alice@example.com", Chunks: 5, Projects: 2},
		},
	}
	mux := newMuxWithEmailAndAdmin(store, "member@example.com", false, "")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/contributors?auth=ok", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin on /dashboard/contributors, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// contributorsGlobalStore is a store that returns all observations regardless of scope,
// simulating the global (admin) read path used by Contributors.
type contributorsGlobalStore struct {
	parityStoreStub
}

func (s contributorsGlobalStore) ListContributorsPaginated(_ *cloudstore.ReadScope, _ string, _, _ int) ([]cloudstore.DashboardContributorRow, int, error) {
	return s.contributors, len(s.contributors), nil
}

// TestContributorsRouteAdminSeesGlobalData verifies that an admin hitting
// /dashboard/contributors gets a 200 and sees all contributors (global scope).
func TestContributorsRouteAdminSeesGlobalData(t *testing.T) {
	store := contributorsGlobalStore{
		parityStoreStub: parityStoreStub{
			contributors: []cloudstore.DashboardContributorRow{
				{CreatedBy: "alice@example.com", Chunks: 5, Projects: 2},
				{CreatedBy: "bob@example.com", Chunks: 3, Projects: 1},
			},
		},
	}
	mux := newMuxWithEmailAndAdmin(store, "alice@example.com", true, "")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/contributors?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin on /dashboard/contributors, got %d body=%q", rec.Code, rec.Body.String())
	}
	// The Contributors page shell should render the CONTRIBUTOR SIGNAL heading.
	body := rec.Body.String()
	if !strings.Contains(body, "CONTRIBUTOR SIGNAL") {
		t.Errorf("expected CONTRIBUTOR SIGNAL heading in contributors page, got body=%q", body)
	}
}

// TestContributorsListAdminSeesGlobalContributors verifies that the list partial
// /dashboard/contributors/list returns all contributors for an admin (global scope).
func TestContributorsListAdminSeesGlobalContributors(t *testing.T) {
	store := contributorsGlobalStore{
		parityStoreStub: parityStoreStub{
			contributors: []cloudstore.DashboardContributorRow{
				{CreatedBy: "alice@example.com", Chunks: 5, Projects: 2},
				{CreatedBy: "bob@example.com", Chunks: 3, Projects: 1},
			},
		},
	}
	mux := newMuxWithEmailAndAdmin(store, "alice@example.com", true, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/contributors/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin on /dashboard/contributors/list, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice@example.com") {
		t.Errorf("expected alice in contributors list, got body=%q", body)
	}
	if !strings.Contains(body, "bob@example.com") {
		t.Errorf("expected bob in contributors list (global scope), got body=%q", body)
	}
}

// TestContributorsListNonAdminGets403 verifies that the list partial also
// returns 403 for non-admin users.
func TestContributorsListNonAdminGets403(t *testing.T) {
	mux := newMuxWithEmailAndAdmin(parityStoreStub{}, "member@example.com", false, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/contributors/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin on /dashboard/contributors/list, got %d", rec.Code)
	}
}

// TestContributorDetailNonAdminGets403 verifies that a non-admin user hitting
// GET /dashboard/contributors/{contributor} receives 403 Forbidden.
// This is the C1/C2 privacy-gate test: must FAIL before the C1 admin gate is added
// to handleContributorDetail, and PASS after.
func TestContributorDetailNonAdminGets403(t *testing.T) {
	store := parityStoreStub{
		contributors: []cloudstore.DashboardContributorRow{
			{CreatedBy: "bob@example.com", Chunks: 3, Projects: 1},
		},
	}
	// member@example.com is NOT an admin.
	mux := newMuxWithEmailAndAdmin(store, "member@example.com", false, "")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/contributors/bob@example.com?auth=ok", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin on GET /dashboard/contributors/{contributor}, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── W3: Browser sessions/prompts scope enforcement ─────────────────────────

// scopedSessionStore honours ReadScope for sessions: returns only the rows whose
// UserEmail matches scope.Email when scope.IsAdmin is false.
type scopedSessionStore struct {
	parityStoreStub
}

func (s scopedSessionStore) ListRecentSessionsPaginated(scope *cloudstore.ReadScope, _, _ string, _, _ int) ([]cloudstore.DashboardSessionRow, int, error) {
	all := s.sessions
	if scope == nil || scope.IsAdmin {
		return all, len(all), nil
	}
	var filtered []cloudstore.DashboardSessionRow
	for _, row := range all {
		if strings.EqualFold(row.UserEmail, scope.Email) {
			filtered = append(filtered, row)
		}
	}
	return filtered, len(filtered), nil
}

// scopedPromptStore honours ReadScope for prompts: returns only the rows whose
// UserEmail matches scope.Email when scope.IsAdmin is false.
type scopedPromptStore struct {
	parityStoreStub
}

func (s scopedPromptStore) ListRecentPromptsPaginated(scope *cloudstore.ReadScope, _, _ string, _, _ int) ([]cloudstore.DashboardPromptRow, int, error) {
	all := s.prompts
	if scope == nil || scope.IsAdmin {
		return all, len(all), nil
	}
	var filtered []cloudstore.DashboardPromptRow
	for _, row := range all {
		if strings.EqualFold(row.UserEmail, scope.Email) {
			filtered = append(filtered, row)
		}
	}
	return filtered, len(filtered), nil
}

// TestBrowserSessionsAdminSeesAllData mirrors TestBrowserObservationsAdminSeesAllData
// for /dashboard/browser/sessions. Seeds 2-user data; asserts alice (admin) sees both
// her own and bob's sessions (global scope — S6 reversal 2026-07-07).
//
// Renamed and inverted from TestBrowserSessionsAdminSeesOnlyOwnData.
func TestBrowserSessionsAdminSeesAllData(t *testing.T) {
	store := scopedSessionStore{
		parityStoreStub: parityStoreStub{
			sessions: []cloudstore.DashboardSessionRow{
				{Project: "proj-a", SessionID: "sess-alice", UserEmail: "alice@example.com", StartedAt: "2026-06-01T10:00:00Z"},
				{Project: "proj-a", SessionID: "sess-bob", UserEmail: "bob@example.com", StartedAt: "2026-06-01T11:00:00Z"},
			},
		},
	}
	// Alice is admin; Browser must now return ALL sessions (global scope).
	mux := newMuxWithEmailAndAdmin(store, "alice@example.com", true, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/sessions?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "sess-alice") {
		t.Errorf("admin browser sessions: expected alice's session, got body=%q", body)
	}
	// After S6 reversal: admin MUST see other users' sessions too.
	if !strings.Contains(body, "sess-bob") {
		t.Errorf("admin browser sessions: expected bob's session (admin global scope), got body=%q", body)
	}
}

// TestBrowserPromptsAdminSeesAllData mirrors TestBrowserObservationsAdminSeesAllData
// for /dashboard/browser/prompts. Seeds 2-user data; asserts alice (admin) sees both
// her own and bob's prompts (global scope — S6 reversal 2026-07-07).
//
// Renamed and inverted from TestBrowserPromptsAdminSeesOnlyOwnData.
func TestBrowserPromptsAdminSeesAllData(t *testing.T) {
	store := scopedPromptStore{
		parityStoreStub: parityStoreStub{
			prompts: []cloudstore.DashboardPromptRow{
				{Project: "proj-a", SessionID: "s1", UserEmail: "alice@example.com", Content: "Alice prompt", CreatedAt: "2026-06-01T10:00:00Z"},
				{Project: "proj-a", SessionID: "s2", UserEmail: "bob@example.com", Content: "Bob prompt", CreatedAt: "2026-06-01T11:00:00Z"},
			},
		},
	}
	// Alice is admin; Browser must now return ALL prompts (global scope).
	mux := newMuxWithEmailAndAdmin(store, "alice@example.com", true, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/prompts?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Alice prompt") {
		t.Errorf("admin browser prompts: expected alice's prompt, got body=%q", body)
	}
	// After S6 reversal: admin MUST see other users' prompts too.
	if !strings.Contains(body, "Bob prompt") {
		t.Errorf("admin browser prompts: expected bob's prompt (admin global scope), got body=%q", body)
	}
}

// ─── S1: contributorsGlobalStore — capture scope and assert admin-global pass-through ───

// contributorsGlobalScopeStore extends contributorsGlobalStore to capture the last
// ReadScope passed to ListContributorsPaginated, enabling the test to assert that
// the handler called with IsAdmin: true (global scope).
type contributorsGlobalScopeStore struct {
	parityStoreStub
	lastScope *cloudstore.ReadScope
}

func (s *contributorsGlobalScopeStore) ListContributorsPaginated(scope *cloudstore.ReadScope, _ string, _, _ int) ([]cloudstore.DashboardContributorRow, int, error) {
	s.lastScope = scope
	return s.contributors, len(s.contributors), nil
}

// TestContributorsRouteAdminSeesGlobalData_ScopeContract extends the existing
// contributors-global test with a real contract assertion: verifies that the handler
// passes IsAdmin: true to the store, so the test fails if the handler regresses to
// own-scope (IsAdmin: false).
func TestContributorsRouteAdminSeesGlobalData_ScopeContract(t *testing.T) {
	store := &contributorsGlobalScopeStore{
		parityStoreStub: parityStoreStub{
			contributors: []cloudstore.DashboardContributorRow{
				{CreatedBy: "alice@example.com", Chunks: 5, Projects: 2},
				{CreatedBy: "bob@example.com", Chunks: 3, Projects: 1},
			},
		},
	}
	mux := newMuxWithEmailAndAdmin(store, "alice@example.com", true, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/contributors/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin on /dashboard/contributors/list, got %d body=%q", rec.Code, rec.Body.String())
	}
	if store.lastScope == nil {
		t.Fatal("expected handler to pass a non-nil ReadScope to the store")
	}
	if !store.lastScope.IsAdmin {
		t.Errorf("S1 contract: handler must pass IsAdmin=true to store for Contributors (global scope), got IsAdmin=%v", store.lastScope.IsAdmin)
	}
}
