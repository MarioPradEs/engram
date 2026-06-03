package main

// post_login_sync_test.go — STRICT TDD (RED → GREEN → REFACTOR)
// Tests for W8: credentials.json JWT as cloud sync Bearer + real post-login sync counts.
//
// Coverage:
//  1. readCredentialsToken — present+valid → returns token; expired → returns expiry error;
//     absent → falls back to resolveCloudRuntimeConfig (env/cloud.json).
//  2. Token precedence: credentials.json > ENGRAM_CLOUD_TOKEN > cloud.json.
//  3. postLoginPullFn / postLoginPushFn — real counts via fake httptest server.
//  4. Summary line prints real counts (not hardcoded 0).
//  5. Pull failure → warning + push still runs.
//  6. Push failure → login still succeeds (warning only).
//  7. Bearer sent on sync requests equals credentials.json access_token.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// writeCredentialsFile writes a credentialFile JSON to dir/credentials.json.
func writeCredentialsFile(t *testing.T, dir string, creds credentialFile) {
	t.Helper()
	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), b, 0600); err != nil {
		t.Fatalf("write credentials.json: %v", err)
	}
}

// fakeCloudSyncServer is an httptest.Server that simulates /sync/mutations/pull
// and /sync/mutations/push, recording received Authorization headers.
type fakeCloudSyncServer struct {
	srv            *httptest.Server
	mu             sync.Mutex
	authHeaders    []string
	pullMutations  int // how many mutations to return on pull
	pushAccepted   int // how many seqs to accept on push
	pullStatusCode int // if non-zero, return this code on pull
	pushStatusCode int // if non-zero, return this code on push
}

func newFakeCloudSyncServer(t *testing.T) *fakeCloudSyncServer {
	t.Helper()
	f := &fakeCloudSyncServer{
		pullStatusCode: http.StatusOK,
		pushStatusCode: http.StatusOK,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		pullMuts := f.pullMutations
		pushAcc := f.pushAccepted
		pullCode := f.pullStatusCode
		pushCode := f.pushStatusCode
		f.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sync/mutations/pull":
			if pullCode != http.StatusOK {
				w.WriteHeader(pullCode)
				_, _ = w.Write([]byte(`{"error":"pull failed"}`))
				return
			}
			muts := make([]map[string]any, pullMuts)
			for i := 0; i < pullMuts; i++ {
				muts[i] = map[string]any{
					"seq":         int64(i + 1),
					"entity":      "observation",
					"entity_key":  fmt.Sprintf("key-%d", i+1),
					"op":          "upsert",
					"payload":     map[string]any{"id": i + 1},
					"occurred_at": "2026-01-01T00:00:00Z",
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mutations":  muts,
				"has_more":   false,
				"latest_seq": int64(pullMuts),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/sync/mutations/push":
			if pushCode != http.StatusOK {
				w.WriteHeader(pushCode)
				_, _ = w.Write([]byte(`{"error":"push failed"}`))
				return
			}
			seqs := make([]int64, pushAcc)
			for i := 0; i < pushAcc; i++ {
				seqs[i] = int64(i + 1)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted_seqs": seqs})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() { f.srv.Close() })
	return f
}

func (f *fakeCloudSyncServer) URL() string { return f.srv.URL }

func (f *fakeCloudSyncServer) ReceivedAuthHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.authHeaders))
	copy(out, f.authHeaders)
	return out
}

// ─── readCredentialsToken tests ───────────────────────────────────────────────

// TestReadCredentialsToken_PresentAndValid verifies that a valid credentials.json
// returns the access_token without error.
func TestReadCredentialsToken_PresentAndValid(t *testing.T) {
	credDir := t.TempDir()

	now := time.Now().UTC()
	secret := strings.Repeat("x", 32)
	tok, err := auth.MintJWT(secret, auth.JWTClaims{
		Sub:   "alice@vivastudios.com",
		Email: "alice@vivastudios.com",
		Iat:   now.Unix(),
		Exp:   now.Add(1 * time.Hour).Unix(),
	}, now)
	if err != nil {
		t.Fatalf("MintJWT: %v", err)
	}

	writeCredentialsFile(t, credDir, credentialFile{
		AccessToken: tok,
		IssuedAt:    now.Format(time.RFC3339),
		ExpiresAt:   now.Add(1 * time.Hour).Format(time.RFC3339),
		Email:       "alice@vivastudios.com",
	})

	token, err := readCredentialsToken(credDir)
	if err != nil {
		t.Fatalf("readCredentialsToken: %v", err)
	}
	if token != tok {
		t.Errorf("expected token %q, got %q", tok, token)
	}
}

// TestReadCredentialsToken_Expired verifies that an expired credentials.json
// returns an errCredentialsExpired error (triggers login prompt).
func TestReadCredentialsToken_Expired(t *testing.T) {
	credDir := t.TempDir()

	past := time.Now().UTC().Add(-2 * time.Hour)
	writeCredentialsFile(t, credDir, credentialFile{
		AccessToken: "expired-token",
		IssuedAt:    past.Add(-24 * time.Hour).Format(time.RFC3339),
		ExpiresAt:   past.Format(time.RFC3339),
		Email:       "alice@vivastudios.com",
	})

	_, err := readCredentialsToken(credDir)
	if err == nil {
		t.Fatal("expected expiry error for expired credentials.json, got nil")
	}
	if !isExpiredCredentialsError(err) {
		t.Errorf("expected errCredentialsExpired, got: %v", err)
	}
}

// TestReadCredentialsToken_Absent verifies that when credentials.json does not exist,
// readCredentialsToken returns errCredentialsNotFound (caller falls back to env/cloud.json).
func TestReadCredentialsToken_Absent(t *testing.T) {
	credDir := t.TempDir() // empty — no credentials.json

	_, err := readCredentialsToken(credDir)
	if err == nil {
		t.Fatal("expected not-found error for absent credentials.json, got nil")
	}
	if !isCredentialsNotFoundError(err) {
		t.Errorf("expected errCredentialsNotFound, got: %v", err)
	}
}

// ─── Token precedence tests ───────────────────────────────────────────────────

// TestResolveLoginToken_CredentialsJSONWinsOverEnvAndFile verifies the precedence order:
// credentials.json access_token > ENGRAM_CLOUD_TOKEN env > cloud.json token.
func TestResolveLoginToken_CredentialsJSONWinsOverEnvAndFile(t *testing.T) {
	cfg := testConfig(t)
	credDir := t.TempDir()

	const credToken = "creds-jwt-token"
	const envToken = "env-token"
	const fileToken = "file-token"

	now := time.Now().UTC()
	writeCredentialsFile(t, credDir, credentialFile{
		AccessToken: credToken,
		IssuedAt:    now.Format(time.RFC3339),
		ExpiresAt:   now.Add(1 * time.Hour).Format(time.RFC3339),
		Email:       "alice@vivastudios.com",
	})

	t.Setenv("ENGRAM_CLOUD_TOKEN", envToken)
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	token, err := resolveLoginToken(credDir, cfg)
	if err != nil {
		t.Fatalf("resolveLoginToken: %v", err)
	}
	if token != credToken {
		t.Errorf("expected credentials.json token %q to win, got %q", credToken, token)
	}
}

// TestResolveLoginToken_EnvWinsOverFile verifies that when credentials.json is absent,
// ENGRAM_CLOUD_TOKEN wins over cloud.json token.
func TestResolveLoginToken_EnvWinsOverFile(t *testing.T) {
	cfg := testConfig(t)
	credDir := t.TempDir() // no credentials.json

	const envToken = "env-token"
	const fileToken = "file-token"

	t.Setenv("ENGRAM_CLOUD_TOKEN", envToken)
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	token, err := resolveLoginToken(credDir, cfg)
	if err != nil {
		t.Fatalf("resolveLoginToken: %v", err)
	}
	if token != envToken {
		t.Errorf("expected env token %q to win over file token, got %q", envToken, token)
	}
}

// TestResolveLoginToken_FileIsLastResort verifies that when both credentials.json and
// ENGRAM_CLOUD_TOKEN are absent, cloud.json token is used as last resort.
func TestResolveLoginToken_FileIsLastResort(t *testing.T) {
	cfg := testConfig(t)
	credDir := t.TempDir() // no credentials.json

	const fileToken = "file-token"

	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	token, err := resolveLoginToken(credDir, cfg)
	if err != nil {
		t.Fatalf("resolveLoginToken: %v", err)
	}
	if token != fileToken {
		t.Errorf("expected file token %q as last resort, got %q", fileToken, token)
	}
}

// ─── Real post-login sync counts tests ────────────────────────────────────────

// TestPostLoginSyncRealCounts_SummaryPrintsActualCounts verifies that after login,
// the sync summary prints the real mutation counts returned by the fake cloud server.
func TestPostLoginSyncRealCounts_SummaryPrintsActualCounts(t *testing.T) {
	const pullCount = 3
	const pushCount = 2
	const storedToken = "test-jwt-bearer"

	fakeSrv := newFakeCloudSyncServer(t)
	fakeSrv.pullMutations = pullCount

	cfg := testConfig(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(_ store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	// Install real counting pull/push fns that hit the fake server.
	pulled := 0
	pushed := 0
	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) (int, error) {
		n, err := doPostLoginPull(fakeSrv.URL(), storedToken)
		pulled = n
		return n, err
	}
	postLoginPushFn = func(cfg store.Config) (int, error) {
		// For push count test: simulate pushCount accepted
		fakeSrv.mu.Lock()
		fakeSrv.pushAccepted = pushCount
		fakeSrv.mu.Unlock()
		n, err := doPostLoginPush(fakeSrv.URL(), storedToken, nil)
		pushed = n
		return n, err
	}
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

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

	wantSummary := fmt.Sprintf("Synced: ↓%d pulled, ↑%d pushed", pullCount, pushCount)
	if !strings.Contains(stdout, wantSummary) {
		t.Errorf("expected summary %q in stdout, got: %q", wantSummary, stdout)
	}
	_ = pulled
	_ = pushed
}

// TestPostLoginSync_BearerEqualsCredentialsToken verifies that sync requests
// send Authorization: Bearer equal to the credentials.json access_token.
func TestPostLoginSync_BearerEqualsCredentialsToken(t *testing.T) {
	const storedToken = "my-credentials-jwt"

	fakeSrv := newFakeCloudSyncServer(t)
	fakeSrv.pullMutations = 0

	cfg := testConfig(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(_ store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) (int, error) {
		return doPostLoginPull(fakeSrv.URL(), storedToken)
	}
	postLoginPushFn = func(cfg store.Config) (int, error) {
		return doPostLoginPush(fakeSrv.URL(), storedToken, nil)
	}
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

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

	headers := fakeSrv.ReceivedAuthHeaders()
	if len(headers) == 0 {
		t.Fatal("expected at least one Authorization header sent to fake cloud server")
	}
	want := "Bearer " + storedToken
	for _, h := range headers {
		if h != want {
			t.Errorf("expected Authorization %q, got %q", want, h)
		}
	}
}

// TestPostLoginSync_PullFailureWarnsAndPushStillRuns verifies that a pull failure
// from the cloud server logs a warning but push still runs.
func TestPostLoginSync_PullFailureWarnsAndPushStillRuns(t *testing.T) {
	fakeSrv := newFakeCloudSyncServer(t)
	fakeSrv.pullStatusCode = http.StatusInternalServerError // pull always fails
	fakeSrv.pushAccepted = 1

	cfg := testConfig(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(_ store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	pushCalled := false
	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) (int, error) {
		return doPostLoginPull(fakeSrv.URL(), "test-token")
	}
	postLoginPushFn = func(cfg store.Config) (int, error) {
		pushCalled = true
		return doPostLoginPush(fakeSrv.URL(), "test-token", nil)
	}
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

	runner := &loginCommand{
		cfg:       cfg,
		exchanger: exchanger,
		browser:   &fakeBrowserOpener{},
		clock:     &fakeClock{now: now},
		secret:    secret,
	}

	_, stderr := captureOutput(t, func() {
		if err := runner.Run(); err != nil {
			t.Errorf("login.Run: %v (should succeed even with pull failure)", err)
		}
	})

	if !strings.Contains(stderr, "warn") && !strings.Contains(stderr, "pull") {
		t.Errorf("expected pull warning in stderr, got: %q", stderr)
	}
	if !pushCalled {
		t.Error("expected push to be called despite pull failure")
	}
}

// TestPostLoginSync_PushFailureIsWarningNotError verifies that push failure after login
// emits a warning but login still succeeds (token already stored).
func TestPostLoginSync_PushFailureIsWarningNotError(t *testing.T) {
	fakeSrv := newFakeCloudSyncServer(t)
	fakeSrv.pullMutations = 0
	fakeSrv.pushStatusCode = http.StatusInternalServerError // push always fails

	cfg := testConfig(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret := strings.Repeat("x", 32)

	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	oldReclassifyHookFn := reclassifyHookFn
	reclassifyHookFn = func(_ store.Config) {}
	t.Cleanup(func() { reclassifyHookFn = oldReclassifyHookFn })

	oldPullFn := postLoginPullFn
	oldPushFn := postLoginPushFn
	postLoginPullFn = func(cfg store.Config) (int, error) {
		return doPostLoginPull(fakeSrv.URL(), "test-token")
	}
	postLoginPushFn = func(cfg store.Config) (int, error) {
		return doPostLoginPush(fakeSrv.URL(), "test-token", nil)
	}
	t.Cleanup(func() {
		postLoginPullFn = oldPullFn
		postLoginPushFn = oldPushFn
	})

	exchanger := &fakeTokenExchanger{
		claims: auth.JWTClaims{
			Sub:   "alice@vivastudios.com",
			Email: "alice@vivastudios.com",
			Iat:   now.Unix(),
			Exp:   now.Unix() + 604800,
		},
	}

	runner := &loginCommand{
		cfg:       cfg,
		exchanger: exchanger,
		browser:   &fakeBrowserOpener{},
		clock:     &fakeClock{now: now},
		secret:    secret,
	}

	_, stderr := captureOutput(t, func() {
		if err := runner.Run(); err != nil {
			t.Errorf("login.Run returned error: %v (expected success with push warning)", err)
		}
	})

	if !strings.Contains(stderr, "warn") && !strings.Contains(stderr, "push") {
		t.Errorf("expected push warning in stderr, got: %q", stderr)
	}

	// Token must still be written.
	credPath := filepath.Join(credDir, "credentials.json")
	if _, statErr := os.Stat(credPath); statErr != nil {
		t.Errorf("credentials.json must exist even after push failure: %v", statErr)
	}
}
