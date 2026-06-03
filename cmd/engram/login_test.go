package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
type fakeBrowserOpener struct {
	openedURL string
	err       error
}

func (f *fakeBrowserOpener) Open(url string) error {
	f.openedURL = url
	return f.err
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

	// Verify file permissions 0600.
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("stat credentials.json: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("credentials.json perm: got %o, want 0600", info.Mode().Perm())
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
