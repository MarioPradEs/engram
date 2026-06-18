package dashboard

// admin_deletion_requests_handler.go — per-observation deletion-request handlers (D5).
//
// Guiding principle (spec §guiding-principle, enforced in all comments and guards):
// By default, nothing is deleted. Memory evolves through CORRECTIONS (observation
// revisions via revision_count / mem_update). Hard deletion is the RARE EXCEPTION
// reserved for content that must actually disappear because it is sensitive or
// erroneous — not the normal knowledge-maintenance mechanism.
//
// Routes registered in dashboard.go Mount():
//   POST /dashboard/browser/observations/{syncID}/request-removal  (member-only)
//   GET  /dashboard/admin/deletion-requests                        (admin-only)
//   POST /dashboard/admin/deletion-requests/accept                 (admin-only)
//   POST /dashboard/admin/deletion-requests/reject                 (admin-only)

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// handleRequestRemoval handles POST /dashboard/browser/observations/{syncID}/request-removal.
//
// Member-only: admin users are rejected with 403 (they use the admin review page).
// Ownership check: the observation's stored user_email must match the caller's email.
// Spec scenarios: DR-H1, DR-H2, DR-H3, DR-H4.
func (h *handlers) handleRequestRemoval(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)

	// Admin gate: admin users MUST NOT use the member request-removal flow.
	// They manage deletion via the admin review page. (Spec: admin-cannot-use-member-flow)
	if p.IsAdmin() {
		http.Error(w, "forbidden: admins use the admin review page to manage deletion requests", http.StatusForbidden)
		return
	}
	// Identity gate: member must have a verified email from the JWT.
	callerEmail := p.Email()
	if strings.TrimSpace(callerEmail) == "" {
		http.Error(w, "identity required for deletion request submission", http.StatusForbidden)
		return
	}

	if h.cfg.Store == nil {
		http.Error(w, "store not configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	syncID := strings.TrimSpace(r.PathValue("syncID"))
	if syncID == "" {
		// Fall back to form value for non-PathValue routers.
		syncID = strings.TrimSpace(r.FormValue("sync_id"))
	}
	reason := strings.TrimSpace(r.FormValue("reason"))

	if syncID == "" {
		http.Error(w, "sync_id is required", http.StatusBadRequest)
		return
	}

	// Ownership check: load the observation by syncID alone using an admin scope to
	// retrieve it across all projects, then verify the caller's email matches.
	//
	// C1 fix: use GetObservationBySyncID instead of GetObservationDetail("","",syncID).
	// GetObservationDetail requires a non-empty project (normalizeDashboardProject
	// returns ErrDashboardProjectInvalid for ""), causing every call here to return 500.
	// GetObservationBySyncID is designed for this cross-project lookup pattern.
	//
	// Two-step spec behavior:
	//   - Observation not found at all → 404
	//   - Observation found but user_email != caller → 403 not_observation_owner
	adminScope := &cloudstore.ReadScope{IsAdmin: true}
	obs, err := h.cfg.Store.GetObservationBySyncID(adminScope, syncID)
	if err != nil {
		if errors.Is(err, cloudstore.ErrDashboardObservationNotFound) {
			http.Error(w, "observation not found", http.StatusNotFound)
			return
		}
		log.Printf("[engram-cloud] handleRequestRemoval: GetObservationBySyncID: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Explicit ownership assertion: the stored user_email MUST match the caller.
	// This is the primary security gate that MUST fail if the check were removed.
	if !strings.EqualFold(strings.TrimSpace(obs.UserEmail), callerEmail) {
		http.Error(w, "not_observation_owner: you may only request removal of observations you created", http.StatusForbidden)
		return
	}

	// Insert pending deletion request.
	_, err = h.cfg.Store.CreateDeletionRequest(r.Context(), cloudstore.DeletionRequest{
		TargetSyncID:   syncID,
		RequesterEmail: callerEmail,
		Reason:         reason,
	})
	if err != nil {
		if errors.Is(err, cloudstore.ErrDeletionRequestConflict) {
			http.Error(w, "request_already_pending: a pending deletion request already exists for this observation", http.StatusConflict)
			return
		}
		log.Printf("[engram-cloud] handleRequestRemoval: CreateDeletionRequest: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", "/dashboard/browser/observations")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dashboard/browser/observations", http.StatusSeeOther)
}

// handleAdminDeletionRequests handles GET /dashboard/admin/deletion-requests.
//
// Admin-only. Lists all pending deletion requests with the pending count badge.
// Spec scenario: DR-H5, DR-H6.
func (h *handlers) handleAdminDeletionRequests(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.cfg.Store == nil {
		http.Error(w, "store not configured", http.StatusServiceUnavailable)
		return
	}

	pending, err := h.cfg.Store.ListPendingDeletionRequests(r.Context())
	if err != nil {
		log.Printf("[engram-cloud] handleAdminDeletionRequests: ListPendingDeletionRequests: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pendingCount := len(pending)

	component := AdminDeletionRequestsPage(pending, pendingCount)
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Deletion Requests", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminDeletionRequestAccept handles POST /dashboard/admin/deletion-requests/accept.
//
// Admin-only. Hard-deletes the targeted observation and marks the request accepted.
// Re-verifies pending status before acting (concurrent double-accept returns 409).
// Spec scenarios: DR-H7, DR-H8.
func (h *handlers) handleAdminDeletionRequestAccept(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.cfg.Store == nil {
		http.Error(w, "store not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	idStr := strings.TrimSpace(r.FormValue("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, fmt.Sprintf("invalid request id %q", idStr), http.StatusBadRequest)
		return
	}

	adminEmail := p.Email()
	if adminEmail == "" {
		// Static admin token — use display name as fallback identifier.
		adminEmail = p.DisplayName()
	}

	// AcceptDeletionRequest is the atomic path:
	//   1. Verifies status == pending (returns ErrRequestAlreadyDecided otherwise)
	//   2. Inserts hard-delete mutation into cloud_mutations
	//   3. Sets status=accepted, decided_by, decided_at
	//   4. Invalidates the dashboard read model
	// The hard-delete is EXACTLY the targeted observation — no collateral deletions.
	if err := h.cfg.Store.AcceptDeletionRequest(r.Context(), id, adminEmail); err != nil {
		if errors.Is(err, cloudstore.ErrRequestAlreadyDecided) {
			http.Error(w, "request_already_decided: this request has already been decided", http.StatusConflict)
			return
		}
		if errors.Is(err, cloudstore.ErrDeletionRequestNotFound) {
			http.Error(w, "deletion request not found", http.StatusNotFound)
			return
		}
		log.Printf("[engram-cloud] handleAdminDeletionRequestAccept: AcceptDeletionRequest id=%d: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", "/dashboard/admin/deletion-requests")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dashboard/admin/deletion-requests", http.StatusSeeOther)
}

// handleAdminDeletionRequestReject handles POST /dashboard/admin/deletion-requests/reject.
//
// Admin-only. Marks a pending request as rejected without touching the observation.
// Spec scenario: DR-H9.
func (h *handlers) handleAdminDeletionRequestReject(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.cfg.Store == nil {
		http.Error(w, "store not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	idStr := strings.TrimSpace(r.FormValue("id"))
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, fmt.Sprintf("invalid request id %q", idStr), http.StatusBadRequest)
		return
	}

	adminEmail := p.Email()
	if adminEmail == "" {
		adminEmail = p.DisplayName()
	}

	if err := h.cfg.Store.RejectDeletionRequest(r.Context(), id, adminEmail); err != nil {
		if errors.Is(err, cloudstore.ErrRequestAlreadyDecided) {
			http.Error(w, "request_already_decided: this request has already been decided", http.StatusConflict)
			return
		}
		if errors.Is(err, cloudstore.ErrDeletionRequestNotFound) {
			http.Error(w, "deletion request not found", http.StatusNotFound)
			return
		}
		log.Printf("[engram-cloud] handleAdminDeletionRequestReject: RejectDeletionRequest id=%d: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", "/dashboard/admin/deletion-requests")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dashboard/admin/deletion-requests", http.StatusSeeOther)
}
