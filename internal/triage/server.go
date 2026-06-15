// Package triage provides the local triage web UI server for reviewing and
// classifying observation scope per project.
//
// The triage server is a dedicated, loopback-only HTTP server bound to
// 127.0.0.1:7438 (default). It uses its own http.ServeMux — completely
// independent of the main Engram API server (internal/server) — so that
// triage routes, auth model, and dependencies stay isolated from the
// full daemon API surface.
//
// Trust model: loopback-only, no authentication (same as engram serve :7437).
// The server is intended to be started by `engram triage` from the CLI and
// accessed via a browser opened automatically on startup.
//
// REQ-01..06, A-01..A-03/A-07.
package triage

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/Gentleman-Programming/engram/internal/store"
)

const (
	// DefaultPort is the default local port for the triage UI (7438 = "TRIG" vibes).
	DefaultPort = 7438

	// EnvPort is the environment variable to override the triage port.
	EnvPort = "ENGRAM_TRIAGE_PORT"

	// EnvNoBrowser disables auto-open when set to "1".
	EnvNoBrowser = "ENGRAM_TRIAGE_NO_BROWSER"
)

// BrowserOpener opens a URL in the default system browser.
// It matches the signature of github.com/cli/browser.OpenURL so the real
// opener can be injected in production and a no-op stub in tests.
type BrowserOpener func(url string) error

// Server is the local triage HTTP server. It owns its own ServeMux and is
// independent of internal/server.Server (the main Engram API daemon).
type Server struct {
	store   *store.Store
	port    int
	mux     *http.ServeMux
	listen  func(network, address string) (net.Listener, error)
	ln      net.Listener // pre-injected listener (for tests); overrides listen
	browser BrowserOpener
}

// New creates a triage Server. When port is 0 and no listener is pre-injected,
// Start will attempt to bind to DefaultPort.
// s may be nil in unit tests that only exercise the HTTP handler layer.
func New(s *store.Store, port int) *Server {
	if port == 0 {
		port = resolvePort()
	}
	srv := &Server{
		store:  s,
		port:   port,
		listen: net.Listen,
	}
	srv.mux = http.NewServeMux()
	srv.routes()
	return srv
}

// resolvePort reads ENGRAM_TRIAGE_PORT or falls back to DefaultPort.
func resolvePort() int {
	if p := os.Getenv(EnvPort); p != "" {
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return DefaultPort
}

// SetBrowserOpener replaces the browser-open function. Providing a no-op
// function suppresses auto-open without setting the environment variable.
// This seam exists primarily for unit tests.
func (s *Server) SetBrowserOpener(fn BrowserOpener) {
	s.browser = fn
}

// SetListener injects a pre-bound net.Listener. When set, Start skips its
// own net.Listen call and serves directly on the injected listener.
// This seam exists primarily for unit tests that need an OS-assigned port.
func (s *Server) SetListener(ln net.Listener) {
	s.ln = ln
}

// Handler returns the underlying http.Handler for use with httptest or
// net/http.Serve in tests.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Start binds the server to its configured address (or uses a pre-injected
// listener), invokes the browser opener (unless suppressed), and begins
// serving HTTP. It blocks until the listener is closed or an error occurs.
//
// Auto-open is skipped when ENGRAM_TRIAGE_NO_BROWSER=1.
// Browser-open errors are logged but non-fatal — the server continues.
func (s *Server) Start() error {
	ln := s.ln
	if ln == nil {
		addr := fmt.Sprintf("127.0.0.1:%d", s.port)
		var err error
		ln, err = s.listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("triage server: listen %s: %w (use %s to change port)", addr, err, EnvPort)
		}
	}

	// Resolve the actual address now that the listener is bound.
	actualAddr := ln.Addr().String()
	url := "http://" + actualAddr
	log.Printf("[triage] local triage UI listening on %s", url)

	// Auto-open browser (non-fatal, skippable via env).
	if os.Getenv(EnvNoBrowser) != "1" {
		opener := s.browser
		if opener != nil {
			if err := opener(url); err != nil {
				log.Printf("[triage] browser open failed (non-fatal): %v", err)
			}
		}
	}

	return http.Serve(ln, s.mux) //nolint:gosec // intentionally loopback-only
}

// routes registers all triage HTTP handlers on the server's mux.
// WU-3 registers stub routes only; real view handlers are added in WU-4.
func (s *Server) routes() {
	// Health check — always available for smoke tests and monitoring.
	s.mux.HandleFunc("GET /health", s.handleHealth)

	// Index / landing page stub — returns placeholder until WU-4 adds the
	// real templ-rendered projects page.
	s.mux.HandleFunc("GET /", s.handleIndex)
}

// handleHealth returns 200 OK with a simple JSON status body.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"status":"ok","service":"triage"}`)
}

// handleIndex is the stub landing-page handler for WU-3.
// WU-4 replaces this with the real templ-rendered projects list.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `<!DOCTYPE html>
<html>
<head><title>Engram Triage</title></head>
<body>
<h1>Engram Triage</h1>
<p>triage UI is loading — real view coming in WU-4.</p>
</body>
</html>`)
}
