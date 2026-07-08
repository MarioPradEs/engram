package dashboard

// admin_deletion_requests_ui_test.go — TDD tests for deletion-requests UI wiring.
//
// Tests cover:
//   - Phase 2: Admin nav badge (DR-03-A, DR-03-B)
//   - Phase 3: ObservationDetailPage owner-guard + pending state (DR-01-A–D)
//   - Phase 4: Accept confirm dialog (DR-04-A, DR-04-B)
//   - Phase 5: BrowserPage member decision notice (DR-05-A, DR-05-B)
//   - Phase 3 (client): MemberDeletionRequestButton reason textarea required

import (
	"context"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// ── Stub with configurable pending count ────────────────────────────────────

// pendingCountStore embeds parityStoreStub and overrides PendingDeletionRequestCount
// and ListDeletionRequestsForRequester so Phase 2 and Phase 3 tests can control
// the pending badge count and per-requester deletion request list.
type pendingCountStore struct {
	parityStoreStub
	pendingCount          int
	requesterRequests     []cloudstore.StoredDeletionRequest
	pendingCountErr       error
	requesterRequestsErr  error
}

func (s *pendingCountStore) PendingDeletionRequestCount(_ context.Context) (int, error) {
	if s.pendingCountErr != nil {
		return 0, s.pendingCountErr
	}
	return s.pendingCount, nil
}

func (s *pendingCountStore) ListDeletionRequestsForRequester(_ context.Context, _ string) ([]cloudstore.StoredDeletionRequest, error) {
	if s.requesterRequestsErr != nil {
		return nil, s.requesterRequestsErr
	}
	return s.requesterRequests, nil
}

// ── Phase 2: Admin Nav Badge (DR-03-A, DR-03-B) ─────────────────────────────

// TestAdminNav_PendingBadge_ShownWhenPositive asserts that when pendingCount > 0,
// the admin nav renders a pending-badge span and includes a Deletion Requests link.
// Spec scenario DR-03-A.
func TestAdminNav_PendingBadge_ShownWhenPositive(t *testing.T) {
	store := &pendingCountStore{pendingCount: 3}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DR-03-A: expected 200 for admin page, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "pending-badge") {
		t.Errorf("DR-03-A: expected pending-badge span in admin nav when pendingCount>0, body=%q", body)
	}
	if !strings.Contains(body, "Deletion Requests") {
		t.Errorf("DR-03-A: expected 'Deletion Requests' link in admin nav, body=%q", body)
	}
}

// TestAdminNav_PendingBadge_HiddenWhenZero asserts that when pendingCount == 0,
// no badge is rendered but the Deletion Requests link is still present.
// Spec scenario DR-03-B.
func TestAdminNav_PendingBadge_HiddenWhenZero(t *testing.T) {
	store := &pendingCountStore{pendingCount: 0}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DR-03-B: expected 200 for admin page, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "pending-badge") {
		t.Errorf("DR-03-B: expected NO pending-badge when pendingCount==0, body=%q", body)
	}
	if !strings.Contains(body, "Deletion Requests") {
		t.Errorf("DR-03-B: expected 'Deletion Requests' link in admin nav even with 0 count, body=%q", body)
	}
}

// TestHandleAdmin_BadgeCountPassedToNav asserts that the badge count from the store
// is correctly rendered in the admin nav across a representative handler.
// Spec scenario DR-03-A.
func TestHandleAdmin_BadgeCountPassedToNav(t *testing.T) {
	store := &pendingCountStore{pendingCount: 7}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/admin?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "7") {
		t.Errorf("DR-03-A: expected badge count '7' in rendered nav, body=%q", body)
	}
}

// ── Phase 3: ObservationDetailPage Owner Guard (DR-01-A–D) ──────────────────

// buildObsDetailMux is a helper that creates a test mux backed by a store
// with a single observation and a configurable list of deletion requests for the requester.
func buildObsDetailMux(ownerEmail, callerEmail string, isAdmin bool, store DashboardStore) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:      func(_ *http.Request) bool { return isAdmin },
		GetUserEmail: func(_ *http.Request) string { return callerEmail },
		Store:        store,
	})
	return mux
}

// TestObservationDetailPage_OwnerNonAdmin_ShowsButton asserts that the owner
// (non-admin) sees MemberDeletionRequestButton. Spec scenario DR-01-A.
func TestObservationDetailPage_OwnerNonAdmin_ShowsButton(t *testing.T) {
	const (
		syncID      = "obs-owner-shows-btn"
		ownerEmail  = "alice@vivastudios.com"
		project     = "proj-a"
		sessionID   = "sess-1"
	)
	store := &pendingCountStore{
		parityStoreStub: parityStoreStub{
			observations: []cloudstore.DashboardObservationRow{
				{SyncID: syncID, Project: project, SessionID: sessionID, UserEmail: ownerEmail, Type: "decision", Title: "Owner obs"},
			},
		},
		requesterRequests: nil, // no pending requests
	}
	mux := buildObsDetailMux(ownerEmail, ownerEmail, false, store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/dashboard/observations/"+project+"/"+sessionID+"/"+syncID+"?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DR-01-A: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "request-removal") {
		t.Errorf("DR-01-A: expected request-removal form for owner, body=%q", body)
	}
}

// TestObservationDetailPage_NonOwner_HidesButton asserts that a non-owner member
// does NOT see MemberDeletionRequestButton. Spec scenario DR-01-B.
func TestObservationDetailPage_NonOwner_HidesButton(t *testing.T) {
	const (
		syncID      = "obs-nonowner-hides-btn"
		ownerEmail  = "carol@vivastudios.com"
		callerEmail = "eve@vivastudios.com"
		project     = "proj-b"
		sessionID   = "sess-2"
	)
	store := &pendingCountStore{
		parityStoreStub: parityStoreStub{
			observations: []cloudstore.DashboardObservationRow{
				{SyncID: syncID, Project: project, SessionID: sessionID, UserEmail: ownerEmail, Type: "decision", Title: "Carol's obs"},
			},
		},
	}
	mux := buildObsDetailMux(ownerEmail, callerEmail, false, store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/dashboard/observations/"+project+"/"+sessionID+"/"+syncID+"?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DR-01-B: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "request-removal") {
		t.Errorf("DR-01-B: expected NO request-removal form for non-owner, body=%q", body)
	}
}

// TestObservationDetailPage_Admin_HidesButton asserts that an admin user
// does NOT see MemberDeletionRequestButton. Spec scenario DR-01-C.
func TestObservationDetailPage_Admin_HidesButton(t *testing.T) {
	const (
		syncID    = "obs-admin-hides-btn"
		email     = "admin@vivastudios.com"
		project   = "proj-c"
		sessionID = "sess-3"
	)
	store := &pendingCountStore{
		parityStoreStub: parityStoreStub{
			observations: []cloudstore.DashboardObservationRow{
				{SyncID: syncID, Project: project, SessionID: sessionID, UserEmail: email, Type: "decision", Title: "Admin's obs"},
			},
		},
	}
	mux := buildObsDetailMux(email, email, true, store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/dashboard/observations/"+project+"/"+sessionID+"/"+syncID+"?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DR-01-C: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "request-removal") {
		t.Errorf("DR-01-C: expected NO request-removal form for admin, body=%q", body)
	}
}

// TestObservationDetailPage_RemovalPending_ShowsPendingState asserts that when a pending
// deletion request exists for the observation, the pending indicator renders and
// the submit form does NOT appear. Spec scenario DR-01-D.
func TestObservationDetailPage_RemovalPending_ShowsPendingState(t *testing.T) {
	const (
		syncID      = "obs-removal-pending"
		ownerEmail  = "alice@vivastudios.com"
		project     = "proj-d"
		sessionID   = "sess-4"
	)
	store := &pendingCountStore{
		parityStoreStub: parityStoreStub{
			observations: []cloudstore.DashboardObservationRow{
				{SyncID: syncID, Project: project, SessionID: sessionID, UserEmail: ownerEmail, Type: "decision", Title: "Pending obs"},
			},
		},
		requesterRequests: []cloudstore.StoredDeletionRequest{
			{TargetSyncID: syncID, RequesterEmail: ownerEmail, Status: "pending"},
		},
	}
	mux := buildObsDetailMux(ownerEmail, ownerEmail, false, store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/dashboard/observations/"+project+"/"+sessionID+"/"+syncID+"?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DR-01-D: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "request-removal") {
		t.Errorf("DR-01-D: expected NO submit form when removal already pending, body=%q", body)
	}
	if !strings.Contains(body, "pending") {
		t.Errorf("DR-01-D: expected 'pending' indicator when removal already pending, body=%q", body)
	}
}

// ── Phase 3 client: MemberDeletionRequestButton textarea required ────────────

// TestMemberDeletionRequestButton_ReasonTextareaIsRequired asserts that the reason
// textarea has the `required` attribute. Spec scenario DR-02 (client-side UX).
func TestMemberDeletionRequestButton_ReasonTextareaIsRequired(t *testing.T) {
	var buf bytes.Buffer
	component := MemberDeletionRequestButton("obs-syncid-test")
	if err := component.Render(testContext(), &buf); err != nil {
		t.Fatalf("render error: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "textarea") {
		t.Errorf("expected textarea element in MemberDeletionRequestButton, body=%q", body)
	}
	if !strings.Contains(body, "required") {
		t.Errorf("expected required attribute on textarea in MemberDeletionRequestButton, body=%q", body)
	}
}

// ── Phase 4: Accept Confirm Dialog (DR-04-A, DR-04-B) ────────────────────────

// TestAdminDeletionRequestsPage_AcceptFormHasConfirm asserts that the Accept form
// contains an onsubmit confirm dialog. Spec scenario DR-04-A.
func TestAdminDeletionRequestsPage_AcceptFormHasConfirm(t *testing.T) {
	var buf bytes.Buffer
	requests := []cloudstore.StoredDeletionRequest{
		{ID: 1, TargetSyncID: "obs-123", RequesterEmail: "alice@vivastudios.com", Reason: "sensitive", Status: "pending"},
	}
	component := AdminDeletionRequestsPage(requests, 1)
	if err := component.Render(testContext(), &buf); err != nil {
		t.Fatalf("render error: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "onsubmit") {
		t.Errorf("DR-04-A: expected onsubmit confirm on Accept form, body=%q", body)
	}
	if !strings.Contains(body, "confirm(") {
		t.Errorf("DR-04-A: expected confirm() call in Accept form onsubmit, body=%q", body)
	}
}

// TestAdminDeletionRequestsPage_RejectFormNoConfirm asserts that the Reject form
// does NOT contain an onsubmit attribute (reject is non-destructive). Spec scenario DR-04-B.
func TestAdminDeletionRequestsPage_RejectFormNoConfirm(t *testing.T) {
	var buf bytes.Buffer
	requests := []cloudstore.StoredDeletionRequest{
		{ID: 1, TargetSyncID: "obs-456", RequesterEmail: "bob@vivastudios.com", Reason: "wrong", Status: "pending"},
	}
	component := AdminDeletionRequestsPage(requests, 1)
	if err := component.Render(testContext(), &buf); err != nil {
		t.Fatalf("render error: %v", err)
	}
	body := buf.String()
	// Find the Reject form action URL and assert no onsubmit follows before the next </form>.
	rejectActionIdx := strings.Index(body, "/deletion-requests/reject")
	if rejectActionIdx < 0 {
		t.Fatalf("DR-04-B: reject form not found, body=%q", body)
	}
	// Find the closing </form> after the reject action.
	formEndIdx := strings.Index(body[rejectActionIdx:], "</form>")
	if formEndIdx < 0 {
		t.Fatalf("DR-04-B: </form> not found after reject action, body=%q", body)
	}
	rejectFormBlock := body[rejectActionIdx : rejectActionIdx+formEndIdx]
	if strings.Contains(rejectFormBlock, "onsubmit") {
		t.Errorf("DR-04-B: Reject form should NOT have onsubmit confirm, block=%q", rejectFormBlock)
	}
}

// ── Phase 5: BrowserPage Member Decision Notice (DR-05-A, DR-05-B) ───────────

// TestBrowserPage_WithDecisions_ShowsNotice asserts that decided deletion requests
// render the deletion-notice div. Spec scenario DR-05-A.
func TestBrowserPage_WithDecisions_ShowsNotice(t *testing.T) {
	store := &pendingCountStore{
		parityStoreStub: parityStoreStub{},
		requesterRequests: []cloudstore.StoredDeletionRequest{
			{TargetSyncID: "obs-decided", Status: "accepted", RequesterEmail: "alice@vivastudios.com"},
		},
	}
	mux := buildObsDetailMux("alice@vivastudios.com", "alice@vivastudios.com", false, store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/browser?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DR-05-A: expected 200 for browser page, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "deletion-notice") {
		t.Errorf("DR-05-A: expected deletion-notice div when member has decided requests, body=%q", body)
	}
}

// TestBrowserPage_NoDecisions_HidesNotice asserts that no deletion-notice renders
// when the member has no decided requests. Spec scenario DR-05-B.
func TestBrowserPage_NoDecisions_HidesNotice(t *testing.T) {
	store := &pendingCountStore{
		parityStoreStub:   parityStoreStub{},
		requesterRequests: nil, // no requests
	}
	mux := buildObsDetailMux("alice@vivastudios.com", "alice@vivastudios.com", false, store)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/browser?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DR-05-B: expected 200 for browser page, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "deletion-notice") {
		t.Errorf("DR-05-B: expected NO deletion-notice when member has no decided requests, body=%q", body)
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// testContext returns a background context for direct templ render tests.
func testContext() context.Context {
	return context.Background()
}
