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
	"io/fs"
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
	store          *store.Store
	triageStore    TriageStore        // narrow interface; set by NewWithStore or derived from store
	mutableStore   MutableTriageStore // non-nil when mutation endpoints are active (WU-5)
	port           int
	trustedOrigins map[string]struct{} // CSRF: allowed Origin values, derived from port at construction
	mux            *http.ServeMux
	listen         func(network, address string) (net.Listener, error)
	ln             net.Listener // pre-injected listener (for tests); overrides listen
	browser        BrowserOpener
	cwdDir         string // filesystem path of the project directory at launch (for ResolveDefaultScope)
	cwdProject     string // project name matching cwdDir (Option A: cwd-only default badge)
}

// StoreAdapter wraps *store.Store to satisfy MutableTriageStore.
// It exposes only the narrow subset of store methods used by triage handlers,
// keeping the dependency surface minimal and the handlers independently testable.
type StoreAdapter struct {
	s *store.Store
}

// NewStoreAdapter returns a MutableTriageStore backed by a real *store.Store.
// Use this in production wiring (cmdTriage) instead of passing *store.Store directly.
func NewStoreAdapter(s *store.Store) MutableTriageStore {
	return &StoreAdapter{s: s}
}

func (a *StoreAdapter) ListProjectsWithStats() ([]store.ProjectStats, error) {
	return a.s.ListProjectsWithStats()
}

func (a *StoreAdapter) RecentObservations(project, scope string, limit int) ([]store.Observation, error) {
	return a.s.RecentObservations(project, scope, limit)
}

// UpdateObservationScope proxies to store.UpdateObservation, setting only the
// scope field. This is the canonical mutation path for triage handlers — it
// does NOT re-implement any store logic (design decision #4).
func (a *StoreAdapter) UpdateObservationScope(id int64, internalScope string) error {
	_, err := a.s.UpdateObservation(id, store.UpdateObservationParams{
		Scope: &internalScope,
	})
	return err
}

// New creates a triage Server backed by a real *store.Store.
// When port is 0 and no listener is pre-injected, Start will attempt to bind to DefaultPort.
// s may be nil in unit tests that only exercise the HTTP handler layer.
func New(s *store.Store, port int) *Server {
	if port == 0 {
		port = resolvePort()
	}
	var ts TriageStore
	if s != nil {
		ts = s
	}
	srv := &Server{
		store:          s,
		triageStore:    ts,
		port:           port,
		trustedOrigins: buildTrustedOrigins(port),
		listen:         net.Listen,
	}
	srv.mux = http.NewServeMux()
	srv.routes()
	return srv
}

// NewWithStore creates a triage Server with a narrow TriageStore interface.
// This constructor is used in tests and by cmdTriage when injecting a stub store.
// s may be nil (real store not needed when ts provides the data).
// cwdDir is the project root directory for resolving default_scope (Option A).
func NewWithStore(s *store.Store, ts TriageStore, port int, cwdDir string) *Server {
	if port == 0 {
		port = resolvePort()
	}
	srv := &Server{
		store:          s,
		triageStore:    ts,
		port:           port,
		trustedOrigins: buildTrustedOrigins(port),
		listen:         net.Listen,
		cwdDir:         cwdDir,
	}
	srv.mux = http.NewServeMux()
	srv.routes()
	return srv
}

// NewWithMutableStore creates a triage Server with a MutableTriageStore that
// supports both read and mutation endpoints (WU-5). The mutableStore satisfies
// TriageStore as well, so it is used for all handler operations.
// cwdDir is the project root directory for resolving default_scope and writing
// config.json (Option A: only the cwd project can have its default set).
func NewWithMutableStore(s *store.Store, ms MutableTriageStore, port int, cwdDir string) *Server {
	if port == 0 {
		port = resolvePort()
	}
	srv := &Server{
		store:          s,
		triageStore:    ms,
		mutableStore:   ms,
		port:           port,
		trustedOrigins: buildTrustedOrigins(port),
		listen:         net.Listen,
		cwdDir:         cwdDir,
	}
	srv.mux = http.NewServeMux()
	srv.routes()
	return srv
}

// SetCwdProject sets the project name that corresponds to the launch directory.
// Only this project shows a resolved default-scope badge on the index page.
// This is called by cmdTriage after project detection (Option A boundary).
func (s *Server) SetCwdProject(project string) {
	s.cwdProject = project
}

// buildTrustedOrigins returns the set of Origin header values that the CSRF
// middleware accepts for a server bound to port p. Both the 127.0.0.1 and
// localhost forms are included because browsers may send either.
func buildTrustedOrigins(p int) map[string]struct{} {
	return map[string]struct{}{
		fmt.Sprintf("http://127.0.0.1:%d", p): {},
		fmt.Sprintf("http://localhost:%d", p):  {},
	}
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

// originCheckMiddleware returns an http.HandlerFunc that rejects cross-origin
// requests to state-changing endpoints. If the request carries an Origin
// header that is NOT in s.trustedOrigins (derived from the server's runtime
// port at construction), it responds with 403 Forbidden.
//
// Rationale: the loopback triage server has no authentication. An attacker's
// web page could make cross-origin POST requests (CSRF) if there were no
// check. Same-origin forms and curl do not send an Origin header, so they
// are unaffected. The allowed set is built per-server from the actual port so
// that servers running on any port other than 7438 are not falsely blocked.
func (s *Server) originCheckMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if _, ok := s.trustedOrigins[origin]; !ok {
				http.Error(w,
					"forbidden: cross-origin requests are not allowed on the loopback triage server",
					http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

// routes registers all triage HTTP handlers on the server's mux.
func (s *Server) routes() {
	// Health check — always available for smoke tests and monitoring.
	s.mux.HandleFunc("GET /health", s.handleHealth)

	// Index / landing page: grouped-by-project read view (WU-4).
	s.mux.HandleFunc("GET /", s.handleIndex)

	// Per-project observation list (WU-4).
	s.mux.HandleFunc("GET /project/{name}", s.handleProject)

	// WU-5 mutation endpoints — wrapped with origin-check CSRF middleware.
	// Per-item scope toggle: sets the scope of one observation directly.
	s.mux.HandleFunc("POST /observations/{id}/scope", s.originCheckMiddleware(s.handleToggleScope))
	// Bulk share-all: with confirm=1 moves the project backlog to shared.
	s.mux.HandleFunc("POST /project/{name}/share-all", s.originCheckMiddleware(s.handleShareAll))
	// Classify: sets the project default_scope in config.json (cwd project only).
	s.mux.HandleFunc("POST /project/{name}/classify", s.originCheckMiddleware(s.handleClassify))

	// Static assets: pico.min.css, htmx.min.js, triage.css.
	// Served under /triage/static/ to avoid collisions with any future API prefix.
	// Sub-FS into the "static" directory embedded in StaticFS so that
	// http.FileServer sees the files at the root (no "static/" prefix in paths).
	staticSub, err := fs.Sub(StaticFS, "static")
	if err != nil {
		panic("triage: failed to sub StaticFS: " + err.Error())
	}
	s.mux.Handle("GET /triage/static/", http.StripPrefix("/triage/static/", http.FileServer(http.FS(staticSub))))
}

// handleHealth returns 200 OK with a simple JSON status body.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"status":"ok","service":"triage"}`)
}

