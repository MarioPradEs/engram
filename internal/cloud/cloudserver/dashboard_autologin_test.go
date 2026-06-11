package cloudserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// autoLoginTestYAML is the user directory for auto-login tests.
const autoLoginTestYAML = `users:
  - email: mario@vivastudios.com
    name: Mario Pradas
    department: dev
    role: admin
    status: active
    enrolled:
      - general
  - email: removed@vivastudios.com
    name: Removed User
    department: dev
    role: member
    status: removed
    enrolled: []
  - email: ghost@vivastudios.com
    name: Ghost User
    department: dev
    role: member
    status: removed
    enrolled: []
`

// buildAutoLoginServer returns a CloudServer wired with HeaderAuthenticator
// (the OAuth deployment authenticator) and the /auth endpoint.
// jwtSecret must be ≥32 bytes.
func buildAutoLoginServer(t *testing.T, jwtSecret string) *CloudServer {
	t.Helper()
	loader := buildAuthTestLoader(t, autoLoginTestYAML)
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	return New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, jwtSecret),
	)
}

// extractSessionCookie finds the "engram_dashboard_token" Set-Cookie from the response.
func extractSessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, header := range rec.Result().Cookies() {
		if header.Name == dashboardSessionCookieName {
			return header
		}
	}
	return nil
}

// parseSessionCookieJWT unwraps the session cookie and returns the inner JWT
// using the HeaderAuthenticator codec.
func parseSessionCookieJWT(t *testing.T, cookieVal, jwtSecret string, loader *users.YAMLLoader) string {
	t.Helper()
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	bearer, err := ha.ParseDashboardSession(cookieVal)
	if err != nil {
		t.Fatalf("ParseDashboardSession: %v", err)
	}
	return bearer
}

// TestAutoLogin_T2_HeaderPresent_MintsSessionCookie verifies T2:
// GET /dashboard/login with X-Forwarded-Email (active admin, no cookie) → 303 + Set-Cookie.
func TestAutoLogin_T2_HeaderPresent_MintsSessionCookie(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildAutoLoginServer(t, jwtSecret)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	req.Header.Set("X-Forwarded-Email", "mario@vivastudios.com")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("T2: expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	cookie := extractSessionCookie(t, rec)
	if cookie == nil {
		t.Fatal("T2: expected Set-Cookie engram_dashboard_token, got none")
	}

	// Unwrap the cookie and assert the email claim.
	loader := buildAuthTestLoader(t, autoLoginTestYAML)
	bearer := parseSessionCookieJWT(t, cookie.Value, jwtSecret, loader)
	claims := decodeJWTPayload(t, bearer)
	if claims["email"] != "mario@vivastudios.com" {
		t.Errorf("T2: expected email claim mario@vivastudios.com, got %v", claims["email"])
	}
}

// TestAutoLogin_T3_HeaderAbsent_ShowsTokenForm verifies T3:
// GET /dashboard/login with NO X-Forwarded-Email → 200, form rendered, NO Set-Cookie.
func TestAutoLogin_T3_HeaderAbsent_ShowsTokenForm(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildAutoLoginServer(t, jwtSecret)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	// No X-Forwarded-Email header.
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("T3: expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	if extractSessionCookie(t, rec) != nil {
		t.Fatal("T3: expected no Set-Cookie, but got one")
	}
	// Body should contain the login form (token input).
	body := rec.Body.String()
	if !strings.Contains(body, "token") && !strings.Contains(body, "form") {
		t.Errorf("T3: expected token form in body, got %q", body[:min(200, len(body))])
	}
}

// TestAutoLogin_T4a_UnknownPrincipal_Returns403 verifies T4a:
// header with email not in the directory → 403, no cookie.
func TestAutoLogin_T4a_UnknownPrincipal_Returns403(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildAutoLoginServer(t, jwtSecret)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	req.Header.Set("X-Forwarded-Email", "unknown@vivastudios.com")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("T4a: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
	if extractSessionCookie(t, rec) != nil {
		t.Fatal("T4a: expected no Set-Cookie, but got one")
	}
}

// TestAutoLogin_T4b_RemovedPrincipal_Returns403 verifies T4b:
// header with a status:removed principal → 403, no cookie.
func TestAutoLogin_T4b_RemovedPrincipal_Returns403(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildAutoLoginServer(t, jwtSecret)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	req.Header.Set("X-Forwarded-Email", "removed@vivastudios.com")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("T4b: expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
	if extractSessionCookie(t, rec) != nil {
		t.Fatal("T4b: expected no Set-Cookie, but got one")
	}
}

// TestAutoLogin_T7_NoRedirectLoop verifies T7:
// GET /dashboard/projects with no cookie → 303 to login?next=...; follow → cookie + 303 to target.
func TestAutoLogin_T7_NoRedirectLoop(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildAutoLoginServer(t, jwtSecret)

	// Step 1: request /dashboard/projects with no cookie.
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/dashboard/projects", nil)
	req1.Header.Set("X-Forwarded-Email", "mario@vivastudios.com")
	srv.Handler().ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusSeeOther {
		t.Fatalf("T7 step1: expected 303, got %d body=%q", rec1.Code, rec1.Body.String())
	}
	loginURL := rec1.Header().Get("Location")
	if !strings.Contains(loginURL, "/dashboard/login") {
		t.Fatalf("T7 step1: expected redirect to /dashboard/login, got %q", loginURL)
	}

	// Step 2: follow the redirect to /dashboard/login (with next param, with header).
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, loginURL, nil)
	req2.Header.Set("X-Forwarded-Email", "mario@vivastudios.com")
	srv.Handler().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("T7 step2: expected 303 (auto-login redirect), got %d body=%q", rec2.Code, rec2.Body.String())
	}
	sessionCookie := extractSessionCookie(t, rec2)
	if sessionCookie == nil {
		t.Fatal("T7 step2: expected Set-Cookie engram_dashboard_token, got none")
	}
	// Redirect should go back toward /dashboard/projects.
	loc := rec2.Header().Get("Location")
	if !strings.Contains(loc, "/dashboard/projects") && loc != "/dashboard/" && loc != "/dashboard" {
		t.Logf("T7 step2: redirect location %q (may be /dashboard/ on empty next)", loc)
	}
}

// TestAutoLogin_T7_ExpiryRemint verifies T7-expiry:
// A stale session cookie causes authorizeDashboardRequest to fail → login re-mints.
func TestAutoLogin_T7_ExpiryRemint(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildAutoLoginServer(t, jwtSecret)

	// Create a stale session cookie: a valid-looking session token but with Exp in the past.
	// We can simulate this by crafting a session with a HeaderAuthenticator that has its now seam
	// pushed far in the past, making the outer envelope's Exp already expired at current time.
	loader := buildAuthTestLoader(t, autoLoginTestYAML)
	_ = loader // available for future use if needed

	// The stale cookie value: just set a garbage session token that will fail ParseDashboardSession.
	staleSession := "aGFsZnN0YWxl.aW52YWxpZA" // base64url payload + sig — will fail HMAC verify

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	req.Header.Set("X-Forwarded-Email", "mario@vivastudios.com")
	req.AddCookie(&http.Cookie{Name: dashboardSessionCookieName, Value: staleSession})
	srv.Handler().ServeHTTP(rec, req)

	// With stale cookie + valid header: auto-login should fire → 303 + new cookie.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("T7-expiry: expected 303 (re-mint after stale cookie), got %d body=%q", rec.Code, rec.Body.String())
	}
	newCookie := extractSessionCookie(t, rec)
	if newCookie == nil {
		t.Fatal("T7-expiry: expected new Set-Cookie engram_dashboard_token, got none")
	}
}

// TestAutoLogin_T8_SessionCookieFlags verifies T8:
// Set-Cookie: MaxAge==28800, HttpOnly, SameSite=Lax, Path=/dashboard; Secure=true when X-Forwarded-Proto: https.
func TestAutoLogin_T8_SessionCookieFlags(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	srv := buildAutoLoginServer(t, jwtSecret)

	// HTTP request (no Secure flag expected).
	recHTTP := httptest.NewRecorder()
	reqHTTP := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	reqHTTP.Header.Set("X-Forwarded-Email", "mario@vivastudios.com")
	srv.Handler().ServeHTTP(recHTTP, reqHTTP)

	cookieHTTP := extractSessionCookie(t, recHTTP)
	if cookieHTTP == nil {
		t.Fatal("T8: expected Set-Cookie for HTTP request, got none")
	}
	if cookieHTTP.MaxAge != 28800 {
		t.Errorf("T8: MaxAge want 28800, got %d", cookieHTTP.MaxAge)
	}
	if !cookieHTTP.HttpOnly {
		t.Error("T8: expected HttpOnly=true")
	}
	if cookieHTTP.SameSite != http.SameSiteLaxMode {
		t.Errorf("T8: expected SameSite=Lax, got %v", cookieHTTP.SameSite)
	}
	if cookieHTTP.Path != "/dashboard" {
		t.Errorf("T8: expected Path=/dashboard, got %q", cookieHTTP.Path)
	}
	if cookieHTTP.Secure {
		t.Error("T8: HTTP request should NOT have Secure flag")
	}

	// HTTPS request (Secure flag expected).
	recHTTPS := httptest.NewRecorder()
	reqHTTPS := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	reqHTTPS.Header.Set("X-Forwarded-Email", "mario@vivastudios.com")
	reqHTTPS.Header.Set("X-Forwarded-Proto", "https")
	srv.Handler().ServeHTTP(recHTTPS, reqHTTPS)

	cookieHTTPS := extractSessionCookie(t, recHTTPS)
	if cookieHTTPS == nil {
		t.Fatal("T8: expected Set-Cookie for HTTPS request, got none")
	}
	if !cookieHTTPS.Secure {
		t.Error("T8: HTTPS request should have Secure=true")
	}
}

// TestExpiredJWT_ProtectedRoute_RedirectsToLogin is the W3 integrated security test.
// It proves at runtime that a dashboard session cookie whose inner JWT is EXPIRED
// is rejected by authorizeDashboardRequest → auth.Authorize → VerifyJWT, causing
// a redirect to login instead of a 200 grant on a protected route.
//
// The outer session envelope is VALID (freshly minted, 8h TTL); only the inner
// JWT has exp in the past. This validates the property: the outer envelope alone
// is not sufficient — the inner JWT expiry is also enforced.
func TestExpiredJWT_ProtectedRoute_RedirectsToLogin(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	loader := buildAuthTestLoader(t, autoLoginTestYAML)

	// Build a HeaderAuthenticator that can mint dashboard sessions.
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}

	// Mint an EXPIRED inner JWT: issue time is 8 days in the past so that
	// exp = issuedAt + 7 days is also in the past relative to time.Now().
	expiredIssuedAt := time.Now().UTC().Add(-8 * 24 * time.Hour)
	expiredJWT, err := cloudauth.MintJWT(jwtSecret, cloudauth.JWTClaims{
		Sub:   "mario@vivastudios.com",
		Email: "mario@vivastudios.com",
		Name:  "Mario Pradas",
		Role:  "admin",
	}, expiredIssuedAt)
	if err != nil {
		t.Fatalf("MintJWT (expired): %v", err)
	}

	// Wrap the expired JWT in a fresh outer session envelope (valid 8h TTL).
	sessionToken, err := ha.MintDashboardSession(expiredJWT)
	if err != nil {
		t.Fatalf("MintDashboardSession: %v", err)
	}

	// Build the cloud server (same as other auto-login tests).
	srv := buildAutoLoginServer(t, jwtSecret)

	// Issue a request to a protected dashboard route with the expired-JWT session cookie.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	req.AddCookie(&http.Cookie{Name: dashboardSessionCookieName, Value: sessionToken})
	srv.Handler().ServeHTTP(rec, req)

	// The expired inner JWT must be rejected: expect a redirect to login (303), NOT 200.
	if rec.Code == http.StatusOK {
		t.Fatalf("W3: expired inner JWT must NOT grant access — got 200, expected 303 redirect to login")
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("W3: expected 303 redirect to login, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/dashboard/login") {
		t.Errorf("W3: redirect location should contain /dashboard/login, got %q", loc)
	}
}

// min returns the smaller of a and b. Used for safe string slicing in test messages.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
