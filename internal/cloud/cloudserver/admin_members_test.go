package cloudserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// ─── test YAML fixtures ───────────────────────────────────────────────────────

const membersTestAdminYAML = `users:
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

const membersTestSoleAdminYAML = `users:
  - email: mario@vivastudios.com
    name: Mario Pradas
    department: dev
    role: admin
    status: active
    enrolled:
      - general
`

// ─── helpers ─────────────────────────────────────────────────────────────────

// buildMembersServer returns a CloudServer wired with a YAMLLoader backed by a
// real temp file at usersPath. It wires ListProvisionedUsers, UserReload, and
// UsersFilePath on the dashboard.MountConfig via the server's member_management.go
// helpers — exactly as newCloudRuntime does in production.
func buildMembersServer(t *testing.T, jwtSecret, usersPath string) *CloudServer {
	t.Helper()
	loader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("buildMembersServer: NewYAMLLoader: %v", err)
	}
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("buildMembersServer: NewHeaderAuthenticatorWithJWT: %v", err)
	}
	srv := New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, jwtSecret),
		WithUserDirectoryReload(loader.Reload),
		WithUsersFilePath(usersPath),
	)
	return srv
}

// writeUsersYAML writes yaml content to the given path (overwrite).
func writeUsersYAML(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeUsersYAML: %v", err)
	}
}

// initTempUsersFile creates a temp directory, writes yaml, and returns (dir, path).
func initTempUsersFile(t *testing.T, yaml string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "users.yaml")
	writeUsersYAML(t, path, yaml)
	return dir, path
}

// ─── AM1: member session → 403 on GET /dashboard/admin/members ───────────────

func TestAdminMembers_AM1_MemberGets403(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestAdminYAML)
	srv := buildMembersServer(t, jwtSecret, usersPath)

	// Mint member session cookie.
	memberCookie := mintSessionCookieForEmail(t, srv, "esanchez@vivastudios.com", jwtSecret)

	req := makeSessionRequest(http.MethodGet, "/dashboard/admin/members", memberCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /dashboard/admin/members for member: status=%d, want 403", rec.Code)
	}
}

// ─── AM2: admin session → 200 + lists provisioned users ─────────────────────

func TestAdminMembers_AM2_AdminGets200WithProvisionedUsers(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestAdminYAML)
	srv := buildMembersServer(t, jwtSecret, usersPath)

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	req := makeSessionRequest(http.MethodGet, "/dashboard/admin/members", adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /dashboard/admin/members for admin: status=%d body=%q, want 200", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "mario@vivastudios.com") {
		t.Error("response body does not contain mario@vivastudios.com")
	}
	if !strings.Contains(body, "esanchez@vivastudios.com") {
		t.Error("response body does not contain esanchez@vivastudios.com")
	}
}

// ─── AM3: admin adds valid user → 200, users.yaml updated ───────────────────

func TestAdminMembers_AM3_AdminAddsValidUser(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestSoleAdminYAML)
	srv := buildMembersServer(t, jwtSecret, usersPath)

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("email", "nueva@vivastudios.com")
	form.Set("name", "Nueva User")
	form.Set("department", "dev")
	form.Set("role", "member")
	form.Set("status", "active")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/members/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Errorf("POST /dashboard/admin/members/add: status=%d body=%q, want 200 or 303", rec.Code, rec.Body.String())
	}

	// Reload the YAMLLoader directly to verify the file was updated.
	loader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("reload after add: %v", err)
	}
	p, ok := loader.Lookup("nueva@vivastudios.com")
	if !ok {
		t.Error("nueva@vivastudios.com not found in users.yaml after add")
	}
	if p.Department != "dev" {
		t.Errorf("nueva.Department = %q, want dev", p.Department)
	}
}

// ─── AM4: admin adds duplicate email → 409 ───────────────────────────────────

func TestAdminMembers_AM4_AdminAddsDuplicateEmail_409(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestAdminYAML)
	srv := buildMembersServer(t, jwtSecret, usersPath)

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("email", "esanchez@vivastudios.com") // already exists
	form.Set("name", "Dup User")
	form.Set("department", "qa")
	form.Set("role", "member")
	form.Set("status", "active")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/members/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate email: status=%d, want 409", rec.Code)
	}
}

// ─── AM5: admin adds non-vivastudios email → 400 ─────────────────────────────

func TestAdminMembers_AM5_AdminAddsExternalEmail_400(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestSoleAdminYAML)
	srv := buildMembersServer(t, jwtSecret, usersPath)

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("email", "externa@gmail.com")
	form.Set("name", "External")
	form.Set("department", "dev")
	form.Set("role", "member")
	form.Set("status", "active")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/members/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("external email: status=%d, want 400", rec.Code)
	}
}

// ─── AM6: demote last admin → 400 ────────────────────────────────────────────

func TestAdminMembers_AM6_DemoteLastAdmin_400(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestSoleAdminYAML) // sole admin
	srv := buildMembersServer(t, jwtSecret, usersPath)

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("email", "mario@vivastudios.com")
	form.Set("role", "member") // demote the only admin
	form.Set("department", "dev")
	form.Set("status", "active")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/members/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("demote last admin: status=%d, want 400", rec.Code)
	}
}

// ─── AM7: admin deactivates member → status=removed in YAML ──────────────────

func TestAdminMembers_AM7_AdminDeactivatesMember(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestAdminYAML)
	srv := buildMembersServer(t, jwtSecret, usersPath)

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("email", "esanchez@vivastudios.com")
	form.Set("status", "removed")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/members/deactivate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Errorf("deactivate: status=%d body=%q, want 200 or 303", rec.Code, rec.Body.String())
	}

	// Verify entry remains but status changed.
	loader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("reload after deactivate: %v", err)
	}
	p, ok := loader.Lookup("esanchez@vivastudios.com")
	if !ok {
		t.Error("esanchez@vivastudios.com not found after deactivate — entry should remain")
	}
	if p.Status != "removed" {
		t.Errorf("esanchez status = %q, want removed", p.Status)
	}
}

// ─── AM8: git commit failure → reload still fires ────────────────────────────

func TestAdminMembers_AM8_GitCommitFailure_ReloadStillFires(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestSoleAdminYAML)

	// Build a loader with a custom reload counter.
	loader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader: %v", err)
	}
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	reloadCalled := false
	srv := New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, jwtSecret),
		WithUserDirectoryReload(func() error {
			reloadCalled = true
			return loader.Reload()
		}),
		WithUsersFilePath(usersPath),
	)

	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	// Add a user to a directory that is NOT a git repo — git commit will fail.
	form := url.Values{}
	form.Set("email", "gitfail@vivastudios.com")
	form.Set("name", "Git Fail")
	form.Set("department", "dev")
	form.Set("role", "member")
	form.Set("status", "active")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/members/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	// Regardless of git commit outcome, we expect 200 or 303 (non-error response).
	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Errorf("after git-fail add: status=%d body=%q, want 200 or 303", rec.Code, rec.Body.String())
	}

	// The reload callback MUST have been called even if git failed.
	if !reloadCalled {
		t.Error("reload was NOT called after write (git commit failure should not block reload)")
	}

	// The new user should be in memory (reload succeeded).
	p, ok := loader.Lookup("gitfail@vivastudios.com")
	if !ok {
		t.Error("gitfail@vivastudios.com not found in loader after reload — file was written but reload didn't pick it up")
	}
	if p.Email != "gitfail@vivastudios.com" {
		t.Errorf("gitfail.Email = %q", p.Email)
	}
}

// ─── Source check: admin/users list comes from YAMLLoader, not contributors ──

// TestAdminMembers_ListFromYAML_NotContributors verifies that a user who has
// never pushed any observation still appears in the admin members list because
// the list is sourced from the provisioned users.yaml (D4 spec requirement).
func TestAdminMembers_ListFromYAML_NotContributors(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestAdminYAML)
	srv := buildMembersServer(t, jwtSecret, usersPath)

	// The fakeStore has no contributors data at all — if the endpoint relied on
	// ListContributorsPaginated it would return nothing. Verify esanchez still appears.
	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)
	req := makeSessionRequest(http.MethodGet, "/dashboard/admin/members", adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/admin/members: status=%d", rec.Code)
	}
	body := rec.Body.String()
	// esanchez has never pushed a contribution; she must still appear via YAMLLoader.
	if !strings.Contains(body, "esanchez@vivastudios.com") {
		t.Error("esanchez@vivastudios.com not in members list despite being in users.yaml and having no contributions — list is NOT sourced from YAMLLoader")
	}

	// Verify the UsersFilePath is wired onto the handlers (via MountConfig) by
	// checking the MountConfig closure is non-nil through the server's provisioned-users func.
	pvFn := srv.listProvisionedUsersFunc()
	if pvFn == nil {
		t.Error("listProvisionedUsersFunc() returned nil — ListProvisionedUsers not wired")
	}
	provisionedList := pvFn()
	emails := make(map[string]bool, len(provisionedList))
	for _, u := range provisionedList {
		emails[u.Email] = true
	}
	if !emails["esanchez@vivastudios.com"] {
		t.Error("esanchez@vivastudios.com not in provisioned users list from YAMLLoader.List()")
	}
	if !emails["mario@vivastudios.com"] {
		t.Error("mario@vivastudios.com not in provisioned users list from YAMLLoader.List()")
	}
}

// ─── member gets 403 on POST actions ──────────────────────────────────────────

func TestAdminMembers_MemberGets403OnPostActions(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestAdminYAML)
	srv := buildMembersServer(t, jwtSecret, usersPath)
	memberCookie := mintSessionCookieForEmail(t, srv, "esanchez@vivastudios.com", jwtSecret)

	routes := []string{
		"/dashboard/admin/members/add",
		"/dashboard/admin/members/edit",
		"/dashboard/admin/members/deactivate",
	}

	form := url.Values{"email": {"x@vivastudios.com"}, "name": {"X"}, "department": {"dev"}, "role": {"member"}, "status": {"active"}}
	for _, route := range routes {
		route := route
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(memberCookie)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST %s for member: status=%d, want 403", route, rec.Code)
			}
		})
	}
}

// ─── W1 fix: deactivating the sole active admin must return 400 ───────────────
//
// Without the last-admin guard in handleAdminMembersDeactivate, the write
// would succeed: loadAndValidate counts admins by role regardless of status,
// so a removed admin still satisfies the "at least one admin" check, allowing
// autoLoginFromHeader to block that admin permanently (admin lockout).

func TestAdminMembers_DeactivateLastAdmin_400(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("k", 32)
	_, usersPath := initTempUsersFile(t, membersTestSoleAdminYAML)

	// Capture the file content BEFORE the request so we can assert it is unchanged.
	beforeBytes, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatalf("read users.yaml before request: %v", err)
	}

	srv := buildMembersServer(t, jwtSecret, usersPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("email", "mario@vivastudios.com") // sole active admin
	form.Set("status", "removed")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/members/deactivate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("deactivate sole active admin: status=%d body=%q, want 400", rec.Code, rec.Body.String())
	}

	// The file MUST be unchanged — guard must fire before writePrincipalsAndReload.
	afterBytes, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatalf("read users.yaml after request: %v", err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Error("users.yaml was modified despite sole-admin deactivation attempt — guard did not fire before write")
	}
}

// Ensure ProvisionedUser conversion helper is exercised through the test.
var _ = dashboard.ProvisionedUser{}
