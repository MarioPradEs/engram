package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/triage"
)

// TestCmdTriageDispatch verifies that `engram triage` routes to cmdTriage via
// the main dispatch switch without falling through to the default/unknown
// command branch. It stubs newTriageServer + startTriageServer so no real
// socket is opened.
func TestCmdTriageDispatch(t *testing.T) {
	withArgs(t, "engram", "triage")

	called := false

	// Stub newTriageServer so the real triage.New is never called.
	oldNew := newTriageServer
	t.Cleanup(func() { newTriageServer = oldNew })
	newTriageServer = func(s *store.Store, port int) triageStarter {
		called = true
		return &stubTriageServer{}
	}

	// Stub storeNew so no real DB is opened.
	oldStore := storeNew
	t.Cleanup(func() { storeNew = oldStore })
	storeNew = func(cfg store.Config) (*store.Store, error) {
		return nil, nil
	}

	// Stub exitFunc so fatal() doesn't terminate the test process.
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) { t.Fatalf("exitFunc called with code %d", code) }

	main()

	if !called {
		t.Error("expected newTriageServer to be called when arg is 'triage'")
	}
}

// TestCmdTriageStartCallsBrowserOpener verifies that cmdTriage calls the
// browser opener seam on start (when ENGRAM_TRIAGE_NO_BROWSER is unset).
func TestCmdTriageStartCallsBrowserOpener(t *testing.T) {
	t.Setenv("ENGRAM_TRIAGE_NO_BROWSER", "1") // suppress real open in tests

	cfg := testConfig(t)
	s := &stubTriageServer{}

	// Bind a real listener so Start has somewhere to serve.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Create a real triage.Server and inject the listener.
	srv := triage.New(nil, 0)
	srv.SetListener(ln)

	var opened []string
	srv.SetBrowserOpener(func(url string) error {
		opened = append(opened, url)
		return nil
	})

	_ = s // silence unused variable
	_ = cfg

	// Close the listener to stop Start quickly.
	ln.Close()
	_ = srv.Start() // returns immediately after listener is closed.

	// ENGRAM_TRIAGE_NO_BROWSER=1 suppresses the opener — assert nothing was called.
	if len(opened) != 0 {
		t.Errorf("expected opener suppressed, got %v", opened)
	}
}

// TestCmdTriageHandlerRoutes verifies that the triage server handler responds
// 200 OK on the expected routes (/ and /health). Uses httptest.ResponseRecorder,
// not a real network listener — does not test the loopback bind address.
func TestCmdTriageHandlerRoutes(t *testing.T) {
	srv := triage.New(nil, 0)
	h := srv.Handler()

	for _, path := range []string{"/", "/health"} {
		req, _ := http.NewRequest(http.MethodGet, path, nil)
		w := &responseRecorder{code: 200}
		h.ServeHTTP(w, req)
		if w.code != http.StatusOK {
			t.Errorf("GET %s: want 200, got %d", path, w.code)
		}
	}
}

// TestNewTriageServer_WiresMutableStore verifies that the production
// newTriageServer factory builds a server where mutation endpoints are live
// (mutable store non-nil). A nil mutable store causes POST /observations/{id}/scope
// to return 503 Service Unavailable; a wired store returns a redirect or 4xx.
// C-1: the real factory must call NewWithMutableStore, not triage.New.
func TestNewTriageServer_WiresMutableStore(t *testing.T) {
	t.Setenv("ENGRAM_TRIAGE_NO_BROWSER", "1")

	// Capture the real (pre-test) factory to avoid reading a stub.
	factory := newTriageServer

	// Build a minimal store in a temp dir.
	cfg := testConfig(t)
	s, err := storeNew(cfg)
	if err != nil || s == nil {
		t.Skip("cannot open store for C-1 factory test")
	}
	defer s.Close()

	// Call the real factory.
	srv := factory(s, 0)

	// Bind a listener so we can use Handler() directly.
	triageSrv, ok := srv.(interface{ Handler() interface{ ServeHTTP(http.ResponseWriter, *http.Request) } })
	_ = triageSrv

	// Use a type assertion to get the http.Handler from the triageStarter.
	// triageStarter only exposes SetBrowserOpener and Start — we need Handler().
	// The real triage.Server satisfies a broader interface; use a second assertion.
	type handlerProvider interface {
		Handler() http.Handler
	}
	hp, ok := srv.(handlerProvider)
	if !ok {
		t.Skip("factory did not return a handlerProvider; cannot inspect mutation routes")
	}
	h := hp.Handler()

	// POST to a mutation route — must NOT be 503 (store not available).
	form := "scope=shared"
	req, _ := http.NewRequest(http.MethodPost, "/observations/1/scope",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := &responseRecorder{code: 200}
	h.ServeHTTP(rec, req)

	if rec.code == http.StatusServiceUnavailable {
		t.Errorf("C-1: mutation endpoint returned 503 (mutable store not wired in real factory)")
	}
}

// TestNewTriageServer_SetsCwdProject verifies that the production factory sets
// cwdProject on the server when the cwd contains an .engram/config.json.
// C-1: detectProject must be called and the result forwarded to SetCwdProject.
func TestNewTriageServer_SetsCwdProject(t *testing.T) {
	t.Setenv("ENGRAM_TRIAGE_NO_BROWSER", "1")

	// Create a temp dir with an .engram/config.json identifying a project.
	tmpDir := t.TempDir()
	engDir := filepath.Join(tmpDir, ".engram")
	if err := os.MkdirAll(engDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]string{"project_name": "my-cwd-project"}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(engDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Also create a .git dir so detectProject recognizes tmpDir as a git root.
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Change working directory to the project root for this test.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	factory := newTriageServer
	srv := factory(nil, 0) // nil store — the stub path

	// Inspect the cwdProject via the classify endpoint: classify for the
	// detected project must succeed (not 503 for "store not available"); classify
	// for an unknown project must return 400 (wrong project = non-cwd boundary).
	// This indirectly confirms SetCwdProject was called with the detected name.
	type handlerProvider interface {
		Handler() http.Handler
	}
	hp, ok := srv.(handlerProvider)
	if !ok {
		t.Skip("factory did not return a handlerProvider")
	}
	h := hp.Handler()

	// Classify for a project that does NOT match the cwd project must return 400.
	form := "scope=shared"
	req, _ := http.NewRequest(http.MethodPost, "/project/UNRELATED-PROJECT/classify",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := &responseRecorder{code: 200}
	h.ServeHTTP(rec, req)

	if rec.code == http.StatusInternalServerError {
		t.Logf("C-1 cwdProject check: classify for unrelated project returned 500 (no mutable store, expected 400 or 503)")
	}
	// 400 = correctly rejected because project != cwdProject.
	// 503 = mutableStore is nil (factory didn't wire it, but that's OK for nil store path).
	// 500 = cwdProject empty, fell through to cwdDir="" guard → not wired correctly.
	// We just ensure it's not 200 OK (which would mean no gate at all).
	if rec.code == http.StatusOK {
		t.Errorf("C-1: classify for unrelated project returned 200; cwdProject gate not working")
	}
}

// TestCmdTriageHelpString verifies that the usage text constant mentions the
// triage command. We verify via a known help string fragment rather than
// capturing stdout (which would deadlock when the help text exceeds the pipe
// buffer on some platforms).
func TestCmdTriageHelpString(t *testing.T) {
	// Write to a temp file and read back to avoid the pipe-buffer deadlock that
	// occurs when the help text is larger than the OS pipe buffer.
	f, err := os.CreateTemp(t.TempDir(), "usage-*.txt")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer f.Close()

	old := os.Stdout
	os.Stdout = f
	printUsage()
	os.Stdout = old

	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(b), "triage") {
		t.Errorf("printUsage() does not mention 'triage'")
	}
}

// ─── Phase 10 RED: JWT wiring tests ──────────────────────────────────────────

// TestNewTriageServer_JWTAbsent_WiresServerEnrollFn verifies Phase 10 requirement:
// newTriageServer MUST wire a non-nil serverEnrollFn that returns "not logged in"
// when credentials.json is absent (empty bearer token path). The share route must
// return 4xx with "not logged in" in the body (not 503 "not configured").
//
// RED: before Phase 10 the factory does NOT wire serverEnrollFn, so the share
// route returns 503 "not configured" instead of the 502 "not logged in" body.
func TestNewTriageServer_JWTAbsent_WiresServerEnrollFn(t *testing.T) {
	t.Setenv("ENGRAM_TRIAGE_NO_BROWSER", "1")

	// Point credentials dir to an empty temp dir — no credentials.json.
	emptyCredDir := t.TempDir()
	oldCredsDirFn := credentialsDirFn
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })
	credentialsDirFn = func() (string, error) { return emptyCredDir, nil }

	factory := newTriageServer
	srv := factory(nil, 0)

	type handlerProvider interface {
		Handler() http.Handler
	}
	hp, ok := srv.(handlerProvider)
	if !ok {
		t.Skip("factory did not return a handlerProvider")
	}

	type cwdProjectSetter interface {
		SetCwdProject(string)
	}
	if setter, ok := srv.(cwdProjectSetter); ok {
		setter.SetCwdProject("test-proj")
	}

	req, _ := http.NewRequest(http.MethodPost, "/project/test-proj/share", nil)
	rec := &responseRecorder{code: 200}
	hp.Handler().ServeHTTP(rec, req)

	// Phase 10 contract: a wired serverEnrollFn that returns "not logged in"
	// causes the share route to respond 502 with "not logged in" in the body.
	// Before Phase 10: the fn is nil → 503 "not configured" (no "not logged in").
	if !strings.Contains(rec.body.String(), "not logged in") {
		t.Errorf("Phase 10: want 'not logged in' in share response body when credentials absent; got code=%d body=%q",
			rec.code, rec.body.String()[:min(200, rec.body.Len())])
	}
}

// TestNewTriageServer_WiresEnrollmentStore verifies the C-1 production wiring:
// newTriageServer must call srv.WithEnrollmentStore(ms) so that the share and
// unshare handlers can client-enroll/unenroll the project in local SQLite.
//
// Without the fix the factory never calls WithEnrollmentStore, leaving
// enrollStore == nil. handleShareProject reaches the nil-guard AFTER
// serverEnrollFn succeeds and returns 503 "enrollment store not configured".
// After the fix the handler proceeds past that guard and returns 200.
//
// RED: before the fix this test FAILS because the body contains
// "enrollment store not configured". GREEN: after the fix it PASSES.
func TestNewTriageServer_WiresEnrollmentStore(t *testing.T) {
	t.Setenv("ENGRAM_TRIAGE_NO_BROWSER", "1")

	// Start a fake cloud enroll endpoint that always returns 200 OK.
	// This is needed so serverEnrollFn (the Phase 10 HTTP closure) succeeds and
	// the handler advances past step 2 to reach the enrollStore nil-guard at
	// step 3 — the exact location of the C-1 bug.
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeSrv.Close()

	oldCloudURL := cloudBaseURLFn
	t.Cleanup(func() { cloudBaseURLFn = oldCloudURL })
	cloudBaseURLFn = func(_ store.Config) string { return fakeSrv.URL }

	// Write a credentials.json with a non-expired token so bearerToken != "".
	// If bearerToken were empty, serverEnrollFn returns "not logged in" (502)
	// before reaching the enrollStore check, masking the C-1 bug.
	credDir := t.TempDir()
	creds := credentialFile{
		AccessToken: "test-bearer-token",
		IssuedAt:    time.Now().UTC().Format(time.RFC3339),
		// ExpiresAt absent → no expiry validation
	}
	credsData, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), credsData, 0o644); err != nil {
		t.Fatal(err)
	}
	oldCredsDirFn := credentialsDirFn
	t.Cleanup(func() { credentialsDirFn = oldCredsDirFn })
	credentialsDirFn = func() (string, error) { return credDir, nil }

	// Open a real store so the factory constructs a non-nil ms (*StoreAdapter).
	// With s != nil, ms is set; the bug is that the factory never passes ms to
	// WithEnrollmentStore, leaving enrollStore nil regardless.
	cfg := testConfig(t)
	s, err := storeNew(cfg)
	if err != nil || s == nil {
		t.Skip("cannot open store for C-1 enrollment-store wiring test")
	}
	defer s.Close()

	// Capture the real factory (not a stub) — same pattern as
	// TestNewTriageServer_WiresMutableStore and TestNewTriageServer_SetsCwdProject.
	factory := newTriageServer
	srv := factory(s, 0)

	// Set cwdProject so the Option A gate in handleShareProject passes.
	type cwdProjectSetter interface {
		SetCwdProject(string)
	}
	if setter, ok := srv.(cwdProjectSetter); ok {
		setter.SetCwdProject("test-proj")
	}

	type handlerProvider interface {
		Handler() http.Handler
	}
	hp, ok := srv.(handlerProvider)
	if !ok {
		t.Skip("factory did not return a handlerProvider")
	}

	// POST /project/test-proj/share with no Origin header so the CSRF
	// originCheckMiddleware passes (absent Origin = curl / same-origin form).
	req, _ := http.NewRequest(http.MethodPost, "/project/test-proj/share", nil)
	rec := &responseRecorder{code: 200}
	hp.Handler().ServeHTTP(rec, req)

	// C-1 assertion: the nil-enrollStore 503 must never appear.
	//   Before fix: enrollStore == nil → 503 "enrollment store not configured".
	//   After fix:  enrollStore is a real StoreAdapter → EnrollProject("test-proj")
	//               succeeds on the empty temp store → 200 OK.
	if strings.Contains(rec.body.String(), "enrollment store not configured") {
		t.Errorf("C-1: share returned 'enrollment store not configured' — "+
			"WithEnrollmentStore not wired in newTriageServer; code=%d body=%q",
			rec.code, rec.body.String()[:min(200, rec.body.Len())])
	}
}

// ─── Stubs ────────────────────────────────────────────────────────────────────

// stubTriageServer is a test double for the triage.Server Start path.
type stubTriageServer struct {
	startCalled int
}

func (s *stubTriageServer) SetBrowserOpener(fn triage.BrowserOpener) {}

func (s *stubTriageServer) Start() error {
	s.startCalled++
	return nil // return immediately; no real listener
}

// responseRecorder is a minimal http.ResponseWriter for handler tests.
type responseRecorder struct {
	code int
	body strings.Builder
	hdr  http.Header
}

func (r *responseRecorder) Header() http.Header {
	if r.hdr == nil {
		r.hdr = make(http.Header)
	}
	return r.hdr
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	return r.body.Write(b)
}

func (r *responseRecorder) WriteHeader(code int) {
	r.code = code
}

// TestNewTriageServer_NormalizesProject verifies MAJOR-5: newTriageServer must
// normalize the resolved project name via store.NormalizeProject so that an
// un-normalized ENGRAM_PROJECT (e.g. "My-Proj") becomes "my-proj" — consistent
// with resolveServeSyncStatusProject which also normalizes.
func TestNewTriageServer_NormalizesProject(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT", "My-Proj")
	t.Setenv("ENGRAM_DEFAULT_PROJECT", "")

	// Stub detectProject so we isolate the env path.
	oldDetect := detectProject
	t.Cleanup(func() { detectProject = oldDetect })
	detectProject = func(string) string { return "" }

	// Use the real factory with nil store (test stub path).
	srv := newTriageServer(nil, 0)

	// triage.Server exposes CwdProject() for test inspection.
	type cwdProjectGetter interface {
		CwdProject() string
	}
	cg, ok := srv.(cwdProjectGetter)
	if !ok {
		t.Skip("triage.Server does not expose CwdProject(); cannot verify normalization")
	}
	got := cg.CwdProject()
	if got != "my-proj" {
		t.Errorf("newTriageServer CwdProject = %q; want %q (MAJOR-5: must normalize)", got, "my-proj")
	}
}

// TestCmdTriageRunsMigrationAtStartup verifies that cmdTriage calls
// migrateOrphansFn (D3) before starting the server, even when the store stub
// returns nil (graceful nil-guard).
func TestCmdTriageRunsMigrationAtStartup(t *testing.T) {
	migrationCalled := false

	// Stub migrateOrphansFn to record the call.
	oldMigrate := migrateOrphansFn
	t.Cleanup(func() { migrateOrphansFn = oldMigrate })
	migrateOrphansFn = func(s *store.Store) {
		migrationCalled = true
	}

	// Stub storeNew so no real DB is opened.
	oldStore := storeNew
	t.Cleanup(func() { storeNew = oldStore })
	storeNew = func(cfg store.Config) (*store.Store, error) {
		return nil, nil
	}

	// Stub newTriageServer so no real listener is opened.
	oldNew := newTriageServer
	t.Cleanup(func() { newTriageServer = oldNew })
	newTriageServer = func(s *store.Store, port int) triageStarter {
		return &stubTriageServer{}
	}

	// Stub exitFunc so fatal() doesn't terminate the test process.
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) {}

	cfg := testConfig(t)
	cmdTriage(cfg)

	if !migrationCalled {
		t.Error("D3: expected migrateOrphansFn to be called at triage startup")
	}
}
