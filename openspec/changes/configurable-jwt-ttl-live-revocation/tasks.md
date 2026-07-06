# Tasks: configurable-jwt-ttl-live-revocation

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~330–370 (additions + deletions) |
| 400-line budget risk | Medium |
| Chained PRs recommended | Yes |
| Suggested split | PR 1: D1+D2+D4+D5 (~195 lines) → PR 2: D3 Watch (~145 lines) |
| Delivery strategy | ask-on-risk |
| Chain strategy | stacked-to-main |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: stacked-to-main
400-line budget risk: Medium

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Configurable TTL (D1+D2) + expiry warning (D4) + config (D5) | PR 1 | Base: master; self-contained; SIGHUP reload still works; ~195 lines |
| 2 | Live users.yaml fsnotify watcher (D3) | PR 2 | Base: PR 1 (stacked-to-main); adds Watch goroutine + tests; ~145 lines |

---

## Phase 1: AUTH constants + MintJWT signature (D1a)

- [ ] 1.1 **[RED]** `internal/cloud/auth/jwt_test.go` — Update all `MintJWT` calls to new 4-arg form `(secret, claims, now, 90*24*time.Hour)`; L56 `wantExp` → `now.Unix() + int64((90*24*time.Hour).Seconds())`; `TestVerifyJWT_Expired` verify at `now.Add(91*24*time.Hour)`. Run `go test ./internal/cloud/auth/...` → compile error (forced RED).
- [ ] 1.2 **[GREEN]** `internal/cloud/auth/jwt.go` — Add `DefaultJWTTTL = 90*24*time.Hour`, `MinJWTTTL = 24*time.Hour`; change `MintJWT(secret string, claims JWTClaims, now time.Time, ttl time.Duration)`; body: `if ttl <= 0 { ttl = DefaultJWTTTL }; claims.Exp = now.Add(ttl).Unix()`; drop `jwtExpSeconds`. Fix all callers that now fail to compile: `auth_endpoint.go:100`, `cloudserver.go:414`, `login.go:404` — pass `0` as temporary placeholder (uses DefaultJWTTTL). `go test ./internal/cloud/auth/...` → GREEN.

## Phase 2: resolveJWTTTL() helper (D2 — clamp + env parse)

- [ ] 2.1 **[RED]** New `cmd/engram/cloud_test.go` — add `TestResolveJWTTTL` table test: `""→DefaultJWTTTL (no stderr)`, `"1h"→MinJWTTTL+warn`, `"garbage"→DefaultJWTTTL+warn`, `"2160h"→2160h (no warn)`. Run `go test ./cmd/engram/...` → RED (`resolveJWTTTL` undefined).
- [ ] 2.2 **[GREEN]** `cmd/engram/cloud.go` — implement `resolveJWTTTL() time.Duration`: `os.Getenv("ENGRAM_JWT_TTL")` empty → return `auth.DefaultJWTTTL`; `time.ParseDuration` error → `auth.DefaultJWTTTL` + `fmt.Fprintln(os.Stderr, ...)` warn; result < `auth.MinJWTTTL` → clamp + warn. Run `go test ./cmd/engram/...` → GREEN.

## Phase 3: Wire TTL through cloudserver (D1b)

- [ ] 3.1 `internal/cloud/cloudserver/cloudserver.go` — add `jwtTTL time.Duration` field. `internal/cloud/cloudserver/auth_endpoint.go` — add `WithJWTTTL(d time.Duration) Option`; wire `s.jwtTTL` into `handleAuth` L100 and `autoLoginFromHeader` L414 `MintJWT` calls (replace `0` placeholders); update "7-day JWT" comments to "configurable-TTL JWT".
- [ ] 3.2 `cmd/engram/cloud.go` — add `cloudserver.WithJWTTTL(resolveJWTTTL())` option to the `cloudserver.New(...)` call in the `usersFile != ""` branch of `newCloudRuntime`. Compile + `go test ./...` must pass.

## Phase 4: Fix login.go TTL divergence (D2 completion)

- [ ] 4.1 **[RED]** `cmd/engram/login_test.go:807` — change expected TTL from `7*24*time.Hour` to `90*24*time.Hour`. Run `go test ./cmd/engram/...` → RED (L396 still hardcodes 604800).
- [ ] 4.2 **[GREEN]** `cmd/engram/login.go` — L396: replace `claims.Exp = now.Unix() + 604800` with `ttl := resolveJWTTTL(); claims.Exp = now.Add(ttl).Unix()`; L404: pass `ttl` as 4th arg to `MintJWT`. `ExpiresAt` in credentials.json now derives from same `ttl` (no by-value divergence). `go test ./cmd/engram/...` → GREEN.

## Phase 5: Proactive session expiry warning (D4)

- [ ] 5.1 **[RED]** New `cmd/engram/expiry_test.go` — `TestWarnIfSessionExpiringSoon_Expires3d` (writes `credentials.json` with `expires_at = now+3d`, injected `io.Writer`, asserts warning); `TestWarnIfSessionExpiringSoon_30d` (now+30d, writer must be empty); `TestWarnIfSessionExpiringSoon_AlreadyExpired` (past `expires_at`, no proactive warning). Run → RED.
- [ ] 5.2 **[GREEN]** `cmd/engram/main.go` — implement `warnIfSessionExpiringSoon(w io.Writer, credDir string, threshold time.Duration, now time.Time)`: reads `credentials.json` `expires_at` RFC3339; writes warn to `w` when `0 < until <= threshold`; no JWT decode. Add `sessionExpiryWarnThreshold = 168*time.Hour`. Call `warnIfSessionExpiringSoon(os.Stderr, credDir, sessionExpiryWarnThreshold, time.Now())` at top of `tryStartAutosync` (non-fatal). → GREEN.

## Phase 6: EngramCloud config alignment (D5 — manual verify, no TDD)

- [ ] 6.1 `EngramCloud/oauth2-proxy/oauth2-proxy.cfg` — change `cookie_expire = 168h` → `cookie_expire = 2160h`. Verify by inspection.
- [ ] 6.2 `EngramCloud/docker-compose.yml` — add `ENGRAM_JWT_TTL: 2160h` to cloud service environment block. Rollback: remove var + restore `cookie_expire = 168h`, redeploy.

---

## Phase 7: Live users.yaml watcher (D3) — PR 2, stacked on PR 1

- [ ] 7.1 **[PREREQ]** `go.mod` — promote `github.com/fsnotify/fsnotify` from `indirect` to direct dep: `go get github.com/fsnotify/fsnotify@v1.7.0` + `go mod tidy`.
- [ ] 7.2 **[RED]** New `internal/cloud/users/loader_test.go` — `TestWatcherTriggersReload_DirectWrite` (os.WriteFile new content, poll Lookup for updated user); `TestWatcherTriggersReload_AtomicRename` (os.Rename tmp→target, poll Lookup); `TestWatcherIgnoresUnrelated` (write sibling file, assert Reload NOT called). Run → RED (`Watch` method undefined).
- [ ] 7.3 **[GREEN]** `internal/cloud/users/loader.go` — implement `func (l *YAMLLoader) Watch(ctx context.Context) error`: fsnotify.Watcher on `filepath.Dir(l.path)`; filter `Create|Write|Rename` where `filepath.Clean(event.Name) == filepath.Clean(l.path)`; 100ms debounce timer; call `l.Reload()`; return on `ctx.Done()`. Use polling helper in tests to tolerate debounce delay. → GREEN.
- [ ] 7.4 **[REFACTOR]** `loader.go` — extract `const watchDebounce = 100*time.Millisecond`. Add `TestHeaderAuth_RevokedUserDeniedAfterReload` to `internal/cloud/auth/header_auth_test.go`: call `loader.Reload()` after marking user `status: removed`, assert HeaderAuthenticator returns 403. → REFACTOR GREEN.
- [ ] 7.5 `cmd/engram/cloud.go` — add `ctx context.Context` + `cancel context.CancelFunc` fields to `defaultCloudRuntime`; in `Start()` add `go func() { if err := loader.Watch(r.ctx); err != nil && !errors.Is(err, context.Canceled) { log.Printf(...) } }()` after SIGHUP goroutine; call `r.cancel()` via deferred in Start(); wire context from `cmdCloudServe`.

## Phase 8: Final cleanup and verification

- [ ] 8.1 **[REFACTOR]** Search repo for remaining `604800` literals and "7-day" JWT comments; replace with `auth.DefaultJWTTTL` references or updated copy. Run `go test ./...` — zero failures required.
