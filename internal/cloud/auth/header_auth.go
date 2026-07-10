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

// ─── Audit Constants (mirrored from cloudstore for callers that only import auth) ───

// OutcomeAllowed is the audit outcome for an accepted auth event.
const OutcomeAllowed = cloudstore.OutcomeAllowed

// OutcomeDenied is the audit outcome for a rejected auth event.
const OutcomeDenied = cloudstore.OutcomeDenied

// SourceOAuth is the audit source for OAuth2-proxy dashboard flow events.
const SourceOAuth = cloudstore.SourceOAuth

// SourceJWT is the audit source for JWT-bearer events (sync/API path).
const SourceJWT = cloudstore.SourceJWT

// SourceLegacy is the audit source for ENGRAM_CLOUD_TOKEN emergency bypass events.
const SourceLegacy = cloudstore.SourceLegacy

// ReasonUnknownEmail is the reason code when an email is not found in the directory.
const ReasonUnknownEmail = cloudstore.ReasonUnknownEmail

// ReasonInvalidJWT is the reason code when a JWT fails validation.
const ReasonInvalidJWT = cloudstore.ReasonInvalidJWT

// ReasonRemovedUser is the reason code when the user account has been removed.
const ReasonRemovedUser = cloudstore.ReasonRemovedUser

// ReasonAccountOffboarding is the reason code when the user account is in offboarding state.
const ReasonAccountOffboarding = cloudstore.ReasonAccountOffboarding

// ReasonMissingCredential is the reason code when no credential was presented.
const ReasonMissingCredential = cloudstore.ReasonMissingCredential

// ReasonBypassAdminMissing is the reason code when the bypass token is presented
// but no sole-admin account exists.
const ReasonBypassAdminMissing = cloudstore.ReasonBypassAdminMissing

// ReasonInvalidDomain is the reason code when the email domain is not allowed.
const ReasonInvalidDomain = cloudstore.ReasonInvalidDomain

// ─── AuthAuditRecorder interface ─────────────────────────────────────────────

// AuthAuditRecorder is the narrow write-side interface used by HeaderAuthenticator
// to record auth events. *cloudstore.CloudStore satisfies it via RecordAuthEvent.
// Inject via SetAuditRecorder (optional setter — nil recorder = no-op).
//
// All implementations MUST be failure-safe: a write error MUST NOT propagate to
// the auth path. *cloudstore.CloudStore.RecordAuthEvent already enforces this
// (fire-and-forget goroutine, DB error → log WARN and swallow).
type AuthAuditRecorder interface {
	RecordAuthEvent(ctx context.Context, email, outcome, source, reasonCode string)
}

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

// HeaderAuthenticator resolves a caller's identity, enforces user lifecycle
// status (active / offboarding / removed), and provides per-request project
// enrollment.  It operates in two modes depending on whether jwtSecret is set:
//
// JWT mode (NewHeaderAuthenticatorWithJWT with non-empty jwtSecret):
//
//	Used on /sync/* routes that Caddy routes directly to the cloud server,
//	bypassing oauth2-proxy.  On this path X-Forwarded-Email is
//	attacker-controlled and is COMPLETELY IGNORED.  Only a valid Bearer JWT
//	(or the emergency bypass token) is accepted.
//	Precedence: bypass → Bearer JWT → 401.
//
// Header mode (NewHeaderAuthenticator / NewHeaderAuthenticatorWithJWT with ""):
//
//	Used on oauth2-proxy-protected routes where X-Forwarded-Email is
//	trustworthy (injected by the proxy).  Bearer JWT is NOT checked.
//	Precedence: bypass → X-Forwarded-Email → 401.
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
type HeaderAuthenticator struct {
	loader         UserLoader
	bypassToken    string           // ENGRAM_CLOUD_TOKEN value; empty means bypass disabled
	jwtSecret      string           // when non-empty, Bearer JWT tokens are accepted on /sync/*
	now            func() time.Time // injectable time seam for testing; nil means time.Now
	auditRecorder  AuthAuditRecorder // optional; nil means audit is disabled
}

// SetAuditRecorder wires an AuthAuditRecorder into the authenticator.
// Using a setter (not the constructor) preserves all existing NewHeaderAuthenticatorWithJWT
// call sites unchanged (~15 across the codebase). Calling SetAuditRecorder(nil) is a no-op
// that disables auditing — safe for all existing tests that omit the recorder.
func (ha *HeaderAuthenticator) SetAuditRecorder(r AuthAuditRecorder) {
	ha.auditRecorder = r
}

// recordAuth is the internal fire-and-forget helper. It checks the nil guard so
// every instrumentation site is a single-line call without boilerplate.
// The call is NOT wrapped in a goroutine here because RecordAuthEvent itself is
// required to be failure-safe and fire-and-forget (*cloudstore.CloudStore.RecordAuthEvent
// already spawns a goroutine and swallows errors). Fake recorders in tests are
// synchronous so assertions are immediate and deterministic.
func (ha *HeaderAuthenticator) recordAuth(ctx context.Context, email, outcome, source, reasonCode string) {
	if ha.auditRecorder == nil {
		return
	}
	ha.auditRecorder.RecordAuthEvent(ctx, email, outcome, source, reasonCode)
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
// Precedence when jwtSecret is non-empty (JWT mode — used on /sync/* direct routes):
//
//  1. Emergency bypass (Bearer == bypassToken)  → admin identity
//  2. Bearer JWT present                       → verify + re-resolve principal
//  3. No valid credentials                     → 401
//
// SECURITY NOTE: When jwtSecret is set the X-Forwarded-Email header is NOT
// trusted and is completely ignored.  On the /sync/* path Caddy routes requests
// DIRECTLY to the cloud server (bypassing oauth2-proxy), so X-Forwarded-Email
// is attacker-controlled.  Trusting it would allow any caller to impersonate
// any @vivastudios.com user without a valid JWT (C1 auth bypass).
//
// The /auth endpoint handler reads X-Forwarded-Email directly (it is behind
// oauth2-proxy) and does NOT use Authorize — it is unaffected by this change.
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
// HTTP status semantics depend on which mode the authenticator is in.
//
// JWT mode (jwtSecret set — used on /sync/* direct routes):
//   - Emergency bypass (Bearer == bypassToken)     → 200 as sole admin
//   - Valid Bearer JWT                             → 200 (principal from directory)
//   - Expired/tampered JWT                         → 401 (plain error)
//   - Valid domain, not in directory               → 403 user_not_provisioned
//   - status=removed                               → 403 account_removed
//   - status=offboarding + write (POST)            → 200 (allowed; returns principal)
//   - status=offboarding + read (GET)              → 403 account_offboarding
//   - status=active                                → 200 (allowed)
//   - X-Forwarded-Email header present             → IGNORED (attacker-controlled on direct path)
//   - No valid credentials                         → 401
//
// Header mode (no jwtSecret — legacy/oauth2-proxy routes):
//   - Emergency bypass (Bearer == bypassToken)     → 200 as sole admin
//   - X-Forwarded-Email present + valid domain     → resolved from directory
//   - Missing X-Forwarded-Email                    → 401 (plain error, not AuthError)
//   - Email not ending in @vivastudios.com         → 401 (plain error)
//
// The principal is stored in r.Context() so downstream methods
// (AuthorizeProject, Attribution, EnrolledProjects) are concurrency-safe.
func (ha *HeaderAuthenticator) Authorize(r *http.Request) (*http.Request, error) {
	ctx := r.Context()

	// Emergency bypass takes precedence over all other auth paths.
	// Works in both JWT mode and header mode.
	if ha.bypassToken != "" {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		parts := strings.Fields(authHeader)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] == ha.bypassToken {
			ar := ha.loader.(AdminResolver) // safe: constructor validates this
			admin, ok := ar.SoleAdmin()
			if !ok {
				// Audit: bypass token presented but no sole admin found.
				ha.recordAuth(ctx, "", OutcomeDenied, SourceLegacy, ReasonBypassAdminMissing)
				return r, &AuthError{Status: http.StatusInternalServerError, Code: "bypass_admin_missing",
					Msg: "bypass token configured but sole admin not found in directory"}
			}
			// Audit: legacy bypass ACCEPTED — one of the two discrete success sites.
			ha.recordAuth(ctx, admin.Email, OutcomeAllowed, SourceLegacy, "")
			return r.WithContext(context.WithValue(ctx, principalContextKey{}, &admin)), nil
		}
	}

	// JWT mode: jwtSecret is set — this authenticator is used on /sync/* routes
	// that Caddy routes DIRECTLY to the cloud server (bypassing oauth2-proxy).
	// On this path, X-Forwarded-Email is attacker-controlled and MUST be ignored.
	// Only a valid Bearer JWT is accepted as proof of identity.
	if ha.jwtSecret != "" {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		parts := strings.Fields(authHeader)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != "" {
			claims, err := VerifyJWT(ha.jwtSecret, parts[1], time.Now().UTC())
			if err != nil {
				// Audit: JWT present but invalid (signature, expiry, etc.).
				ha.recordAuth(ctx, "", OutcomeDenied, SourceJWT, ReasonInvalidJWT)
				return r, fmt.Errorf("auth: invalid bearer jwt: %w", err)
			}
			jwtEmail := strings.ToLower(strings.TrimSpace(claims.Email))
			if jwtEmail == "" {
				// Audit: JWT valid but missing email claim — treat as invalid JWT.
				ha.recordAuth(ctx, "", OutcomeDenied, SourceJWT, ReasonInvalidJWT)
				return r, fmt.Errorf("auth: bearer jwt missing email claim")
			}
			return ha.resolveByEmail(r, jwtEmail, SourceJWT)
		}
		// No valid Bearer JWT — reject.  Do NOT fall through to X-Forwarded-Email.
		// Audit: JWT mode with no credential at all.
		ha.recordAuth(ctx, "", OutcomeDenied, SourceJWT, ReasonMissingCredential)
		return r, fmt.Errorf("auth: bearer jwt required on direct sync routes")
	}

	// Header mode (legacy / oauth2-proxy path): trust X-Forwarded-Email because
	// the request has passed through oauth2-proxy which injected it.
	email := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Email")))
	if email != "" {
		return ha.resolveByEmail(r, email, SourceOAuth)
	}

	// No credentials present in header mode.
	// Audit: missing X-Forwarded-Email — source is oauth (header mode path).
	ha.recordAuth(ctx, "", OutcomeDenied, SourceOAuth, ReasonMissingCredential)
	return r, fmt.Errorf("auth: X-Forwarded-Email is required")
}

// resolveByEmail looks up the principal for email, enforces the lifecycle matrix,
// and returns an enriched request with the principal in context on success.
// source (SourceJWT or SourceOAuth) is threaded from the caller so audit rows
// carry the correct source without re-inferring it inside this function.
func (ha *HeaderAuthenticator) resolveByEmail(r *http.Request, email, source string) (*http.Request, error) {
	ctx := r.Context()

	if !strings.HasSuffix(email, "@vivastudios.com") {
		// Audit: wrong domain — not a @vivastudios.com address.
		ha.recordAuth(ctx, email, OutcomeDenied, source, ReasonInvalidDomain)
		return r, fmt.Errorf("auth: email %q is not a @vivastudios.com address", email)
	}

	p, ok := ha.loader.Lookup(email)
	if !ok {
		// Audit: email not found in provisioned users.
		ha.recordAuth(ctx, email, OutcomeDenied, source, ReasonUnknownEmail)
		return r, &AuthError{
			Status: http.StatusForbidden,
			Code:   "user_not_provisioned",
			Msg:    fmt.Sprintf("user %q is not in the directory", email),
		}
	}

	switch strings.ToLower(p.Status) {
	case "removed":
		// Audit: user account has been removed.
		ha.recordAuth(ctx, email, OutcomeDenied, source, ReasonRemovedUser)
		return r, &AuthError{
			Status: http.StatusForbidden,
			Code:   "account_removed",
			Msg:    fmt.Sprintf("user %q account has been removed", email),
		}
	case "offboarding":
		// Offboarding: push (POST) allowed; all other methods (GET etc.) blocked.
		if r.Method != http.MethodPost {
			// Audit: offboarding user attempting read access.
			ha.recordAuth(ctx, email, OutcomeDenied, source, ReasonAccountOffboarding)
			return r, &AuthError{
				Status: http.StatusForbidden,
				Code:   "account_offboarding",
				Msg:    fmt.Sprintf("user %q account is offboarding; read access is disabled", email),
			}
		}
		// Write path: allowed — fall through to store principal (no audit row for allowed sync requests).
	}

	return r.WithContext(context.WithValue(ctx, principalContextKey{}, &p)), nil
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
