package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// sessionEnvelope signs and verifies the opaque outer wrapper for dashboard
// session tokens. It is package-private and shared by both auth.Service and
// auth.HeaderAuthenticator so there is exactly ONE HMAC-envelope code path.
//
// Format: base64url(payload) + "." + base64url(hmac-sha256(base64url(payload)))
// This mirrors the existing auth.Service.MintDashboardSession / sign logic in
// auth.go:76-95 — extracted verbatim so both codecs share one implementation.
type sessionEnvelope struct {
	secret []byte
}

// sign computes HMAC-SHA256 of payloadPart using the envelope's secret.
func (e sessionEnvelope) sign(payloadPart string) []byte {
	mac := hmac.New(sha256.New, e.secret)
	_, _ = mac.Write([]byte(payloadPart))
	return mac.Sum(nil)
}

// seal encodes payload as a base64url string and appends an HMAC signature.
// Returns a two-part "payload.sig" opaque token.
func (e sessionEnvelope) seal(payload []byte) string {
	payloadPart := base64.RawURLEncoding.EncodeToString(payload)
	sig := e.sign(payloadPart)
	return payloadPart + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// open verifies the HMAC signature and returns the raw payload bytes.
// Returns ErrInvalidDashboardSessionToken on any verification failure.
func (e sessionEnvelope) open(token string) ([]byte, error) {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, ErrInvalidDashboardSessionToken
	}
	expectedSig := e.sign(parts[0])
	providedSig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidDashboardSessionToken
	}
	if !hmac.Equal(expectedSig, providedSig) {
		return nil, ErrInvalidDashboardSessionToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidDashboardSessionToken
	}
	return payload, nil
}

// dashSessionClaims is the HeaderAuthenticator wrapper payload.
// It carries the full bearer JWT (not a token hash) plus an 8h outer expiry.
// The JWT itself is the source of truth for identity; the outer envelope only
// provides short-lived browser-session binding.
type dashSessionClaims struct {
	JWT string `json:"jwt"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// envelope returns a sessionEnvelope keyed by ha.jwtSecret.
func (ha *HeaderAuthenticator) envelope() sessionEnvelope {
	return sessionEnvelope{secret: []byte(ha.jwtSecret)}
}

// MintDashboardSession wraps bearerToken in an 8h HMAC-signed outer envelope.
// The token is opaque to clients; ParseDashboardSession verifies and unwraps it.
//
// Returns ErrBearerTokenNotConfigured when ha.jwtSecret is empty (deployments
// without JWT do not support auto-login — consistent with header-only mode).
func (ha *HeaderAuthenticator) MintDashboardSession(bearerToken string) (string, error) {
	if ha.jwtSecret == "" {
		return "", ErrBearerTokenNotConfigured
	}
	bearerToken = strings.TrimSpace(bearerToken)
	if bearerToken == "" {
		return "", ErrBearerTokenNotConfigured
	}
	now := ha.nowFunc()
	claims := dashSessionClaims{
		JWT: bearerToken,
		Iat: now.UTC().Unix(),
		Exp: now.UTC().Add(8 * time.Hour).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	return ha.envelope().seal(payload), nil
}

// ParseDashboardSession verifies the outer HMAC envelope and its 8h TTL.
// Returns the raw bearer value (JWT or static admin token) on success so
// authorizeDashboardRequest can route it appropriately:
//   - static admin token: checked by hmac.Equal against s.dashboardAdmin
//   - JWT: passed to s.auth.Authorize(Bearer <jwt>) for re-resolution
//
// The outer HMAC envelope provides integrity; the caller is responsible for
// validating the returned value through its own code path. No inner JWT
// verification is performed here — that would break the static admin token path
// where claims.JWT contains a raw non-JWT bearer value.
//
// Returns ErrBearerTokenNotConfigured when ha.jwtSecret is empty.
// Returns ErrInvalidDashboardSessionToken for any verification failure.
func (ha *HeaderAuthenticator) ParseDashboardSession(sessionToken string) (string, error) {
	if ha.jwtSecret == "" {
		return "", ErrBearerTokenNotConfigured
	}
	payload, err := ha.envelope().open(sessionToken)
	if err != nil {
		return "", err
	}
	var claims dashSessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ErrInvalidDashboardSessionToken
	}
	// Outer 8h expiry check.
	if claims.Exp <= ha.nowFunc().UTC().Unix() {
		return "", ErrInvalidDashboardSessionToken
	}
	if strings.TrimSpace(claims.JWT) == "" {
		return "", ErrInvalidDashboardSessionToken
	}
	return claims.JWT, nil
}

// nowFunc returns the current time, using the injected seam when available
// (tests only) and time.Now otherwise.
func (ha *HeaderAuthenticator) nowFunc() time.Time {
	if ha.now != nil {
		return ha.now()
	}
	return time.Now()
}
