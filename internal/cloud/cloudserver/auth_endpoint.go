package cloudserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// AuthUserLoader is the interface the /auth endpoint needs to resolve principals.
// Satisfied by *users.YAMLLoader.
type AuthUserLoader interface {
	Lookup(email string) (users.Principal, bool)
}

// WithAuthEndpoint registers the GET /auth endpoint on the server.
// The endpoint is NOT behind withAuth — it relies on oauth2-proxy having injected
// X-Forwarded-Email, and it mints a 7-day engram JWT for the resolved principal.
//
// Security:
//   - redirect_uri MUST be a loopback address (127.0.0.1 or localhost with an
//     explicit port) to prevent open-redirect / token exfiltration (RFC 8252).
//   - state is echoed back untouched (CSRF protection enforced by the CLI).
//   - jwtSecret must be at least 32 bytes; passed from ENGRAM_JWT_SECRET.
func WithAuthEndpoint(loader AuthUserLoader, jwtSecret string) Option {
	return func(s *CloudServer) {
		s.authLoader = loader
		s.authJWTSecret = jwtSecret
	}
}

// registerAuthEndpoint mounts GET /auth on the mux. Called from routes() when
// authLoader is configured (i.e. WithAuthEndpoint was applied).
func (s *CloudServer) registerAuthEndpoint() {
	s.mux.HandleFunc("GET /auth", s.handleAuth)
}

// handleAuth implements the server side of the CLI OAuth loopback flow (RFC 8252).
//
// Flow:
//  1. Read X-Forwarded-Email (injected by oauth2-proxy — its absence means the
//     request was not proxied, so we return 401).
//  2. Resolve the principal from the directory.
//  3. Enforce lifecycle: removed → 403; offboarding is ALLOWED (login to drain).
//  4. Validate redirect_uri is a loopback address (security: open-redirect guard).
//  5. Mint a 7-day JWT signed with ENGRAM_JWT_SECRET.
//  6. HTTP 302 → redirect_uri?token=<JWT>&state=<state>
func (s *CloudServer) handleAuth(w http.ResponseWriter, r *http.Request) {
	// 1. Require X-Forwarded-Email (must come through oauth2-proxy).
	email := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Email")))
	if email == "" {
		http.Error(w, "unauthorized: X-Forwarded-Email is required", http.StatusUnauthorized)
		return
	}

	// 2. Resolve principal from directory.
	if s.authLoader == nil {
		http.Error(w, "internal error: auth endpoint not configured", http.StatusInternalServerError)
		return
	}
	p, ok := s.authLoader.Lookup(email)
	if !ok {
		jsonResponse(w, http.StatusForbidden, map[string]any{
			"error":   "user_not_provisioned",
			"message": fmt.Sprintf("user %q is not in the directory", email),
		})
		return
	}

	// 3. Lifecycle enforcement.
	switch strings.ToLower(p.Status) {
	case "removed":
		jsonResponse(w, http.StatusForbidden, map[string]any{
			"error":   "account_removed",
			"message": fmt.Sprintf("user %q account has been removed", email),
		})
		return
	case "offboarding":
		// Offboarding is ALLOWED here (login must work to allow data drain).
		// The /sync/* routes will enforce read-blocking via withAuth.
	}

	// 4. Validate redirect_uri is a loopback address.
	rawRedirectURI := strings.TrimSpace(r.URL.Query().Get("redirect_uri"))
	if err := validateLoopbackRedirectURI(rawRedirectURI); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}

	// 5. Mint 7-day JWT.
	secret := s.authJWTSecret
	if len(secret) < 32 {
		http.Error(w, "internal error: jwt secret too short or not configured", http.StatusInternalServerError)
		return
	}
	token, err := auth.MintJWT(secret, auth.JWTClaims{
		Sub:        p.Email,
		Email:      p.Email,
		Name:       p.Name,
		Department: p.Department,
		Role:       p.Role,
	}, time.Now().UTC())
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error: mint jwt: %v", err), http.StatusInternalServerError)
		return
	}

	// 6. Redirect to loopback callback with token + state.
	state := r.URL.Query().Get("state")
	redirectTarget := rawRedirectURI + "?token=" + url.QueryEscape(token)
	if state != "" {
		redirectTarget += "&state=" + url.QueryEscape(state)
	}
	http.Redirect(w, r, redirectTarget, http.StatusFound)
}

// validateLoopbackRedirectURI ensures the redirect_uri is a loopback address
// (http://127.0.0.1:<port>/... or http://localhost:<port>/...) to prevent
// open-redirect attacks and token exfiltration per RFC 8252 §8.3.
func validateLoopbackRedirectURI(raw string) error {
	if raw == "" {
		return fmt.Errorf("redirect_uri is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("redirect_uri is not a valid URL: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "http") {
		return fmt.Errorf("redirect_uri scheme must be http (loopback only)")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host != "127.0.0.1" && host != "localhost" {
		return fmt.Errorf("redirect_uri host must be 127.0.0.1 or localhost (loopback only, got %q)", host)
	}
	// Require an explicit port to avoid ambiguity (RFC 8252 §7.3).
	port := strings.TrimSpace(parsed.Port())
	if port == "" {
		return fmt.Errorf("redirect_uri must include an explicit port (e.g. http://127.0.0.1:PORT/...)")
	}
	return nil
}
