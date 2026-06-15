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
var newTriageServer = func(s *store.Store, port int) triageStarter {
	return triage.New(s, port)
}

// cmdTriage is the entry point for `engram triage`. It:
//  1. Opens the store.
//  2. Resolves the triage port (ENGRAM_TRIAGE_PORT or 7438).
//  3. Starts the local triage HTTP server on 127.0.0.1:<port>.
//  4. Auto-opens the default browser to the local URL (skippable via
//     ENGRAM_TRIAGE_NO_BROWSER=1 or headless environments).
//  5. Shuts down gracefully on SIGINT/SIGTERM.
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
