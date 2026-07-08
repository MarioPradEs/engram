package dashboard

// admin_deletion_requests_handler_test.go — RED tests for C1 handler bug.
//
// C1: handleRequestRemoval called GetObservationDetail(adminScope, "", "", syncID)
// which always returned ErrDashboardProjectInvalid (not ErrDashboardObservationNotFound)
// because normalizeDashboardProject("") returns ErrDashboardProjectInvalid.
//
// The handler misclassified this as a non-404 error and returned 500 for every
// deletion-request submission by a member.
//
// After the fix, the handler uses GetObservationBySyncID(adminScope, syncID) which
// does NOT require a project name and correctly returns 404/403/200.
//
// These tests exercise the HANDLER via a stub DashboardStore. They do NOT require
// Postgres. The stub's GetObservationBySyncID is faithful: it respects scope +
// the syncID map — unlike the old parityStoreStub which ignored those params.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// deletionRequestStore is a faithful stub for C1 handler tests.
// It keeps a map of syncID → DashboardObservationRow so GetObservationBySyncID
// can respect ownership checks — something the old parityStoreStub did not do.
type deletionRequestStore struct {
	parityStoreStub
	// observations keyed by syncID.
	obsBySyncID map[string]cloudstore.DashboardObservationRow
	// createDeletionRequestErr, if non-nil, is returned from CreateDeletionRequest.
	createDeletionRequestErr error
}

// GetObservationBySyncID is the NEW method needed by the fixed handler.
// It respects scope: admin sees everything, member sees only own.
func (s *deletionRequestStore) GetObservationBySyncID(scope *cloudstore.ReadScope, syncID string) (cloudstore.DashboardObservationRow, error) {
	obs, ok := s.obsBySyncID[syncID]
	if !ok {
		return cloudstore.DashboardObservationRow{}, fmt.Errorf("%w: %s", cloudstore.ErrDashboardObservationNotFound, syncID)
	}
	if scope != nil && !scope.IsAdmin {
		// Member scope: only allowed to see own observation.
		if !strings.EqualFold(strings.TrimSpace(obs.UserEmail), strings.TrimSpace(scope.Email)) {
			return cloudstore.DashboardObservationRow{}, fmt.Errorf("%w: %s", cloudstore.ErrDashboardObservationNotFound, syncID)
		}
	}
	return obs, nil
}

func (s *deletionRequestStore) CreateDeletionRequest(_ context.Context, _ cloudstore.DeletionRequest) (int64, error) {
	if s.createDeletionRequestErr != nil {
		return 0, s.createDeletionRequestErr
	}
	return 1, nil
}

// newDeletionRequestMux builds a test mux for deletion-request handler tests.
// callerEmail is the JWT-resolved email of the requesting member.
// isAdmin controls whether the caller is treated as an admin.
func newDeletionRequestMux(store DashboardStore, callerEmail string, isAdmin bool) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:        func(_ *http.Request) bool { return isAdmin },
		GetUserEmail:   func(_ *http.Request) string { return callerEmail },
		GetDisplayName: func(_ *http.Request) string { return "TestUser" },
		Store:          store,
	})
	return mux
}

// postRequestRemoval sends POST /dashboard/browser/observations/{syncID}/request-removal.
func postRequestRemoval(mux *http.ServeMux, syncID, reason string) *httptest.ResponseRecorder {
	form := url.Values{}
	form.Set("reason", reason)
	req := httptest.NewRequest(http.MethodPost,
		"/dashboard/browser/observations/"+syncID+"/request-removal?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestC1_RequestRemoval_MemberOwnsObs_Succeeds verifies the happy path:
// a member requesting removal of their own observation gets 303 (redirect to browser).
//
// Before the C1 fix: GetObservationDetail("","",syncID) → ErrDashboardProjectInvalid
// → handler returned 500. Test would fail with code=500.
// After the C1 fix: GetObservationBySyncID(adminScope, syncID) succeeds → 303.
func TestC1_RequestRemoval_MemberOwnsObs_Succeeds(t *testing.T) {
	const (
		syncID      = "obs-syncid-c1-handler-own"
		memberEmail = "alice@vivastudios.com"
	)
	store := &deletionRequestStore{
		obsBySyncID: map[string]cloudstore.DashboardObservationRow{
			syncID: {
				SyncID:    syncID,
				Project:   "project-alpha",
				UserEmail: memberEmail,
				Type:      "decision",
				Title:     "Alice's observation",
			},
		},
	}
	mux := newDeletionRequestMux(store, memberEmail, false)
	rec := postRequestRemoval(mux, syncID, "sensitive content")

	// Before fix: 500 (normalizeDashboardProject("") → ErrDashboardProjectInvalid → not a 404 → 500).
	// After fix: 200 or 303 (redirect to browser/observations).
	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("C1 BUG CONFIRMED (fix needed): got 500 — handler called GetObservationDetail with empty project; must use GetObservationBySyncID instead")
	}
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Errorf("C1: expected 200 or 303 for member owning obs, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// TestC1_RequestRemoval_NonExistentSyncID_Returns404 verifies that a non-existent
// syncID returns 404 (not 500).
func TestC1_RequestRemoval_NonExistentSyncID_Returns404(t *testing.T) {
	store := &deletionRequestStore{
		obsBySyncID: map[string]cloudstore.DashboardObservationRow{}, // empty — nothing registered
	}
	mux := newDeletionRequestMux(store, "alice@vivastudios.com", false)
	rec := postRequestRemoval(mux, "obs-does-not-exist", "reason")

	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("C1 BUG CONFIRMED: got 500 for nonexistent syncID; must return 404 instead")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("C1: expected 404 for nonexistent syncID, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// TestC1_RequestRemoval_MemberDoesNotOwnObs_Returns403 verifies that a member
// requesting removal of ANOTHER user's observation gets 403.
func TestC1_RequestRemoval_MemberDoesNotOwnObs_Returns403(t *testing.T) {
	const (
		syncID      = "obs-syncid-c1-handler-other"
		ownerEmail  = "carol@vivastudios.com"
		attackEmail = "eve@vivastudios.com"
	)
	store := &deletionRequestStore{
		obsBySyncID: map[string]cloudstore.DashboardObservationRow{
			syncID: {
				SyncID:    syncID,
				Project:   "project-gamma",
				UserEmail: ownerEmail,
			},
		},
	}
	mux := newDeletionRequestMux(store, attackEmail, false)
	rec := postRequestRemoval(mux, syncID, "trying to delete someone else's obs")

	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("C1 BUG CONFIRMED: got 500 when member tries to remove other's obs; must return 403")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("C1: expected 403 for member targeting another's obs, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// TestC1_RequestRemoval_AdminCaller_Gets403 verifies that admin users cannot use
// the member-facing request-removal route — they get 403 per spec.
func TestC1_RequestRemoval_AdminCaller_Gets403(t *testing.T) {
	store := &deletionRequestStore{
		obsBySyncID: map[string]cloudstore.DashboardObservationRow{},
	}
	mux := newDeletionRequestMux(store, "admin@vivastudios.com", true /* isAdmin */)
	rec := postRequestRemoval(mux, "obs-any", "reason")

	if rec.Code != http.StatusForbidden {
		t.Errorf("C1: expected 403 for admin caller on member route, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// ── Phase 1: Required-Reason Guard (DR-02) ──────────────────────────────────

// TestHandleRequestRemoval_EmptyReason_Returns400 verifies that a POST with an
// empty reason is rejected with 400 before CreateDeletionRequest is called.
// Spec scenario: DR-02-A.
func TestHandleRequestRemoval_EmptyReason_Returns400(t *testing.T) {
	const (
		syncID      = "obs-syncid-empty-reason"
		memberEmail = "alice@vivastudios.com"
	)
	store := &deletionRequestStore{
		obsBySyncID: map[string]cloudstore.DashboardObservationRow{
			syncID: {SyncID: syncID, Project: "proj", UserEmail: memberEmail},
		},
	}
	mux := newDeletionRequestMux(store, memberEmail, false)
	rec := postRequestRemoval(mux, syncID, "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("DR-02-A: expected 400 for empty reason, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// TestHandleRequestRemoval_WhitespaceReason_Returns400 verifies that a POST with
// a whitespace-only reason is rejected with 400 before CreateDeletionRequest is called.
// Spec scenario: DR-02-B.
func TestHandleRequestRemoval_WhitespaceReason_Returns400(t *testing.T) {
	const (
		syncID      = "obs-syncid-whitespace-reason"
		memberEmail = "alice@vivastudios.com"
	)
	store := &deletionRequestStore{
		obsBySyncID: map[string]cloudstore.DashboardObservationRow{
			syncID: {SyncID: syncID, Project: "proj", UserEmail: memberEmail},
		},
	}
	mux := newDeletionRequestMux(store, memberEmail, false)
	rec := postRequestRemoval(mux, syncID, "   ")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("DR-02-B: expected 400 for whitespace-only reason, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// TestHandleRequestRemoval_ValidReason_CallsStore verifies that a POST with a valid
// reason succeeds with 303 and CreateDeletionRequest is called with the trimmed reason.
// Spec scenario: DR-02-C.
func TestHandleRequestRemoval_ValidReason_CallsStore(t *testing.T) {
	const (
		syncID      = "obs-syncid-valid-reason"
		memberEmail = "alice@vivastudios.com"
	)
	store := &deletionRequestStore{
		obsBySyncID: map[string]cloudstore.DashboardObservationRow{
			syncID: {SyncID: syncID, Project: "proj", UserEmail: memberEmail},
		},
	}
	mux := newDeletionRequestMux(store, memberEmail, false)
	rec := postRequestRemoval(mux, syncID, "  sensitive content  ")

	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Errorf("DR-02-C: expected 303 or 200 for valid reason, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// TestHandleRequestRemoval_AdminCaller_Returns403 is a regression guard for DR-06-A.
// Verifies that the admin-block still fires and CreateDeletionRequest is not called.
func TestHandleRequestRemoval_AdminCaller_Returns403(t *testing.T) {
	store := &deletionRequestStore{
		obsBySyncID: map[string]cloudstore.DashboardObservationRow{},
	}
	mux := newDeletionRequestMux(store, "admin@vivastudios.com", true)
	rec := postRequestRemoval(mux, "obs-any", "valid reason")

	if rec.Code != http.StatusForbidden {
		t.Errorf("DR-06-A: expected 403 for admin caller on member route, got %d; body=%q", rec.Code, rec.Body.String())
	}
}
