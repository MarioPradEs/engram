package main

import (
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/cli/browser"

	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/triage"
)

// triageStarter is the narrow interface that cmdTriage needs from a triage.Server.
// Declaring it here keeps cmd/engram/triage_test.go able to stub it without
// touching internal/triage.
type triageStarter interface {
	SetBrowserOpener(fn triage.BrowserOpener)
	Start() error
}

// newTriageServer is the production factory for a triage.Server. It is a
// package-level var so tests can replace it with a stub to avoid real listeners.
//
// C-1: the real factory wires a MutableTriageStore so all mutation endpoints
// are live, and calls SetCwdProject so the classify boundary (Option A) is
// enforced correctly. A nil adapter is passed when s is nil (test stub path).
var newTriageServer = func(s *store.Store, port int) triageStarter {
	// Resolve cwd and the project name that matches it (D1 precedence).
	cwdDir, _ := os.Getwd()
	var detected string
	if cwdDir != "" {
		detected = detectProject(cwdDir)
	}
	cwdProject := resolveProjectName(
		os.Getenv("ENGRAM_PROJECT"),
		detected,
		os.Getenv("ENGRAM_DEFAULT_PROJECT"),
	)
	// MAJOR-5: normalize the resolved name so newTriageServer is consistent with
	// resolveServeSyncStatusProject and cmdMCP (all call sites must normalize).
	if cwdProject != "" {
		if normalized, _ := store.NormalizeProject(cwdProject); normalized != "" {
			cwdProject = normalized
		}
	}

	// Build a nil-safe mutable store adapter (guard for test stubs where s==nil).
	var ms triage.MutableTriageStore
	if s != nil {
		ms = triage.NewStoreAdapter(s)
	}

	srv := triage.NewWithMutableStore(s, ms, port, cwdDir)
	srv.SetCwdProject(cwdProject)
	return srv
}

// migrateOrphansFn is the injectable seam for MigrateEmptyProjectToPersonal.
// Production code uses the real store method.  Tests can replace this var to
// assert that the migration is triggered at triage startup without opening a
// real store.
var migrateOrphansFn = func(s *store.Store) {
	if s == nil {
		return
	}
	if _, err := s.MigrateEmptyProjectToPersonal("personal"); err != nil {
		log.Printf("[triage] orphan migration warning (non-fatal): %v", err)
	}
}

// cmdTriage is the entry point for `engram triage`. It:
//  1. Opens the store.
//  2. Resolves the triage port (ENGRAM_TRIAGE_PORT or 7438).
//  3. Runs the one-time orphan migration (best-effort, non-fatal).
//  4. Starts the local triage HTTP server on 127.0.0.1:<port>.
//  5. Auto-opens the default browser to the local URL (skippable via
//     ENGRAM_TRIAGE_NO_BROWSER=1 or headless environments).
//  6. Shuts down gracefully on SIGINT/SIGTERM.
//
// Trust model: loopback-only, no authentication — same as `engram serve`.
func cmdTriage(cfg store.Config) {
	port := triage.DefaultPort
	if p := os.Getenv(triage.EnvPort); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			port = n
		}
	}

	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	if s != nil {
		// Guard is load-bearing: the injectable storeNew stub used in
		// TestCmdTriageDispatch returns (nil, nil), so s can be nil here.
		defer s.Close()
	}

	// D3: run orphan migration best-effort before starting the server so any
	// pre-existing project='' rows are reassigned to "personal".
	migrateOrphansFn(s)

	srv := newTriageServer(s, port)

	// Wire the real browser opener unless ENGRAM_TRIAGE_NO_BROWSER=1 suppresses it.
	// The no-browser guard is also enforced inside triage.Server.Start, but we set
	// the opener here so the seam is visible and testable from cmd/engram.
	if os.Getenv(triage.EnvNoBrowser) != "1" {
		srv.SetBrowserOpener(browser.OpenURL)
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[triage] shutting down...")
		exitFunc(0)
	}()

	if err := srv.Start(); err != nil {
		fatal(err)
	}
}
