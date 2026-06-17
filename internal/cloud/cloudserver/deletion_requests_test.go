package cloudserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
)

// ─── Stub DashboardStore for deletion-request handler tests ──────────────────
//
// drStoreStub embeds scopingStoreStub (for all the existing DashboardStore
// methods) and adds in-memory deletion-request bookkeeping.
// No database is required — ownership is verified by checking the observation
// map seeded in scopingStoreStub.allObservations.

type drStoreStub struct {
	scopingStoreStub
	mu       sync.Mutex
	nextID   int64
	requests map[int64]*cloudstore.StoredDeletionRequest
	deleted  map[string]bool // sync_id → hard-deleted
}

func newDRStoreStub(obs []cloudstore.DashboardObservationRow) *drStoreStub {
	return &drStoreStub{
		scopingStoreStub: scopingStoreStub{allObservations: obs},
		nextID:           1,
		requests:         make(map[int64]*cloudstore.StoredDeletionRequest),
		deleted:          make(map[string]bool),
	}
}

// Implement the deletion-request DashboardStore extension methods.

func (s *drStoreStub) CreateDeletionRequest(_ context.Context, req cloudstore.DeletionRequest) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Check for duplicate pending.
	for _, r := range s.requests {
		if r.TargetSyncID == req.TargetSyncID && r.Status == "pending" {
			return 0, cloudstore.ErrDeletionRequestConflict
		}
	}
	id := s.nextID
	s.nextID++
	s.requests[id] = &cloudstore.StoredDeletionRequest{
		ID:             id,
		TargetSyncID:   req.TargetSyncID,
		RequesterEmail: req.RequesterEmail,
		Reason:         req.Reason,
		Status:         "pending",
	}
	return id, nil
}

func (s *drStoreStub) GetDeletionRequest(_ context.Context, id int64) (cloudstore.StoredDeletionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return cloudstore.StoredDeletionRequest{}, cloudstore.ErrDashboardObservationNotFound
	}
	return *r, nil
}

func (s *drStoreStub) ListPendingDeletionRequests(_ context.Context) ([]cloudstore.StoredDeletionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []cloudstore.StoredDeletionRequest
	for _, r := range s.requests {
		if r.Status == "pending" {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (s *drStoreStub) AcceptDeletionRequest(_ context.Context, id int64, adminEmail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return cloudstore.ErrDashboardObservationNotFound
	}
	if r.Status != "pending" {
		return cloudstore.ErrRequestAlreadyDecided
	}
	r.Status = "accepted"
	r.DecidedBy = adminEmail
	// Mark observation as hard-deleted.
	s.deleted[r.TargetSyncID] = true
	return nil
}

func (s *drStoreStub) RejectDeletionRequest(_ context.Context, id int64, adminEmail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return cloudstore.ErrDashboardObservationNotFound
	}
	if r.Status != "pending" {
		return cloudstore.ErrRequestAlreadyDecided
	}
	r.Status = "rejected"
	r.DecidedBy = adminEmail
	return nil
}

func (s *drStoreStub) PendingDeletionRequestCount(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, r := range s.requests {
		if r.Status == "pending" {
			count++
		}
	}
	return count, nil
}

func (s *drStoreStub) ListDeletionRequestsForRequester(_ context.Context, email string) ([]cloudstore.StoredDeletionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []cloudstore.StoredDeletionRequest
	for _, r := range s.requests {
		if strings.EqualFold(r.RequesterEmail, email) {
			out = append(out, *r)
		}
	}
	return out, nil
}

// Compile-time check: drStoreStub must implement dashboard.DashboardStore.
var _ dashboard.DashboardStore = (*drStoreStub)(nil)

// ─── drStoreStub also implements the ChunkStore interface (via fakeStore embed) ──

// ─── Test fixtures ─────────────────────────────────────────────────────────────

const drTestYAML = `users:
  - email: mario@vivastudios.com
    name: Mario Pradas
    department: dev
    role: admin
    status: active
    enrolled:
      - general
  - email: esanchez@vivastudios.com
    name: Elena Sanchez
    department: qa
    role: member
    status: active
    enrolled:
      - general
`

var drTestObservations = []cloudstore.DashboardObservationRow{
	{
		SyncID:    "obs-own",
		Project:   "proj-a",
		SessionID: "sess-1",
		Type:      "decision",
		Title:     "Elena observation",
		UserEmail: "esanchez@vivastudios.com",
		CreatedAt: "2026-06-01T10:00:00Z",
	},
	{
		SyncID:    "obs-other",
		Project:   "proj-a",
		SessionID: "sess-2",
		Type:      "bugfix",
		Title:     "Mario observation",
		UserEmail: "mario@vivastudios.com",
		CreatedAt: "2026-06-01T11:00:00Z",
	},
}

// buildDRServer builds a CloudServer backed by drStoreStub.
func buildDRServer(t *testing.T, jwtSecret string) (*CloudServer, *drStoreStub) {
	t.Helper()
	store := newDRStoreStub(drTestObservations)
	loader := buildAuthTestLoader(t, drTestYAML)
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("buildDRServer: NewHeaderAuthenticatorWithJWT: %v", err)
	}
	srv := New(store, ha, 0, WithAuthEndpoint(loader, jwtSecret))
	return srv, store
}

// ─── DR-H1: member POST request-removal for own sync_id → 200, pending row ───

func TestDR_H1_MemberRequestsOwnObservation_200(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("r", 32)
	srv, store := buildDRServer(t, jwtSecret)

	memberCookie := mintSessionCookieForEmail(t, srv, "esanchez@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("sync_id", "obs-own")
	form.Set("reason", "sensitive test content")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/browser/observations/obs-own/request-removal", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(memberCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Errorf("DR-H1: expected 200 or 303, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Verify pending row was inserted.
	count, err := store.PendingDeletionRequestCount(nil)
	if err != nil {
		t.Fatalf("PendingDeletionRequestCount: %v", err)
	}
	if count != 1 {
		t.Errorf("DR-H1: expected 1 pending request, got %d", count)
	}
}

// ─── DR-H2: member POST for other user's sync_id → 403 not_observation_owner ─

func TestDR_H2_MemberRequestsOtherObservation_403(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("r", 32)
	srv, store := buildDRServer(t, jwtSecret)

	memberCookie := mintSessionCookieForEmail(t, srv, "esanchez@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("sync_id", "obs-other") // belongs to mario@, not esanchez@
	form.Set("reason", "tampered request")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/browser/observations/obs-other/request-removal", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(memberCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("DR-H2: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not_observation_owner") {
		t.Errorf("DR-H2: expected 'not_observation_owner' in body, got %q", rec.Body.String())
	}

	// No row must have been inserted.
	count, _ := store.PendingDeletionRequestCount(nil)
	if count != 0 {
		t.Errorf("DR-H2: expected 0 pending requests after denied attempt, got %d", count)
	}
}

// ─── DR-H3: member POST for non-existent sync_id → 404 ───────────────────────

func TestDR_H3_MemberRequestsNonExistentObservation_404(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("r", 32)
	srv, _ := buildDRServer(t, jwtSecret)

	memberCookie := mintSessionCookieForEmail(t, srv, "esanchez@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("sync_id", "obs-does-not-exist")
	form.Set("reason", "non-existent obs")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/browser/observations/obs-does-not-exist/request-removal", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(memberCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("DR-H3: expected 404, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── DR-H4: admin POST request-removal → 403 (admin uses review page) ────────

func TestDR_H4_AdminCannotUseRequestRemovalFlow_403(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("r", 32)
	srv, _ := buildDRServer(t, jwtSecret)

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("sync_id", "obs-own")
	form.Set("reason", "admin trying member route")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/browser/observations/obs-own/request-removal", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("DR-H4: expected 403 for admin on member route, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── DR-H5: admin GET /dashboard/admin/deletion-requests → 200 + pending list ─

func TestDR_H5_AdminGetsReviewPage_200(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("r", 32)
	srv, store := buildDRServer(t, jwtSecret)

	// Pre-seed a pending request.
	_, _ = store.CreateDeletionRequest(nil, cloudstore.DeletionRequest{
		TargetSyncID:   "obs-own",
		RequesterEmail: "esanchez@vivastudios.com",
		Reason:         "pre-seeded",
	})

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	req := makeSessionRequest(http.MethodGet, "/dashboard/admin/deletion-requests", adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("DR-H5: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "esanchez@vivastudios.com") {
		t.Errorf("DR-H5: expected requester email in response body, got %q", rec.Body.String())
	}
}

// ─── DR-H6: member GET admin review page → 403 ───────────────────────────────

func TestDR_H6_MemberCannotAccessAdminReviewPage_403(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("r", 32)
	srv, _ := buildDRServer(t, jwtSecret)

	memberCookie := mintSessionCookieForEmail(t, srv, "esanchez@vivastudios.com", jwtSecret)

	req := makeSessionRequest(http.MethodGet, "/dashboard/admin/deletion-requests", memberCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("DR-H6: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── DR-H7: admin POST accept → 200; observation hard-deleted; request accepted ─

func TestDR_H7_AdminAcceptsRequest_ObservationDeleted(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("r", 32)
	srv, store := buildDRServer(t, jwtSecret)

	// Pre-seed pending request.
	reqID, err := store.CreateDeletionRequest(nil, cloudstore.DeletionRequest{
		TargetSyncID:   "obs-own",
		RequesterEmail: "esanchez@vivastudios.com",
		Reason:         "to be accepted",
	})
	if err != nil {
		t.Fatalf("CreateDeletionRequest: %v", err)
	}

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("id", fmt.Sprint(reqID))
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/deletion-requests/accept", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Errorf("DR-H7: expected 200 or 303, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Verify observation was hard-deleted in stub.
	if !store.deleted["obs-own"] {
		t.Error("DR-H7: expected obs-own to be marked as hard-deleted in store stub")
	}

	// Verify request is accepted.
	stored, err := store.GetDeletionRequest(nil, reqID)
	if err != nil {
		t.Fatalf("GetDeletionRequest: %v", err)
	}
	if stored.Status != "accepted" {
		t.Errorf("DR-H7: request status=%q, want accepted", stored.Status)
	}
}

// ─── DR-H8: admin POST accept already-accepted id → 409 ─────────────────────

func TestDR_H8_AdminAcceptsAlreadyDecidedRequest_409(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("r", 32)
	srv, store := buildDRServer(t, jwtSecret)

	reqID, _ := store.CreateDeletionRequest(nil, cloudstore.DeletionRequest{
		TargetSyncID:   "obs-own",
		RequesterEmail: "esanchez@vivastudios.com",
		Reason:         "already decided",
	})
	// Accept it first.
	_ = store.AcceptDeletionRequest(nil, reqID, "mario@vivastudios.com")

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("id", fmt.Sprint(reqID))
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/deletion-requests/accept", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("DR-H8: expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── DR-H9: admin POST reject → 200; observation intact ──────────────────────

func TestDR_H9_AdminRejectsRequest_ObservationIntact(t *testing.T) {
	t.Parallel()
	jwtSecret := strings.Repeat("r", 32)
	srv, store := buildDRServer(t, jwtSecret)

	reqID, _ := store.CreateDeletionRequest(nil, cloudstore.DeletionRequest{
		TargetSyncID:   "obs-own",
		RequesterEmail: "esanchez@vivastudios.com",
		Reason:         "to be rejected",
	})

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("id", fmt.Sprint(reqID))
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/deletion-requests/reject", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Errorf("DR-H9: expected 200 or 303, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Observation must NOT be hard-deleted.
	if store.deleted["obs-own"] {
		t.Error("DR-H9: obs-own must NOT be hard-deleted after reject")
	}

	// Request status → rejected.
	stored, _ := store.GetDeletionRequest(nil, reqID)
	if stored.Status != "rejected" {
		t.Errorf("DR-H9: request status=%q, want rejected", stored.Status)
	}
}

