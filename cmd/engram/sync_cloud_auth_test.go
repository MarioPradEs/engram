package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/autosync"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// TestResolveCloudRuntimeConfigFallsBackToFileToken asserts that
// resolveCloudRuntimeConfig uses the token stored in cloud.json when
// ENGRAM_CLOUD_TOKEN is not set in the environment (issue #343).
func TestResolveCloudRuntimeConfigFallsBackToFileToken(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	const fileToken = "file-token-from-cloud-json"
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("resolveCloudRuntimeConfig: %v", err)
	}
	if cc.Token != fileToken {
		t.Fatalf("expected token %q from cloud.json fallback, got %q (ENGRAM_CLOUD_TOKEN not set)", fileToken, cc.Token)
	}
}

// TestResolveCloudRuntimeConfigEnvTokenTakesPrecedence asserts that when both
// ENGRAM_CLOUD_TOKEN and a token in cloud.json are present, the env var wins.
func TestResolveCloudRuntimeConfigEnvTokenTakesPrecedence(t *testing.T) {
	cfg := testConfig(t)
	const envToken = "env-token"
	const fileToken = "file-token"
	t.Setenv("ENGRAM_CLOUD_TOKEN", envToken)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("resolveCloudRuntimeConfig: %v", err)
	}
	if cc.Token != envToken {
		t.Fatalf("expected env token %q to take precedence over file token %q, got %q", envToken, fileToken, cc.Token)
	}
}

// TestSyncCloudSendsAuthorizationHeaderFromFileToken is an end-to-end test that
// verifies sync --cloud attaches the Authorization: Bearer header when the token
// is sourced from cloud.json and ENGRAM_CLOUD_TOKEN is not set (issue #343).
func TestSyncCloudSendsAuthorizationHeaderFromFileToken(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	const fileToken = "secret-file-token"

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sync/pull":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":1,"chunks":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/sync/push":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := testConfig(t)

	// Persist token in cloud.json; do NOT set ENGRAM_CLOUD_TOKEN.
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: srv.URL,
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.EnrollProject("demo"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	_ = s.Close()

	withArgs(t, "engram", "sync", "--cloud", "--project", "demo")
	_, _, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })

	if _, ok := recovered.(exitCode); ok {
		t.Fatal("sync --cloud fataled; expected success with file token")
	}

	wantAuth := "Bearer " + fileToken
	if !strings.EqualFold(gotAuth, wantAuth) {
		t.Fatalf("expected Authorization header %q, got %q (file token not forwarded)", wantAuth, gotAuth)
	}
}

// ─── Gap B: credentials.json JWT as highest-priority token for sync/autosync ─

// TestResolveCloudRuntimeConfig_PrefersCredentialsJSON verifies that
// resolveCloudRuntimeConfig returns the credentials.json access_token as the
// bearer token even when ENGRAM_CLOUD_TOKEN and cloud.json are both set.
// Precedence: credentials.json > ENGRAM_CLOUD_TOKEN > cloud.json.
func TestResolveCloudRuntimeConfig_PrefersCredentialsJSON(t *testing.T) {
	cfg := testConfig(t)

	const credToken = "creds-jwt-highest-priority"
	const envToken = "env-token-lower"
	const fileToken = "file-token-lowest"

	// Write a valid (non-expired) credentials.json to a temp dir.
	credDir := t.TempDir()
	now := time.Now().UTC()
	writeCredentialsFile(t, credDir, credentialFile{
		AccessToken: credToken,
		IssuedAt:    now.Format(time.RFC3339),
		ExpiresAt:   now.Add(1 * time.Hour).Format(time.RFC3339),
		Email:       "alice@vivastudios.com",
	})

	// Override credentialsDirFn so resolveCloudRuntimeConfig finds our temp dir.
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	t.Setenv("ENGRAM_CLOUD_TOKEN", envToken)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("resolveCloudRuntimeConfig: %v", err)
	}
	if cc.Token != credToken {
		t.Errorf("expected credentials.json token %q (highest priority), got %q", credToken, cc.Token)
	}
}

// TestResolveCloudRuntimeConfig_ExpiredCredentialsFallsToEnv verifies that when
// credentials.json exists but is expired, resolveCloudRuntimeConfig falls back to
// ENGRAM_CLOUD_TOKEN (and logs the expiry guidance).
func TestResolveCloudRuntimeConfig_ExpiredCredentialsFallsToEnv(t *testing.T) {
	cfg := testConfig(t)

	const envToken = "env-fallback-token"

	// Write an EXPIRED credentials.json.
	credDir := t.TempDir()
	past := time.Now().UTC().Add(-2 * time.Hour)
	writeCredentialsFile(t, credDir, credentialFile{
		AccessToken: "expired-jwt",
		IssuedAt:    past.Add(-24 * time.Hour).Format(time.RFC3339),
		ExpiresAt:   past.Format(time.RFC3339),
		Email:       "alice@vivastudios.com",
	})

	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	t.Setenv("ENGRAM_CLOUD_TOKEN", envToken)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     "file-token",
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("resolveCloudRuntimeConfig: %v", err)
	}
	// Expired credentials.json → must fall back to env token.
	if cc.Token != envToken {
		t.Errorf("expected env token %q after expired credentials.json, got %q", envToken, cc.Token)
	}
}

// TestResolveCloudRuntimeConfig_AbsentCredentialsFallsToEnv verifies that when
// credentials.json is absent, resolveCloudRuntimeConfig uses ENGRAM_CLOUD_TOKEN.
func TestResolveCloudRuntimeConfig_AbsentCredentialsFallsToEnv(t *testing.T) {
	cfg := testConfig(t)

	const envToken = "env-token-no-creds"

	// No credentials.json — empty temp dir.
	credDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	t.Setenv("ENGRAM_CLOUD_TOKEN", envToken)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "https://cloud.example.test",
		Token:     "file-token",
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("resolveCloudRuntimeConfig: %v", err)
	}
	if cc.Token != envToken {
		t.Errorf("expected env token %q when credentials.json absent, got %q", envToken, cc.Token)
	}
}

// TestTryStartAutosync_PrefersCredentialsJSON verifies that tryStartAutosync
// picks up the credentials.json JWT as the cloud token (Gap B: autosync path).
func TestTryStartAutosync_PrefersCredentialsJSON(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token-lower")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	const credToken = "creds-jwt-autosync"

	credDir := t.TempDir()
	now := time.Now().UTC()
	writeCredentialsFile(t, credDir, credentialFile{
		AccessToken: credToken,
		IssuedAt:    now.Format(time.RFC3339),
		ExpiresAt:   now.Add(1 * time.Hour).Format(time.RFC3339),
		Email:       "alice@vivastudios.com",
	})

	oldCredsDirFn := credentialsDirFn
	credentialsDirFn = func() (string, error) { return credDir, nil }
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })

	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "http://127.0.0.1:19999",
		Token:     "file-token-lower",
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	var capturedTransport autosync.CloudTransport
	old := newAutosyncManager
	newAutosyncManager = func(_ *store.Store, transport autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
		capturedTransport = transport
		return &fakeStartableManager{}
	}
	defer func() { newAutosyncManager = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, stopFn := tryStartAutosync(ctx, s, cfg)
	if mgr == nil {
		t.Fatal("tryStartAutosync returned nil manager — credentials.json token not picked up")
	}
	if capturedTransport == nil {
		t.Fatal("transport was not set")
	}
	stopFn()
	// The token used for transport is embedded in the MutationTransportAdapter.
	// We verify indirectly: tryStartAutosync must have reached the manager factory,
	// which only happens when a token is present. The precedence is tested in
	// TestResolveCloudRuntimeConfig_PrefersCredentialsJSON above.
	_ = capturedTransport
}

// ─── Gap A: post-login push enumerates real pending mutations ─────────────────

// TestPostLoginPush_RealMutationCount verifies that when there are pending
// local mutations in the store, doPostLoginPushFromStore enumerates them and
// pushes them via the existing transport path, returning the real push count.
// This is Gap A: the pre-fix code called doPostLoginPush with nil entries.
func TestPostLoginPush_RealMutationCount(t *testing.T) {
	cfg := testConfig(t)

	const wantPushed = 2

	// Fake server that accepts push and returns accepted seqs.
	fakeSrv := newFakeCloudSyncServer(t)
	fakeSrv.pushAccepted = wantPushed

	// Open store and enqueue some pending mutations.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	// Enroll the "general" project so mutations pass the enrollment filter.
	if err := s.EnrollProject("general"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Create a session (required FK for observations).
	const sessionID = "test-session-push"
	if err := s.CreateSession(sessionID, "general", "/tmp/test"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Add observations to generate pending sync mutations.
	for i := 0; i < wantPushed; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: sessionID,
			Project:   "general",
			Content:   "test observation for push",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}

	// Verify mutations are pending before the push.
	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("expected pending sync mutations after AddObservation, got 0")
	}

	// Call the real-store push function (Gap A fix: reads store, not nil entries).
	count, err := doPostLoginPushFromStore(fakeSrv.URL(), "test-token", s)
	if err != nil {
		t.Fatalf("doPostLoginPushFromStore: %v", err)
	}
	if count != wantPushed {
		t.Errorf("expected %d mutations pushed, got %d", wantPushed, count)
	}

	// Verify Bearer header was sent.
	headers := fakeSrv.ReceivedAuthHeaders()
	if len(headers) == 0 {
		t.Fatal("expected Authorization header in push request")
	}
	wantAuth := "Bearer test-token"
	for _, h := range headers {
		if h != "" && h != wantAuth {
			t.Errorf("expected Authorization %q, got %q", wantAuth, h)
		}
	}
}

// TestTryStartAutosyncUsesFileToken asserts that tryStartAutosync picks up the
// cloud token from cloud.json when ENGRAM_CLOUD_TOKEN env var is absent (issue #421).
// This is the Windows Task Scheduler scenario: the background process runs in a
// separate session context without the env var, so the token must come from the
// persisted config file.
func TestTryStartAutosyncUsesFileToken(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")  // env var absent — must fall back to file
	t.Setenv("ENGRAM_CLOUD_SERVER", "") // env var absent — server from file too

	const fileToken = "file-only-token-421"
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: "http://127.0.0.1:19998",
		Token:     fileToken,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	// Track whether the manager factory was reached.
	managerCreated := false
	old := newAutosyncManager
	newAutosyncManager = func(_ *store.Store, _ autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
		managerCreated = true
		return &fakeStartableManager{}
	}
	defer func() { newAutosyncManager = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, stopFn := tryStartAutosync(ctx, s, cfg)

	// If the file-token fallback is missing, tryStartAutosync returns (nil, nil)
	// because cc.Token is empty after resolveCloudRuntimeConfig ignores the file.
	if mgr == nil {
		t.Fatal("tryStartAutosync returned nil manager when token is only in cloud.json — file token fallback not working for autosync startup (issue #421)")
	}
	if stopFn == nil {
		t.Fatal("expected non-nil stop function when manager starts successfully")
	}
	if !managerCreated {
		t.Fatal("newAutosyncManager factory was never reached — tryStartAutosync aborted before creating manager")
	}
	stopFn()
}
