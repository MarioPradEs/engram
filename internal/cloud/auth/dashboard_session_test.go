package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// stubUserLoader is a minimal UserLoader for dashboard session tests.
// No YAML file needed.
type stubUserLoader struct {
	users map[string]users.Principal
}

func (s *stubUserLoader) Lookup(email string) (users.Principal, bool) {
	p, ok := s.users[email]
	return p, ok
}

func (s *stubUserLoader) SoleAdmin() (users.Principal, bool) {
	for _, p := range s.users {
		if strings.EqualFold(p.Role, "admin") {
			return p, true
		}
	}
	return users.Principal{}, false
}

func newTestHeaderAuthenticator(t *testing.T, secret string) *HeaderAuthenticator {
	t.Helper()
	loader := &stubUserLoader{users: map[string]users.Principal{
		"mario@vivastudios.com": {
			Email:  "mario@vivastudios.com",
			Name:   "Mario Pradas",
			Role:   "admin",
			Status: "active",
		},
	}}
	ha, err := NewHeaderAuthenticatorWithJWT(loader, "", secret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	return ha
}

// TestDashboardSessionCodec_RoundTrip verifies that MintDashboardSession →
// ParseDashboardSession returns the original bearer JWT.
func TestDashboardSessionCodec_RoundTrip(t *testing.T) {
	t.Parallel()

	secret := "thisis32bytesecretforthehmacsig!"
	// Use a fixed now for deterministic expiry: both the outer envelope (8h) and inner JWT (7d) are valid.
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ha := newTestHeaderAuthenticatorWithNow(t, secret, now)

	// Mint a 7-day bearer JWT at "now".
	bearerJWT, err := MintJWT(secret, JWTClaims{
		Sub:   "mario@vivastudios.com",
		Email: "mario@vivastudios.com",
		Name:  "Mario Pradas",
		Role:  "admin",
	}, now)
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}

	// Mint the session token from the bearer (clock at "now").
	sessionToken, err := ha.MintDashboardSession(bearerJWT)
	if err != nil {
		t.Fatalf("MintDashboardSession: %v", err)
	}
	if sessionToken == "" {
		t.Fatal("MintDashboardSession returned empty token")
	}

	// Parse 1 hour later — well within both the 8h outer window and 7d JWT window.
	haParse := newTestHeaderAuthenticatorWithNow(t, secret, now.Add(1*time.Hour))
	gotBearer, err := haParse.ParseDashboardSession(sessionToken)
	if err != nil {
		t.Fatalf("ParseDashboardSession: %v", err)
	}
	if gotBearer != bearerJWT {
		t.Errorf("ParseDashboardSession returned %q, want original bearer JWT", gotBearer)
	}
}

// TestDashboardSessionCodec_TamperedHMAC verifies that tampered outer HMAC → error.
func TestDashboardSessionCodec_TamperedHMAC(t *testing.T) {
	t.Parallel()

	secret := "thisis32bytesecretforthehmacsig!"
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ha := newTestHeaderAuthenticatorWithNow(t, secret, now)

	bearerJWT, _ := MintJWT(secret, JWTClaims{Sub: "u", Email: "mario@vivastudios.com"}, now)
	sessionToken, _ := ha.MintDashboardSession(bearerJWT)

	// Tamper: flip the last character of the HMAC signature part.
	parts := strings.Split(sessionToken, ".")
	if len(parts) != 2 {
		t.Fatalf("expected 2-part session token, got %d", len(parts))
	}
	sig := []byte(parts[1])
	sig[len(sig)-1] ^= 0xFF
	tampered := parts[0] + "." + string(sig)

	_, err := ha.ParseDashboardSession(tampered)
	if err == nil {
		t.Fatal("expected error for tampered HMAC, got nil")
	}
	if !errors.Is(err, ErrInvalidDashboardSessionToken) {
		t.Errorf("want ErrInvalidDashboardSessionToken, got %v", err)
	}
}

// TestDashboardSessionCodec_OuterExpired verifies that an outer Exp in the past → error.
func TestDashboardSessionCodec_OuterExpired(t *testing.T) {
	t.Parallel()

	secret := "thisis32bytesecretforthehmacsig!"
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	// Mint the JWT and session token both at "now".
	haMint := newTestHeaderAuthenticatorWithNow(t, secret, now)
	bearerJWT, _ := MintJWT(secret, JWTClaims{Sub: "u", Email: "mario@vivastudios.com"}, now)

	sessionToken, err := haMint.MintDashboardSession(bearerJWT)
	if err != nil {
		t.Fatalf("MintDashboardSession: %v", err)
	}

	// Parse 9 hours later — outer envelope has 8h TTL, so it should be expired.
	ha9h := newTestHeaderAuthenticatorWithNow(t, secret, now.Add(9*time.Hour))
	_, err = ha9h.ParseDashboardSession(sessionToken)
	if err == nil {
		t.Fatal("expected error for expired outer envelope, got nil")
	}
}

// TestDashboardSessionCodec_InnerJWTExpired verifies that ParseDashboardSession returns the
// wrapped JWT even when the inner JWT is past its 7d TTL — the codec only enforces the outer
// 8h envelope. Callers (authorizeDashboardRequest) are responsible for verifying the JWT they
// receive (via Authorize → VerifyJWT). This keeps the codec single-concern (integrity + 8h TTL).
func TestDashboardSessionCodec_InnerJWTExpired(t *testing.T) {
	t.Parallel()

	secret := "thisis32bytesecretforthehmacsig!"

	// Mint the JWT 8 days in the past so it is expired by the caller's standards.
	pastNow := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	expiredJWT, _ := MintJWT(secret, JWTClaims{Sub: "u", Email: "mario@vivastudios.com"}, pastNow)

	// Wrap it in a session minted at (pastNow + 6h) so outer env is valid.
	// We parse at (pastNow + 7h) — outer envelope still valid (< 8h).
	sessionMintTime := pastNow.Add(6 * time.Hour)
	haMint := newTestHeaderAuthenticatorWithNow(t, secret, sessionMintTime)
	sessionToken, err := haMint.MintDashboardSession(expiredJWT)
	if err != nil {
		t.Fatalf("MintDashboardSession: %v", err)
	}

	// Parse within the outer 8h window — codec should succeed and return the JWT.
	parseNow := pastNow.Add(7 * time.Hour)
	haParse := newTestHeaderAuthenticatorWithNow(t, secret, parseNow)
	gotJWT, err := haParse.ParseDashboardSession(sessionToken)
	if err != nil {
		t.Fatalf("ParseDashboardSession: expected nil error (outer envelope valid), got %v", err)
	}
	if gotJWT != expiredJWT {
		t.Errorf("ParseDashboardSession: want original JWT back, got %q", gotJWT)
	}
	// Demonstrate that the CALLER's Authorize rejects the JWT when it is actually expired.
	// The JWT was minted at pastNow (2025-01-01), which has a 7d TTL. Verify at 8 days later.
	expiredParseNow := pastNow.Add(8 * 24 * time.Hour)
	_, verifyErr := VerifyJWT(secret, gotJWT, expiredParseNow)
	if verifyErr == nil {
		t.Error("VerifyJWT: expected error for expired inner JWT when called by consumer, got nil")
	}
}

// TestDashboardSessionCodec_EmptySecret verifies empty jwtSecret → ErrBearerTokenNotConfigured.
func TestDashboardSessionCodec_EmptySecret(t *testing.T) {
	t.Parallel()

	loader := &stubUserLoader{users: map[string]users.Principal{}}
	// Empty secret — construct directly (empty disables JWT mode, doesn't error).
	haEmpty := &HeaderAuthenticator{loader: loader, jwtSecret: ""}

	_, err := haEmpty.MintDashboardSession("sometoken")
	if !errors.Is(err, ErrBearerTokenNotConfigured) {
		t.Errorf("MintDashboardSession: want ErrBearerTokenNotConfigured, got %v", err)
	}

	_, err = haEmpty.ParseDashboardSession("sometoken.sig")
	if !errors.Is(err, ErrBearerTokenNotConfigured) {
		t.Errorf("ParseDashboardSession: want ErrBearerTokenNotConfigured, got %v", err)
	}
}

// newTestHeaderAuthenticatorWithNow creates a HeaderAuthenticator with an injected
// now seam for deterministic expiry tests.
func newTestHeaderAuthenticatorWithNow(t *testing.T, secret string, now time.Time) *HeaderAuthenticator {
	t.Helper()
	loader := &stubUserLoader{users: map[string]users.Principal{
		"mario@vivastudios.com": {
			Email:  "mario@vivastudios.com",
			Name:   "Mario Pradas",
			Role:   "admin",
			Status: "active",
		},
	}}
	ha, err := NewHeaderAuthenticatorWithJWT(loader, "", secret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	ha.now = func() time.Time { return now }
	return ha
}
