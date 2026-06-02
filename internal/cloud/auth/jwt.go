package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const jwtExpSeconds = 604800 // 7 days

// jwtHeader is the fixed JOSE header for all tokens issued by this package.
// alg=HS256 (HMAC-SHA256), typ=JWT.
var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// JWTClaims holds the 7 standard claims for an engram-cloud identity token.
// iat and exp are set by MintJWT — callers supply Sub, Email, Name, Department, Role.
type JWTClaims struct {
	Sub        string `json:"sub"`
	Email      string `json:"email"`
	Name       string `json:"name,omitempty"`
	Department string `json:"department,omitempty"`
	Role       string `json:"role,omitempty"`
	Iat        int64  `json:"iat"`
	Exp        int64  `json:"exp"`
}

// MintJWT mints a signed HS256 JWT with the 7 standard claims.
// secret must be at least 32 bytes. now is the issuance time (enables
// deterministic tests). The returned token is header.payload.signature
// in base64url encoding with no padding (RFC 7519).
func MintJWT(secret string, claims JWTClaims, now time.Time) (string, error) {
	if len(secret) < 32 {
		return "", ErrSecretTooShort
	}
	claims.Iat = now.Unix()
	claims.Exp = now.Unix() + jwtExpSeconds

	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("auth: marshal jwt claims: %w", err)
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := jwtHeader + "." + payloadPart
	sig := jwtSign([]byte(secret), signingInput)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifyJWT verifies the HMAC-SHA256 signature and expiry of a JWT produced
// by MintJWT. Returns the decoded claims on success.
// now is the reference time for expiry checks.
func VerifyJWT(secret, token string, now time.Time) (JWTClaims, error) {
	if len(secret) < 32 {
		return JWTClaims{}, ErrSecretTooShort
	}
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return JWTClaims{}, fmt.Errorf("auth: malformed jwt (expected 3 parts)")
	}

	// Verify signature.
	signingInput := parts[0] + "." + parts[1]
	expectedSig := jwtSign([]byte(secret), signingInput)
	providedSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return JWTClaims{}, fmt.Errorf("auth: malformed jwt signature")
	}
	if !hmac.Equal(expectedSig, providedSig) {
		return JWTClaims{}, fmt.Errorf("auth: jwt signature mismatch")
	}

	// Decode payload.
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return JWTClaims{}, fmt.Errorf("auth: malformed jwt payload")
	}
	var claims JWTClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return JWTClaims{}, fmt.Errorf("auth: decode jwt payload: %w", err)
	}

	// Check expiry.
	if now.Unix() >= claims.Exp {
		return JWTClaims{}, fmt.Errorf("auth: jwt expired")
	}

	return claims, nil
}

// jwtSign computes HMAC-SHA256 of signingInput with key.
func jwtSign(key []byte, signingInput string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(signingInput))
	return mac.Sum(nil)
}
