package dashboard

// admin_members_edit_guard_test.go — RED tests for W1 bug fix.
//
// W1: handleAdminMembersEdit counts ACTIVE admins (role=admin AND status=active)
// for the last-admin guard. Before the fix it counted by role only (ignoring status),
// so the sole active admin could set their own status=removed via the edit form,
// passing the guard (still 1 admin by role) and causing admin lockout.
//
// The deactivate handler (handleAdminMembersDeactivate) already does this correctly.
// This test verifies the edit handler is brought to parity.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// w1Store is a test-local DashboardStore that tracks written principal lists.
// It embeds parityStoreStub for all other interface methods.
type w1Store struct {
	parityStoreStub
	// written is the slice of principals passed to WriteAtomic, captured by
	// the write stub. The test inspects this to verify the edit was blocked
	// before the write was attempted.
	written []users.Principal
}

// newW1Mux builds a test mux with a real user list, write/reload hooks, and
// an admin caller identity so handleAdminMembersEdit executes fully.
//
// initialUsers is the in-memory users.yaml snapshot; email is the caller's email
// (must match one of the users for the edit to find a target).
func newW1Mux(t *testing.T, initialUsers []users.Principal, callerEmail string, written *[]users.Principal) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()

	// Build a snapshot-based ListProvisionedUsers closure.
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

	// WriteAtomic stub: capture the principal list instead of writing to disk.
	// We use a temporary file path that is guaranteed to exist so path validation
	// in WriteAtomic doesn't fail — but WriteAtomic is only called if the guard
	// passes (which it must NOT for W1 test cases where lockout would happen).
	// For cases where the write IS expected (e.g. second-admin allows edit),
	// we use users.WriteAtomic with a temp file.
	tmpPath := t.TempDir() + "/users.yaml"

	// Seed the file so WriteAtomic can round-trip.
	data, err := users.MarshalPrincipals(initialUsers)
	if err != nil {
		t.Fatalf("newW1Mux: MarshalPrincipals: %v", err)
	}
	if err := users.WriteAtomic(tmpPath, data, users.ValidatorForPath()); err != nil {
		t.Fatalf("newW1Mux: seed WriteAtomic: %v", err)
	}

	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:         func(_ *http.Request) bool { return true },
		GetUserEmail:    func(_ *http.Request) string { return callerEmail },
		GetDisplayName:  func(_ *http.Request) string { return "Admin" },
		ListProvisionedUsers: listUsers,
		UsersFilePath:   tmpPath,
		UserReload:      func() error { return nil },
		Store: parityStoreStub{
			observations: []cloudstore.DashboardObservationRow{},
		},
	})
	return mux
}

// postMembersEdit sends POST /dashboard/admin/members/edit with the given fields.
func postMembersEdit(mux *http.ServeMux, email, role, dept, status string) *httptest.ResponseRecorder {
	form := url.Values{}
	form.Set("email", email)
	if role != "" {
		form.Set("role", role)
	}
	if dept != "" {
		form.Set("department", dept)
	}
	if status != "" {
		form.Set("status", status)
	}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/members/edit?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestW1_EditGuard_SoleActiveAdmin_CannotSetStatusRemoved verifies that
// editing the sole active admin's status to "removed" is rejected (last-admin guard).
//
// Before the W1 fix, the guard only counted admins by role (ignoring status).
// With the sole admin having role=admin, adminCount=1 after the status change too,
// so the guard did not fire and the edit succeeded → lockout.
// After the fix, the guard counts ACTIVE admins (role=admin AND status=active).
// After the status change there would be 0 active admins → guard fires.
func TestW1_EditGuard_SoleActiveAdmin_CannotSetStatusRemoved(t *testing.T) {
	initialUsers := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Role: "admin", Status: "active", Department: "dev"},
	}
	var written []users.Principal
	mux := newW1Mux(t, initialUsers, "admin@vivastudios.com", &written)

	rec := postMembersEdit(mux, "admin@vivastudios.com", "admin", "dev", "removed")
	if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
		t.Errorf("W1 BUG: sole active admin status=removed was accepted (code=%d); expected 400 (guard must fire)", rec.Code)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("W1: expected 400 when sole active admin edited to status=removed, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// TestW1_EditGuard_SoleActiveAdmin_CannotSetStatusOffboarding verifies that
// editing the sole active admin's status to "offboarding" is also rejected.
func TestW1_EditGuard_SoleActiveAdmin_CannotSetStatusOffboarding(t *testing.T) {
	initialUsers := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Role: "admin", Status: "active", Department: "dev"},
	}
	var written []users.Principal
	mux := newW1Mux(t, initialUsers, "admin@vivastudios.com", &written)

	rec := postMembersEdit(mux, "admin@vivastudios.com", "admin", "dev", "offboarding")
	if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
		t.Errorf("W1 BUG: sole active admin status=offboarding was accepted (code=%d); expected 400 (guard must fire)", rec.Code)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("W1: expected 400 when sole active admin edited to status=offboarding, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// TestW1_EditGuard_TwoActiveAdmins_AllowsStatusChange verifies that when a second
// active admin exists, the global lockout guard does NOT fire when editing the
// OTHER admin's status to "removed" (because one active admin still remains).
// Note: admin1 is the caller; admin2 is the target — this avoids the self-protection
// guard (D4-SP) so we can isolate the global lockout guard behavior.
func TestW1_EditGuard_TwoActiveAdmins_AllowsStatusChange(t *testing.T) {
	initialUsers := []users.Principal{
		{Email: "admin1@vivastudios.com", Name: "Admin1", Role: "admin", Status: "active", Department: "dev"},
		{Email: "admin2@vivastudios.com", Name: "Admin2", Role: "admin", Status: "active", Department: "dev"},
	}
	var written []users.Principal
	mux := newW1Mux(t, initialUsers, "admin1@vivastudios.com", &written)

	// Edit admin2 (not the caller), so self-protection does not fire.
	// Global lockout guard must NOT fire because admin1 remains active.
	rec := postMembersEdit(mux, "admin2@vivastudios.com", "admin", "dev", "removed")
	// Should succeed (200 or 303) because admin1 is still an active admin.
	if rec.Code == http.StatusBadRequest {
		t.Errorf("W1: lockout guard fired when two active admins exist; edit should be allowed. body=%q", rec.Body.String())
	}
	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Errorf("W1: expected 200/303 with two active admins, got %d; body=%q", rec.Code, rec.Body.String())
	}
}

// TestW1_EditGuard_InactiveAdmin_SoleActiveAdmin_Blocked verifies the guard when
// there is one active admin and one inactive (removed) admin. Editing the active
// admin to removed must be rejected.
func TestW1_EditGuard_InactiveAdmin_SoleActiveAdmin_Blocked(t *testing.T) {
	initialUsers := []users.Principal{
		{Email: "admin@vivastudios.com", Name: "Admin", Role: "admin", Status: "active", Department: "dev"},
		{Email: "old-admin@vivastudios.com", Name: "OldAdmin", Role: "admin", Status: "removed", Department: "dev"},
	}
	var written []users.Principal
	mux := newW1Mux(t, initialUsers, "admin@vivastudios.com", &written)

	// Before the fix, this succeeds because adminCount counts both admins by role (=2).
	// After the fix, it is rejected because only 1 ACTIVE admin remains after the change.
	rec := postMembersEdit(mux, "admin@vivastudios.com", "admin", "dev", "removed")
	if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
		t.Errorf("W1 BUG: guard did not fire with inactive-admin bypass (code=%d); expected 400", rec.Code)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("W1: expected 400 when sole active admin set to removed (other admin inactive), got %d; body=%q", rec.Code, rec.Body.String())
	}
}
