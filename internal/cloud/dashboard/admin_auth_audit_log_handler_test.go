package dashboard

// ─── PR3: Admin Auth Audit Log Dashboard View Tests ──────────────────────────
//
// TDD cycle: RED → GREEN → REFACTOR
// Tests written FIRST (RED). They will fail with "undefined handler" until
// the handler + interface extension + templ components are added (GREEN).
//
// Scenarios covered (per spec §audit-admin-view):
//   - Admin user can access the auth audit log shell page (GET /dashboard/admin/audit-log/auth)
//   - Non-admin user receives HTTP 403 on the shell page
//   - Admin user can access the list partial (GET /dashboard/admin/audit-log/auth/list)
//   - Non-admin user receives HTTP 403 on the list partial
//   - Empty state renders without error when no rows exist
//   - Rows are rendered with correct fields when entries exist
//   - List partial is partial-only (no <html> wrapper even for non-HTMX requests)

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// authAuditStoreStub embeds parityStoreStub and adds ListAuthAuditEntriesPaginated
// so existing tests do not need to change. This provides a minimal DashboardStore
// implementation for the new interface method.
type authAuditStoreStub struct {
	parityStoreStub
	authAuditEntries []cloudstore.AuthAuditEntry
	authAuditTotal   int
	authAuditErr     error
}

func (s authAuditStoreStub) ListAuthAuditEntriesPaginated(ctx context.Context, page, pageSize int) ([]cloudstore.AuthAuditEntry, int, error) {
	if s.authAuditErr != nil {
		return nil, 0, s.authAuditErr
	}
	return s.authAuditEntries, s.authAuditTotal, nil
}

// TestHandleAdminAuthAuditLog_admin_seesShellPage verifies that an admin user
// receives HTTP 200 and the auth audit log shell page at GET /dashboard/admin/audit-log/auth.
func TestHandleAdminAuthAuditLog_admin_seesShellPage(t *testing.T) {
	store := authAuditStoreStub{
		authAuditEntries: []cloudstore.AuthAuditEntry{
			{ID: 1, OccurredAt: "2026-07-10T10:00:00Z", Email: "alice@vivastudios.com", Outcome: "denied", Source: "jwt", ReasonCode: "invalid_jwt"},
		},
		authAuditTotal: 1,
	}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/auth?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin auth audit log shell, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Shell page must contain the section title.
	if !strings.Contains(body, "Auth Audit Log") {
		t.Errorf("expected 'Auth Audit Log' in shell body, got body=%q", body)
	}
}

// TestHandleAdminAuthAuditLog_nonAdmin_gets403 verifies that a non-admin user
// receives HTTP 403 when requesting GET /dashboard/admin/audit-log/auth.
func TestHandleAdminAuthAuditLog_nonAdmin_gets403(t *testing.T) {
	store := authAuditStoreStub{}
	mux := newAuthedMux(store, false) // isAdmin = false
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/auth?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin auth audit log shell, got %d", rec.Code)
	}
}

// TestHandleAdminAuthAuditLogList_admin_seesRows verifies that an admin user
// receives HTTP 200 and auth audit log entries at GET /dashboard/admin/audit-log/auth/list.
func TestHandleAdminAuthAuditLogList_admin_seesRows(t *testing.T) {
	store := authAuditStoreStub{
		authAuditEntries: []cloudstore.AuthAuditEntry{
			{ID: 1, OccurredAt: "2026-07-10T10:00:00Z", Email: "alice@vivastudios.com", Outcome: "denied", Source: "jwt", ReasonCode: "invalid_jwt"},
			{ID: 2, OccurredAt: "2026-07-10T09:00:00Z", Email: "bob@vivastudios.com", Outcome: "allowed", Source: "oauth", ReasonCode: ""},
		},
		authAuditTotal: 2,
	}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/auth/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin auth audit log list, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Must contain at least the email from the first row.
	if !strings.Contains(body, "alice@vivastudios.com") {
		t.Errorf("expected alice@vivastudios.com in list body, got body=%q", body)
	}
	if !strings.Contains(body, "invalid_jwt") {
		t.Errorf("expected invalid_jwt reason code in list body, got body=%q", body)
	}
}

// TestHandleAdminAuthAuditLogList_nonAdmin_gets403 verifies that a non-admin user
// receives HTTP 403 when requesting GET /dashboard/admin/audit-log/auth/list.
func TestHandleAdminAuthAuditLogList_nonAdmin_gets403(t *testing.T) {
	store := authAuditStoreStub{}
	mux := newAuthedMux(store, false) // isAdmin = false
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/auth/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin auth audit log list, got %d", rec.Code)
	}
}

// TestHandleAdminAuthAuditLogList_emptyState_renders verifies that when the store
// returns zero rows, the list partial renders an empty-state message without error.
func TestHandleAdminAuthAuditLogList_emptyState_renders(t *testing.T) {
	store := authAuditStoreStub{
		authAuditEntries: nil,
		authAuditTotal:   0,
	}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/auth/list?auth=ok", nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty auth audit log list, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Empty state must mention "No" or similar indicator of emptiness.
	if !strings.Contains(body, "No") {
		t.Errorf("expected 'No' (empty state marker) in empty list body, got body=%q", body)
	}
}

// TestHandleAdminAuthAuditLogList_partialOnly verifies that the list endpoint
// always returns a fragment (no full <html> Layout wrapper) even for non-HTMX requests.
// This mirrors N7 from the sync-audit log contract.
func TestHandleAdminAuthAuditLogList_partialOnly(t *testing.T) {
	store := authAuditStoreStub{}
	mux := newAuthedAdminMux(store)
	rec := httptest.NewRecorder()
	// Deliberately no HX-Request header.
	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/audit-log/auth/list?auth=ok", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for partial-only check, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") {
		t.Errorf("handleAdminAuthAuditLogList returned full Layout wrapper; got <html> in body")
	}
}
