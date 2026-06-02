package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// buildTestLoader returns a minimal YAMLLoader from a temp YAML file.
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
  - email: alice@example.com
    name: Alice
    department: engineering
    role: admin
    status: active
    enrolled:
      - project-a
  - email: bob@example.com
    name: Bob
    department: engineering
    role: member
    status: active
    enrolled:
      - project-b
  - email: removed@example.com
    name: Removed User
    department: operations
    role: member
    status: removed
    enrolled: []
`

// TestHeaderAuthenticatorAuthorize verifies X-Forwarded-Email based auth.
func TestHeaderAuthenticatorAuthorize(t *testing.T) {
	t.Parallel()

	loader := buildTestLoader(t, testYAML)
	ha := NewHeaderAuthenticator(loader)

	cases := []struct {
		name    string
		email   string
		wantErr bool
	}{
		{"active user passes", "alice@example.com", false},
		{"active member passes", "bob@example.com", false},
		{"removed user rejected", "removed@example.com", true},
		{"unknown email rejected", "nobody@example.com", true},
		{"empty email rejected", "", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.email != "" {
				req.Header.Set("X-Forwarded-Email", tc.email)
			}
			err := ha.Authorize(req)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for email %q, got nil", tc.email)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for email %q: %v", tc.email, err)
			}
		})
	}
}

// TestHeaderAuthenticatorEnrolledProjectsInjectsGeneral verifies that
// HeaderAuthenticator.EnrolledProjects() always includes "general" in the
// returned set regardless of the user's explicit enrolled list (design Q5).
func TestHeaderAuthenticatorEnrolledProjectsInjectsGeneral(t *testing.T) {
	t.Parallel()

	loader := buildTestLoader(t, testYAML)
	ha := NewHeaderAuthenticator(loader)

	// Set the request context via Authorize so the current principal is known.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "alice@example.com")
	if err := ha.Authorize(req); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	enrolled := ha.EnrolledProjects()

	hasGeneral := false
	for _, p := range enrolled {
		if p == "general" {
			hasGeneral = true
		}
	}
	if !hasGeneral {
		t.Errorf("EnrolledProjects: expected 'general' to be injected, got %v", enrolled)
	}

	// alice is explicitly enrolled in project-a — it must also be present.
	hasProjectA := false
	for _, p := range enrolled {
		if p == "project-a" {
			hasProjectA = true
		}
	}
	if !hasProjectA {
		t.Errorf("EnrolledProjects: expected 'project-a' to be present, got %v", enrolled)
	}
}

// TestHeaderAuthenticatorAuthorizeProject checks AuthorizeProject against
// the caller's enrolled set + general.
func TestHeaderAuthenticatorAuthorizeProject(t *testing.T) {
	t.Parallel()

	loader := buildTestLoader(t, testYAML)
	ha := NewHeaderAuthenticator(loader)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "alice@example.com")
	if err := ha.Authorize(req); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	cases := []struct {
		project string
		wantErr bool
	}{
		{"project-a", false},  // explicitly enrolled
		{"general", false},    // always injected
		{"project-b", true},   // alice is not enrolled
		{"", true},            // empty project
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.project, func(t *testing.T) {
			t.Parallel()
			err := ha.AuthorizeProject(tc.project)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for project %q, got nil", tc.project)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for project %q: %v", tc.project, err)
			}
		})
	}
}
