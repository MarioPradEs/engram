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
	enrollStore    EnrollmentStore    // Phase 5: client-side enroll/unenroll for share actions
	serverEnrollFn func(project, bearerToken string) error // Phase 5: server-side enroll closure (D9)
	bearerToken    string // Phase 7: JWT read from credentials.json at startup; passed to serverEnrollFn
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

// ObservationsByTag proxies to store.ObservationsByTag (PR#2 / E2a).
// facet must be in {"juego","tipo"} — allow-list enforced by the store method.
func (a *StoreAdapter) ObservationsByTag(project, facet, value string, limit int) ([]store.Observation, error) {
	return a.s.ObservationsByTag(project, facet, value, limit)
}

// DistinctTagValues proxies to store.DistinctTagValues (PR#2 / E2a).
// facet must be in {"juego","tipo"} — allow-list enforced by the store method.
func (a *StoreAdapter) DistinctTagValues(project, facet string) ([]string, error) {
	return a.s.DistinctTagValues(project, facet)
}

// EnrollProject proxies to store.EnrollProject, satisfying EnrollmentStore.
// Idempotent — re-enrolling an already-enrolled project is a no-op.
func (a *StoreAdapter) EnrollProject(project string) error {
	return a.s.EnrollProject(project)
}

// UnenrollProject proxies to store.UnenrollProject, satisfying EnrollmentStore.
// Idempotent — unenrolling a non-enrolled project is a no-op.
func (a *StoreAdapter) UnenrollProject(project string) error {
	return a.s.UnenrollProject(project)
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

// CwdProject returns the project name set via SetCwdProject.
// Exposed for testing so callers can assert the resolved/normalized value.
func (s *Server) CwdProject() string {
	return s.cwdProject
}

// WithEnrollmentStore injects the EnrollmentStore used by handleShareProject and
// handleUnshareProject to client-enroll or unenroll a project in the local SQLite store.
// Must be called before Start. In production, pass a *triage.StoreAdapter or
// *store.Store directly (both satisfy the interface). In tests, pass a fake.
func (s *Server) WithEnrollmentStore(es EnrollmentStore) {
	s.enrollStore = es
}

// WithServerEnrollFn injects the closure used by handleShareProject to call the
// cloud self-service enroll endpoint. The closure receives the project name and
// the caller's bearer token (JWT). It should return an error if the server is
// unreachable or the token is invalid/empty. A nil fn means share will always
// fail (safe default); cmdTriage wires the real HTTP call here.
func (s *Server) WithServerEnrollFn(fn func(project, bearerToken string) error) {
	s.serverEnrollFn = fn
}

// WithBearerToken sets the JWT that handleShareProject passes to serverEnrollFn.
// Call this at startup after reading credentials.json. An empty token is allowed
// (serverEnrollFn should return "not logged in" when the token is empty).
func (s *Server) WithBearerToken(token string) {
	s.bearerToken = token
}

// buildTrustedOrigins returns the set of Origin header values that the CSRF
// middleware accepts for a server bound to port p. Both the 127.0.0.1 and
// localhost forms are included because browsers may send either.
func buildTrustedOrigins(p int) map[string]struct{} {
	return map[string]struct{}{
		fmt.Sprintf("http://127.0.0.1:%d", p): {},
		fmt.Sprintf("http://localhost:%d", p): {},
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
	// Bulk set-scope: bidirectional — scope=shared or scope=personal; confirm=1 to execute (REQ-35, AD1).
	s.mux.HandleFunc("POST /project/{name}/set-scope", s.originCheckMiddleware(s.handleSetProjectScope))
	// Classify: sets the project default_scope in config.json (cwd project only).
	s.mux.HandleFunc("POST /project/{name}/classify", s.originCheckMiddleware(s.handleClassify))
	// Share: atomically server-enroll + client-enroll + write default_scope=shared (D9).
	s.mux.HandleFunc("POST /project/{name}/share", s.originCheckMiddleware(s.handleShareProject))
	// PR#3 / E2b: bulk-by-tag scope action (REQ-50, D2, D3, D5, D7; AD1).
	s.mux.HandleFunc("POST /project/{name}/tag-scope", s.originCheckMiddleware(s.handleTagScope))
	// PR#3 / E2b: htmx tag-values fragment — populates value <select> on facet change (REQ-52, AD2).
	s.mux.HandleFunc("GET /project/{name}/tag-values", s.handleTagValues)

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
