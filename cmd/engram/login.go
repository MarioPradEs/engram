package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Credential token errors ──────────────────────────────────────────────────

// errCredentialsNotFound is returned by readCredentialsToken when no credentials.json exists.
type errCredentialsNotFound struct{}

func (e errCredentialsNotFound) Error() string {
	return "credentials.json not found; falling back to env/cloud.json token"
}

// isCredentialsNotFoundError returns true if err is an errCredentialsNotFound.
func isCredentialsNotFoundError(err error) bool {
	var e errCredentialsNotFound
	return errors.As(err, &e)
}

// errCredentialsExpired is returned by readCredentialsToken when the stored token is expired.
type errCredentialsExpired struct {
	ExpiresAt time.Time
}

func (e errCredentialsExpired) Error() string {
	return fmt.Sprintf("credentials.json token expired at %s; run `engram login` to re-authenticate", e.ExpiresAt.Format(time.RFC3339))
}

// isExpiredCredentialsError returns true if err is an errCredentialsExpired.
func isExpiredCredentialsError(err error) bool {
	var e errCredentialsExpired
	return errors.As(err, &e)
}

// ─── Injectable crypto/encoding functions (for testability) ──────────────────

// base64DecodeRawURL is the seam for base64.RawURLEncoding.DecodeString.
var base64DecodeRawURL = base64.RawURLEncoding.DecodeString

// jsonUnmarshal is the seam for json.Unmarshal.
var jsonUnmarshal = json.Unmarshal

// ─── Interface seams (injectable for tests) ───────────────────────────────────

// TokenExchanger is the seam for converting an OAuth authorization code into JWT claims.
type TokenExchanger interface {
	Exchange(code string) (auth.JWTClaims, error)
}

// RawTokenExchanger is an extended seam for exchangers that return both a raw
// token string (server-minted JWT) and the decoded claims. When loginCommand's
// exchanger also implements this interface, the raw token is stored directly
// instead of being re-minted client-side.
type RawTokenExchanger interface {
	TokenExchanger
	ExchangeWithToken() (rawToken string, claims auth.JWTClaims, err error)
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

// postLoginPullFn performs the post-login pull sync and returns the number of
// mutations pulled. Uses the JWT from credentials.json (or env/cloud.json fallback)
// and the cloud URL from cloudBaseURLFn. Overridden in tests.
var postLoginPullFn func(cfg store.Config) (int, error) = func(cfg store.Config) (int, error) {
	credDir, err := credentialsDirFn()
	if err != nil {
		return 0, fmt.Errorf("post-login pull: resolve credentials dir: %w", err)
	}
	token, tokenErr := resolveLoginToken(credDir, cfg)
	if tokenErr != nil {
		return 0, fmt.Errorf("post-login pull: resolve token: %w", tokenErr)
	}
	if token == "" {
		return 0, nil // no token configured — skip pull silently
	}
	cloudURL := cloudBaseURLFn(cfg)
	return doPostLoginPull(cloudURL, token)
}

// postLoginPushFn performs the post-login push sync and returns the number of
// mutations pushed. Uses the JWT from credentials.json (or env/cloud.json fallback)
// and the cloud URL from cloudBaseURLFn. Overridden in tests.
var postLoginPushFn func(cfg store.Config) (int, error) = func(cfg store.Config) (int, error) {
	credDir, err := credentialsDirFn()
	if err != nil {
		return 0, fmt.Errorf("post-login push: resolve credentials dir: %w", err)
	}
	token, tokenErr := resolveLoginToken(credDir, cfg)
	if tokenErr != nil {
		return 0, fmt.Errorf("post-login push: resolve token: %w", tokenErr)
	}
	if token == "" {
		return 0, nil // no token configured — skip push silently
	}
	cloudURL := cloudBaseURLFn(cfg)
	return doPostLoginPush(cloudURL, token, nil)
}

// ─── Credential file ─────────────────────────────────────────────────────────

// credentialFile is the JSON structure written to ~/.engram/credentials.json.
type credentialFile struct {
	AccessToken string `json:"access_token"`
	IssuedAt    string `json:"issued_at"`
	ExpiresAt   string `json:"expires_at"`
	Email       string `json:"email"`
}

// ─── Credentials.json helpers ────────────────────────────────────────────────

// readCredentialsToken reads ~/.engram/credentials.json (or the dir passed in)
// and returns the access_token if the file exists and the token has not expired.
//
// Returns:
//   - (token, nil) if the file exists and is not expired.
//   - ("", errCredentialsNotFound{}) if the file does not exist.
//   - ("", errCredentialsExpired{}) if the file exists but the token is expired.
func readCredentialsToken(credDir string) (string, error) {
	path := filepath.Join(credDir, "credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errCredentialsNotFound{}
		}
		return "", fmt.Errorf("readCredentialsToken: %w", err)
	}
	var creds credentialFile
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("readCredentialsToken: parse credentials.json: %w", err)
	}
	if strings.TrimSpace(creds.AccessToken) == "" {
		return "", errCredentialsNotFound{}
	}
	if creds.ExpiresAt != "" {
		expiresAt, parseErr := time.Parse(time.RFC3339, creds.ExpiresAt)
		if parseErr == nil && time.Now().UTC().After(expiresAt) {
			return "", errCredentialsExpired{ExpiresAt: expiresAt}
		}
	}
	return creds.AccessToken, nil
}

// resolveLoginToken returns the bearer token to use for post-login sync.
// Precedence: credentials.json access_token > ENGRAM_CLOUD_TOKEN env > cloud.json.
//
// If credentials.json exists but is expired, it prints an expiry warning and
// falls through to the legacy sources so the operator can still sync with a
// static token while the interactive re-login is pending.
func resolveLoginToken(credDir string, cfg store.Config) (string, error) {
	token, err := readCredentialsToken(credDir)
	if err == nil {
		return token, nil
	}
	if isExpiredCredentialsError(err) {
		// Warn but fall through — let the legacy token source handle this cycle.
		fmt.Fprintf(os.Stderr, "[login] warn: %v\n", err)
	}
	// Fall through to legacy sources: env > cloud.json.
	cc, ccErr := resolveCloudRuntimeConfig(cfg)
	if ccErr != nil {
		return "", fmt.Errorf("resolveLoginToken: %w", ccErr)
	}
	if cc != nil && strings.TrimSpace(cc.Token) != "" {
		return strings.TrimSpace(cc.Token), nil
	}
	return "", nil
}

// ─── Post-login sync helpers ──────────────────────────────────────────────────

// doPostLoginPull pulls mutations from the cloud server using the given bearer token.
// Returns the number of mutations pulled and any transport error.
// When cloudURL or token is empty, returns (0, nil) immediately (no-op).
func doPostLoginPull(cloudURL, token string) (int, error) {
	cloudURL = strings.TrimSpace(cloudURL)
	token = strings.TrimSpace(token)
	if cloudURL == "" || token == "" {
		return 0, nil
	}
	mt, err := remote.NewMutationTransport(cloudURL, token)
	if err != nil {
		return 0, fmt.Errorf("post-login pull: create transport: %w", err)
	}
	resp, err := mt.PullMutations(0, 500)
	if err != nil {
		return 0, fmt.Errorf("post-login pull: %w", err)
	}
	return len(resp.Mutations), nil
}

// doPostLoginPush pushes a batch of mutation entries to the cloud server using the
// given bearer token. entries may be nil/empty (resulting in a push of zero mutations,
// which still validates connectivity). Returns the number of accepted mutations and
// any transport error.
// When cloudURL or token is empty, returns (0, nil) immediately (no-op).
func doPostLoginPush(cloudURL, token string, entries []remote.MutationEntry) (int, error) {
	cloudURL = strings.TrimSpace(cloudURL)
	token = strings.TrimSpace(token)
	if cloudURL == "" || token == "" {
		return 0, nil
	}
	mt, err := remote.NewMutationTransport(cloudURL, token)
	if err != nil {
		return 0, fmt.Errorf("post-login push: create transport: %w", err)
	}
	accepted, err := mt.PushMutations(entries)
	if err != nil {
		return 0, fmt.Errorf("post-login push: %w", err)
	}
	return len(accepted), nil
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
//  3. Exchange code for claims (or receive server-minted JWT directly)
//  4. Store token → write credentials.json (perms 0600)
//  5. Enroll "general" project (design Q5)
//  6. Reclassify (blocking, before push — design Q2)
//  7. Pull (warning on failure, continues)
//  8. Push (warning on failure, login still succeeds)
//  9. Print sync summary
func (c *loginCommand) Run() error {
	now := c.clock.Now()

	// Determine token and claims via exchanger.
	// If the exchanger implements RawTokenExchanger, use the server-minted token
	// directly (Opción A: server signs with ENGRAM_JWT_SECRET, we just store it).
	var tokenStr string
	var claims auth.JWTClaims
	if rte, ok := c.exchanger.(RawTokenExchanger); ok {
		raw, rawClaims, err := rte.ExchangeWithToken()
		if err != nil {
			return fmt.Errorf("login: token exchange: %w", err)
		}
		tokenStr = raw
		claims = rawClaims
	} else {
		var err error
		claims, err = c.exchanger.Exchange("")
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
		// Mint the JWT client-side (placeholder / test path).
		secret := c.secret
		if secret == "" {
			secret = strings.Repeat("x", 32) // dev fallback — real servers use ENGRAM_JWT_SECRET
		}
		var mintErr error
		tokenStr, mintErr = auth.MintJWT(secret, claims, now)
		if mintErr != nil {
			return fmt.Errorf("login: mint jwt: %w", mintErr)
		}
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

	pulled, pullErr := postLoginPullFn(c.cfg)
	if pullErr != nil {
		fmt.Fprintf(os.Stderr, "[login] warn: post-login pull failed: %v (continuing)\n", pullErr)
	}

	pushed, pushErr := postLoginPushFn(c.cfg)
	if pushErr != nil {
		fmt.Fprintf(os.Stderr, "[login] warn: post-login push failed: %v (retry with `engram sync --cloud`)\n", pushErr)
	}

	// Print sync summary (spec: cli-auth §"Synced: ↓{N} pulled, ↑{M} pushed").
	fmt.Printf("Synced: ↓%d pulled, ↑%d pushed\n", pulled, pushed)

	return nil
}

// ─── Real loopback OAuth flow (Opción A: /auth → token-in-redirect) ──────────

// loopbackAuthExchanger implements RawTokenExchanger using the RFC 8252 loopback
// redirect pattern. It starts a local HTTP server on an ephemeral port, generates
// a random CSRF state, opens the browser at <cloudAuthURL>?redirect_uri=...&state=...,
// waits for the server to redirect back with ?token=<JWT>&state=<state>, validates
// state, decodes the JWT payload (without re-verifying — the server already signed
// it), and returns the raw token string plus the decoded claims.
//
// Security:
//   - State is 32 bytes of crypto/rand (CSRF protection).
//   - State is validated on callback; mismatch → error.
//   - redirect_uri is always 127.0.0.1 (not user-controlled).
//   - The server validates redirect_uri is a loopback address (open-redirect guard).
type loopbackAuthExchanger struct {
	cloudAuthURL string       // e.g. "https://engram.vivastudios.com/auth"
	browser      BrowserOpener
}

// newLoopbackAuthExchanger creates a loopbackAuthExchanger.
// cloudAuthURL is the full /auth endpoint on the Engram cloud server.
func newLoopbackAuthExchanger(cloudAuthURL string, browser BrowserOpener) *loopbackAuthExchanger {
	return &loopbackAuthExchanger{cloudAuthURL: cloudAuthURL, browser: browser}
}

// Exchange implements TokenExchanger (for compatibility). Delegates to ExchangeWithToken.
func (e *loopbackAuthExchanger) Exchange(_ string) (auth.JWTClaims, error) {
	_, claims, err := e.ExchangeWithToken()
	return claims, err
}

// ExchangeWithToken implements RawTokenExchanger.
// Returns the raw server-minted JWT string and the decoded claims.
func (e *loopbackAuthExchanger) ExchangeWithToken() (string, auth.JWTClaims, error) {
	// Start a local loopback server to receive the /auth redirect callback.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", auth.JWTClaims{}, fmt.Errorf("loopback: listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// Generate a random CSRF state.
	state, err := generateRandomState()
	if err != nil {
		return "", auth.JWTClaims{}, fmt.Errorf("loopback: generate state: %w", err)
	}

	type callbackResult struct {
		token string
		err   error
	}
	resultCh := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		receivedState := r.URL.Query().Get("state")
		if receivedState != state {
			resultCh <- callbackResult{err: fmt.Errorf("loopback: state mismatch (csrf protection): got %q, want %q", receivedState, state)}
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			resultCh <- callbackResult{err: fmt.Errorf("loopback: callback missing token")}
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("<html><body>Login complete. You may close this tab.</body></html>"))
		resultCh <- callbackResult{token: token}
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// Build the full /auth URL and open the browser.
	authURL := e.cloudAuthURL +
		"?redirect_uri=" + url.QueryEscape(redirectURI) +
		"&state=" + url.QueryEscape(state)
	if openErr := e.browser.Open(authURL); openErr != nil {
		fmt.Printf("Could not open browser automatically. Open this URL:\n  %s\n", authURL)
	}

	// Wait for the callback (5-minute timeout per RFC 8252 §8.1).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	select {
	case result := <-resultCh:
		if result.err != nil {
			return "", auth.JWTClaims{}, result.err
		}
		// Decode JWT claims without re-verifying signature (server already signed it).
		claims, parseErr := decodeJWTClaimsUnsafe(result.token)
		if parseErr != nil {
			return "", auth.JWTClaims{}, fmt.Errorf("loopback: decode token claims: %w", parseErr)
		}
		return result.token, claims, nil
	case <-ctx.Done():
		return "", auth.JWTClaims{}, fmt.Errorf("loopback: OAuth flow timed out after 5 minutes")
	}
}

// generateRandomState returns 32 bytes of URL-safe random hex.
func generateRandomState() (string, error) {
	buf := make([]byte, 16)
	if _, err := cryptoRandRead(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", buf), nil
}

// decodeJWTClaimsUnsafe decodes the payload of a JWT without verifying the
// signature. Used by the CLI after receiving a server-minted token in the
// loopback redirect — the server validated and signed it, we only need the
// claims for display / credential storage.
func decodeJWTClaimsUnsafe(token string) (auth.JWTClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return auth.JWTClaims{}, fmt.Errorf("malformed jwt (expected 3 parts, got %d)", len(parts))
	}
	payload, err := base64DecodeRawURL(parts[1])
	if err != nil {
		return auth.JWTClaims{}, fmt.Errorf("decode jwt payload: %w", err)
	}
	var claims auth.JWTClaims
	if err := jsonUnmarshal(payload, &claims); err != nil {
		return auth.JWTClaims{}, fmt.Errorf("unmarshal jwt claims: %w", err)
	}
	return claims, nil
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

// cloudBaseURLFn resolves the cloud base URL for the login flow.
// Priority: ENGRAM_CLOUD_URL env var → ENGRAM_CLOUD_SERVER env var (legacy).
// Falls back to the default production URL.
var cloudBaseURLFn = func(cfg store.Config) string {
	if v := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_SERVER")); v != "" {
		return strings.TrimRight(v, "/")
	}
	// Fall back to stored cloud config (set by `engram cloud config --server`).
	cc, err := loadCloudConfig(cfg)
	if err == nil && cc != nil && strings.TrimSpace(cc.ServerURL) != "" {
		return strings.TrimRight(strings.TrimSpace(cc.ServerURL), "/")
	}
	return "https://engram.vivastudios.com"
}

// cmdLogin is the top-level `engram login` command handler.
// It uses the real loopback OAuth flow (Opción A) in production:
//   - Opens browser at <cloud>/auth?redirect_uri=<loopback>&state=<random>
//   - Server validates oauth2-proxy X-Forwarded-Email, mints JWT, redirects back
//   - CLI stores the server-minted JWT in ~/.engram/credentials.json
func cmdLogin(cfg store.Config) {
	cloudBase := cloudBaseURLFn(cfg)
	authURL := cloudBase + "/auth"

	browser := &noopBrowserOpener{}
	exchanger := newLoopbackAuthExchanger(authURL, browser)

	runner := &loginCommand{
		cfg:       cfg,
		exchanger: exchanger,
		browser:   browser,
		clock:     realClock{},
		// secret is not used when loopbackAuthExchanger returns a server-minted token.
		secret: strings.TrimSpace(os.Getenv("ENGRAM_JWT_SECRET")),
	}
	if err := runner.Run(); err != nil {
		fatal(err)
	}
}

// noopBrowserOpener prints the URL when it cannot open a browser automatically.
type noopBrowserOpener struct{}

func (n *noopBrowserOpener) Open(openURL string) error {
	fmt.Printf("Open this URL in your browser:\n  %s\n", openURL)
	return nil
}
