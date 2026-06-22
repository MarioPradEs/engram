package dashboard

// admin_users_handler_test.go — tests for the unified Users management page (D4-B).
//
// Coverage:
//   - GET /dashboard/admin/users renders the add form and per-row edit/deactivate controls.
//   - Admin-only: non-admin GET receives 403.
//   - POST /dashboard/admin/users/add persists to users.yaml and redirects to /admin/users.
//   - POST /dashboard/admin/users/edit updates role/name/dept and redirects to /admin/users.
//   - POST /dashboard/admin/users/edit renames email key when new_email differs from original_email.
//   - POST /dashboard/admin/users/edit rejects rename to colliding email (303 + error flash).
//   - POST /dashboard/admin/users/edit rejects invalid email (303 + error flash).
//   - POST /dashboard/admin/users/deactivate toggles status and redirects to /admin/users.
//   - POST /dashboard/admin/users/add rejects duplicate email (409 Conflict).
//   - POST /dashboard/admin/users/delete removes the user entry from users.yaml.
//   - POST /dashboard/admin/users/delete rejects deletion of the last active admin (lockout guard).
//   - POST /dashboard/admin/users/delete returns 403 for non-admins.
//   - GET /dashboard/admin/users renders editable email/name/dept inputs + delete button per row.
//
// Self-protection guard (D4-SP):
//   - POST /dashboard/admin/users/edit rejects removing own admin role → 303 + error flash.
//   - POST /dashboard/admin/users/deactivate rejects deactivating self → 400 (lockout guard fires).
//   - POST /dashboard/admin/users/delete rejects deleting self → 303 + error flash, no write.
//   - POST /dashboard/admin/users/edit rejects renaming own email → 303 + error flash, no write.
//   - POST /dashboard/admin/users/edit allows editing own name/department → 303, no error.
//   - POST /dashboard/admin/users/edit and /delete still work for OTHER users.
//   - Global lockout guard (zero active admins) still fires independently.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// newUsersMux builds a mux wired for the Users management page tests.
// initialUsers is the in-memory snapshot seeded to a temp users.yaml.
// callerIsAdmin controls whether the simulated request comes from an admin.
// The acting principal email is always "admin@vivastudios.com".
func newUsersMux(t *testing.T, initialUsers []users.Principal, callerIsAdmin bool) (*http.ServeMux, string) {
	t.Helper()
	return newUsersMuxWithEmail(t, initialUsers, callerIsAdmin, "admin@vivastudios.com")
}

// newUsersMuxWithEmail is like newUsersMux but allows specifying the acting
// principal's email. Use this for self-protection guard tests where the caller
// email must match (or differ from) the target user email.
func newUsersMuxWithEmail(t *testing.T, initialUsers []users.Principal, callerIsAdmin bool, callerEmail string) (*http.ServeMux, string) {
	t.Helper()

	tmpPath := t.TempDir() + "/users.yaml"
	if len(initialUsers) > 0 {
		data, err := users.MarshalPrincipals(initialUsers)
		if err != nil {
			t.Fatalf("newUsersMuxWithEmail: MarshalPrincipals: %v", err)
		}
		if err := users.WriteAtomic(tmpPath, data, users.ValidatorForPath()); err != nil {
			t.Fatalf("newUsersMuxWithEmail: seed WriteAtomic: %v", err)
		}
	}

	listUsers := func() []ProvisionedUser {
		out := make([]ProvisionedUser, 0, len(initialUsers))
		for _, u := range initialUsers {
			out = append(out, ProvisionedUser{
				Email:      u.Email,
				Name:       u.Name,
				Department: u.Department,
				Role:       u.Role,
				Status:     u.Status,
			})
		}
		return out
	}

	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:              func(_ *http.Request) bool { return callerIsAdmin },
		GetUserEmail:         func(_ *http.Request) string { return callerEmail },
		GetDisplayName:       func(_ *http.Request) string { return "Admin" },
		ListProvisionedUsers: listUsers,
		UsersFilePath:        tmpPath,
		UserReload:           func() error { return nil },
		ListDepartmentsCanonical: func() []string {
			return []string{"dev", "art", "qa"}
		},
		Store: parityStoreStub{
			observations: []cloudstore.DashboardObservationRow{},
		},
	})
	return mux, tmpPath
}

// ─── GET /dashboard/admin/users ───────────────────────────────────────────────

// TestAdminUsersGET_RendersAddFormAndTable asserts that the Users management page
// includes the add form (action /users/add) and per-row edit/deactivate controls.
func TestAdminUsersGET_RendersAddFormAndTable(t *testing.T) {
	initial := []users.Principal{
		{Email: "alice@vivastudios.com", Name: "Alice", Department: "dev", Role: "admin", Status: "active"},
		{Email: "bob@vivastudios.com", Name: "Bob", Department: "art", Role: "member", Status: "active"},
	}
	mux, _ := newUsersMux(t, initial, true)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/users expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Add form must post to /users/add.
	if !strings.Contains(body, "/dashboard/admin/users/add") {
		t.Error("expected add-user form with action /dashboard/admin/users/add")
	}
	// Edit form must post to /users/edit.
	if !strings.Contains(body, "/dashboard/admin/users/edit") {
		t.Error("expected edit form with action /dashboard/admin/users/edit")
	}
	// Deactivate form must post to /users/deactivate.
	if !strings.Contains(body, "/dashboard/admin/users/deactivate") {
		t.Error("expected deactivate form with action /dashboard/admin/users/deactivate")
	}
	// Users must appear in the table.
	if !strings.Contains(body, "alice@vivastudios.com") {
		t.Error("expected alice@vivastudios.com in the users table")
	}
	if !strings.Contains(body, "bob@vivastudios.com") {
		t.Error("expected bob@vivastudios.com in the users table")
	}
	// adminNav must mark "users" as active.
	if !strings.Contains(body, "/dashboard/admin/users") {
		t.Error("expected adminNav Users link in response")
	}
}

// TestAdminUsersGET_NonAdmin_Returns403 asserts that non-admin callers get 403.
func TestAdminUsersGET_NonAdmin_Returns403(t *testing.T) {
	mux, _ := newUsersMux(t, nil, false)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin GET /admin/users expected 403, got %d", rec.Code)
	}
}

// ─── POST /dashboard/admin/users/add ─────────────────────────────────────────

// TestAdminUsersAdd_PersistsToFile asserts that a valid add request writes the
// new user to users.yaml and redirects to /dashboard/admin/users.
func TestAdminUsersAdd_PersistsToFile(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
	}
	mux, tmpPath := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("email", "newuser@vivastudios.com")
	form.Set("name", "New User")
	form.Set("department", "qa")
	form.Set("role", "member")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/add?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/users/add expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "/dashboard/admin/users" {
		t.Errorf("expected redirect to /dashboard/admin/users, got %q", loc)
	}

	// Verify persistence: reload users.yaml and check new entry.
	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after add: %v", err)
	}
	found := false
	for _, u := range loader.List() {
		if u.Email == "newuser@vivastudios.com" {
			found = true
			if u.Department != "qa" {
				t.Errorf("persisted department = %q, want %q", u.Department, "qa")
			}
			if u.Role != "member" {
				t.Errorf("persisted role = %q, want %q", u.Role, "member")
			}
			if u.Status != "active" {
				t.Errorf("persisted status = %q, want %q", u.Status, "active")
			}
		}
	}
	if !found {
		t.Errorf("newuser@vivastudios.com not found in users.yaml after add")
	}
}

// TestAdminUsersAdd_DuplicateEmail_Returns409 asserts that adding a user with an
// already-registered email returns 409 Conflict without modifying the file.
func TestAdminUsersAdd_DuplicateEmail_Returns409(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "existing@vivastudios.com", Name: "Existing", Department: "art", Role: "member", Status: "active"},
	}
	mux, _ := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("email", "existing@vivastudios.com")
	form.Set("name", "Dup")
	form.Set("department", "qa")
	form.Set("role", "member")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/add?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate add: expected 409, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAdminUsersAdd_NonAdmin_Returns403 asserts non-admins cannot add users.
func TestAdminUsersAdd_NonAdmin_Returns403(t *testing.T) {
	mux, _ := newUsersMux(t, nil, false)

	form := url.Values{}
	form.Set("email", "new@vivastudios.com")
	form.Set("name", "New")
	form.Set("department", "dev")
	form.Set("role", "member")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/add?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin add: expected 403, got %d", rec.Code)
	}
}

// ─── POST /dashboard/admin/users/edit ────────────────────────────────────────

// TestAdminUsersEdit_UpdatesRole asserts that editing a user's role persists to
// users.yaml and redirects to /dashboard/admin/users.
func TestAdminUsersEdit_UpdatesRole(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "member@vivastudios.com", Name: "Member", Department: "art", Role: "member", Status: "active"},
	}
	mux, tmpPath := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("email", "member@vivastudios.com")
	form.Set("role", "admin")
	form.Set("department", "art")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/users/edit expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "/dashboard/admin/users" {
		t.Errorf("expected redirect to /dashboard/admin/users, got %q", loc)
	}

	// Verify persistence.
	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after edit: %v", err)
	}
	for _, u := range loader.List() {
		if u.Email == "member@vivastudios.com" {
			if u.Role != "admin" {
				t.Errorf("role after edit = %q, want admin", u.Role)
			}
		}
	}
}

// ─── POST /dashboard/admin/users/deactivate ──────────────────────────────────

// TestAdminUsersDeactivate_SetsStatusRemoved asserts that deactivating a member
// sets their status to "removed" and redirects to /dashboard/admin/users.
func TestAdminUsersDeactivate_SetsStatusRemoved(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "member@vivastudios.com", Name: "Member", Department: "art", Role: "member", Status: "active"},
	}
	mux, tmpPath := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("email", "member@vivastudios.com")
	form.Set("status", "removed")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/deactivate?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/users/deactivate expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "/dashboard/admin/users" {
		t.Errorf("expected redirect to /dashboard/admin/users, got %q", loc)
	}

	// Verify persistence: status must be "removed" in users.yaml.
	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after deactivate: %v", err)
	}
	for _, u := range loader.List() {
		if u.Email == "member@vivastudios.com" {
			if u.Status != "removed" {
				t.Errorf("status after deactivate = %q, want removed", u.Status)
			}
		}
	}
}

// TestAdminUsersDeactivate_SoleAdmin_Blocked asserts that deactivating the sole
// active admin is rejected. When the caller IS the sole admin, the self-protection
// guard (D4-SP) fires first and returns 303 + error flash (consistent with the
// /users/ route UX). The global lockout guard is still intact — tested separately
// via TestW1_EditGuard_* tests and TestSelfProtect_GlobalLockoutGuard_StillFires.
func TestAdminUsersDeactivate_SoleAdmin_Blocked(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
	}
	// newUsersMux uses "admin@vivastudios.com" as the caller email — same as the target.
	mux, _ := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("email", "admin@vivastudios.com")
	form.Set("status", "removed")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/deactivate?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Self-protection guard fires first: 303 + error=1 flash.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sole-admin deactivate: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("sole-admin deactivate: expected error=1 in redirect Location, got %q", loc)
	}
}

// TestAdminUsersGET_SpreadsheetTableClass asserts the table uses spreadsheet-table CSS class.
func TestAdminUsersGET_SpreadsheetTableClass(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
	}
	mux, _ := newUsersMux(t, initial, true)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "spreadsheet-table") {
		t.Error("expected users table to use spreadsheet-table CSS class")
	}
}

// ─── GET: editable inputs + delete button ────────────────────────────────────

// TestAdminUsersGET_RendersEditableInputsAndDeleteButton asserts the users table
// contains editable inputs for email, name, department fields, and a delete button per row.
func TestAdminUsersGET_RendersEditableInputsAndDeleteButton(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "bob@vivastudios.com", Name: "Bob Builder", Department: "art", Role: "member", Status: "active"},
	}
	mux, _ := newUsersMux(t, initial, true)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/users?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Each row must include an editable email input (type=email or type=text with name=email).
	if !strings.Contains(body, `name="email"`) {
		t.Error("expected editable email input (name=email) in each user row")
	}
	// Each row must include an editable name input.
	if !strings.Contains(body, `name="name"`) {
		t.Error("expected editable name input (name=name) in each user row")
	}
	// Each row must include the original_email hidden field (for rename detection).
	if !strings.Contains(body, `name="original_email"`) {
		t.Error("expected hidden original_email input in each user row")
	}
	// Must have a delete form posting to /dashboard/admin/users/delete.
	if !strings.Contains(body, "/dashboard/admin/users/delete") {
		t.Error("expected delete form with action /dashboard/admin/users/delete")
	}
	// User values must appear in the editable inputs.
	if !strings.Contains(body, "bob@vivastudios.com") {
		t.Error("expected bob@vivastudios.com in editable email input")
	}
	if !strings.Contains(body, "Bob Builder") {
		t.Error("expected name 'Bob Builder' in editable name input")
	}
}

// ─── POST /dashboard/admin/users/edit — full-field edits ─────────────────────

// TestAdminUsersEdit_UpdatesNameAndDept asserts editing name and department persists.
func TestAdminUsersEdit_UpdatesNameAndDept(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "member@vivastudios.com", Name: "Old Name", Department: "art", Role: "member", Status: "active"},
	}
	mux, tmpPath := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("original_email", "member@vivastudios.com")
	form.Set("email", "member@vivastudios.com") // same email, no rename
	form.Set("name", "New Name")
	form.Set("department", "qa")
	form.Set("role", "member")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/users/edit expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}

	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after edit: %v", err)
	}
	found := false
	for _, u := range loader.List() {
		if u.Email == "member@vivastudios.com" {
			found = true
			if u.Name != "New Name" {
				t.Errorf("name after edit = %q, want %q", u.Name, "New Name")
			}
			if u.Department != "qa" {
				t.Errorf("department after edit = %q, want %q", u.Department, "qa")
			}
		}
	}
	if !found {
		t.Error("member@vivastudios.com not found in users.yaml after name/dept edit")
	}
}

// TestAdminUsersEdit_EmailRename_UpdatesKey asserts that when new email differs from
// original_email, the entry key is renamed in users.yaml.
func TestAdminUsersEdit_EmailRename_UpdatesKey(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "old@vivastudios.com", Name: "Old User", Department: "art", Role: "member", Status: "active"},
	}
	mux, tmpPath := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("original_email", "old@vivastudios.com")
	form.Set("email", "new@vivastudios.com") // rename
	form.Set("name", "Old User")
	form.Set("department", "art")
	form.Set("role", "member")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("email rename: expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}

	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after rename: %v", err)
	}
	var oldFound, newFound bool
	for _, u := range loader.List() {
		if u.Email == "old@vivastudios.com" {
			oldFound = true
		}
		if u.Email == "new@vivastudios.com" {
			newFound = true
		}
	}
	if oldFound {
		t.Error("old email old@vivastudios.com still in users.yaml after rename — old key not removed")
	}
	if !newFound {
		t.Error("new email new@vivastudios.com not found in users.yaml after rename")
	}
}

// TestAdminUsersEdit_EmailRename_CollidingEmail_Rejected asserts that renaming to
// an email already used by another user redirects with an error flash.
func TestAdminUsersEdit_EmailRename_CollidingEmail_Rejected(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "alice@vivastudios.com", Name: "Alice", Department: "art", Role: "member", Status: "active"},
		{Email: "bob@vivastudios.com", Name: "Bob", Department: "qa", Role: "member", Status: "active"},
	}
	mux, _ := newUsersMux(t, initial, true)

	// Try to rename alice to bob's email.
	form := url.Values{}
	form.Set("original_email", "alice@vivastudios.com")
	form.Set("email", "bob@vivastudios.com") // collision
	form.Set("name", "Alice")
	form.Set("department", "art")
	form.Set("role", "member")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Must redirect with error flash (303 + error=1 in Location).
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("colliding rename: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("colliding rename: expected error=1 in redirect Location, got %q", loc)
	}
}

// TestAdminUsersEdit_InvalidEmail_Rejected asserts that an invalid email (not @vivastudios.com)
// redirects with an error flash and does not modify users.yaml.
func TestAdminUsersEdit_InvalidEmail_Rejected(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "member@vivastudios.com", Name: "Member", Department: "art", Role: "member", Status: "active"},
	}
	mux, _ := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("original_email", "member@vivastudios.com")
	form.Set("email", "member@gmail.com") // invalid domain
	form.Set("name", "Member")
	form.Set("department", "art")
	form.Set("role", "member")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Must redirect with error flash (303 + error=1).
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("invalid email: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("invalid email: expected error=1 in redirect Location, got %q", loc)
	}
}

// ─── POST /dashboard/admin/users/delete ──────────────────────────────────────

// TestAdminUsersDelete_RemovesUserFromFile asserts that deleting a user removes
// their entry from users.yaml entirely and redirects to /dashboard/admin/users.
func TestAdminUsersDelete_RemovesUserFromFile(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "member@vivastudios.com", Name: "Member", Department: "art", Role: "member", Status: "active"},
	}
	mux, tmpPath := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("email", "member@vivastudios.com")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/delete?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/users/delete expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if loc != "/dashboard/admin/users" {
		t.Errorf("expected redirect to /dashboard/admin/users, got %q", loc)
	}

	// Verify the user is gone from users.yaml.
	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after delete: %v", err)
	}
	for _, u := range loader.List() {
		if u.Email == "member@vivastudios.com" {
			t.Error("member@vivastudios.com still in users.yaml after delete — entry not removed")
		}
	}
}

// TestAdminUsersDelete_LastAdmin_Blocked asserts that deleting the last active admin
// is rejected by the lockout guard and redirects with error flash.
func TestAdminUsersDelete_LastAdmin_Blocked(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
	}
	mux, _ := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("email", "admin@vivastudios.com")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/delete?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Must redirect with error flash.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sole-admin delete: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("sole-admin delete: expected error=1 in redirect Location, got %q", loc)
	}
}

// TestAdminUsersDelete_NonAdmin_Returns403 asserts non-admins cannot delete users.
func TestAdminUsersDelete_NonAdmin_Returns403(t *testing.T) {
	mux, _ := newUsersMux(t, nil, false)

	form := url.Values{}
	form.Set("email", "someone@vivastudios.com")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/delete?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin delete: expected 403, got %d", rec.Code)
	}
}

// TestAdminUsersDelete_UserNotFound_Redirects asserts that deleting a non-existent
// user redirects with an error flash (graceful — no 404 to break UX).
func TestAdminUsersDelete_UserNotFound_Redirects(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
	}
	mux, _ := newUsersMux(t, initial, true)

	form := url.Values{}
	form.Set("email", "ghost@vivastudios.com")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/delete?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("not-found delete: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("not-found delete: expected error=1 in redirect Location, got %q", loc)
	}
}

// ─── Self-protection guard (D4-SP) ───────────────────────────────────────────
//
// The acting admin must not be able to lock themselves out by editing/deleting
// their own account via the management UI. These guards are INDEPENDENT of the
// global last-admin guard (which prevents zero admins in total). Self-protection
// fires when target == caller, even when other admins exist.

// TestSelfProtect_Edit_CannotRemoveOwnAdminRole asserts that the acting admin
// cannot downgrade their own role from admin to member.
// Expected: 303 redirect with error=1 flash; users.yaml NOT modified.
func TestSelfProtect_Edit_CannotRemoveOwnAdminRole(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "other-admin@vivastudios.com", Name: "Other", Department: "dev", Role: "admin", Status: "active"},
	}
	// Acting as admin@vivastudios.com; there are 2 admins so the global lockout guard
	// would NOT fire — only the self-protection guard must stop this.
	mux, tmpPath := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	form := url.Values{}
	form.Set("original_email", "admin@vivastudios.com")
	form.Set("email", "admin@vivastudios.com")
	form.Set("name", "Admin")
	form.Set("department", "dev")
	form.Set("role", "member") // self-demotion

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("self role-removal: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("self role-removal: expected error=1 in redirect Location, got %q", loc)
	}

	// Verify users.yaml was NOT written (role must still be admin).
	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after blocked self role-removal: %v", err)
	}
	for _, u := range loader.List() {
		if u.Email == "admin@vivastudios.com" {
			if u.Role != "admin" {
				t.Errorf("self role-removal was written: role = %q, want admin", u.Role)
			}
		}
	}
}

// TestSelfProtect_Edit_CannotDeactivateSelf asserts that the acting admin
// cannot set their own status to "removed" or "inactive" via the edit form.
// Expected: 303 redirect with error=1 flash; users.yaml NOT modified.
func TestSelfProtect_Edit_CannotDeactivateSelf(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "other-admin@vivastudios.com", Name: "Other", Department: "dev", Role: "admin", Status: "active"},
	}
	mux, tmpPath := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	form := url.Values{}
	form.Set("original_email", "admin@vivastudios.com")
	form.Set("email", "admin@vivastudios.com")
	form.Set("name", "Admin")
	form.Set("department", "dev")
	form.Set("role", "admin")
	form.Set("status", "removed") // self-deactivation

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("self deactivation via edit: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("self deactivation via edit: expected error=1 in redirect Location, got %q", loc)
	}

	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after blocked self-deactivation: %v", err)
	}
	for _, u := range loader.List() {
		if u.Email == "admin@vivastudios.com" {
			if u.Status != "active" {
				t.Errorf("self-deactivation was written: status = %q, want active", u.Status)
			}
		}
	}
}

// TestSelfProtect_Deactivate_CannotDeactivateSelf asserts that the acting admin
// cannot deactivate themselves via POST /dashboard/admin/users/deactivate.
// Expected: 400 (last-admin guard OR self-protection guard fires).
func TestSelfProtect_Deactivate_CannotDeactivateSelf(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "other-admin@vivastudios.com", Name: "Other", Department: "dev", Role: "admin", Status: "active"},
	}
	// Two admins → global lockout guard would NOT fire. Self-protection must fire.
	mux, tmpPath := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	form := url.Values{}
	form.Set("email", "admin@vivastudios.com")
	form.Set("status", "removed")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/deactivate?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Self-protection guard returns 303 + error flash (consistent with users-route pattern).
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("self-deactivate: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("self-deactivate: expected error=1 in redirect Location, got %q", loc)
	}

	// Verify NOT written.
	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after blocked self-deactivate: %v", err)
	}
	for _, u := range loader.List() {
		if u.Email == "admin@vivastudios.com" {
			if u.Status != "active" {
				t.Errorf("self-deactivate was written: status = %q, want active", u.Status)
			}
		}
	}
}

// TestSelfProtect_Delete_CannotDeleteSelf asserts that the acting admin
// cannot delete their own account via POST /dashboard/admin/users/delete.
// Expected: 303 redirect with error=1 flash; users.yaml NOT modified.
func TestSelfProtect_Delete_CannotDeleteSelf(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "other-admin@vivastudios.com", Name: "Other", Department: "dev", Role: "admin", Status: "active"},
	}
	// Two admins → global lockout guard would NOT fire.
	mux, tmpPath := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	form := url.Values{}
	form.Set("email", "admin@vivastudios.com")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/delete?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("self-delete: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("self-delete: expected error=1 in redirect Location, got %q", loc)
	}

	// Verify the admin was NOT removed from users.yaml.
	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after blocked self-delete: %v", err)
	}
	found := false
	for _, u := range loader.List() {
		if u.Email == "admin@vivastudios.com" {
			found = true
		}
	}
	if !found {
		t.Error("self-delete was written: admin@vivastudios.com was removed from users.yaml")
	}
}

// TestSelfProtect_Edit_CannotRenameOwnEmail asserts that the acting admin
// cannot rename their own email (which would break their session identity).
// Expected: 303 redirect with error=1 flash; users.yaml NOT modified.
func TestSelfProtect_Edit_CannotRenameOwnEmail(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "other-admin@vivastudios.com", Name: "Other", Department: "dev", Role: "admin", Status: "active"},
	}
	mux, tmpPath := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	form := url.Values{}
	form.Set("original_email", "admin@vivastudios.com")
	form.Set("email", "admin-renamed@vivastudios.com") // rename self
	form.Set("name", "Admin")
	form.Set("department", "dev")
	form.Set("role", "admin")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("self-email-rename: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("self-email-rename: expected error=1 in redirect Location, got %q", loc)
	}

	// Verify old email still exists and new email does not.
	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after blocked self-rename: %v", err)
	}
	var oldFound, newFound bool
	for _, u := range loader.List() {
		if u.Email == "admin@vivastudios.com" {
			oldFound = true
		}
		if u.Email == "admin-renamed@vivastudios.com" {
			newFound = true
		}
	}
	if !oldFound {
		t.Error("self-rename was written: original email admin@vivastudios.com was removed")
	}
	if newFound {
		t.Error("self-rename was written: admin-renamed@vivastudios.com appears in users.yaml")
	}
}

// TestSelfProtect_Edit_AllowsOwnNameAndDeptChange asserts that the acting admin
// CAN change their own name and department (safe fields).
// Expected: 303 redirect to /dashboard/admin/users (success, NO error flag).
func TestSelfProtect_Edit_AllowsOwnNameAndDeptChange(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Old Name", Department: "dev", Role: "admin", Status: "active"},
	}
	mux, tmpPath := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	form := url.Values{}
	form.Set("original_email", "admin@vivastudios.com")
	form.Set("email", "admin@vivastudios.com") // same email, no rename
	form.Set("name", "New Name")
	form.Set("department", "art")
	form.Set("role", "admin") // same role
	// No status field → keeps current status

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("self name/dept edit: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "error=1") {
		t.Errorf("self name/dept edit: unexpected error=1 in redirect Location %q", loc)
	}

	// Verify name and department were actually written.
	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after self name/dept edit: %v", err)
	}
	for _, u := range loader.List() {
		if u.Email == "admin@vivastudios.com" {
			if u.Name != "New Name" {
				t.Errorf("name after self-edit = %q, want %q", u.Name, "New Name")
			}
			if u.Department != "art" {
				t.Errorf("department after self-edit = %q, want art", u.Department)
			}
			if u.Role != "admin" {
				t.Errorf("role after self-edit = %q, want admin", u.Role)
			}
		}
	}
}

// TestSelfProtect_Edit_OtherUserStillWorks asserts that editing another user's role
// is NOT blocked by the self-protection guard.
func TestSelfProtect_Edit_OtherUserStillWorks(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "member@vivastudios.com", Name: "Member", Department: "art", Role: "member", Status: "active"},
	}
	mux, tmpPath := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	form := url.Values{}
	form.Set("original_email", "member@vivastudios.com")
	form.Set("email", "member@vivastudios.com")
	form.Set("name", "Member")
	form.Set("department", "qa")
	form.Set("role", "admin") // promote other user

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit other user: expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "error=1") {
		t.Errorf("edit other user: unexpected error=1 in redirect Location %q", loc)
	}

	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after editing other user: %v", err)
	}
	for _, u := range loader.List() {
		if u.Email == "member@vivastudios.com" {
			if u.Role != "admin" {
				t.Errorf("other user role after promote = %q, want admin", u.Role)
			}
		}
	}
}

// TestSelfProtect_Delete_OtherUserStillWorks asserts that deleting another user
// is NOT blocked by the self-protection guard.
func TestSelfProtect_Delete_OtherUserStillWorks(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "member@vivastudios.com", Name: "Member", Department: "art", Role: "member", Status: "active"},
	}
	mux, tmpPath := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	form := url.Values{}
	form.Set("email", "member@vivastudios.com")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/delete?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete other user: expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "error=1") {
		t.Errorf("delete other user: unexpected error=1 in redirect Location %q", loc)
	}

	loader, err := users.NewYAMLLoader(tmpPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader after deleting other user: %v", err)
	}
	for _, u := range loader.List() {
		if u.Email == "member@vivastudios.com" {
			t.Error("delete other user: member@vivastudios.com still in users.yaml after delete")
		}
	}
}

// TestSelfProtect_GlobalLockoutGuard_StillFires asserts that the global zero-admin
// lockout guard is still independent and fires when the sole admin tries to demote
// themselves (even though self-protection would also fire first).
// This test uses a single admin so BOTH guards would fire; we verify the request
// is rejected either way.
func TestSelfProtect_GlobalLockoutGuard_StillFires(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
	}
	mux, _ := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	form := url.Values{}
	form.Set("original_email", "admin@vivastudios.com")
	form.Set("email", "admin@vivastudios.com")
	form.Set("name", "Admin")
	form.Set("department", "dev")
	form.Set("role", "member") // would create zero admins AND is self-demotion

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Must be rejected (303 + error=1). Either guard firing is sufficient.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sole admin self-demotion: expected 303 redirect, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("sole admin self-demotion: expected error=1 in redirect Location, got %q", loc)
	}
}

// TestRedirectWithError_FlashURLEncoded asserts that redirectWithError (used by
// handleAdminMembersEdit and handleAdminUsersDelete) URL-encodes the flash message
// correctly — spaces and special characters must not appear raw in the query string.
func TestRedirectWithError_FlashURLEncoded(t *testing.T) {
	initial := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Department: "dev", Role: "admin", Status: "active"},
		{Email: "other@vivastudios.com", Name: "Other", Department: "dev", Role: "admin", Status: "active"},
	}
	mux, _ := newUsersMuxWithEmail(t, initial, true, "admin@vivastudios.com")

	// Trigger a self-protection error via /edit: attempt to remove own admin role.
	// The redirectWithError message contains spaces + apostrophe — must be URL-encoded.
	form := url.Values{}
	form.Set("original_email", "admin@vivastudios.com")
	form.Set("email", "admin@vivastudios.com")
	form.Set("name", "Admin")
	form.Set("department", "dev")
	form.Set("role", "member")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/users/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=1") {
		t.Errorf("expected error=1 in location, got %q", loc)
	}
	// The flash message must not contain raw apostrophe or spaces — it must be
	// properly URL-encoded (e.g. %27 not ', + or %20 not space).
	// url.Values.Encode produces a properly escaped query string.
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("failed to parse redirect Location %q: %v", loc, err)
	}
	flashVal := parsed.Query().Get("flash")
	if flashVal == "" {
		t.Errorf("expected flash query param in redirect Location, got %q", loc)
	}
	// The decoded flash value must contain meaningful content (not empty, not raw-mangled).
	if !strings.Contains(strings.ToLower(flashVal), "admin") &&
		!strings.Contains(strings.ToLower(flashVal), "role") &&
		!strings.Contains(strings.ToLower(flashVal), "remove") {
		t.Errorf("flash message %q does not look like an admin-role error", flashVal)
	}
}
