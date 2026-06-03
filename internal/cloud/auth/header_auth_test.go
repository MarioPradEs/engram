package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

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
