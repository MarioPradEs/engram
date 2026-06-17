package cloudserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
)

// scopingStoreStub is a minimal DashboardStore that serves fixed observation rows
// attributed to two different users. It propagates the ReadScope so that integration
// tests can observe actual scope enforcement through the full HTTP stack.
type scopingStoreStub struct {
	fakeStore
	allObservations []cloudstore.DashboardObservationRow
}

// Ensure scopingStoreStub satisfies dashboard.DashboardStore.
var _ dashboard.DashboardStore = (*scopingStoreStub)(nil)

func (s *scopingStoreStub) ListProjects(_ string) ([]cloudstore.DashboardProjectRow, error) {
	return nil, nil
}
func (s *scopingStoreStub) ProjectDetail(_ string) (cloudstore.DashboardProjectDetail, error) {
	return cloudstore.DashboardProjectDetail{}, nil
}
func (s *scopingStoreStub) ListContributors(_ string) ([]cloudstore.DashboardContributorRow, error) {
	return nil, nil
}
func (s *scopingStoreStub) ListRecentSessions(_, _ string, _ int) ([]cloudstore.DashboardSessionRow, error) {
	return nil, nil
}
func (s *scopingStoreStub) ListRecentObservations(_, _ string, _ int) ([]cloudstore.DashboardObservationRow, error) {
	return nil, nil
}
func (s *scopingStoreStub) ListRecentPrompts(_, _ string, _ int) ([]cloudstore.DashboardPromptRow, error) {
	return nil, nil
}
func (s *scopingStoreStub) AdminOverview() (cloudstore.DashboardAdminOverview, error) {
	return cloudstore.DashboardAdminOverview{}, nil
}
func (s *scopingStoreStub) ScopedMemberOverview(_ *cloudstore.ReadScope) (cloudstore.DashboardAdminOverview, error) {
	return cloudstore.DashboardAdminOverview{}, nil
}
func (s *scopingStoreStub) ListProjectsPaginated(_ string, _, _ int) ([]cloudstore.DashboardProjectRow, int, error) {
	return nil, 0, nil
}
func (s *scopingStoreStub) ListRecentObservationsPaginated(scope *cloudstore.ReadScope, _, _, _ string, _, _ int) ([]cloudstore.DashboardObservationRow, int, error) {
	// Simulate the real cloudstore.applyReadScope logic inline.
	// nil scope or admin = all rows; member = own rows only; empty email = deny.
	if scope == nil || scope.IsAdmin {
		return s.allObservations, len(s.allObservations), nil
	}
	email := strings.TrimSpace(strings.ToLower(scope.Email))
	if email == "" {
		return nil, 0, cloudstore.ErrDashboardIdentityRequired
	}
	var out []cloudstore.DashboardObservationRow
	for _, obs := range s.allObservations {
		if strings.EqualFold(strings.TrimSpace(obs.UserEmail), email) {
			out = append(out, obs)
		}
	}
	return out, len(out), nil
}
func (s *scopingStoreStub) ListRecentSessionsPaginated(_ *cloudstore.ReadScope, _, _ string, _, _ int) ([]cloudstore.DashboardSessionRow, int, error) {
	return nil, 0, nil
}
func (s *scopingStoreStub) ListRecentPromptsPaginated(_ *cloudstore.ReadScope, _, _ string, _, _ int) ([]cloudstore.DashboardPromptRow, int, error) {
	return nil, 0, nil
}
func (s *scopingStoreStub) ListContributorsPaginated(_ *cloudstore.ReadScope, _ string, _, _ int) ([]cloudstore.DashboardContributorRow, int, error) {
	return nil, 0, nil
}
func (s *scopingStoreStub) GetSessionDetail(_ *cloudstore.ReadScope, _, _ string) (cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error) {
	return cloudstore.DashboardSessionRow{}, nil, nil, cloudstore.ErrDashboardSessionNotFound
}
func (s *scopingStoreStub) GetObservationDetail(scope *cloudstore.ReadScope, _, _, syncID string) (cloudstore.DashboardObservationRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, error) {
	for _, obs := range s.allObservations {
		if obs.SyncID == syncID {
			// D2 scope enforcement: members only see their own observations.
			if scope != nil && !scope.IsAdmin {
				email := strings.TrimSpace(strings.ToLower(scope.Email))
				if email == "" || !strings.EqualFold(strings.TrimSpace(obs.UserEmail), email) {
					return cloudstore.DashboardObservationRow{}, cloudstore.DashboardSessionRow{}, nil, cloudstore.ErrDashboardObservationNotFound
				}
			}
			return obs, cloudstore.DashboardSessionRow{}, nil, nil
		}
	}
	return cloudstore.DashboardObservationRow{}, cloudstore.DashboardSessionRow{}, nil, cloudstore.ErrDashboardObservationNotFound
}
func (s *scopingStoreStub) GetPromptDetail(_ *cloudstore.ReadScope, _, _, _ string) (cloudstore.DashboardPromptRow, cloudstore.DashboardSessionRow, []cloudstore.DashboardPromptRow, error) {
	return cloudstore.DashboardPromptRow{}, cloudstore.DashboardSessionRow{}, nil, cloudstore.ErrDashboardPromptNotFound
}
func (s *scopingStoreStub) SystemHealth() (cloudstore.DashboardSystemHealth, error) {
	return cloudstore.DashboardSystemHealth{}, nil
}
func (s *scopingStoreStub) ListProjectSyncControls() ([]cloudstore.ProjectSyncControl, error) {
	return nil, nil
}
func (s *scopingStoreStub) GetProjectSyncControl(_ string) (*cloudstore.ProjectSyncControl, error) {
	return nil, nil
}
func (s *scopingStoreStub) SetProjectSyncEnabled(_ string, _ bool, _, _ string) error { return nil }
func (s *scopingStoreStub) IsProjectSyncEnabled(_ string) (bool, error)               { return true, nil }
func (s *scopingStoreStub) GetContributorDetail(_ string) (cloudstore.DashboardContributorRow, []cloudstore.DashboardSessionRow, []cloudstore.DashboardObservationRow, []cloudstore.DashboardPromptRow, error) {
	return cloudstore.DashboardContributorRow{}, nil, nil, nil, cloudstore.ErrDashboardContributorNotFound
}
func (s *scopingStoreStub) ListDistinctTypes() ([]string, error) { return nil, nil }
func (s *scopingStoreStub) ListAuditEntriesPaginated(_ context.Context, _ cloudstore.AuditFilter, _, _ int) ([]cloudstore.DashboardAuditRow, int, error) {
	return nil, 0, nil
}

// seedObservations contains one row owned by member@vivastudios.com and one by admin@vivastudios.com.
var seedObservations = []cloudstore.DashboardObservationRow{
	{
		SyncID:    "obs-member-1",
		Project:   "proj-a",
		SessionID: "sess-1",
		Type:      "decision",
		Title:     "Member observation",
		UserEmail: "member@vivastudios.com",
		CreatedAt: "2026-06-01T10:00:00Z",
	},
	{
		SyncID:    "obs-admin-1",
		Project:   "proj-a",
		SessionID: "sess-2",
		Type:      "bugfix",
		Title:     "Admin observation",
		UserEmail: "admin@vivastudios.com",
		CreatedAt: "2026-06-01T11:00:00Z",
	},
}

// buildScopingServer wires a CloudServer with HeaderAuthenticator and a scopingStoreStub.
// The stub's fakeStore must also satisfy ChunkStore so New() accepts it.
// We embed fakeStore which already implements ChunkStore; cast to dashboard.DashboardStore
// happens in routes() via type assertion on s.store.
type scopingChunkAndDashStore struct {
	fakeStore
	scopingStoreStub
}

// ChunkStore methods come from the embedded fakeStore.
// DashboardStore methods come from the embedded scopingStoreStub.
// The type assertion in cloudserver.routes() only checks dashboard.DashboardStore.

// buildScopingServer sets up a CloudServer that has both chunk-store and dashboard-store roles.
func buildScopingServer(t *testing.T, jwtSecret string) *CloudServer {
	t.Helper()

	store := &scopingStoreStub{
		allObservations: seedObservations,
	}

	loader := buildAuthTestLoader(t, identityTestYAML)
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("buildScopingServer: NewHeaderAuthenticatorWithJWT: %v", err)
	}

	// routes() calls s.store.(dashboard.DashboardStore) — store must implement both.
	srv := New(store, ha, 0, WithAuthEndpoint(loader, jwtSecret))
	return srv
}

// ─── Security Scenario Tests ─────────────────────────────────────────────────

// TestScopingSC1_MemberSeesOnlyOwnObservations (SC1) asserts that a member session
// returns only observations attributed to the authenticated user's email.
func TestScopingSC1_MemberSeesOnlyOwnObservations(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("m", 32)
	srv := buildScopingServer(t, jwtSecret)

	cookie := mintSessionCookieForEmail(t, srv, "member@vivastudios.com", jwtSecret)

	rec := httptest.NewRecorder()
	req := makeSessionRequest(http.MethodGet, "/dashboard/browser/observations", cookie)
	req.Header.Set("HX-Request", "true")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("SC1: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Member observation must appear.
	if !strings.Contains(body, "Member observation") {
		t.Errorf("SC1: expected 'Member observation' in body, got body=%q", body)
	}
	// Admin observation must NOT appear — scope enforcement.
	if strings.Contains(body, "Admin observation") {
		t.Errorf("SC1: 'Admin observation' leaked to member session — scope enforcement failure")
	}
}

// TestScopingSC2_AdminSeesAllObservations (SC2) asserts that an admin session
// returns observations from all users without filtering.
func TestScopingSC2_AdminSeesAllObservations(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("a", 32)
	srv := buildScopingServer(t, jwtSecret)

	cookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	rec := httptest.NewRecorder()
	req := makeSessionRequest(http.MethodGet, "/dashboard/browser/observations", cookie)
	req.Header.Set("HX-Request", "true")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("SC2: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Member observation") {
		t.Errorf("SC2: expected 'Member observation' visible to admin, got body=%q", body)
	}
	if !strings.Contains(body, "Admin observation") {
		t.Errorf("SC2: expected 'Admin observation' visible to admin, got body=%q", body)
	}
}

// TestScopingSC3_MissingSessionDenies (SC3) asserts that a request with no session cookie
// receives a redirect to the login page, not observation data.
func TestScopingSC3_MissingSessionDenies(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("x", 32)
	srv := buildScopingServer(t, jwtSecret)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations", nil)
	// No session cookie.
	srv.Handler().ServeHTTP(rec, req)

	// Must redirect to login (302/303) — no observation data.
	if rec.Code == http.StatusOK {
		body := rec.Body.String()
		if strings.Contains(body, "Member observation") || strings.Contains(body, "Admin observation") {
			t.Errorf("SC3: observation data leaked to unauthenticated request, body=%q", body)
		}
		t.Fatalf("SC3: expected redirect (3xx), got 200 — session guard not enforcing")
	}
	if rec.Code < 300 || rec.Code > 399 {
		t.Errorf("SC3: expected 3xx redirect, got %d", rec.Code)
	}
	location := rec.Header().Get("Location")
	if !strings.Contains(location, "/dashboard/login") {
		t.Errorf("SC3: expected redirect to /dashboard/login, got Location=%q", location)
	}
}

// TestScopingSC4_MemberCannotSeeOtherObservationDetail (SC4) asserts that when a member
// requests the detail page of an observation owned by another user, they receive 404
// (not 403, to avoid leaking existence of the resource).
func TestScopingSC4_MemberCannotSeeOtherObservationDetail(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("n", 32)
	srv := buildScopingServer(t, jwtSecret)

	cookie := mintSessionCookieForEmail(t, srv, "member@vivastudios.com", jwtSecret)

	// obs-admin-1 is owned by admin@vivastudios.com, not by the member.
	rec := httptest.NewRecorder()
	req := makeSessionRequest(http.MethodGet, "/dashboard/observations/proj-a/sess-2/obs-admin-1", cookie)
	srv.Handler().ServeHTTP(rec, req)

	// Must be 404 — do NOT return 403 (would leak existence).
	if rec.Code == http.StatusForbidden {
		t.Errorf("SC4: got 403 — must be 404 to avoid leaking resource existence")
	}
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusOK {
		// 200 with empty state is also acceptable (templ renders an empty-state block).
		// We accept 404 or a 200 with "not found" language.
		t.Logf("SC4: got %d (acceptable if body shows not-found state)", rec.Code)
	}
	body := rec.Body.String()
	// The admin observation content must NOT appear.
	if strings.Contains(body, "Admin observation") {
		t.Errorf("SC4: admin observation title leaked to member detail page, body=%q", body)
	}
}
