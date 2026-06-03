package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Interface seams (injectable for tests) ───────────────────────────────────

// TokenExchanger is the seam for converting an OAuth authorization code into JWT claims.
type TokenExchanger interface {
	Exchange(code string) (auth.JWTClaims, error)
}

// BrowserOpener is the seam for opening the OAuth URL in a web browser.
type BrowserOpener interface {
	Open(url string) error
}

// Clock is the seam for time so tests can use deterministic timestamps.
type Clock interface {
	Now() time.Time
}

// realClock implements Clock using the system time.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// ─── Injectable variables for testability ────────────────────────────────────

// credentialsDirFn returns the directory where credentials.json is stored.
// Defaults to ~/.engram; overridden in tests to use t.TempDir().
var credentialsDirFn = func() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("login: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".engram"), nil
}

// reclassifyHookFn is the post-auth reclassify hook. Called after token is written,
// before pull/push. Overridden in tests to avoid side effects.
var reclassifyHookFn func(cfg store.Config) = func(cfg store.Config) {
	cmdReclassify(cfg)
}

// postLoginPullFn performs the post-login pull sync. Overridden in tests.
var postLoginPullFn func(cfg store.Config) error = func(cfg store.Config) error {
	return nil // real implementation: wire mutation pull transport
}

// postLoginPushFn performs the post-login push sync. Overridden in tests.
var postLoginPushFn func(cfg store.Config) error = func(cfg store.Config) error {
	return nil // real implementation: wire mutation push transport
}

// ─── Credential file ─────────────────────────────────────────────────────────

// credentialFile is the JSON structure written to ~/.engram/credentials.json.
type credentialFile struct {
	AccessToken string `json:"access_token"`
	IssuedAt    string `json:"issued_at"`
	ExpiresAt   string `json:"expires_at"`
	Email       string `json:"email"`
}

// ─── loginCommand ─────────────────────────────────────────────────────────────

// loginCommand implements the `engram login` flow with injectable seams.
type loginCommand struct {
	cfg      store.Config
	exchanger TokenExchanger
	browser  BrowserOpener
	clock    Clock
	secret   string // JWT signing secret (from ENGRAM_JWT_SECRET or default)
}

// Run executes the full login flow:
//  1. Start loopback HTTP server to receive OAuth callback
//  2. Open browser at OAuth URL (or print URL for device code fallback)
//  3. Exchange code for claims
//  4. MintJWT → write credentials.json (perms 0600)
//  5. Enroll "general" project (design Q5)
//  6. Reclassify (blocking, before push — design Q2)
//  7. Pull (warning on failure, continues)
//  8. Push (warning on failure, login still succeeds)
func (c *loginCommand) Run() error {
	now := c.clock.Now()

	// Determine claims via exchanger (fake in tests, loopback in prod).
	claims, err := c.exchanger.Exchange("")
	if err != nil {
		return fmt.Errorf("login: token exchange: %w", err)
	}

	// Ensure claims have iat/exp set (they may come pre-set from a real OAuth flow).
	if claims.Iat == 0 {
		claims.Iat = now.Unix()
	}
	if claims.Exp == 0 {
		claims.Exp = now.Unix() + 604800 // 7 days
	}

	// Mint the JWT.
	secret := c.secret
	if secret == "" {
		secret = strings.Repeat("x", 32) // dev fallback — real servers use ENGRAM_JWT_SECRET
	}
	tokenStr, err := auth.MintJWT(secret, claims, now)
	if err != nil {
		return fmt.Errorf("login: mint jwt: %w", err)
	}

	// Write credentials.json.
	credDir, err := credentialsDirFn()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(credDir, 0700); err != nil {
		return fmt.Errorf("login: create credentials dir: %w", err)
	}

	issuedAt := time.Unix(claims.Iat, 0).UTC()
	expiresAt := time.Unix(claims.Exp, 0).UTC()
	creds := credentialFile{
		AccessToken: tokenStr,
		IssuedAt:    issuedAt.Format(time.RFC3339),
		ExpiresAt:   expiresAt.Format(time.RFC3339),
		Email:       claims.Email,
	}
	credBytes, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("login: marshal credentials: %w", err)
	}
	credPath := filepath.Join(credDir, "credentials.json")
	if err := os.WriteFile(credPath, credBytes, 0600); err != nil {
		return fmt.Errorf("login: write credentials: %w", err)
	}
	fmt.Printf("Logged in as %s\n", claims.Email)
	fmt.Printf("Credentials stored at %s\n", credPath)

	// Enroll "general" project (design Q5: client-side enrollment at login).
	s, err := storeNew(c.cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[login] warn: open store for general enrollment: %v\n", err)
	} else {
		if err := s.EnrollProject("general"); err != nil {
			fmt.Fprintf(os.Stderr, "[login] warn: enroll general project: %v\n", err)
		}
		_ = s.Close()
	}

	// Activate the push gate before reclassify runs (W7).
	// On a fresh install, IsReclassifyComplete defaults true (no sync_state row),
	// which would leave the push gate inactive if login is interrupted between
	// token write and reclassify completion. Mark incomplete first so the gate is
	// ACTIVE throughout the reclassify pass; MarkReclassifyComplete (called by
	// cmdReclassify) lifts the gate only once classification genuinely finishes.
	{
		gs, err := storeNew(c.cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[login] warn: open store for push gate: %v\n", err)
		} else {
			if err := gs.MarkReclassifyIncomplete(store.DefaultSyncTargetKey); err != nil {
				fmt.Fprintf(os.Stderr, "[login] warn: mark reclassify incomplete: %v\n", err)
			}
			_ = gs.Close()
		}
	}

	// Canonical post-auth order (design Q2): reclassify → pull → push.
	reclassifyHookFn(c.cfg)

	if err := postLoginPullFn(c.cfg); err != nil {
		fmt.Fprintf(os.Stderr, "[login] warn: post-login pull failed: %v (continuing)\n", err)
	}

	if err := postLoginPushFn(c.cfg); err != nil {
		fmt.Fprintf(os.Stderr, "[login] warn: post-login push failed: %v (retry with `engram sync --cloud`)\n", err)
	}

	return nil
}

// ─── Real loopback OAuth flow ─────────────────────────────────────────────────

// loopbackExchanger implements TokenExchanger using an RFC 8252 loopback redirect.
// It starts a local HTTP server, opens the browser at authURL, waits for the callback,
// then exchanges the code for claims using the provided exchange function.
type loopbackExchanger struct {
	authURL  string
	exchange func(code string) (auth.JWTClaims, error)
}

func (e *loopbackExchanger) Exchange(_ string) (auth.JWTClaims, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return auth.JWTClaims{}, fmt.Errorf("loopback: listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("loopback: OAuth callback missing code")
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("<html><body>Login complete. You may close this tab.</body></html>"))
		codeCh <- code
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	fullAuthURL := e.authURL + "&redirect_uri=" + redirectURI
	_ = fullAuthURL // browser would open this
	_ = redirectURI

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	defer func() { _ = srv.Shutdown(context.Background()) }()

	select {
	case code := <-codeCh:
		return e.exchange(code)
	case err := <-errCh:
		return auth.JWTClaims{}, err
	case <-ctx.Done():
		return auth.JWTClaims{}, fmt.Errorf("loopback: OAuth flow timed out")
	}
}

// ─── Expiry detection ─────────────────────────────────────────────────────────

// handleSyncAuthError detects a 401 response and prints the session-expired message.
// Returns the original error (wrapped) without retrying. If the error is not a 401,
// it is returned as-is.
func handleSyncAuthError(err error, msgFn func() string, _ func() error) error {
	if err == nil {
		return nil
	}
	// Check if the error is an HTTP 401.
	var statusErr *remote.HTTPStatusError
	if isHTTPStatusErr(err, &statusErr) && statusErr.IsAuthFailure() {
		fmt.Fprintln(os.Stderr, msgFn())
		return err // no retry
	}
	// Also check by wrapping with errors.As.
	if isAuthError(err) {
		fmt.Fprintln(os.Stderr, msgFn())
		return err
	}
	return err
}

// isHTTPStatusErr attempts to extract an *HTTPStatusError from the given error.
func isHTTPStatusErr(err error, target **remote.HTTPStatusError) bool {
	if err == nil {
		return false
	}
	var e *remote.HTTPStatusError
	if errors.As(err, &e) {
		if target != nil {
			*target = e
		}
		return true
	}
	return false
}

// isAuthError returns true if the error indicates a 401 authentication failure.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *remote.HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.IsAuthFailure()
	}
	return false
}

// ─── cmdLogin entry point ─────────────────────────────────────────────────────

// cmdLogin is the top-level `engram login` command handler.
// It uses the loopback OAuth flow in production and the injectable seams in tests.
func cmdLogin(cfg store.Config) {
	secret := strings.TrimSpace(os.Getenv("ENGRAM_JWT_SECRET"))
	if secret == "" {
		secret = strings.Repeat("x", 32) // dev fallback
	}

	// In production, use a real exchanger. The server URL and client ID would come
	// from config or env (Phase 4 wires oauth2-proxy fully).
	// For now, use the placeholder exchanger that prompts for device code.
	exchanger := &placeholderExchanger{}

	runner := &loginCommand{
		cfg:       cfg,
		exchanger: exchanger,
		browser:   &noopBrowserOpener{},
		clock:     realClock{},
		secret:    secret,
	}
	if err := runner.Run(); err != nil {
		fatal(err)
	}
}

// placeholderExchanger is a production stub for the OAuth exchange. It returns a
// synthetic identity using ENGRAM_LOGIN_EMAIL env var (fallback: "unknown@vivastudios.com").
// Full oauth2-proxy wiring is a Phase 4 deliverable.
type placeholderExchanger struct{}

func (p *placeholderExchanger) Exchange(_ string) (auth.JWTClaims, error) {
	email := strings.TrimSpace(os.Getenv("ENGRAM_LOGIN_EMAIL"))
	if email == "" {
		email = "unknown@vivastudios.com"
	}
	now := time.Now().UTC()
	return auth.JWTClaims{
		Sub:        email,
		Email:      email,
		Department: strings.TrimSpace(os.Getenv("ENGRAM_LOGIN_DEPT")),
		Role:       "member",
		Iat:        now.Unix(),
		Exp:        now.Unix() + 604800,
	}, nil
}

// noopBrowserOpener does nothing (used in production until full oauth2-proxy wiring).
type noopBrowserOpener struct{}

func (n *noopBrowserOpener) Open(url string) error {
	fmt.Printf("Open this URL in your browser:\n  %s\n", url)
	return nil
}
