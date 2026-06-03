package cloudserver

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func buildAuthTestLoader(t *testing.T, yaml string) *users.YAMLLoader {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/users.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write test users yaml: %v", err)
	}
	loader, err := users.NewYAMLLoader(path)
	if err != nil {
		t.Fatalf("NewYAMLLoader: %v", err)
	}
	return loader
}

func decodeJWTPayload(t *testing.T, token string) map[string]interface{} {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 jwt parts, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode jwt payload: %v", err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal jwt claims: %v", err)
	}
	return claims
}

// buildAuthEndpointServer returns a CloudServer wired with the /auth endpoint.
func buildAuthEndpointServer(loader *users.YAMLLoader, jwtSecret string) *CloudServer {
	ha, err := auth.NewHeaderAuthenticator(loader, "")
	if err != nil {
		panic("buildAuthEndpointServer: " + err.Error())
	}
	return New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, jwtSecret),
	)
}

// ─── Piece A tests ────────────────────────────────────────────────────────────

const authEndpointTestYAML = `users:
  - email: alice@vivastudios.com
    name: Alice Admin
    department: dev
    role: admin
    status: active
    enrolled:
      - project-a
  - email: removed@vivastudios.com
    name: Removed User
    department: qa
    role: member
    status: removed
    enrolled: []
  - email: offboarding@vivastudios.com
    name: Offboarding User
    department: qa
    role: member
    status: offboarding
    enrolled: []
`

// TestAuthEndpointMissingXForwardedEmail_returns401 verifies that /auth
// without X-Forwarded-Email returns 401 (not proxied through oauth2-proxy).
func TestAuthEndpointMissingXForwardedEmail_returns401(t *testing.T) {
	t.Parallel()
	loader := buildAuthTestLoader(t, authEndpointTestYAML)
	srv := buildAuthEndpointServer(loader, strings.Repeat("s", 32))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/auth?redirect_uri=http://127.0.0.1:9999/callback&state=abc", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAuthEndpointRemovedUser_returns403 verifies removed users cannot mint tokens.
func TestAuthEndpointRemovedUser_returns403(t *testing.T) {
	t.Parallel()
	loader := buildAuthTestLoader(t, authEndpointTestYAML)
	srv := buildAuthEndpointServer(loader, strings.Repeat("s", 32))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/auth?redirect_uri=http://127.0.0.1:9999/callback&state=csrf", nil)
	req.Header.Set("X-Forwarded-Email", "removed@vivastudios.com")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if body["error"] != "account_removed" {
		t.Errorf("expected error=account_removed, got %v", body["error"])
	}
}

// TestAuthEndpointNonLoopbackRedirectURI_returns400 tests open-redirect protection.
func TestAuthEndpointNonLoopbackRedirectURI_returns400(t *testing.T) {
	t.Parallel()
	loader := buildAuthTestLoader(t, authEndpointTestYAML)
	srv := buildAuthEndpointServer(loader, strings.Repeat("s", 32))

	cases := []string{
		"https://evil.com/callback",
		"http://notlocalhost:9999/callback",
		"",
		"ftp://127.0.0.1/bad",
	}
	for _, uri := range cases {
		uri := uri
		t.Run("uri="+uri, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			path := "/auth"
			if uri != "" {
				path += "?redirect_uri=" + uri + "&state=csrf"
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for redirect_uri=%q, got %d body=%q",
					uri, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAuthEndpointValidUser_redirectsWithTokenAndState is the happy path.
// Active user + loopback redirect_uri → 302, token present, exp-iat==604800, state echoed.
func TestAuthEndpointValidUser_redirectsWithTokenAndState(t *testing.T) {
	t.Parallel()
	loader := buildAuthTestLoader(t, authEndpointTestYAML)
	srv := buildAuthEndpointServer(loader, strings.Repeat("s", 32))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/auth?redirect_uri=http://127.0.0.1:9999/callback&state=my-state", nil)
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "http://127.0.0.1:9999/callback") {
		t.Fatalf("expected redirect to loopback callback, got %q", loc)
	}
	if !strings.Contains(loc, "state=my-state") {
		t.Errorf("expected state=my-state in redirect location, got %q", loc)
	}
	if !strings.Contains(loc, "token=") {
		t.Fatalf("expected token in redirect location, got %q", loc)
	}
	// Extract token and verify exp-iat == 604800 (7 days).
	tokenIdx := strings.Index(loc, "token=")
	tokenPart := loc[tokenIdx+len("token="):]
	if ampIdx := strings.Index(tokenPart, "&"); ampIdx != -1 {
		tokenPart = tokenPart[:ampIdx]
	}
	claims := decodeJWTPayload(t, tokenPart)
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if exp-iat != 604800 {
		t.Errorf("expected exp-iat=604800, got %v", exp-iat)
	}
	if claims["email"] != "alice@vivastudios.com" {
		t.Errorf("expected email=alice@vivastudios.com, got %v", claims["email"])
	}
}

// TestAuthEndpointLocalhostRedirectURI accepts localhost (valid loopback host).
func TestAuthEndpointLocalhostRedirectURI(t *testing.T) {
	t.Parallel()
	loader := buildAuthTestLoader(t, authEndpointTestYAML)
	srv := buildAuthEndpointServer(loader, strings.Repeat("s", 32))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/auth?redirect_uri=http://localhost:9999/callback&state=s1", nil)
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 for localhost redirect_uri, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAuthEndpointOffboardingUser_canMint verifies offboarding users can mint
// a token (login must work to allow data drain per design).
func TestAuthEndpointOffboardingUser_canMint(t *testing.T) {
	t.Parallel()
	loader := buildAuthTestLoader(t, authEndpointTestYAML)
	srv := buildAuthEndpointServer(loader, strings.Repeat("s", 32))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/auth?redirect_uri=http://127.0.0.1:9999/callback&state=drain", nil)
	req.Header.Set("X-Forwarded-Email", "offboarding@vivastudios.com")
	srv.Handler().ServeHTTP(rec, req)

	// Offboarding users must be allowed to mint a token (to drain their data).
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 for offboarding user, got %d body=%q", rec.Code, rec.Body.String())
	}
}
