package dashboard

// RED → GREEN tests for browser-tab admin scoping (S6 reversal, 2026-07-07).
//
// Previously, handleBrowserObservations / handleBrowserSessions /
// handleBrowserPrompts hardcoded IsAdmin: false in the ReadScope, so admins
// only ever saw their own rows in the Browser tab.
//
// Mario reversed that decision on 2026-07-07: admins should see ALL users'
// data in the Browser tab too. Non-admins are unaffected.
//
// RED phase  — these tests FAIL against the old "IsAdmin: false" code.
// GREEN phase — tests PASS after the three one-line fixes.
//
// Regression guard — non-admin tests pass both before and after the change.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// browserScopeCapturingStub captures the ReadScope passed to each of the three
// browser paginated methods and simulates real store filtering:
//   - scope.IsAdmin == true  → return all rows (admin sees everyone)
//   - scope.IsAdmin == false → return only rows matching scope.Email
//
// It embeds parityStoreStub for all other DashboardStore methods.
type browserScopeCapturingStub struct {
	parityStoreStub
	capturedObsScope      *cloudstore.ReadScope
	capturedSessionsScope *cloudstore.ReadScope
	capturedPromptsScope  *cloudstore.ReadScope
}

func (s *browserScopeCapturingStub) ListRecentObservationsPaginated(
	scope *cloudstore.ReadScope, _, _, _ string, _, _ int,
) ([]cloudstore.DashboardObservationRow, int, error) {
	s.capturedObsScope = scope
	rows := s.parityStoreStub.observations
	if scope != nil && !scope.IsAdmin {
		var filtered []cloudstore.DashboardObservationRow
		for _, r := range rows {
			if strings.EqualFold(r.UserEmail, scope.Email) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	return rows, len(rows), nil
}

func (s *browserScopeCapturingStub) ListRecentSessionsPaginated(
	scope *cloudstore.ReadScope, _, _ string, _, _ int,
) ([]cloudstore.DashboardSessionRow, int, error) {
	s.capturedSessionsScope = scope
	rows := s.parityStoreStub.sessions
	if scope != nil && !scope.IsAdmin {
		var filtered []cloudstore.DashboardSessionRow
		for _, r := range rows {
			if strings.EqualFold(r.UserEmail, scope.Email) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	return rows, len(rows), nil
}

func (s *browserScopeCapturingStub) ListRecentPromptsPaginated(
	scope *cloudstore.ReadScope, _, _ string, _, _ int,
) ([]cloudstore.DashboardPromptRow, int, error) {
	s.capturedPromptsScope = scope
	rows := s.parityStoreStub.prompts
	if scope != nil && !scope.IsAdmin {
		var filtered []cloudstore.DashboardPromptRow
		for _, r := range rows {
			if strings.EqualFold(r.UserEmail, scope.Email) {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	return rows, len(rows), nil
}

// ─── Seed data helpers ──────────────────────────────────────────────────────

// browserScopeSeededStore returns a *browserScopeCapturingStub pre-seeded with
// one observation, one session, and one prompt for each of two users:
//   - mario@vivastudios.com ("Mario obs", "sess-mario-1", "Mario prompt")
//   - sperez@vivastudios.com ("Sperez obs", "sess-sperez-1", "Sperez prompt")
//
// The two users' rows are intentionally distinguishable so tests can assert
// cross-user visibility or the absence of it.
func browserScopeSeededStore() *browserScopeCapturingStub {
	return &browserScopeCapturingStub{
		parityStoreStub: parityStoreStub{
			observations: []cloudstore.DashboardObservationRow{
				{
					Project:   "proj-a",
					SessionID: "sess-mario-1",
					SyncID:    "sync-mario-obs-1",
					ChunkID:   "c1",
					Type:      "decision",
					Title:     "Mario obs",
					UserEmail: "mario@vivastudios.com",
					CreatedAt: "2026-07-07T10:00:00Z",
				},
				{
					Project:   "proj-a",
					SessionID: "sess-sperez-1",
					SyncID:    "sync-sperez-obs-1",
					ChunkID:   "c2",
					Type:      "decision",
					Title:     "Sperez obs",
					UserEmail: "sperez@vivastudios.com",
					CreatedAt: "2026-07-07T11:00:00Z",
				},
			},
			sessions: []cloudstore.DashboardSessionRow{
				{
					Project:   "proj-a",
					SessionID: "sess-mario-1",
					UserEmail: "mario@vivastudios.com",
					StartedAt: "2026-07-07T10:00:00Z",
				},
				{
					Project:   "proj-a",
					SessionID: "sess-sperez-1",
					UserEmail: "sperez@vivastudios.com",
					StartedAt: "2026-07-07T11:00:00Z",
				},
			},
			prompts: []cloudstore.DashboardPromptRow{
				{
					Project:   "proj-a",
					SessionID: "sess-mario-1",
					SyncID:    "sync-mario-prompt-1",
					ChunkID:   "c1",
					Content:   "Mario prompt",
					UserEmail: "mario@vivastudios.com",
					CreatedAt: "2026-07-07T10:05:00Z",
				},
				{
					Project:   "proj-a",
					SessionID: "sess-sperez-1",
					SyncID:    "sync-sperez-prompt-1",
					ChunkID:   "c2",
					Content:   "Sperez prompt",
					UserEmail: "sperez@vivastudios.com",
					CreatedAt: "2026-07-07T11:05:00Z",
				},
			},
		},
	}
}

// ─── Admin sees other users (RED before fix, GREEN after) ───────────────────

// TestBrowserObservations_AdminSeesOtherUserRows verifies that an admin
// principal browsing observations receives rows owned by other users.
//
// The store is seeded with observations from mario@ and sperez@. The request
// is made as admin mario@. After the fix (IsAdmin: p.IsAdmin()), scope.IsAdmin
// is true and the stub returns all rows, so sperez@'s title appears in the body.
//
// FAILS before fix: handler hardcodes IsAdmin: false → stub filters to mario@'s
// rows only → "Sperez obs" is absent → assertion fails.
// PASSES after fix.
func TestBrowserObservations_AdminSeesOtherUserRows(t *testing.T) {
	stub := browserScopeSeededStore()
	mux := newScopedMux(stub, "mario@vivastudios.com", true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("admin observations: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	// Scope must have been captured with IsAdmin=true.
	if stub.capturedObsScope == nil {
		t.Fatal("admin observations: scope was not captured — handler did not pass scope to store")
	}
	if !stub.capturedObsScope.IsAdmin {
		t.Errorf("admin observations: scope.IsAdmin = false, want true (handler still hardcodes IsAdmin: false)")
	}
	// The admin must see sperez@'s observation even though the request is from mario@.
	body := rec.Body.String()
	if !strings.Contains(body, "Sperez obs") {
		t.Errorf("admin observations: expected sperez@'s observation title %q in body, got body=%q", "Sperez obs", body)
	}
}

// TestBrowserSessions_AdminSeesOtherUserRows verifies that an admin principal
// browsing sessions receives rows owned by other users.
//
// FAILS before fix: IsAdmin: false → only mario@'s session ID appears.
// PASSES after fix.
func TestBrowserSessions_AdminSeesOtherUserRows(t *testing.T) {
	stub := browserScopeSeededStore()
	mux := newScopedMux(stub, "mario@vivastudios.com", true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/sessions?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("admin sessions: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if stub.capturedSessionsScope == nil {
		t.Fatal("admin sessions: scope was not captured")
	}
	if !stub.capturedSessionsScope.IsAdmin {
		t.Errorf("admin sessions: scope.IsAdmin = false, want true")
	}
	body := rec.Body.String()
	// sperez@'s session ID must appear in the rendered list.
	if !strings.Contains(body, "sess-sperez-1") {
		t.Errorf("admin sessions: expected sperez@'s session %q in body, got body=%q", "sess-sperez-1", body)
	}
}

// TestBrowserPrompts_AdminSeesOtherUserRows verifies that an admin principal
// browsing prompts receives rows owned by other users.
//
// FAILS before fix: IsAdmin: false → only mario@'s prompt content appears.
// PASSES after fix.
func TestBrowserPrompts_AdminSeesOtherUserRows(t *testing.T) {
	stub := browserScopeSeededStore()
	mux := newScopedMux(stub, "mario@vivastudios.com", true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/prompts?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("admin prompts: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if stub.capturedPromptsScope == nil {
		t.Fatal("admin prompts: scope was not captured")
	}
	if !stub.capturedPromptsScope.IsAdmin {
		t.Errorf("admin prompts: scope.IsAdmin = false, want true")
	}
	body := rec.Body.String()
	// sperez@'s prompt content must appear in the rendered list.
	if !strings.Contains(body, "Sperez prompt") {
		t.Errorf("admin prompts: expected sperez@'s prompt content %q in body, got body=%q", "Sperez prompt", body)
	}
}

// ─── Non-admin still sees only own rows (regression guard) ──────────────────
//
// These tests pass BOTH before and after the fix: the change must not affect
// non-admin principals who must always see only their own data.

// TestBrowserObservations_NonAdminSeesOnlyOwnRows is the regression guard for
// observations. A non-admin request must see mario@'s obs but NOT sperez@'s.
func TestBrowserObservations_NonAdminSeesOnlyOwnRows(t *testing.T) {
	stub := browserScopeSeededStore()
	mux := newScopedMux(stub, "mario@vivastudios.com", false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/observations?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("non-admin observations: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Mario obs") {
		t.Errorf("non-admin observations: expected own title %q in body, got body=%q", "Mario obs", body)
	}
	if strings.Contains(body, "Sperez obs") {
		t.Errorf("non-admin observations: sperez@'s title %q must NOT appear for non-admin, got body=%q", "Sperez obs", body)
	}
}

// TestBrowserSessions_NonAdminSeesOnlyOwnRows is the regression guard for sessions.
func TestBrowserSessions_NonAdminSeesOnlyOwnRows(t *testing.T) {
	stub := browserScopeSeededStore()
	mux := newScopedMux(stub, "mario@vivastudios.com", false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/sessions?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("non-admin sessions: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "sess-mario-1") {
		t.Errorf("non-admin sessions: expected own session %q in body, got body=%q", "sess-mario-1", body)
	}
	if strings.Contains(body, "sess-sperez-1") {
		t.Errorf("non-admin sessions: sperez@'s session %q must NOT appear for non-admin, got body=%q", "sess-sperez-1", body)
	}
}

// TestBrowserPrompts_NonAdminSeesOnlyOwnRows is the regression guard for prompts.
func TestBrowserPrompts_NonAdminSeesOnlyOwnRows(t *testing.T) {
	stub := browserScopeSeededStore()
	mux := newScopedMux(stub, "mario@vivastudios.com", false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/browser/prompts?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("non-admin prompts: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Mario prompt") {
		t.Errorf("non-admin prompts: expected own prompt %q in body, got body=%q", "Mario prompt", body)
	}
	if strings.Contains(body, "Sperez prompt") {
		t.Errorf("non-admin prompts: sperez@'s prompt %q must NOT appear for non-admin, got body=%q", "Sperez prompt", body)
	}
}
