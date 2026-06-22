package dashboard

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
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

// handleAdminMembersEdit handles POST /dashboard/admin/members/edit and
// POST /dashboard/admin/users/edit.
//
// Full-field edit (email rename included):
//   - original_email: current key used to locate the entry (required when editing
//     from the Users management page; falls back to "email" for the legacy /members route).
//   - email: new email value; if it differs from original_email this is a rename.
//   - name: updated display name (may be empty string to leave unchanged).
//   - department: updated department.
//   - role: updated role.
//   - status: updated status (optional; used by activate flows).
//
// On email rename:
//   - Validates the new email (@vivastudios.com domain check).
//   - Checks the new email does not collide with another existing user.
//   - Replaces the old entry's Email field with the new value.
//   - On error → 303 redirect with flash + error=1 (Users page) or 400 (members page).
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

	// Support both /users/edit (sends original_email) and /members/edit (legacy, uses email only).
	originalEmail := strings.ToLower(strings.TrimSpace(r.FormValue("original_email")))
	newEmail := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	newName := strings.TrimSpace(r.FormValue("name"))
	newRole := strings.ToLower(strings.TrimSpace(r.FormValue("role")))
	newDept := strings.ToLower(strings.TrimSpace(r.FormValue("department")))
	newStatus := strings.ToLower(strings.TrimSpace(r.FormValue("status")))

	// When original_email is not provided (legacy /members/edit path), treat the
	// submitted email as both lookup key and new value (no rename).
	if originalEmail == "" {
		originalEmail = newEmail
	}

	// isUsersRoute is true when the request comes via /admin/users/ — those routes
	// use 303+flash for validation errors instead of 4xx so the UI stays intact.
	isUsersRoute := strings.Contains(r.URL.Path, "/admin/users/")

	// redirectWithError issues a 303 to the Users management page with an error flash.
	// Only called when isUsersRoute is true.
	redirectWithError := func(msg string) {
		target := "/dashboard/admin/users?" + url.Values{"flash": {msg}, "error": {"1"}}.Encode()
		http.Redirect(w, r, target, http.StatusSeeOther)
	}

	if h.cfg.ListProvisionedUsers == nil || h.cfg.UsersFilePath == "" {
		http.Error(w, "member management not configured", http.StatusServiceUnavailable)
		return
	}

	// Self-protection guard (D4-SP): the acting admin must not be able to lock
	// themselves out by editing their own account via the management UI.
	// This guard is independent of — and runs before — the global zero-admin guard.
	//
	// Safe fields (allowed even for self): name, department.
	// Blocked self-edits: role demotion, status deactivation, email rename.
	principalEmail := strings.ToLower(strings.TrimSpace(p.Email()))
	isSelfEdit := principalEmail != "" && strings.EqualFold(principalEmail, originalEmail)
	if isSelfEdit {
		// Block role demotion: self editing role to anything other than "admin".
		if newRole != "" && !strings.EqualFold(newRole, "admin") {
			if isUsersRoute {
				redirectWithError("You+can%27t+remove+your+own+admin+role.")
				return
			}
			http.Error(w, "self-edit rejected: you can't remove your own admin role", http.StatusBadRequest)
			return
		}
		// Block status deactivation: self editing status to a non-active value.
		if newStatus != "" && !strings.EqualFold(newStatus, "active") {
			if isUsersRoute {
				redirectWithError("You+can%27t+deactivate+yourself.")
				return
			}
			http.Error(w, "self-edit rejected: you can't deactivate yourself", http.StatusBadRequest)
			return
		}
		// Block email rename: self editing to a different email breaks the session identity.
		isRenameAttempt := newEmail != "" && !strings.EqualFold(newEmail, originalEmail)
		if isRenameAttempt {
			if isUsersRoute {
				redirectWithError("You+can%27t+change+your+own+email+here.")
				return
			}
			http.Error(w, "self-edit rejected: you can't change your own email here", http.StatusBadRequest)
			return
		}
	}

	current := h.provisionedUsersList()

	// Validate the new email when a rename is being attempted.
	isRename := !strings.EqualFold(originalEmail, newEmail)
	if isRename {
		if newEmail == "" || !strings.HasSuffix(newEmail, "@vivastudios.com") {
			if isUsersRoute {
				redirectWithError("invalid+email")
				return
			}
			http.Error(w, fmt.Sprintf("email %q must end with @vivastudios.com", newEmail), http.StatusBadRequest)
			return
		}
		// Collision check: new email must not already belong to another user.
		for _, u := range current {
			if strings.EqualFold(u.Email, newEmail) && !strings.EqualFold(u.Email, originalEmail) {
				if isUsersRoute {
					redirectWithError("email+already+exists")
					return
				}
				http.Error(w, fmt.Sprintf("email %q already exists", newEmail), http.StatusConflict)
				return
			}
		}
	}

	// Find and update the target entry (look up by originalEmail).
	found := false
	updated := make([]users.Principal, 0, len(current))
	for _, u := range current {
		existing := provisionedToUser(u)
		if strings.EqualFold(existing.Email, originalEmail) {
			found = true
			// Apply rename if requested.
			if isRename {
				existing.Email = newEmail
			}
			// Apply field updates; non-empty values overwrite; name may be set to empty.
			if newName != "" {
				existing.Name = newName
			}
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
		if isUsersRoute {
			redirectWithError("user+not+found")
			return
		}
		http.Error(w, fmt.Sprintf("user %q not found", originalEmail), http.StatusNotFound)
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
		if isUsersRoute {
			redirectWithError("last+admin+cannot+be+demoted+or+deactivated")
			return
		}
		http.Error(w, "edit rejected: at least one active admin must remain in the directory", http.StatusBadRequest)
		return
	}

	// Validate the updated entry individually (uses the new email as identifier).
	lookupEmail := newEmail
	if !isRename {
		lookupEmail = originalEmail
	}
	for _, u := range updated {
		if strings.EqualFold(u.Email, lookupEmail) {
			if err := users.ValidatePrincipal(u); err != nil {
				if isUsersRoute {
					redirectWithError("validation+error")
					return
				}
				http.Error(w, fmt.Sprintf("validation error: %v", err), http.StatusBadRequest)
				return
			}
		}
	}

	commitSubject := originalEmail
	if isRename {
		commitSubject = fmt.Sprintf("%s→%s", originalEmail, newEmail)
	}
	h.writePrincipalsAndReload(w, r, updated, fmt.Sprintf("admin: edit member %s", commitSubject))
}

// handleAdminUsersDelete handles POST /dashboard/admin/users/delete.
// Removes the user entry from users.yaml entirely (hard delete — not a status change).
// Admin-lockout guard: deleting the last active admin is rejected → 303 + error flash.
// Entry is identified by the "email" form field.
func (h *handlers) handleAdminUsersDelete(w http.ResponseWriter, r *http.Request) {
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

	redirectWithError := func(msg string) {
		target := "/dashboard/admin/users?" + url.Values{"flash": {msg}, "error": {"1"}}.Encode()
		http.Redirect(w, r, target, http.StatusSeeOther)
	}

	if h.cfg.ListProvisionedUsers == nil || h.cfg.UsersFilePath == "" {
		http.Error(w, "member management not configured", http.StatusServiceUnavailable)
		return
	}

	// Self-protection guard (D4-SP): the acting admin must not delete their own account.
	// Independent of the global zero-admin guard — fires even when other admins exist.
	principalEmail := strings.ToLower(strings.TrimSpace(p.Email()))
	if principalEmail != "" && strings.EqualFold(principalEmail, email) {
		redirectWithError("You+can%27t+delete+your+own+account.")
		return
	}

	current := h.provisionedUsersList()

	// Find and exclude the target entry.
	found := false
	updated := make([]users.Principal, 0, len(current))
	for _, u := range current {
		if strings.EqualFold(u.Email, email) {
			found = true
			// Skip — this entry is being deleted.
			continue
		}
		updated = append(updated, provisionedToUser(u))
	}
	if !found {
		redirectWithError("user+not+found")
		return
	}

	// Admin-lockout guard: ensure at least one active admin remains after deletion.
	activeAdminCount := 0
	for _, u := range updated {
		if strings.EqualFold(u.Role, "admin") && strings.EqualFold(u.Status, "active") {
			activeAdminCount++
		}
	}
	if activeAdminCount == 0 {
		redirectWithError("cannot+delete+last+active+admin")
		return
	}

	h.writePrincipalsAndReload(w, r, updated, fmt.Sprintf("admin: delete member %s", email))
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

	// isUsersRoute is true when the request arrives via /admin/users/ (the unified
	// Users management UI). /admin/members/deactivate is the legacy route; it uses
	// HTTP 400 for errors. /admin/users/deactivate uses 303+flash for UX consistency.
	isUsersDeactivateRoute := strings.Contains(r.URL.Path, "/admin/users/")

	// Self-protection guard (D4-SP): the acting admin must not deactivate themselves.
	// Independent of the global zero-admin guard — fires even when other admins exist.
	principalEmail := strings.ToLower(strings.TrimSpace(p.Email()))
	if principalEmail != "" && strings.EqualFold(principalEmail, email) {
		if isUsersDeactivateRoute {
			target := "/dashboard/admin/users?error=1&flash=You+can%27t+deactivate+yourself."
			http.Redirect(w, r, target, http.StatusSeeOther)
		} else {
			http.Error(w, "self-deactivation rejected: you can't deactivate yourself", http.StatusBadRequest)
		}
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

	// Redirect to the page that owns the form: /users routes go back to the
	// unified Users management page; /members routes go back to the legacy page.
	redirectTarget := "/dashboard/admin/members"
	if strings.Contains(r.URL.Path, "/admin/users/") {
		redirectTarget = "/dashboard/admin/users"
	}
	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", redirectTarget)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectTarget, http.StatusSeeOther)
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
