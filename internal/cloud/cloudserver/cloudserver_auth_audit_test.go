package cloudserver

// PR2 cloudserver audit tests (Strict TDD — RED first).
// Covers:
//   - autoLoginFromHeader OAuth mint → exactly one allowed/oauth row in recorder.
//   - Normal sync request → ZERO rows (negative case).
//   - Concurrent Authorize calls with recorder do not race (-race).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
)

// ─── Fake Audit Recorder ─────────────────────────────────────────────────────

type fakeAuditRecorder struct {
	mu    sync.Mutex
	calls []auditRecordCall
}

type auditRecordCall struct {
	email      string
	outcome    string
	source     string
	reasonCode string
}

func (f *fakeAuditRecorder) RecordAuthEvent(_ context.Context, email, outcome, source, reasonCode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, auditRecordCall{
		email:      email,
		outcome:    outcome,
		source:     source,
		reasonCode: reasonCode,
	})
}

func (f *fakeAuditRecorder) snapshot() []auditRecordCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]auditRecordCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// ─── Constants ────────────────────────────────────────────────────────────────

const auditTestJWTSecret = "a-test-jwt-secret-that-is-32-chars-long!"

const auditTestYAML = `users:
  - email: audituser@vivastudios.com
    name: Audit User
    department: dev
    role: admin
    status: active
    enrolled:
      - general
  - email: removed@vivastudios.com
    name: Removed User
    department: dev
    role: member
    status: removed
    enrolled: []
`

const auditTestJWTTTL = 7 * 24 * time.Hour

// ─── Helpers ─────────────────────────────────────────────────────────────────

// buildAuditServer builds a CloudServer with authLoader and WithAuditRecorder
// option so autoLoginFromHeader can emit audit rows.
func buildAuditServer(t *testing.T, rec cloudauth.AuthAuditRecorder) *CloudServer {
	t.Helper()
	loader := buildAuthTestLoader(t, auditTestYAML)
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", auditTestJWTSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	ha.SetAuditRecorder(rec)
	return New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, auditTestJWTSecret),
		WithAuditRecorder(rec),
	)
}

// ─── Tests ───────────────────────────────────────────────────────────────────

// TestAudit_autoLoginFromHeader_mint_writesAllowedOAuthRow verifies that a
// successful autoLoginFromHeader (OAuth mint) writes exactly one
// outcome=allowed, source=oauth row.
func TestAudit_autoLoginFromHeader_mint_writesAllowedOAuthRow(t *testing.T) {
	t.Parallel()

	rec := &fakeAuditRecorder{}
	srv := buildAuditServer(t, rec)

	// Drive a GET /dashboard/login with a valid X-Forwarded-Email.
	// autoLoginFromHeader fires and mints a JWT → 303 redirect with Set-Cookie.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
	req.Header.Set("X-Forwarded-Email", "audituser@vivastudios.com")
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 (auto-login mint), got %d body=%q", w.Code, w.Body.String())
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 audit row after OAuth mint, got %d: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.email != "audituser@vivastudios.com" {
		t.Errorf("audit row email = %q, want %q", c.email, "audituser@vivastudios.com")
	}
	if c.outcome != cloudauth.OutcomeAllowed {
		t.Errorf("audit row outcome = %q, want %q", c.outcome, cloudauth.OutcomeAllowed)
	}
	if c.source != cloudauth.SourceOAuth {
		t.Errorf("audit row source = %q, want %q", c.source, cloudauth.SourceOAuth)
	}
	if c.reasonCode != "" {
		t.Errorf("audit row reasonCode = %q, want empty (NULL) for allowed rows", c.reasonCode)
	}
}

// TestAudit_successSyncRequest_writesZeroRows verifies the critical negative
// case: a normal sync request with a valid Bearer JWT writes ZERO audit rows
// (the JWT/sync path does not produce allowed rows per spec).
func TestAudit_successSyncRequest_writesZeroRows(t *testing.T) {
	t.Parallel()

	rec := &fakeAuditRecorder{}
	srv := buildAuditServer(t, rec)

	// Mint a valid JWT for the active user.
	token, err := cloudauth.MintJWT(auditTestJWTSecret, cloudauth.JWTClaims{
		Sub:        "audituser@vivastudios.com",
		Email:      "audituser@vivastudios.com",
		Name:       "Audit User",
		Department: "dev",
		Role:       "admin",
	}, time.Now().UTC(), auditTestJWTTTL)
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}

	// Issue a sync GET request (pull manifest).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/pull?project=general", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	srv.Handler().ServeHTTP(w, req)

	// The request should be authorized (2xx or a store-driven error, not 401/403).
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("sync request was rejected (%d) — expected authorized: %s", w.Code, w.Body.String())
	}

	// CRITICAL: normal sync/API requests MUST write ZERO audit rows.
	calls := rec.snapshot()
	if len(calls) != 0 {
		t.Fatalf("expected 0 audit rows for normal sync request, got %d: %+v", len(calls), calls)
	}
}

// TestAudit_raceRecorder verifies that concurrent auth requests with a recorder
// attached do not cause data races. Run with -race (CGO_ENABLED=1).
func TestAudit_raceRecorder(t *testing.T) {
	t.Parallel()

	rec := &fakeAuditRecorder{}
	srv := buildAuditServer(t, rec)

	// Mix of: valid OAuth mint requests (write 1 row each) and
	// denied requests (invalid JWT — write 1 denied row each via header auth).
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Valid login attempts (allowed rows via autoLoginFromHeader).
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "/dashboard/login", nil)
			r.Header.Set("X-Forwarded-Email", "audituser@vivastudios.com")
			w2 := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w2, r)
		}()
	}
	// Invalid JWT requests (denied rows via Authorize in JWT mode).
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, "/sync/pull?project=general", nil)
			r.Header.Set("Authorization", "Bearer invalid-jwt-token")
			w2 := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w2, r)
		}()
	}
	wg.Wait()

	// Recorder must not have data-raced (checked by -race flag).
	calls := rec.snapshot()
	if len(calls) == 0 {
		t.Error("expected some audit rows to be recorded during concurrent requests")
	}
}
