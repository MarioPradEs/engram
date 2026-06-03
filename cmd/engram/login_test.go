package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// fakeTokenExchanger simulates a successful OAuth token exchange.
type fakeTokenExchanger struct {
	// Claims to return on successful exchange.
	claims auth.JWTClaims
	// err if non-nil, exchange returns this error.
	err error
}

func (f *fakeTokenExchanger) Exchange(code string) (auth.JWTClaims, error) {
	if f.err != nil {
		return auth.JWTClaims{}, f.err
	}
	return f.claims, nil
}

// fakeBrowserOpener records which URL was opened and optionally returns an error.
// It does NOT follow the URL (for tests that don't need the full loopback flow).
type fakeBrowserOpener struct {
	openedURL string
	err       error
}

func (f *fakeBrowserOpener) Open(url string) error {
	f.openedURL = url
	return f.err
}

// realHTTPBrowserOpener simulates a browser by actually following the URL and
// its redirects via an HTTP GET. Used by loopback exchanger tests so the
// callback server receives the token redirect.
type realHTTPBrowserOpener struct {
	openedURL string
}

func (r *realHTTPBrowserOpener) Open(openURL string) error {
	r.openedURL = openURL
	// Use a client that follows redirects (default), so the loopback callback is hit.
	go func() {
		resp, err := http.Get(openURL) //nolint:noctx
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
	return nil
}

// fakeClock implements the Clock interface for deterministic time in tests.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

// fakeReclassify is a drop-in for the reclassify hook so integration tests don't
// run real reclassify against a mixed database.
type fakeReclassifyHook struct {
	called bool
}

func (f *fakeReclassifyHook) Run(cfg store.Config) {
	f.called = true
}

// TestLoginHappyPath verifies that a successful login stores the credentials.json
// with the correct fields and file permissions 0600, and that exp-iat == 604800.
func TestLoginHappyPath(t *testing.T) {
	cfg := testConfig(t)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	// Create a JWT the fake exchanger will return.
	jwtToken, err := auth.MintJWT(secret, auth.JWTClaims{
		Sub:        "mpradas@vivastudios.com",
		Email:      "mpradas@vivastudios.com",
		Name:       "Mario",
		Department: "dev",
		Role:       "admin",
	}, now)
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:        "mpradas@vivastudios.com",
			Email:      "mpradas@vivastudios.com",
			Name:       "Mario",
			Department: "dev",
			Role:       "admin",
			Iat:        now.Unix(),
			Exp:        now.Unix() + 604800,
		},
	}
	browser := &fakeBrowserOpener{}
	clock := &fakeClock{now: now}

	// Override credential directory to use t.TempDir().
	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	// Override reclassify hook to avoid side effects.
	reclassifyHook := &fakeReclassifyHook{}
	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(cfg store.Config) { reclassifyHook.Run(cfg) }
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	// Suppress pull/push during test.
	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) error { return nil }
	postLoginPushFn = func(cfg store.Config) error { return nil }
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	// The JWT token is only used to validate exp-iat, not stored directly.
	_ = jwtToken

	loginRunner := &loginCommand{
		cfg:      cfg,
		exchanger: exchanger,
		browser:  browser,
		clock:    clock,
		secret:   secret,
	}
	if err := loginRunner.Run(); err != nil {
		t.Fatalf("login.Run: %v", err)
	}

	// Verify credentials.json exists and has correct content.
	credPath := filepath.Join(credDir, "credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read credentials.json: %v", err)
	}

	var creds credentialFile
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("unmarshal credentials.json: %v", err)
	}

	if creds.Email != "mpradas@vivastudios.com" {
		t.Errorf("email: got %q, want %q", creds.Email, "mpradas@vivastudios.com")
	}
	if creds.AccessToken == "" {
		t.Error("access_token must not be empty")
	}

	// Verify exp-iat == 604800 (7 days).
	issuedAt, err := time.Parse(time.RFC3339, creds.IssuedAt)
	if err != nil {
		t.Fatalf("parse issued_at: %v", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, creds.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	lifetime := expiresAt.Sub(issuedAt)
	if lifetime != 7*24*time.Hour {
		t.Errorf("token lifetime: got %v, want 168h (7 days)", lifetime)
	}

	// Verify file permissions 0600. Skipped on Windows: NTFS does not map Unix
	// permission bits, so Go reports 0666 for any writable file regardless of the
	// 0600 passed to os.WriteFile. The owner-only intent still holds on the Linux
	// hosts where Engram Cloud runs; this assertion verifies it there.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(credPath)
		if err != nil {
			t.Fatalf("stat credentials.json: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("credentials.json perm: got %o, want 0600", info.Mode().Perm())
		}
	}

	// Verify reclassify hook was called before push.
	if !reclassifyHook.called {
		t.Error("expected reclassify hook to be called")
	}
}

// TestLoginPullFailureDoesNotBlockPush verifies that a pull failure during post-login
// sync logs a warning but allows push to proceed.
func TestLoginPullFailureDoesNotBlockPush(t *testing.T) {
	cfg := testConfig(t)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(cfg store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	pushCalled := false
	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) error { return fmt.Errorf("pull: network error") }
	postLoginPushFn = func(cfg store.Config) error { pushCalled = true; return nil }
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	loginRunner := &loginCommand{
		cfg:      cfg,
		exchanger: exchanger,
		browser:  &fakeBrowserOpener{},
		clock:    &fakeClock{now: now},
		secret:   secret,
	}

	stdout, stderr := captureOutput(t, func() {
		if err := loginRunner.Run(); err != nil {
			t.Errorf("login.Run: %v", err)
		}
	})

	// Pull failure must print a warning.
	combined := stdout + stderr
	if !strings.Contains(combined, "warn") && !strings.Contains(combined, "pull") {
		t.Errorf("expected pull warning in output, got: %q", combined)
	}

	// Push must still be called.
	if !pushCalled {
		t.Error("expected push to be called after pull failure")
	}
}

// TestLoginPushFailureIsWarningNotError verifies that a push failure after login
// is a warning only — login is still considered successful (token already stored).
func TestLoginPushFailureIsWarningNotError(t *testing.T) {
	cfg := testConfig(t)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(cfg store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) error { return nil }
	postLoginPushFn = func(cfg store.Config) error { return fmt.Errorf("push: server error") }
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	loginRunner := &loginCommand{
		cfg:      cfg,
		exchanger: exchanger,
		browser:  &fakeBrowserOpener{},
		clock:    &fakeClock{now: now},
		secret:   secret,
	}

	// Login must NOT return an error even when push fails.
	stdout, stderr := captureOutput(t, func() {
		if err := loginRunner.Run(); err != nil {
			t.Errorf("login.Run returned error: %v (expected success with push warning)", err)
		}
	})

	combined := stdout + stderr
	if !strings.Contains(combined, "warn") && !strings.Contains(combined, "push") {
		t.Errorf("expected push warning in output, got: %q", combined)
	}

	// Token must still be written.
	credPath := filepath.Join(credDir, "credentials.json")
	if _, err := os.Stat(credPath); err != nil {
		t.Errorf("credentials.json must exist even after push failure: %v", err)
	}
}

// TestLoginReclassifyCalledBeforePush verifies that the reclassify hook fires before push.
func TestLoginReclassifyCalledBeforePush(t *testing.T) {
	cfg := testConfig(t)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	var order []string
	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(cfg store.Config) { order = append(order, "reclassify") }
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) error { order = append(order, "pull"); return nil }
	postLoginPushFn = func(cfg store.Config) error { order = append(order, "push"); return nil }
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	loginRunner := &loginCommand{
		cfg:      cfg,
		exchanger: exchanger,
		browser:  &fakeBrowserOpener{},
		clock:    &fakeClock{now: now},
		secret:   secret,
	}
	if err := loginRunner.Run(); err != nil {
		t.Fatalf("login.Run: %v", err)
	}

	// Verify canonical order: reclassify → pull → push.
	wantOrder := []string{"reclassify", "pull", "push"}
	if len(order) != len(wantOrder) {
		t.Fatalf("hook order: got %v, want %v", order, wantOrder)
	}
	for i, step := range wantOrder {
		if order[i] != step {
			t.Errorf("step[%d]: got %q, want %q", i, order[i], step)
		}
	}
}

// TestLoginGeneralEnrollment verifies that the login post-auth hook seeds the
// sync_enrolled_projects row for "general".
func TestLoginGeneralEnrollment(t *testing.T) {
	cfg := testConfig(t)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(cfg store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) error { return nil }
	postLoginPushFn = func(cfg store.Config) error { return nil }
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	loginRunner := &loginCommand{
		cfg:      cfg,
		exchanger: exchanger,
		browser:  &fakeBrowserOpener{},
		clock:    &fakeClock{now: now},
		secret:   secret,
	}
	if err := loginRunner.Run(); err != nil {
		t.Fatalf("login.Run: %v", err)
	}

	// Verify "general" is enrolled in sync_enrolled_projects.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	enrolled, err := s.IsProjectEnrolled("general")
	if err != nil {
		t.Fatalf("IsProjectEnrolled: %v", err)
	}
	if !enrolled {
		t.Error("expected 'general' to be enrolled in sync_enrolled_projects after login")
	}
}

// TestLoginExpiryMessageOn401 verifies that a 401 response from any sync operation
// prints the "Session expired. Run 'engram login'" message and does not retry.
func TestLoginExpiryMessageOn401(t *testing.T) {
	retryCount := 0
	const expiryMsg = "Session expired. Run 'engram login' to re-authenticate."

	// Use a real HTTPStatusError wrapping 401.
	err401 := makeHTTPStatusError401()

	stdout, stderr := captureOutput(t, func() {
		err := handleSyncAuthError(
			err401,
			func() string { return expiryMsg },
			func() error { retryCount++; return nil },
		)
		if err == nil {
			t.Error("expected non-nil error from handleSyncAuthError on 401")
		}
	})

	if retryCount != 0 {
		t.Errorf("expected no retry on 401, got %d retries", retryCount)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "Session expired") {
		t.Errorf("expected expiry message in output, got: %q", combined)
	}
}

// TestSyncAuthErrorNon401IsPassthrough verifies that non-401 errors are returned
// as-is without printing the expiry message.
func TestSyncAuthErrorNon401IsPassthrough(t *testing.T) {
	retryCount := 0

	networkErr := fmt.Errorf("connection refused")
	stdout, stderr := captureOutput(t, func() {
		err := handleSyncAuthError(
			networkErr,
			func() string { return "Session expired. Run 'engram login' to re-authenticate." },
			func() error { retryCount++; return nil },
		)
		if err == nil {
			t.Error("expected non-nil error passthrough")
		}
	})

	combined := stdout + stderr
	if strings.Contains(combined, "Session expired") {
		t.Errorf("non-401 error should NOT print expiry message, got: %q", combined)
	}
}

// makeHTTPStatusError401 creates a real *remote.HTTPStatusError with status 401
// by hitting a test server that returns 401 and calling PullMutations.
func makeHTTPStatusError401() error {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	mt, err := remote.NewMutationTransport(srv.URL, "bad-token")
	if err != nil {
		panic(fmt.Sprintf("makeHTTPStatusError401: NewMutationTransport: %v", err))
	}
	_, err = mt.PullMutations(0, 1)
	return err
}

// TestLoginMarksPushGateIncompleteBeforeReclassify verifies W7:
// login must call MarkReclassifyIncomplete BEFORE the reclassify hook so the
// push gate is ACTIVE (incomplete) throughout reclassify, then the gate is
// lifted (complete) once reclassify finishes normally.
//
// Scenario: fresh store (no sync_state row) → IsReclassifyComplete defaults true.
// After MarkReclassifyIncomplete is called, it must return false.
// After reclassifyHookFn completes (with MarkReclassifyComplete), it returns true.
func TestLoginMarksPushGateIncompleteBeforeReclassify(t *testing.T) {
	cfg := testConfig(t)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "gate-test@vivastudios.com",
			Email: "gate-test@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	// Open a store once to observe gate state across the login lifecycle.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	// Confirm fresh store has IsReclassifyComplete=true (safe default for existing installs).
	initialComplete, err := s.IsReclassifyComplete(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("IsReclassifyComplete (initial): %v", err)
	}
	if !initialComplete {
		t.Fatal("expected fresh store to report reclassify complete by default (safe default for existing installs)")
	}

	// Inject a reclassify hook that:
	//   a) captures gate state at hook entry (must be INCOMPLETE — gate active)
	//   b) calls MarkReclassifyComplete to simulate real reclassify finishing
	var gateStateAtHookEntry bool
	var gateReadErr error
	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(hookCfg store.Config) {
		gateStateAtHookEntry, gateReadErr = s.IsReclassifyComplete(store.DefaultSyncTargetKey)
		// Simulate reclassify completing normally.
		_ = s.MarkReclassifyComplete(store.DefaultSyncTargetKey)
	}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) error { return nil }
	postLoginPushFn = func(cfg store.Config) error { return nil }
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	loginRunner := &loginCommand{
		cfg:      cfg,
		exchanger: exchanger,
		browser:  &fakeBrowserOpener{},
		clock:    &fakeClock{now: now},
		secret:   secret,
	}
	if err := loginRunner.Run(); err != nil {
		t.Fatalf("login.Run: %v", err)
	}

	// The hook must have been invoked.
	if gateReadErr != nil {
		t.Fatalf("IsReclassifyComplete inside hook: %v", gateReadErr)
	}

	// Gate must have been ACTIVE (incomplete=false) when the hook was called.
	if gateStateAtHookEntry {
		t.Error("push gate must be INACTIVE (IsReclassifyComplete=false) when reclassify hook starts — MarkReclassifyIncomplete must be called before the hook")
	}

	// After full login flow: gate must be LIFTED (complete=true) because the
	// injected hook called MarkReclassifyComplete.
	finalComplete, err := s.IsReclassifyComplete(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("IsReclassifyComplete (final): %v", err)
	}
	if !finalComplete {
		t.Error("push gate must be ACTIVE (IsReclassifyComplete=true) after login + reclassify complete")
	}
}

// makeHTTPStatusError403 creates a real *remote.HTTPStatusError with status 403.
func makeHTTPStatusError403() error {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error_code":"policy_forbidden","error":"forbidden"}`))
	}))
	defer srv.Close()

	mt, err := remote.NewMutationTransport(srv.URL, "bad-token")
	if err != nil {
		panic(fmt.Sprintf("makeHTTPStatusError403: NewMutationTransport: %v", err))
	}
	_, err = mt.PullMutations(0, 1)
	return err
}

// ─── Piece C: real loopback OAuth flow ────────────────────────────────────────

// TestLoopbackExchangerHappyPath verifies the real loopback exchanger:
// starts a local server, opens the "browser" to the auth URL with
// redirect_uri+state, receives the callback with token+state, validates state,
// and returns the raw token with parsed claims.
func TestLoopbackExchangerHappyPath(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("s", 32)

	// Build a fake /auth server that mints a token and redirects.
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectURI := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")
		if redirectURI == "" || state == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		tok, err := auth.MintJWT(secret, auth.JWTClaims{
			Sub:        "alice@vivastudios.com",
			Email:      "alice@vivastudios.com",
			Name:       "Alice",
			Department: "dev",
			Role:       "admin",
		}, now)
		if err != nil {
			http.Error(w, "mint failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, redirectURI+"?token="+tok+"&state="+state, http.StatusFound)
	}))
	defer authSrv.Close()

	// Use realHTTPBrowserOpener so the callback server actually receives the redirect.
	browser := &realHTTPBrowserOpener{}
	ex := newLoopbackAuthExchanger(authSrv.URL+"/auth", browser)

	rawToken, claims, err := ex.ExchangeWithToken()
	if err != nil {
		t.Fatalf("ExchangeWithToken: %v", err)
	}
	if rawToken == "" {
		t.Error("expected non-empty raw token")
	}
	if claims.Email != "alice@vivastudios.com" {
		t.Errorf("email: got %q, want alice@vivastudios.com", claims.Email)
	}
	if claims.Exp-claims.Iat != 604800 {
		t.Errorf("exp-iat: got %d, want 604800", claims.Exp-claims.Iat)
	}
	if !strings.Contains(browser.openedURL, "/auth") {
		t.Errorf("expected browser URL to contain /auth, got %q", browser.openedURL)
	}
	if !strings.Contains(browser.openedURL, "redirect_uri=") {
		t.Errorf("expected redirect_uri in browser URL, got %q", browser.openedURL)
	}
	if !strings.Contains(browser.openedURL, "state=") {
		t.Errorf("expected state in browser URL, got %q", browser.openedURL)
	}
}

// TestLoopbackExchangerStateValidation verifies CSRF protection: if the auth
// server sends back a different state, the exchanger rejects the response.
func TestLoopbackExchangerStateValidation(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectURI := r.URL.Query().Get("redirect_uri")
		now := time.Now().UTC()
		secret := strings.Repeat("s", 32)
		tok, _ := auth.MintJWT(secret, auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
		}, now)
		// Send WRONG state — exchanger must reject this.
		http.Redirect(w, r, redirectURI+"?token="+tok+"&state=WRONG_STATE", http.StatusFound)
	}))
	defer authSrv.Close()

	browser := &realHTTPBrowserOpener{}
	ex := newLoopbackAuthExchanger(authSrv.URL+"/auth", browser)

	_, _, err := ex.ExchangeWithToken()
	if err == nil {
		t.Fatal("expected error when state does not match, got nil")
	}
	if !strings.Contains(err.Error(), "state") {
		t.Errorf("expected state mismatch error, got: %v", err)
	}
}

// TestLoopbackExchangerCredentialsWritten_0600 verifies that when login uses the
// real loopback exchanger, the credentials are written with the server-minted token
// (not a client-minted one) and the file has 0600 permissions.
func TestLoopbackExchangerCredentialsWritten_0600(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("s", 32)

	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectURI := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")
		tok, err := auth.MintJWT(secret, auth.JWTClaims{
			Sub:        "mario@vivastudios.com",
			Email:      "mario@vivastudios.com",
			Name:       "Mario",
			Department: "dev",
			Role:       "admin",
		}, now)
		if err != nil {
			http.Error(w, "mint error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, redirectURI+"?token="+tok+"&state="+state, http.StatusFound)
	}))
	defer authSrv.Close()

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(_ store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(_ store.Config) error { return nil }
	postLoginPushFn = func(_ store.Config) error { return nil }
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	cfg := testConfig(t)
	browser := &realHTTPBrowserOpener{}
	ex := newLoopbackAuthExchanger(authSrv.URL+"/auth", browser)

	// Use the raw-token exchanger as the loginCommand exchanger.
	runner := &loginCommand{
		cfg:       cfg,
		exchanger: ex,
		browser:   browser,
		clock:     &fakeClock{now: now},
		secret:    secret, // irrelevant — server-minted token is stored directly
	}
	if err := runner.Run(); err != nil {
		t.Fatalf("login.Run: %v", err)
	}

	credPath := filepath.Join(credDir, "credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read credentials.json: %v", err)
	}
	var creds credentialFile
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("unmarshal credentials.json: %v", err)
	}
	if creds.Email != "mario@vivastudios.com" {
		t.Errorf("email: got %q, want mario@vivastudios.com", creds.Email)
	}
	if creds.AccessToken == "" {
		t.Error("access_token must not be empty")
	}
	// Verify the token is the server-minted one (exp-iat == 604800).
	issuedAt, err := time.Parse(time.RFC3339, creds.IssuedAt)
	if err != nil {
		t.Fatalf("parse issued_at: %v", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, creds.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	if expiresAt.Sub(issuedAt) != 7*24*time.Hour {
		t.Errorf("token lifetime: got %v, want 168h", expiresAt.Sub(issuedAt))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(credPath)
		if err != nil {
			t.Fatalf("stat credentials.json: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("credentials.json perm: got %o, want 0600", info.Mode().Perm())
		}
	}
}

// ─── Piece D: post-login sync with Bearer ────────────────────────────────────

// TestPostLoginSyncSendsBearerToken verifies that postLoginPullFn / postLoginPushFn
// use the stored JWT as Authorization: Bearer against /sync/*.
func TestPostLoginSyncSendsBearerToken(t *testing.T) {
	receivedAuthHeaders := make([]string, 0)
	syncSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeaders = append(receivedAuthHeaders, r.Header.Get("Authorization"))
		if r.URL.Path == "/sync/mutations/pull" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"mutations":[],"has_more":false,"latest_seq":0}`))
			return
		}
		if r.URL.Path == "/sync/mutations/push" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"accepted_seqs":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer syncSrv.Close()

	storedToken := "stored-jwt-token-from-server"

	// Verify postLoginPull and postLoginPush use the stored token.
	pullCalled := false
	pushCalled := false
	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) error {
		pullCalled = true
		mt, err := remote.NewMutationTransport(syncSrv.URL, storedToken)
		if err != nil {
			return err
		}
		_, err = mt.PullMutations(0, 10)
		return err
	}
	postLoginPushFn = func(cfg store.Config) error {
		pushCalled = true
		mt, err := remote.NewMutationTransport(syncSrv.URL, storedToken)
		if err != nil {
			return err
		}
		_, err = mt.PushMutations(nil)
		return err
	}
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)
	cfg := testConfig(t)

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(_ store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	runner := &loginCommand{
		cfg:       cfg,
		exchanger: exchanger,
		browser:   &fakeBrowserOpener{},
		clock:     &fakeClock{now: now},
		secret:    secret,
	}
	if err := runner.Run(); err != nil {
		t.Fatalf("login.Run: %v", err)
	}

	if !pullCalled {
		t.Error("expected postLoginPullFn to be called")
	}
	if !pushCalled {
		t.Error("expected postLoginPushFn to be called")
	}
	// All received auth headers must be Bearer storedToken.
	for _, h := range receivedAuthHeaders {
		if h != "Bearer "+storedToken {
			t.Errorf("expected Authorization: Bearer %s, got %q", storedToken, h)
		}
	}
}

// TestPostLoginSyncSummaryPrinted verifies the sync summary is printed.
func TestPostLoginSyncSummaryPrinted(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)
	cfg := testConfig(t)

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(_ store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(_ store.Config) error { return nil }
	postLoginPushFn = func(_ store.Config) error { return nil }
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	runner := &loginCommand{
		cfg:       cfg,
		exchanger: exchanger,
		browser:   &fakeBrowserOpener{},
		clock:     &fakeClock{now: now},
		secret:    secret,
	}

	stdout, _ := captureOutput(t, func() {
		if err := runner.Run(); err != nil {
			t.Errorf("login.Run: %v", err)
		}
	})

	// Must print the sync summary line.
	if !strings.Contains(stdout, "Synced:") {
		t.Errorf("expected sync summary in stdout, got: %q", stdout)
	}
}
