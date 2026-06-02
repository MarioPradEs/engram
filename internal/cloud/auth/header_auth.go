package auth

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// UserLoader is the interface HeaderAuthenticator needs from the users package.
// Satisfied by *users.YAMLLoader.
type UserLoader interface {
	Lookup(email string) (users.Principal, bool)
}

// HeaderAuthenticator resolves a caller's identity from X-Forwarded-Email,
// enforces user status, and provides per-request project enrollment.
//
// It satisfies:
//   - cloudserver.Authenticator  (Authorize)
//   - cloudserver.ProjectAuthorizer (AuthorizeProject)
//   - cloudserver.EnrolledProjectsProvider (EnrolledProjects)
//
// Per design Q5: "general" is always injected into the enrolled set at
// resolution time so team-scoped observations round-trip through the
// project=general convention.
type HeaderAuthenticator struct {
	mu        sync.RWMutex
	loader    UserLoader
	principal *users.Principal // non-nil after a successful Authorize call
}

// NewHeaderAuthenticator returns a HeaderAuthenticator backed by loader.
func NewHeaderAuthenticator(loader UserLoader) *HeaderAuthenticator {
	return &HeaderAuthenticator{loader: loader}
}

// Authorize reads X-Forwarded-Email from r, looks up the user in the directory,
// and rejects removed users. On success the principal is cached for the lifetime
// of this request-scoped call chain (EnrolledProjects / AuthorizeProject).
//
// NOTE: HeaderAuthenticator is NOT safe for use across concurrent requests with
// a shared instance — each request should use its own instance or the caller
// must ensure the principal is set before calling the other methods. In the
// standard server wiring (withAuth middleware) Authorize is called first on
// the same goroutine that calls downstream handlers, so this is safe.
func (ha *HeaderAuthenticator) Authorize(r *http.Request) error {
	email := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Email")))
	if email == "" {
		return fmt.Errorf("auth: X-Forwarded-Email is required")
	}

	p, ok := ha.loader.Lookup(email)
	if !ok {
		return fmt.Errorf("auth: user %q not found in directory", email)
	}
	if strings.EqualFold(p.Status, "removed") {
		return fmt.Errorf("auth: user %q has been removed", email)
	}

	ha.mu.Lock()
	ha.principal = &p
	ha.mu.Unlock()
	return nil
}

// EnrolledProjects returns the union of the user's explicitly enrolled projects
// plus "general" (always injected per design Q5).
// Returns an empty slice if Authorize has not been called yet.
func (ha *HeaderAuthenticator) EnrolledProjects() []string {
	ha.mu.RLock()
	p := ha.principal
	ha.mu.RUnlock()

	if p == nil {
		return []string{}
	}

	seen := make(map[string]struct{})
	seen["general"] = struct{}{} // Q5: always inject general

	for _, proj := range p.Enrolled {
		proj = strings.TrimSpace(proj)
		if proj != "" {
			seen[proj] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for proj := range seen {
		out = append(out, proj)
	}
	sort.Strings(out)
	return out
}

// AuthorizeProject returns nil if project is in the caller's enrolled set
// (including the injected "general"). Returns an error otherwise.
func (ha *HeaderAuthenticator) AuthorizeProject(project string) error {
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("auth: project is required")
	}

	enrolled := ha.EnrolledProjects()
	for _, p := range enrolled {
		if strings.EqualFold(p, project) {
			return nil
		}
	}
	return fmt.Errorf("%w: project %q is not in enrolled set", ErrProjectNotAllowed, project)
}
