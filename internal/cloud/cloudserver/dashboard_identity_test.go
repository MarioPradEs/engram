package cloudserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
)

// identityTestYAML covers admin, member, and offboarding users.
const identityTestYAML = `users:
  - email: mario@vivastudios.com
    name: Mario Pradas
    department: dev
    role: admin
    status: active
    enrolled:
      - general
  - email: member@vivastudios.com
    name: Regular Member
    department: dev
    role: member
    status: active
    enrolled:
      - general
`

// buildIdentityServer returns a CloudServer wired with HeaderAuthenticator,
// the /auth endpoint, AND a dashboard admin token (for backward compat tests).
func buildIdentityServer(t *testing.T, jwtSecret, adminToken string) *CloudServer {
	t.Helper()
	loader := buildAuthTestLoader(t, identityTestYAML)
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	srv := New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, jwtSecret),
		WithDashboardAdminToken(adminToken),
	)
	return srv
}

// mintSessionCookieForEmail mints a real JWT + session cookie for email using the auto-login path.
func mintSessionCookieForEmail(t *testing.T, srv *CloudServer, email, jwtSecret string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	req.Header.Set("X-Forwarded-Email", email)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("mintSessionCookieForEmail: expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	cookie := extractSessionCookie(t, rec)
	if cookie == nil {
		t.Fatal("mintSessionCookieForEmail: expected Set-Cookie engram_dashboard_token, got none")
	}
	return cookie
}

// makeSessionRequest creates an HTTP request with a session cookie attached.
func makeSessionRequest(method, path string, cookie *http.Cookie) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(cookie)
	return req
}

// TestIdentity_T5a_AutoLoginShowsRealName verifies T5a:
// After auto-login cookie, GET /dashboard/ → body contains "Mario Pradas", NOT "OPERATOR".
func TestIdentity_T5a_AutoLoginShowsRealName(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildIdentityServer(t, jwtSecret, "")

	cookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	rec := httptest.NewRecorder()
	req := makeSessionRequest(http.MethodGet, "/dashboard/", cookie)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("T5a: expected 200, got %d body=%q", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Mario Pradas") {
		t.Errorf("T5a: expected body to contain 'Mario Pradas', got excerpt: %q", body[:min(500, len(body))])
	}
	if strings.Contains(body, ">OPERATOR<") {
		t.Errorf("T5a: expected body NOT to contain '>OPERATOR<' after JWT login, got it")
	}
}

// TestIdentity_T5b_StaticAdminTokenShowsOperator verifies T5b:
// Static-admin-token cookie (no JWT) → body shows "OPERATOR" (fallback intact).
func TestIdentity_T5b_StaticAdminTokenShowsOperator(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	adminToken := strings.Repeat("a", 32)
	srv := buildIdentityServer(t, jwtSecret, adminToken)

	// Mint a session via the token-paste path with the admin token.
	// POST /dashboard/login with token=<adminToken>.
	formBody := strings.NewReader("token=" + adminToken)
	recLogin := httptest.NewRecorder()
	reqLogin := httptest.NewRequest(http.MethodPost, "/dashboard/login", formBody)
	reqLogin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(recLogin, reqLogin)

	if recLogin.Code != http.StatusSeeOther {
		t.Fatalf("T5b: POST /dashboard/login expected 303, got %d body=%q", recLogin.Code, recLogin.Body.String())
	}
	sessionCookie := extractSessionCookie(t, recLogin)
	if sessionCookie == nil {
		t.Fatal("T5b: expected Set-Cookie from token-paste login, got none")
	}

	rec := httptest.NewRecorder()
	req := makeSessionRequest(http.MethodGet, "/dashboard/", sessionCookie)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("T5b: expected 200, got %d body=%q", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	body := rec.Body.String()
	if !strings.Contains(body, "OPERATOR") {
		t.Errorf("T5b: expected body to contain 'OPERATOR' for static-admin login, got excerpt: %q", body[:min(500, len(body))])
	}
}

// TestIdentity_T6a_JWTAdminRoleShowsAdminControls verifies T6a:
// JWT with role:admin cookie → admin controls markup IS present.
func TestIdentity_T6a_JWTAdminRoleShowsAdminControls(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildIdentityServer(t, jwtSecret, "")

	cookie := mintSessionCookieForEmail(t, srv, "mario@vivastudios.com", jwtSecret)

	rec := httptest.NewRecorder()
	req := makeSessionRequest(http.MethodGet, "/dashboard/", cookie)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("T6a: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	// Admin controls: the Layout templ renders admin-specific links when IsAdmin() == true.
	// Check for the admin nav link presence.
	if !strings.Contains(body, "/dashboard/admin") {
		t.Errorf("T6a: expected admin controls (href /dashboard/admin) in body for role:admin user, got excerpt: %q", body[:min(500, len(body))])
	}
}

// TestIdentity_T6b_JWTMemberRoleHidesAdminControls verifies T6b:
// JWT with role:member cookie → admin controls NOT present.
func TestIdentity_T6b_JWTMemberRoleHidesAdminControls(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildIdentityServer(t, jwtSecret, "")

	cookie := mintSessionCookieForEmail(t, srv, "member@vivastudios.com", jwtSecret)

	rec := httptest.NewRecorder()
	req := makeSessionRequest(http.MethodGet, "/dashboard/", cookie)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("T6b: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	// Admin nav link should NOT be present for a non-admin user.
	if strings.Contains(body, "href=\"/dashboard/admin\"") {
		t.Errorf("T6b: expected NO admin nav link for role:member user, but found it in: %q", body[:min(500, len(body))])
	}
}

// TestIdentity_T6c_StaticAdminTokenShowsAdminControls verifies T6c:
// static ENGRAM_CLOUD_TOKEN cookie → admin controls ARE present (backward compat).
func TestIdentity_T6c_StaticAdminTokenShowsAdminControls(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	adminToken := strings.Repeat("a", 32)
	srv := buildIdentityServer(t, jwtSecret, adminToken)

	// Login with static admin token via token-paste.
	formBody := strings.NewReader("token=" + adminToken)
	recLogin := httptest.NewRecorder()
	reqLogin := httptest.NewRequest(http.MethodPost, "/dashboard/login", formBody)
	reqLogin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(recLogin, reqLogin)

	if recLogin.Code != http.StatusSeeOther {
		t.Fatalf("T6c: POST /dashboard/login expected 303, got %d body=%q", recLogin.Code, recLogin.Body.String())
	}
	sessionCookie := extractSessionCookie(t, recLogin)
	if sessionCookie == nil {
		t.Fatal("T6c: expected Set-Cookie from token-paste login, got none")
	}

	rec := httptest.NewRecorder()
	req := makeSessionRequest(http.MethodGet, "/dashboard/", sessionCookie)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("T6c: expected 200, got %d body=%q", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	body := rec.Body.String()
	// Static admin token → isDashboardAdmin returns true → admin nav link present.
	if !strings.Contains(body, "/dashboard/admin") {
		t.Errorf("T6c: expected admin controls for static-admin token, got excerpt: %q", body[:min(500, len(body))])
	}
}
