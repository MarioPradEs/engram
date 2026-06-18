package dashboard

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// ─── Admin member-management handlers (D4) ───────────────────────────────────
//
// All handlers in this file are admin-gated. Non-admins receive 403 before any
// business logic runs.
//
// Write flow per spec §atomic-write:
//   1. Validate new state (users.ValidatePrincipal + loadAndValidate via WriteAtomic)
//   2. Serialize to YAML
//   3. Atomic write (temp + rename) via users.WriteAtomic
//   4. Local git auto-commit (non-fatal; logged on failure)
//   5. In-process reload via cfg.UserReload (non-fatal on reload failure; loader retains last-good)

// handleAdminMembers handles GET /dashboard/admin/members.
func (h *handlers) handleAdminMembers(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	members := h.provisionedUsersList()
	component := AdminMembersPage(members, "")
	if isHTMXRequest(r) {
		renderComponent(w, r, component)
		return
	}
	renderComponent(w, r, Layout("Members", p.DisplayName(), "admin", p.IsAdmin(), component))
}

// handleAdminMembersAdd handles POST /dashboard/admin/members/add.
func (h *handlers) handleAdminMembersAdd(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	name := strings.TrimSpace(r.FormValue("name"))
	dept := strings.ToLower(strings.TrimSpace(r.FormValue("department")))
	role := strings.ToLower(strings.TrimSpace(r.FormValue("role")))
	status := strings.ToLower(strings.TrimSpace(r.FormValue("status")))
	if status == "" {
		status = "active"
	}

	// Fast email domain check before loading the full directory.
	if !strings.HasSuffix(email, "@vivastudios.com") {
		http.Error(w, fmt.Sprintf("email %q must end with @vivastudios.com", email), http.StatusBadRequest)
		return
	}

	if h.cfg.ListProvisionedUsers == nil || h.cfg.UsersFilePath == "" {
		http.Error(w, "member management not configured", http.StatusServiceUnavailable)
		return
	}

	current := h.provisionedUsersList()

	// Duplicate check.
	for _, u := range current {
		if strings.EqualFold(u.Email, email) {
			http.Error(w, fmt.Sprintf("email %q already exists", email), http.StatusConflict)
			return
		}
	}

	newEntry := users.Principal{
		Email:      email,
		Name:       name,
		Department: dept,
		Role:       role,
		Status:     status,
	}
	if err := users.ValidatePrincipal(newEntry); err != nil {
		http.Error(w, fmt.Sprintf("validation error: %v", err), http.StatusBadRequest)
		return
	}

	// Build full candidate list.
	principals := append(provisionedToUsers(current), newEntry)
	h.writePrincipalsAndReload(w, r, principals, fmt.Sprintf("admin: add member %s", email))
}

// handleAdminMembersEdit handles POST /dashboard/admin/members/edit.
func (h *handlers) handleAdminMembersEdit(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	newRole := strings.ToLower(strings.TrimSpace(r.FormValue("role")))
	newDept := strings.ToLower(strings.TrimSpace(r.FormValue("department")))
	newStatus := strings.ToLower(strings.TrimSpace(r.FormValue("status")))

	if h.cfg.ListProvisionedUsers == nil || h.cfg.UsersFilePath == "" {
		http.Error(w, "member management not configured", http.StatusServiceUnavailable)
		return
	}

	current := h.provisionedUsersList()

	// Find and update the target entry.
	found := false
	updated := make([]users.Principal, 0, len(current))
	for _, u := range current {
		existing := provisionedToUser(u)
		if strings.EqualFold(existing.Email, email) {
			found = true
			if newRole != "" {
				existing.Role = newRole
			}
			if newDept != "" {
				existing.Department = newDept
			}
			if newStatus != "" {
				existing.Status = newStatus
			}
		}
		updated = append(updated, existing)
	}
	if !found {
		http.Error(w, fmt.Sprintf("user %q not found", email), http.StatusNotFound)
		return
	}

	// Last-admin guard: ensure at least one ACTIVE admin remains after the edit.
	//
	// W1 fix: count only admins with role=admin AND status=active (matching the
	// handleAdminMembersDeactivate guard). The previous implementation counted by
	// role only, ignoring status — allowing the sole active admin to set their own
	// status=removed via this form, which would pass the role-count guard (still
	// role=admin, count=1) and cause a complete admin lockout. The deactivate
	// handler already does the correct active-admin count; this handler must match it.
	activeAdminCount := 0
	for _, u := range updated {
		if strings.EqualFold(u.Role, "admin") && strings.EqualFold(u.Status, "active") {
			activeAdminCount++
		}
	}
	if activeAdminCount == 0 {
		http.Error(w, "edit rejected: at least one active admin must remain in the directory", http.StatusBadRequest)
		return
	}

	// Validate the updated entry individually.
	for _, u := range updated {
		if strings.EqualFold(u.Email, email) {
			if err := users.ValidatePrincipal(u); err != nil {
				http.Error(w, fmt.Sprintf("validation error: %v", err), http.StatusBadRequest)
				return
			}
		}
	}

	h.writePrincipalsAndReload(w, r, updated, fmt.Sprintf("admin: edit member %s", email))
}

// handleAdminMembersDeactivate handles POST /dashboard/admin/members/deactivate.
// Sets status to the submitted "status" field value (expected: offboarding or removed).
// Entry is NEVER deleted from users.yaml.
func (h *handlers) handleAdminMembersDeactivate(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	newStatus := strings.ToLower(strings.TrimSpace(r.FormValue("status")))
	if newStatus == "" {
		newStatus = "removed"
	}

	validDeactivateStatuses := map[string]bool{"offboarding": true, "removed": true}
	if !validDeactivateStatuses[newStatus] {
		http.Error(w, fmt.Sprintf("invalid deactivation status %q (valid: offboarding, removed)", newStatus), http.StatusBadRequest)
		return
	}

	if h.cfg.ListProvisionedUsers == nil || h.cfg.UsersFilePath == "" {
		http.Error(w, "member management not configured", http.StatusServiceUnavailable)
		return
	}

	current := h.provisionedUsersList()
	found := false
	updated := make([]users.Principal, 0, len(current))
	for _, u := range current {
		existing := provisionedToUser(u)
		if strings.EqualFold(existing.Email, email) {
			found = true
			existing.Status = newStatus
		}
		updated = append(updated, existing)
	}
	if !found {
		http.Error(w, fmt.Sprintf("user %q not found", email), http.StatusNotFound)
		return
	}

	// Last-admin guard: ensure at least one active admin remains after the status
	// change. loadAndValidate only checks role (not status), so without this guard
	// deactivating the sole active admin would succeed at the file layer but block
	// that admin from re-authenticating via autoLoginFromHeader — causing a full
	// admin lockout recoverable only by hand-editing users.yaml.
	activeAdminCount := 0
	for _, u := range updated {
		if strings.EqualFold(u.Role, "admin") && strings.EqualFold(u.Status, "active") {
			activeAdminCount++
		}
	}
	if activeAdminCount == 0 {
		http.Error(w, "deactivation rejected: at least one active admin must remain in the directory", http.StatusBadRequest)
		return
	}

	h.writePrincipalsAndReload(w, r, updated, fmt.Sprintf("admin: deactivate member %s (status=%s)", email, newStatus))
}

// ─── Write helper ─────────────────────────────────────────────────────────────

// writePrincipalsAndReload executes the full atomic-write + git-commit + reload
// sequence (spec §atomic-write steps 2–5). On success, responds 200/303.
// On failure, writes the appropriate HTTP error status.
func (h *handlers) writePrincipalsAndReload(w http.ResponseWriter, r *http.Request, principals []users.Principal, commitMsg string) {
	data, err := users.MarshalPrincipals(principals)
	if err != nil {
		http.Error(w, fmt.Sprintf("marshal error: %v", err), http.StatusInternalServerError)
		return
	}

	usersPath := h.cfg.UsersFilePath
	if err := users.WriteAtomic(usersPath, data, users.ValidatorForPath()); err != nil {
		http.Error(w, fmt.Sprintf("write error: %v", err), http.StatusInternalServerError)
		return
	}

	// Step 4: local git commit — non-fatal.
	repoPath := filepath.Dir(usersPath)
	if err := users.RunLocalGitCommit(repoPath, usersPath, commitMsg); err != nil {
		log.Printf("[engram-cloud] git commit failed (non-fatal): %v", err)
		// Continue — reload must still fire.
	}

	// Step 5: in-process reload.
	if h.cfg.UserReload != nil {
		if err := h.cfg.UserReload(); err != nil {
			log.Printf("[engram-cloud] users.Reload after write failed (non-fatal): %v", err)
		}
	}

	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", "/dashboard/admin/members")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/dashboard/admin/members", http.StatusSeeOther)
}

// ─── Conversion helpers ───────────────────────────────────────────────────────

func (h *handlers) provisionedUsersList() []ProvisionedUser {
	if h.cfg.ListProvisionedUsers == nil {
		return nil
	}
	return h.cfg.ListProvisionedUsers()
}

func provisionedToUser(u ProvisionedUser) users.Principal {
	return users.Principal{
		Email:      u.Email,
		Name:       u.Name,
		Department: u.Department,
		Role:       u.Role,
		Status:     u.Status,
		Enrolled:   u.Enrolled,
	}
}

func provisionedToUsers(list []ProvisionedUser) []users.Principal {
	out := make([]users.Principal, 0, len(list))
	for _, u := range list {
		out = append(out, provisionedToUser(u))
	}
	return out
}
