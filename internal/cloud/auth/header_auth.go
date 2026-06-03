package auth

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// principalContextKey is the unexported key used to store the resolved
// Principal in a request's context.
type principalContextKey struct{}

// AuthError is a typed auth failure that carries an HTTP status code and a
// structured error code. cloudserver.withAuth detects it via the
// structuredAuthError interface (HTTPStatus/ErrorCode/ErrorMessage) and writes
// the appropriate status + JSON body {"error":Code,"message":Msg}.
// A plain (untyped) error → HTTP 401 (backward-compatible default).
type AuthError struct {
	Status int
	Code   string
	Msg    string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("auth %d %s: %s", e.Status, e.Code, e.Msg)
}

// HTTPStatus satisfies the structuredAuthError interface checked by cloudserver.
func (e *AuthError) HTTPStatus() int { return e.Status }

// ErrorCode satisfies the structuredAuthError interface checked by cloudserver.
func (e *AuthError) ErrorCode() string { return e.Code }

// ErrorMessage satisfies the structuredAuthError interface checked by cloudserver.
func (e *AuthError) ErrorMessage() string { return e.Msg }

// UserLoader is the interface HeaderAuthenticator needs from the users package.
// Satisfied by *users.YAMLLoader.
type UserLoader interface {
	Lookup(email string) (users.Principal, bool)
}

// AdminResolver is an optional extension of UserLoader that resolves the unique
// admin for the emergency bypass. Satisfied by *users.YAMLLoader.
type AdminResolver interface {
	SoleAdmin() (users.Principal, bool)
}

// HeaderAuthenticator resolves a caller's identity from X-Forwarded-Email,
// enforces user lifecycle status (active / offboarding / removed), and provides
// per-request project enrollment.
//
// Design decisions implemented:
//   - Principal is REQUEST-SCOPED via context (no shared mutable state — safe
//     for a shared singleton under concurrent requests, resolves S4).
//   - AuthError typed errors propagate HTTP status + code to withAuth (resolves S5).
//   - Emergency bypass: if bypassToken != "" and the request carries
//     "Authorization: Bearer <bypassToken>", the loader's sole admin is used
//     as the authenticated identity.
//   - @vivastudios.com domain check: missing or wrong-domain email → 401.
//   - Three-State Access Matrix: offboarding+read → 403 account_offboarding;
//     offboarding+write → allowed; removed → 403 account_removed.
//   - "general" is always injected into EnrolledProjects (design Q5).
//   - Bearer JWT path: if jwtSecret is set and the request carries a Bearer
//     token that is NOT the bypassToken, it is verified via VerifyJWT and the
//     email from the claims is used to resolve the principal from the directory.
//     Precedence: X-Forwarded-Email → bypass → JWT Bearer → 401.
type HeaderAuthenticator struct {
	loader      UserLoader
	bypassToken string // ENGRAM_CLOUD_TOKEN value; empty means bypass disabled
	jwtSecret   string // when non-empty, Bearer JWT tokens are accepted on /sync/*
}

// NewHeaderAuthenticator returns a HeaderAuthenticator backed by loader.
// bypassToken is the value of ENGRAM_CLOUD_TOKEN (empty string = bypass disabled).
// When bypassToken is non-empty, loader must implement AdminResolver and have
// exactly one admin; if not, NewHeaderAuthenticator returns an error.
func NewHeaderAuthenticator(loader UserLoader, bypassToken string) (*HeaderAuthenticator, error) {
	return NewHeaderAuthenticatorWithJWT(loader, bypassToken, "")
}

// NewHeaderAuthenticatorWithJWT is like NewHeaderAuthenticator but also accepts
// Bearer-signed engram JWTs on /sync/* routes. jwtSecret is the HMAC-SHA256
// signing secret used by MintJWT / VerifyJWT (minimum 32 bytes); empty string
// disables JWT Bearer verification (falls back to X-Forwarded-Email only).
//
// Precedence when jwtSecret is non-empty:
//
//  1. Emergency bypass (Bearer == bypassToken)  → admin identity
//  2. X-Forwarded-Email present                → header auth (oauth2-proxy path)
//  3. Bearer JWT present                       → verify + re-resolve principal
//  4. No credentials                           → 401
func NewHeaderAuthenticatorWithJWT(loader UserLoader, bypassToken, jwtSecret string) (*HeaderAuthenticator, error) {
	if bypassToken != "" {
		ar, ok := loader.(AdminResolver)
		if !ok {
			return nil, fmt.Errorf("auth: ENGRAM_CLOUD_TOKEN requires loader to implement AdminResolver (got %T)", loader)
		}
		if _, ok := ar.SoleAdmin(); !ok {
			return nil, fmt.Errorf("auth: ENGRAM_CLOUD_TOKEN requires exactly one admin in the user directory; found 0 or >1 admins")
		}
	}
	return &HeaderAuthenticator{loader: loader, bypassToken: bypassToken, jwtSecret: jwtSecret}, nil
}

// Authorize resolves the caller's identity and stores it in r.Context().
// On success, the enriched request (with principal in context) can be retrieved
// via principalFromContext(r.Context()). The caller in cloudserver.withAuth
// should replace r with the returned request, but since Authorize receives the
// original *http.Request and Go's http package calls the handler with the same
// pointer, we need the approach of returning the principal via a context
// carried through withAuth.
//
// HTTP status semantics:
//   - Missing X-Forwarded-Email                   → 401 (plain error, not AuthError)
//   - Email not ending in @vivastudios.com         → 401 (plain error)
//   - Valid domain, not in directory               → 403 user_not_provisioned
//   - status=removed                               → 403 account_removed
//   - status=offboarding + write (POST)            → 200 (allowed; returns principal)
//   - status=offboarding + read (GET)              → 403 account_offboarding
//   - status=active                                → 200 (allowed)
//   - Emergency bypass (Bearer == bypassToken)     → 200 as sole admin
//
// The principal is stored in r.Context() so downstream methods
// (AuthorizeProject, Attribution, EnrolledProjects) are concurrency-safe.
func (ha *HeaderAuthenticator) Authorize(r *http.Request) (*http.Request, error) {
	// Emergency bypass takes precedence over all other auth paths.
	// (Design: header → bypass → JWT Bearer → 401, but bypass is checked first
	//  because it must work even when X-Forwarded-Email is absent on /sync/* routes.)
	if ha.bypassToken != "" {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		parts := strings.Fields(authHeader)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] == ha.bypassToken {
			ar := ha.loader.(AdminResolver) // safe: constructor validates this
			admin, ok := ar.SoleAdmin()
			if !ok {
				return r, &AuthError{Status: http.StatusInternalServerError, Code: "bypass_admin_missing",
					Msg: "bypass token configured but sole admin not found in directory"}
			}
			return r.WithContext(context.WithValue(r.Context(), principalContextKey{}, &admin)), nil
		}
	}

	// X-Forwarded-Email path (oauth2-proxy route — /auth, /dashboard etc.).
	email := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Email")))
	if email != "" {
		return ha.resolveByEmail(r, email)
	}

	// Bearer JWT path (direct /sync/* route, no oauth2-proxy header).
	if ha.jwtSecret != "" {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		parts := strings.Fields(authHeader)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != "" {
			claims, err := VerifyJWT(ha.jwtSecret, parts[1], time.Now().UTC())
			if err != nil {
				return r, fmt.Errorf("auth: invalid bearer jwt: %w", err)
			}
			jwtEmail := strings.ToLower(strings.TrimSpace(claims.Email))
			if jwtEmail == "" {
				return r, fmt.Errorf("auth: bearer jwt missing email claim")
			}
			return ha.resolveByEmail(r, jwtEmail)
		}
	}

	// No credentials present.
	return r, fmt.Errorf("auth: X-Forwarded-Email is required")
}

// resolveByEmail looks up the principal for email, enforces the lifecycle matrix,
// and returns an enriched request with the principal in context on success.
func (ha *HeaderAuthenticator) resolveByEmail(r *http.Request, email string) (*http.Request, error) {
	if !strings.HasSuffix(email, "@vivastudios.com") {
		return r, fmt.Errorf("auth: email %q is not a @vivastudios.com address", email)
	}

	p, ok := ha.loader.Lookup(email)
	if !ok {
		return r, &AuthError{
			Status: http.StatusForbidden,
			Code:   "user_not_provisioned",
			Msg:    fmt.Sprintf("user %q is not in the directory", email),
		}
	}

	switch strings.ToLower(p.Status) {
	case "removed":
		return r, &AuthError{
			Status: http.StatusForbidden,
			Code:   "account_removed",
			Msg:    fmt.Sprintf("user %q account has been removed", email),
		}
	case "offboarding":
		// Offboarding: push (POST) allowed; all other methods (GET etc.) blocked.
		if r.Method != http.MethodPost {
			return r, &AuthError{
				Status: http.StatusForbidden,
				Code:   "account_offboarding",
				Msg:    fmt.Sprintf("user %q account is offboarding; read access is disabled", email),
			}
		}
		// Write path: allowed — fall through to store principal.
	}

	return r.WithContext(context.WithValue(r.Context(), principalContextKey{}, &p)), nil
}

// principalFromContext retrieves the *users.Principal stored by Authorize.
// Returns nil if the context does not carry a principal (Authorize not yet called
// or called on a different request chain).
func principalFromContext(ctx context.Context) *users.Principal {
	p, _ := ctx.Value(principalContextKey{}).(*users.Principal)
	return p
}

// EnrolledProjects returns the union of the user's explicitly enrolled projects
// plus "general" (always injected per design Q5).
// Returns an empty slice when ctx carries no principal.
func (ha *HeaderAuthenticator) EnrolledProjects(ctx context.Context) []string {
	p := principalFromContext(ctx)
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

// Attribution returns a cloudstore.Attribution populated from the principal in ctx.
// Returns a zero Attribution when ctx carries no principal.
func (ha *HeaderAuthenticator) Attribution(ctx context.Context) cloudstore.Attribution {
	p := principalFromContext(ctx)
	if p == nil {
		return cloudstore.Attribution{}
	}
	return cloudstore.Attribution{
		UserEmail:   p.Email,
		UserName:    p.Name,
		Department:  p.Department,
		UserDeleted: strings.EqualFold(p.Status, "removed"),
	}
}

// AuthorizeProject returns nil if project is in the caller's enrolled set
// (including the injected "general"). Returns an error otherwise.
func (ha *HeaderAuthenticator) AuthorizeProject(ctx context.Context, project string) error {
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("auth: project is required")
	}

	enrolled := ha.EnrolledProjects(ctx)
	for _, p := range enrolled {
		if strings.EqualFold(p, project) {
			return nil
		}
	}
	return fmt.Errorf("%w: project %q is not in enrolled set", ErrProjectNotAllowed, project)
}
