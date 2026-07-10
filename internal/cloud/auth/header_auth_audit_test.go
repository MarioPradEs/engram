package auth

// PR2 tests: AuthAuditRecorder interface, SetAuditRecorder, and instrumentation
// of every deny site and both success sites in HeaderAuthenticator.
//
// These tests follow Strict TDD (RED → GREEN). They are written BEFORE the
// interface and instrumentation exist so the first run produces compile errors
// (RED). The implementation in header_auth.go turns them GREEN.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// ─── Fake Recorder ───────────────────────────────────────────────────────────

// auditCall captures one RecordAuthEvent invocation for assertion.
type auditCall struct {
	email      string
	outcome    string
	source     string
	reasonCode string
}

// fakeRecorder collects RecordAuthEvent calls synchronously (no goroutine) so
// tests can assert on them without sleeps or channels. It satisfies the
// AuthAuditRecorder interface.
type fakeRecorder struct {
	mu    sync.Mutex
	calls []auditCall
}

func (f *fakeRecorder) RecordAuthEvent(_ context.Context, email, outcome, source, reasonCode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, auditCall{email: email, outcome: outcome, source: source, reasonCode: reasonCode})
}

func (f *fakeRecorder) snapshot() []auditCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]auditCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// newTestHAWithBypassAndRecorder builds an HA with a bypass token so we can
// test the bypass deny (admin missing) and bypass accept sites.
func newTestHAWithBypassAndRecorder(t *testing.T, rec AuthAuditRecorder) *HeaderAuthenticator {
	t.Helper()
	loader := buildTestLoader(t, testYAML) // testYAML defined in header_auth_test.go
	ha, err := NewHeaderAuthenticator(loader, "super-secret-bypass")
	if err != nil {
		t.Fatalf("NewHeaderAuthenticator with bypass: %v", err)
	}
	ha.SetAuditRecorder(rec)
	return ha
}

// newTestHAWithRecorder builds a no-bypass, no-JWT HA with recorder.
func newTestHAWithRecorder(t *testing.T, rec AuthAuditRecorder) *HeaderAuthenticator {
	t.Helper()
	loader := buildTestLoader(t, testYAML)
	ha, err := NewHeaderAuthenticator(loader, "")
	if err != nil {
		t.Fatalf("NewHeaderAuthenticator: %v", err)
	}
	ha.SetAuditRecorder(rec)
	return ha
}

// newTestHAJWTWithRecorder builds a JWT-mode HA with recorder.
func newTestHAJWTWithRecorder(t *testing.T, rec AuthAuditRecorder) *HeaderAuthenticator {
	t.Helper()
	loader := buildTestLoader(t, testYAML)
	ha, err := NewHeaderAuthenticatorWithJWT(loader, "", bearerTestSecret) // bearerTestSecret in header_auth_test.go
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	ha.SetAuditRecorder(rec)
	return ha
}

// assertOneCall fails the test unless exactly one call was recorded with the
// expected field values.
func assertOneCall(t *testing.T, rec *fakeRecorder, wantEmail, wantOutcome, wantSource, wantReason string) {
	t.Helper()
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 audit call, got %d: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.email != wantEmail {
		t.Errorf("call.email = %q, want %q", c.email, wantEmail)
	}
	if c.outcome != wantOutcome {
		t.Errorf("call.outcome = %q, want %q", c.outcome, wantOutcome)
	}
	if c.source != wantSource {
		t.Errorf("call.source = %q, want %q", c.source, wantSource)
	}
	if c.reasonCode != wantReason {
		t.Errorf("call.reasonCode = %q, want %q", c.reasonCode, wantReason)
	}
}

// assertZeroCalls fails unless no audit calls were recorded.
func assertZeroCalls(t *testing.T, rec *fakeRecorder) {
	t.Helper()
	calls := rec.snapshot()
	if len(calls) != 0 {
		t.Fatalf("expected 0 audit calls, got %d: %+v", len(calls), calls)
	}
}

// ─── 2.1-RED Tests ───────────────────────────────────────────────────────────

// TestAudit_bypassAccepted_writesAllowedLegacyRow verifies the legacy bypass
// success site: outcome=allowed, source=legacy, reasonCode="".
func TestAudit_bypassAccepted_writesAllowedLegacyRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAWithBypassAndRecorder(t, rec)

	req := httptest.NewRequest(http.MethodGet, "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer super-secret-bypass")

	_, err := ha.Authorize(req)
	if err != nil {
		t.Fatalf("bypass should succeed, got: %v", err)
	}

	assertOneCall(t, rec, "alice@vivastudios.com", OutcomeAllowed, SourceLegacy, "")
}

// TestAudit_invalidJWT_writesDeniedRow verifies JWT-invalid deny site:
// outcome=denied, source=jwt, reasonCode=invalid_jwt.
func TestAudit_invalidJWT_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAJWTWithRecorder(t, rec)

	// Mint a valid token then tamper it to make it invalid.
	token := mintTestJWT(t, "alice@vivastudios.com", "Alice", "dev", "admin", 0)
	parts := splitDot(token)
	if len(parts) != 3 {
		t.Fatal("expected 3-part JWT")
	}
	tampered := parts[0] + "." + parts[1] + "TAMPERED." + parts[2]

	req := httptest.NewRequest(http.MethodGet, "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer "+tampered)

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for invalid JWT")
	}

	assertOneCall(t, rec, "", OutcomeDenied, SourceJWT, ReasonInvalidJWT)
}

// TestAudit_noJWTFallthrough_writesDeniedRow verifies the no-JWT fallthrough
// deny site (JWT mode, no Bearer header): outcome=denied, source=jwt,
// reasonCode=missing_credential.
func TestAudit_noJWTFallthrough_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAJWTWithRecorder(t, rec)

	req := httptest.NewRequest(http.MethodGet, "/sync/pull", nil)
	// No Authorization header at all.

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error with no credentials in JWT mode")
	}

	assertOneCall(t, rec, "", OutcomeDenied, SourceJWT, ReasonMissingCredential)
}

// TestAudit_missingXForwardedEmail_writesDeniedRow verifies the header-mode
// missing credential deny site: outcome=denied, source=oauth,
// reasonCode=missing_credential.
func TestAudit_missingXForwardedEmail_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAWithRecorder(t, rec)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	// No X-Forwarded-Email.

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error with missing X-Forwarded-Email")
	}

	assertOneCall(t, rec, "", OutcomeDenied, SourceOAuth, ReasonMissingCredential)
}

// TestAudit_unknownEmail_writesDeniedRow verifies the resolveByEmail
// unknown-email deny site (header-mode path): outcome=denied, source=oauth,
// reasonCode=unknown_email.
func TestAudit_unknownEmail_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAWithRecorder(t, rec)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "nobody@vivastudios.com")

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for unknown email")
	}

	assertOneCall(t, rec, "nobody@vivastudios.com", OutcomeDenied, SourceOAuth, ReasonUnknownEmail)
}

// TestAudit_unknownEmailJWT_writesDeniedRowSourceJWT verifies that resolveByEmail
// emits source=jwt when called from the JWT path (unknown email scenario).
func TestAudit_unknownEmailJWT_writesDeniedRowSourceJWT(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAJWTWithRecorder(t, rec)

	// Mint JWT for an email that is NOT in the directory.
	token := mintTestJWT(t, "ghost@vivastudios.com", "Ghost", "dev", "member", 0)

	req := httptest.NewRequest(http.MethodGet, "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for user not in directory")
	}

	assertOneCall(t, rec, "ghost@vivastudios.com", OutcomeDenied, SourceJWT, ReasonUnknownEmail)
}

// TestAudit_removedUser_writesDeniedRow verifies the removed-user deny site
// (header-mode path): outcome=denied, source=oauth, reasonCode=removed_user.
func TestAudit_removedUser_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAWithRecorder(t, rec)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "removed@vivastudios.com")

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for removed user")
	}

	assertOneCall(t, rec, "removed@vivastudios.com", OutcomeDenied, SourceOAuth, ReasonRemovedUser)
}

// TestAudit_removedUserJWT_writesDeniedRowSourceJWT verifies removed-user
// deny in JWT mode: source=jwt.
func TestAudit_removedUserJWT_writesDeniedRowSourceJWT(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAJWTWithRecorder(t, rec)

	token := mintTestJWT(t, "removed@vivastudios.com", "Removed User", "qa", "member", 0)

	req := httptest.NewRequest(http.MethodGet, "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for removed user via JWT")
	}

	assertOneCall(t, rec, "removed@vivastudios.com", OutcomeDenied, SourceJWT, ReasonRemovedUser)
}

// TestAudit_offboardingReadDenied_writesDeniedRow verifies the
// offboarding+read deny site (header-mode path): outcome=denied,
// source=oauth, reasonCode=account_offboarding.
func TestAudit_offboardingReadDenied_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAWithRecorder(t, rec)

	req := httptest.NewRequest(http.MethodGet, "/", nil) // GET = read
	req.Header.Set("X-Forwarded-Email", "offboarding@vivastudios.com")

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for offboarding user GET")
	}

	assertOneCall(t, rec, "offboarding@vivastudios.com", OutcomeDenied, SourceOAuth, ReasonAccountOffboarding)
}

// TestAudit_offboardingReadDeniedJWT_writesDeniedRow verifies offboarding+read
// deny via JWT path: source=jwt.
func TestAudit_offboardingReadDeniedJWT_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAJWTWithRecorder(t, rec)

	token := mintTestJWT(t, "offboarding@vivastudios.com", "Offboarding User", "qa", "member", 0)

	req := httptest.NewRequest(http.MethodGet, "/sync/pull", nil) // GET = read
	req.Header.Set("Authorization", "Bearer "+token)

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for offboarding user GET via JWT")
	}

	assertOneCall(t, rec, "offboarding@vivastudios.com", OutcomeDenied, SourceJWT, ReasonAccountOffboarding)
}

// TestAudit_nilRecorder_noopDoesNotPanic verifies that when no recorder is set,
// Authorize succeeds without panicking (nil-safety guard).
func TestAudit_nilRecorder_noopDoesNotPanic(t *testing.T) {
	t.Parallel()
	ha := newTestHA(t) // no recorder set — uses SetAuditRecorder(nil) implicitly

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")

	// Must not panic.
	_, err := ha.Authorize(req)
	if err != nil {
		t.Fatalf("expected success with nil recorder, got: %v", err)
	}
}

// TestAudit_nilRecorderDenied_noopDoesNotPanic verifies that a deny path with nil
// recorder does not panic.
func TestAudit_nilRecorderDenied_noopDoesNotPanic(t *testing.T) {
	t.Parallel()
	ha := newTestHA(t) // no recorder

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "nobody@vivastudios.com")

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected deny for unknown user")
	}
	// Reaching here means no panic — test passes.
}

// TestAudit_activeUserSuccess_writesZeroRows verifies the critical negative
// case: a normal successful auth request (active user, valid header) writes
// ZERO audit rows.
func TestAudit_activeUserSuccess_writesZeroRows(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAWithRecorder(t, rec)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")

	_, err := ha.Authorize(req)
	if err != nil {
		t.Fatalf("expected success for active user, got: %v", err)
	}

	assertZeroCalls(t, rec)
}

// TestAudit_activeUserJWTSuccess_writesZeroRows verifies that a normal sync
// request with a valid Bearer JWT writes ZERO audit rows.
func TestAudit_activeUserJWTSuccess_writesZeroRows(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAJWTWithRecorder(t, rec)

	token := mintTestJWT(t, "alice@vivastudios.com", "Alice", "dev", "admin", 0)

	req := httptest.NewRequest(http.MethodGet, "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	_, err := ha.Authorize(req)
	if err != nil {
		t.Fatalf("expected success for valid JWT, got: %v", err)
	}

	assertZeroCalls(t, rec)
}

// TestAudit_offboardingWrite_writesZeroRows verifies that a POST (write) by an
// offboarding user is ALLOWED and writes ZERO audit rows (offboarding+write is
// permitted per the lifecycle matrix).
func TestAudit_offboardingWrite_writesZeroRows(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAWithRecorder(t, rec)

	req := httptest.NewRequest(http.MethodPost, "/sync/push", nil) // POST = write
	req.Header.Set("X-Forwarded-Email", "offboarding@vivastudios.com")

	_, err := ha.Authorize(req)
	if err != nil {
		t.Fatalf("expected offboarding+write to be allowed, got: %v", err)
	}

	assertZeroCalls(t, rec)
}

// TestAudit_raceRecorder verifies that concurrent Authorize calls with a
// recorder attached do not cause data races. Run with -race.
func TestAudit_raceRecorder(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAWithRecorder(t, rec)

	const goroutines = 40
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Half succeed (active user), half deny (unknown email).
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")
			_, _ = ha.Authorize(req)
		}()
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("X-Forwarded-Email", "ghost@vivastudios.com")
			_, _ = ha.Authorize(req)
		}()
	}
	wg.Wait()

	// All 'ghost' calls should have produced denied rows; 'alice' calls: zero rows.
	calls := rec.snapshot()
	if len(calls) != goroutines {
		t.Errorf("expected %d denied rows (ghost denials), got %d", goroutines, len(calls))
	}
}

// ─── New tests added by PR2 review fixes ──────────────────────────────────────

// TestAudit_jwtMissingEmailClaim_writesDeniedRow covers the branch at ~L273 of
// header_auth.go: JWT validates (signature + expiry OK) but the email claim is
// empty. The site emits outcome=denied, source=jwt, reason_code=invalid_jwt.
//
// This test would FAIL if the instrumentation at that branch were removed
// (the recorder would capture zero calls, causing assertOneCall to fatal).
func TestAudit_jwtMissingEmailClaim_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAJWTWithRecorder(t, rec)

	// Mint a structurally valid, non-expired JWT with an empty email claim.
	// MintJWT does not validate the email field, so this produces a token whose
	// signature verifies cleanly but whose email claim is "".
	token := mintTestJWT(t, "", "NoEmail", "dev", "member", 0)

	req := httptest.NewRequest(http.MethodGet, "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for JWT with empty email claim")
	}

	assertOneCall(t, rec, "", OutcomeDenied, SourceJWT, ReasonInvalidJWT)
}

// mockAdminResolver satisfies both UserLoader and AdminResolver.
// SoleAdmin() returns (admin, true) on the first call (so constructor validation
// passes) and (zero, false) on every subsequent call (so Authorize hits the
// bypass_admin_missing deny site).
//
// This is the only seam available without refactoring production code: the
// constructor calls SoleAdmin() once for validation; Authorize calls it again
// at request time. A stateful mock exploits that gap cleanly.
type mockAdminResolver struct {
	delegate    *users.YAMLLoader // handles Lookup
	soleAdminOK bool              // toggled false after first call
}

func (m *mockAdminResolver) Lookup(email string) (users.Principal, bool) {
	return m.delegate.Lookup(email)
}

func (m *mockAdminResolver) SoleAdmin() (users.Principal, bool) {
	if m.soleAdminOK {
		// First call: return the real sole admin so the constructor accepts us.
		m.soleAdminOK = false
		return m.delegate.SoleAdmin()
	}
	// Subsequent calls: simulate sole admin disappearing (e.g., after a reload
	// that removed or added a second admin between construction and first request).
	return users.Principal{}, false
}

// TestAudit_bypassAdminMissing_writesDeniedRow covers the bypass_admin_missing
// deny site (~L247 of header_auth.go): the bypass token matches but SoleAdmin()
// returns ok=false at request time.
//
// The seam used: mockAdminResolver.SoleAdmin() returns (admin, true) on the
// first call (constructor validation) and (zero, false) on the second call
// (Authorize time). This requires no production refactor and correctly exercises
// the instrumented branch.
//
// This test would FAIL if the ha.recordAuth call at the bypass_admin_missing
// branch were removed (the recorder would capture zero calls).
func TestAudit_bypassAdminMissing_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}

	loader := buildTestLoader(t, testYAML) // real loader with alice as sole admin
	mock := &mockAdminResolver{delegate: loader, soleAdminOK: true}

	// Constructor calls SoleAdmin() once — mock returns true (passes validation).
	ha, err := NewHeaderAuthenticator(mock, "super-secret-bypass")
	if err != nil {
		t.Fatalf("NewHeaderAuthenticator: %v", err)
	}
	ha.SetAuditRecorder(rec)

	// Now mock.soleAdminOK == false, so the next SoleAdmin() call (inside
	// Authorize) will return (zero, false) → bypass_admin_missing branch fires.
	req := httptest.NewRequest(http.MethodGet, "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer super-secret-bypass")

	_, err = ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for bypass_admin_missing")
	}

	assertOneCall(t, rec, "", OutcomeDenied, SourceLegacy, ReasonBypassAdminMissing)
}

// TestAudit_invalidDomain_writesDeniedRow covers the invalid_domain deny site in
// resolveByEmail (~L308 of header_auth.go): an email whose domain is not
// @vivastudios.com presented via X-Forwarded-Email (source=oauth).
//
// Distinct from unknown_email: the domain check fires BEFORE the directory
// lookup — ghost@gmail.com never reaches Lookup() at all.
//
// This test would FAIL if the ha.recordAuth call at the invalid-domain branch
// were removed (the recorder would capture zero calls).
func TestAudit_invalidDomain_writesDeniedRow(t *testing.T) {
	t.Parallel()
	rec := &fakeRecorder{}
	ha := newTestHAWithRecorder(t, rec)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Email", "ghost@gmail.com")

	_, err := ha.Authorize(req)
	if err == nil {
		t.Fatal("expected error for non-vivastudios.com domain")
	}

	assertOneCall(t, rec, "ghost@gmail.com", OutcomeDenied, SourceOAuth, ReasonInvalidDomain)
}

// ─── Helper: split JWT on dots without importing strings ─────────────────────

func splitDot(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// ─── Outcome / Source / Reason constants used by tests ───────────────────────
// These reference the constants that will live in header_auth.go (PR2) or
// cloudstore after import. Tests reference them via the auth package constants
// (re-exported in header_auth.go for convenience of callers that only import auth).

// (The constants OutcomeAllowed, OutcomeDenied, SourceOAuth, SourceJWT,
// SourceLegacy, ReasonInvalidJWT, ReasonMissingCredential, ReasonUnknownEmail,
// ReasonRemovedUser, ReasonAccountOffboarding, ReasonBypassAdminMissing are
// expected to be defined in header_auth.go as package-level constants that
// mirror cloudstore values — or imported from cloudstore.)
