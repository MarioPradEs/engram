package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// buildTestLoader returns a YAMLLoader from a temp YAML file with vivastudios.com emails.
func buildTestLoader(t *testing.T, yaml string) *users.YAMLLoader {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/users.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write users yaml: %v", err)
	}
	loader, err := users.NewYAMLLoader(path)
	if err != nil {
		t.Fatalf("NewYAMLLoader: %v", err)
	}
	return loader
}

const testYAML = `users:
  - email: alice@vivastudios.com
    name: Alice
    department: dev
    role: admin
    status: active
    enrolled:
      - project-a
  - email: bob@vivastudios.com
    name: Bob
    department: dev
    role: member
    status: active
    enrolled:
      - project-b
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
    enrolled:
      - project-c
`

// newTestHA builds a HeaderAuthenticator from testYAML with no bypass token.
func newTestHA(t *testing.T) *HeaderAuthenticator {
	t.Helper()
	loader := buildTestLoader(t, testYAML)
	ha, err := NewHeaderAuthenticator(loader, "")
	if err != nil {
		t.Fatalf("NewHeaderAuthenticator: %v", err)
	}
	return ha
}

// authorizeOK is a helper that calls Authorize and fatals if it returns an error.
// Returns the enriched *http.Request with principal in context.
func authorizeOK(t *testing.T, ha *HeaderAuthenticator, r *http.Request) *http.Request {
	t.Helper()
	enriched, err := ha.Authorize(r)
	if err != nil {
		t.Fatalf("Authorize: expected success, got %v", err)
	}
	return enriched
}

// TestHeaderAuthAuthorizationStatusCodes verifies the Three-State Access Matrix
// and domain check, asserting HTTP status code AND error code for each scenario.
func TestHeaderAuthAuthorizationStatusCodes(t *testing.T) {
	t.Parallel()

	ha := newTestHA(t)

	cases := []struct {
		name       string
		email      string
		method     string
		wantStatus int    // 0 = success (nil error)
		wantCode   string // "" when status == 0
	}{
		// Active user: full access regardless of method.
		{"active GET allowed", "alice@vivastudios.com", http.MethodGet, 0, ""},
		{"active POST allowed", "alice@vivastudios.com", http.MethodPost, 0, ""},

		// Offboarding: write allowed, read blocked.
		{"offboarding POST allowed", "offboarding@vivastudios.com", http.MethodPost, 0, ""},
		{"offboarding GET blocked", "offboarding@vivastudios.com", http.MethodGet, http.StatusForbidden, "account_offboarding"},

		// Removed: always blocked.
		{"removed GET blocked", "removed@vivastudios.com", http.MethodGet, http.StatusForbidden, "account_removed"},
		{"removed POST blocked", "removed@vivastudios.com", http.MethodPost, http.StatusForbidden, "account_removed"},

		// Not in directory (valid domain) → 403 user_not_provisioned.
		{"unknown vivastudios email", "nobody@vivastudios.com", http.MethodGet, http.StatusForbidden, "user_not_provisioned"},

		// Missing X-Forwarded-Email → plain 401 (not AuthError).
		{"missing email header", "", http.MethodGet, http.StatusUnauthorized, ""},

		// Non-vivastudios domain → plain 401 (not AuthError).
		{"non-vivastudios domain", "alice@gmail.com", http.MethodGet, http.StatusUnauthorized, ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, "/sync/something", nil)
			if tc.email != "" {
				req.Header.Set("X-Forwarded-Email", tc.email)
			}
			_, err := ha.Authorize(req)

			if tc.wantStatus == 0 {
				if err != nil {
					t.Errorf("expected success, got error: %v", err)
				}
				return
			}

			// Expect an error.
			if err == nil {
				t.Fatalf("expected error with status %d, got nil", tc.wantStatus)
			}

			var ae *AuthError
			switch {
			case tc.wantCode == "":
				// Plain error (401) — must NOT be an AuthError.
				if asAuthError(err, &ae) {
					t.Errorf("expected plain error (not AuthError), got AuthError{%d %s}", ae.Status, ae.Code)
				}
			default:
				// Typed AuthError — must have correct status and code.
				if !asAuthError(err, &ae) {
					t.Fatalf("expected AuthError with code %q, got plain error: %v", tc.wantCode, err)
				}
				if ae.Status != tc.wantStatus {
					t.Errorf("AuthError.Status = %d, want %d", ae.Status, tc.wantStatus)
				}
				if ae.Code != tc.wantCode {
					t.Errorf("AuthError.Code = %q, want %q", ae.Code, tc.wantCode)
				}
			}
		})
	}
}

// asAuthError wraps errors.As for *AuthError (avoids importing errors in test
// table without an extra package).
func asAuthError(err error, target **AuthError) bool {
	if err == nil {
		return false
	}
	type asIface interface{ As(any) bool }
	// Use type assertion approach to check for *AuthError via errors package.
	// We can call errors.As directly since we're in the same package.
	var ae *AuthError
	ok := false
	var checkErr error = err
	for checkErr != nil {
		if e, cast := checkErr.(*AuthError); cast {
			ae = e
			ok = true
			break
		}
		type unwrap interface{ Unwrap() error }
		if uw, cast := checkErr.(unwrap); cast {
			checkErr = uw.Unwrap()
		} else {
			break
		}
	}
	if ok {
		*target = ae
	}
	return ok
}

// TestHeaderAuthEnrolledProjectsInjectsGeneral verifies "general" is always injected (Q5).
func TestHeaderAuthEnrolledProjectsInjectsGeneral(t *testing.T) {
	t.Parallel()

	ha := newTestHA(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")
	enriched := authorizeOK(t, ha, req)

	enrolled := ha.EnrolledProjects(enriched.Context())

	hasGeneral := false
	hasProjectA := false
	for _, p := range enrolled {
		if p == "general" {
			hasGeneral = true
		}
		if p == "project-a" {
			hasProjectA = true
		}
	}
	if !hasGeneral {
		t.Errorf("expected 'general' in enrolled, got %v", enrolled)
	}
	if !hasProjectA {
		t.Errorf("expected 'project-a' in enrolled, got %v", enrolled)
	}
}

// TestHeaderAuthEnrolledProjectsEmptyWithoutAuthorize verifies EnrolledProjects
// returns an empty slice when no principal is in context.
func TestHeaderAuthEnrolledProjectsEmptyWithoutAuthorize(t *testing.T) {
	t.Parallel()

	ha := newTestHA(t)
	enrolled := ha.EnrolledProjects(context.Background())
	if len(enrolled) != 0 {
		t.Errorf("expected empty enrolled without Authorize, got %v", enrolled)
	}
}

// TestHeaderAuthAuthorizeProject verifies project authorization against enrolled set.
func TestHeaderAuthAuthorizeProject(t *testing.T) {
	t.Parallel()

	ha := newTestHA(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")
	enriched := authorizeOK(t, ha, req)
	ctx := enriched.Context()

	cases := []struct {
		project string
		wantErr bool
	}{
		{"project-a", false}, // explicitly enrolled
		{"general", false},   // always injected
		{"project-b", true},  // alice not enrolled
		{"", true},           // empty project
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.project, func(t *testing.T) {
			t.Parallel()
			err := ha.AuthorizeProject(ctx, tc.project)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for project %q, got nil", tc.project)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for project %q: %v", tc.project, err)
			}
		})
	}
}

// TestHeaderAuthAttribution verifies Attribution returns the correct values from the principal.
func TestHeaderAuthAttribution(t *testing.T) {
	t.Parallel()

	ha := newTestHA(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")
	enriched := authorizeOK(t, ha, req)

	attr := ha.Attribution(enriched.Context())
	if attr.UserEmail != "alice@vivastudios.com" {
		t.Errorf("UserEmail = %q, want alice@vivastudios.com", attr.UserEmail)
	}
	if attr.UserName != "Alice" {
		t.Errorf("UserName = %q, want Alice", attr.UserName)
	}
	if attr.Department != "dev" {
		t.Errorf("Department = %q, want dev", attr.Department)
	}
	if attr.UserDeleted {
		t.Error("UserDeleted should be false for active user")
	}
}

// TestHeaderAuthAttributionEmptyWithoutAuthorize verifies Attribution returns zero value
// when the context carries no principal.
func TestHeaderAuthAttributionEmptyWithoutAuthorize(t *testing.T) {
	t.Parallel()

	ha := newTestHA(t)
	attr := ha.Attribution(context.Background())
	if attr.UserEmail != "" {
		t.Errorf("expected empty Attribution without Authorize, got email=%q", attr.UserEmail)
	}
}

// TestHeaderAuthEmergencyBypassAccepted verifies bypass when ENGRAM_CLOUD_TOKEN is set.
func TestHeaderAuthEmergencyBypassAccepted(t *testing.T) {
	t.Parallel()

	loader := buildTestLoader(t, testYAML)
	ha, err := NewHeaderAuthenticator(loader, "super-secret-bypass")
	if err != nil {
		t.Fatalf("NewHeaderAuthenticator with bypass: %v", err)
	}

	// Request with correct bypass Bearer token — no X-Forwarded-Email needed.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer super-secret-bypass")

	enriched, err := ha.Authorize(req)
	if err != nil {
		t.Fatalf("expected bypass to succeed, got %v", err)
	}

	// Should authenticate as the sole admin (alice@vivastudios.com).
	attr := ha.Attribution(enriched.Context())
	if attr.UserEmail != "alice@vivastudios.com" {
		t.Errorf("bypass should authenticate as sole admin alice, got email=%q", attr.UserEmail)
	}
}

// TestHeaderAuthEmergencyBypassAbsentWhenEnvUnset verifies that a request without
// X-Forwarded-Email is rejected when no bypass token is configured.
func TestHeaderAuthEmergencyBypassAbsentWhenEnvUnset(t *testing.T) {
	t.Parallel()

	ha := newTestHA(t) // no bypass token

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No X-Forwarded-Email, no Authorization header.
	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error when no header and no bypass, got nil")
	}
}

// TestHeaderAuthBypassTokenWithMultipleAdmins verifies that NewHeaderAuthenticator
// returns an error when bypass is configured but the directory has >1 admins.
func TestHeaderAuthBypassTokenWithMultipleAdmins(t *testing.T) {
	t.Parallel()

	loader := buildTestLoader(t, `users:
  - email: alice@vivastudios.com
    name: Alice
    department: dev
    role: admin
    status: active
  - email: bob@vivastudios.com
    name: Bob
    department: dev
    role: admin
    status: active
`)
	_, err := NewHeaderAuthenticator(loader, "token")
	if err == nil {
		t.Fatal("expected error when bypass configured with multiple admins")
	}
}

// TestHeaderAuthConcurrentRequestsDoNotCrossContaminate verifies that two parallel
// requests with different identities never see each other's principal (S4 fix).
// Runs with -race to catch data races.
func TestHeaderAuthConcurrentRequestsDoNotCrossContaminate(t *testing.T) {
	t.Parallel()

	ha := newTestHA(t)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	runAs := func(email, wantName string) {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-Email", email)
		enriched, err := ha.Authorize(req)
		if err != nil {
			t.Errorf("Authorize(%q): unexpected error: %v", email, err)
			return
		}
		attr := ha.Attribution(enriched.Context())
		if attr.UserName != wantName {
			t.Errorf("cross-contamination: email=%q got name=%q, want %q", email, attr.UserName, wantName)
		}
	}

	for i := 0; i < goroutines; i++ {
		go runAs("alice@vivastudios.com", "Alice")
		go runAs("bob@vivastudios.com", "Bob")
	}
	wg.Wait()
}

// ─── Piece B: Bearer JWT auth on /sync/* ────────────────────────────────────

const bearerTestSecret = "a-test-secret-that-is-at-least-32chars-long"

// mintTestJWT is a helper that mints a JWT for tests.
func mintTestJWT(t *testing.T, email, name, dept, role string, offset int64) string {
	t.Helper()
	now := time.Now().UTC()
	claims := JWTClaims{
		Sub:        email,
		Email:      email,
		Name:       name,
		Department: dept,
		Role:       role,
	}
	if offset != 0 {
		// Expired: shift the issuance time into the past so exp is in the past too.
		now = now.Add(time.Duration(offset) * time.Second)
	}
	token, err := MintJWT(bearerTestSecret, claims, now)
	if err != nil {
		t.Fatalf("mintTestJWT: %v", err)
	}
	return token
}

// newTestHAWithBearer builds a HeaderAuthenticator with bearer JWT auth enabled.
func newTestHAWithBearer(t *testing.T) *HeaderAuthenticator {
	t.Helper()
	loader := buildTestLoader(t, testYAML)
	ha, err := NewHeaderAuthenticatorWithJWT(loader, "", bearerTestSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	return ha
}

// TestBearerJWT_ValidToken_ActiveUser verifies that a valid Bearer JWT for an
// active user is authorized and the principal is resolved from the directory.
func TestBearerJWT_ValidToken_ActiveUser(t *testing.T) {
	t.Parallel()
	ha := newTestHAWithBearer(t)
	token := mintTestJWT(t, "alice@vivastudios.com", "Alice", "dev", "admin", 0)

	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	enriched, err := ha.Authorize(req)
	if err != nil {
		t.Fatalf("expected success for valid Bearer JWT, got: %v", err)
	}
	attr := ha.Attribution(enriched.Context())
	if attr.UserEmail != "alice@vivastudios.com" {
		t.Errorf("expected UserEmail=alice@vivastudios.com, got %q", attr.UserEmail)
	}
	if attr.UserName != "Alice" {
		t.Errorf("expected UserName=Alice, got %q", attr.UserName)
	}
}

// TestBearerJWT_ExpiredToken_returns401 verifies expired JWT → 401.
func TestBearerJWT_ExpiredToken_returns401(t *testing.T) {
	t.Parallel()
	ha := newTestHAWithBearer(t)
	// Mint a token issued 8 days ago (exp = iat + 7d, so already expired).
	expiredToken := mintTestJWT(t, "alice@vivastudios.com", "Alice", "dev", "admin", -(8 * 24 * 3600))

	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	req.Header.Set("Authorization", "Bearer "+expiredToken)

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for expired JWT, got nil")
	}
}

// TestBearerJWT_TamperedToken_returns401 verifies that a tampered JWT fails verification.
func TestBearerJWT_TamperedToken_returns401(t *testing.T) {
	t.Parallel()
	ha := newTestHAWithBearer(t)
	token := mintTestJWT(t, "alice@vivastudios.com", "Alice", "dev", "admin", 0)

	// Tamper the payload segment.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("expected 3-part JWT")
	}
	parts[1] = parts[1] + "TAMPERED"
	tampered := strings.Join(parts, ".")

	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	req.Header.Set("Authorization", "Bearer "+tampered)

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for tampered JWT, got nil")
	}
}

// TestBearerJWT_ValidToken_RemovedUser_returns403 verifies that a valid JWT for
// a user who has since been removed from the directory returns 403 account_removed.
func TestBearerJWT_ValidToken_RemovedUser_returns403(t *testing.T) {
	t.Parallel()
	ha := newTestHAWithBearer(t)
	// "removed" user is in testYAML with status=removed.
	token := mintTestJWT(t, "removed@vivastudios.com", "Removed User", "qa", "member", 0)

	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for removed user JWT, got nil")
	}
	var ae *AuthError
	if !asAuthError(err, &ae) {
		t.Fatalf("expected AuthError, got: %v", err)
	}
	if ae.Code != "account_removed" {
		t.Errorf("expected code=account_removed, got %q", ae.Code)
	}
}

// TestBearerJWT_Offboarding_GET_returns403 verifies that an offboarding user
// cannot perform GET requests (read-only blocked per lifecycle matrix).
func TestBearerJWT_Offboarding_GET_returns403(t *testing.T) {
	t.Parallel()
	ha := newTestHAWithBearer(t)
	token := mintTestJWT(t, "offboarding@vivastudios.com", "Offboarding User", "qa", "member", 0)

	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for offboarding user GET via JWT, got nil")
	}
	var ae *AuthError
	if !asAuthError(err, &ae) {
		t.Fatalf("expected AuthError, got: %v", err)
	}
	if ae.Code != "account_offboarding" {
		t.Errorf("expected code=account_offboarding, got %q", ae.Code)
	}
}

// TestBearerJWT_EmergencyBypassStillWorks verifies the ENGRAM_CLOUD_TOKEN bypass
// continues to work when JWT auth is configured alongside it.
func TestBearerJWT_EmergencyBypassStillWorks(t *testing.T) {
	t.Parallel()
	loader := buildTestLoader(t, testYAML)
	ha, err := NewHeaderAuthenticatorWithJWT(loader, "super-secret-bypass", bearerTestSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT with bypass: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	req.Header.Set("Authorization", "Bearer super-secret-bypass")

	enriched, err := ha.Authorize(req)
	if err != nil {
		t.Fatalf("expected bypass to succeed, got: %v", err)
	}
	attr := ha.Attribution(enriched.Context())
	// Sole admin in testYAML is alice@vivastudios.com.
	if attr.UserEmail != "alice@vivastudios.com" {
		t.Errorf("bypass should authenticate as sole admin alice, got %q", attr.UserEmail)
	}
}

// TestBearerJWT_NoCreds_returns401 verifies that a request with no credentials
// (no X-Forwarded-Email and no Authorization header) returns 401.
func TestBearerJWT_NoCreds_returns401(t *testing.T) {
	t.Parallel()
	ha := newTestHAWithBearer(t)

	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error with no credentials, got nil")
	}
}

// TestBearerJWT_HeaderPrecedence_BearerWinsOnJWTInstance verifies that on a
// JWT-mode authenticator (jwtSecret set), a valid Bearer JWT authenticates even
// when X-Forwarded-Email is also present.  On the direct /sync/* path
// X-Forwarded-Email is attacker-controlled; only the Bearer JWT is trusted.
// The identity resolved MUST come from the JWT, not from the header.
//
// This replaces the old TestBearerJWT_HeaderPrecedence which asserted the
// insecure behaviour (X-Forwarded-Email wins).  Under the C1 fix the header
// is ignored on JWT-mode instances and Bob's JWT is the only valid credential.
func TestBearerJWT_HeaderPrecedence_BearerWinsOnJWTInstance(t *testing.T) {
	t.Parallel()
	ha := newTestHAWithBearer(t)
	// JWT for bob; alice is injected as a forged X-Forwarded-Email.
	token := mintTestJWT(t, "bob@vivastudios.com", "Bob", "dev", "member", 0)

	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")

	enriched, err := ha.Authorize(req)
	if err != nil {
		t.Fatalf("expected success with valid Bearer JWT, got: %v", err)
	}
	attr := ha.Attribution(enriched.Context())
	// On a JWT-mode authenticator the identity MUST come from the JWT (bob),
	// not from the attacker-supplied X-Forwarded-Email (alice).
	if attr.UserEmail != "bob@vivastudios.com" {
		t.Errorf("C1 regression: expected JWT identity (bob), got %q — X-Forwarded-Email must NOT win on JWT-mode authenticator", attr.UserEmail)
	}
}

// TestBearerJWT_ForgedHeader_NoJWT_IsRejected is the core C1 regression test.
// On a JWT-mode authenticator (jwtSecret set), a request that carries ONLY a
// forged X-Forwarded-Email (no Bearer JWT) MUST be rejected with 401.
// This is the exact attack vector C1 describes: an attacker reaches /sync/*
// directly (bypassing oauth2-proxy) and sets an arbitrary X-Forwarded-Email.
func TestBearerJWT_ForgedHeader_NoJWT_IsRejected(t *testing.T) {
	t.Parallel()
	ha := newTestHAWithBearer(t) // jwtSecret is set — JWT mode active

	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	// Forged header — no Bearer JWT accompanying it.
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("C1 CRITICAL: forged X-Forwarded-Email (no JWT) was ACCEPTED on JWT-mode authenticator — authentication bypass")
	}
	// Must be a plain 401-equivalent error (not a 403 AuthError).
	var ae *AuthError
	if asAuthError(err, &ae) && ae.Status == http.StatusForbidden {
		t.Errorf("expected plain 401 error, got AuthError 403 %q — wrong error type", ae.Code)
	}
}
