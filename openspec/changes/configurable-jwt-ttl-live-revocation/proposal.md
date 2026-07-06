# Proposal: Configurable JWT TTL + Live users.yaml Revocation

## Intent

Team members forget the **7-day** re-login and autosync stops **silently** — friction with no signal. Authorization already lives in `users.yaml`, checked on every sync via `HeaderAuthenticator → loader.Lookup`, so a **longer JWT is safe as long as revocation stays solid**. Goal: cut re-login friction WITHOUT weakening security — longer configurable JWT, live users.yaml revocation, and a loud expiry warning. No change to the auth authorization model itself.

## Scope

### In Scope (frozen decisions D1–D5)
- **D1 — Configurable TTL**: env `ENGRAM_JWT_TTL`, **default 90d**. Add `ttl time.Duration` param to `MintJWT`; thread from new `cloudserver.WithJWTTTL` option, read in `cmd/engram/cloud.go`. Fix the **second** hardcoded `604800` at `cmd/engram/login.go:397`.
- **D2 — Min-TTL guard**: clamp/validate `ENGRAM_JWT_TTL` to a **24h floor** (dashboard outer session envelope is 8h; a shorter JWT dies before the cookie). Document the floor.
- **D3 — fsnotify watcher**: add `Watch(ctx)` on `YAMLLoader` watching the **parent directory** (admin dashboard writes via atomic `os.Rename`, which only fires on the parent), calling `loader.Reload()`. Wire as a goroutine in `defaultCloudRuntime.Start()` **alongside** the existing SIGHUP goroutine (belt-and-suspenders). Promote fsnotify v1.7.0 indirect → direct.
- **D4 — Proactive expiry warning**: `warnIfSessionExpiringSoon` called from `tryStartAutosync` (serve + mcp startup), reads `ExpiresAt` from `credentials.json` (no JWT decode), prints to **stderr** when the session expires in **≤7 days**.
- **D5 — Companion config (EngramCloud repo)**: `oauth2-proxy.cfg` `cookie_expire` 168h→**2160h (90d)**; `docker-compose.yml` set `ENGRAM_JWT_TTL` on the cloud service. Deploy config, not TDD-tested.

### Out of Scope (Non-Goals)
- **No refresh-token flow** (separate future change).
- No change to the auth authorization model (`users.yaml` already governs).
- No change to the 8h dashboard session envelope.

## Capabilities

### New Capabilities
- `configurable-jwt-ttl`: env-driven JWT lifetime, 90d default, 24h floor.
- `live-user-directory-reload`: fsnotify parent-dir watcher → live revocation.
- `session-expiry-warning`: proactive CLI stderr warning at ≤7 days.

### Modified Capabilities
- None (authorization model unchanged; JWT minting gains a TTL param but revocation semantics are preserved).

## Approach

TTL becomes an explicit `time.Duration` seam (matches the existing `now time.Time` test seam) instead of a hidden constant, read once in `newCloudRuntime()` and clamped to the 24h floor. Live revocation stops relying on manual SIGHUP: the loader owns its own watcher on the parent dir, SIGHUP stays as fallback for container `kill -1`. The CLI stops failing silently by reading the already-present `ExpiresAt` and warning early.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/cloud/auth/jwt.go` | Modified | `MintJWT(... ttl time.Duration)`; drop hardcoded 604800 const |
| `internal/cloud/cloudserver/cloudserver.go` | Modified | `jwtTTL` field + `WithJWTTTL` option; auto-login path passes TTL |
| `internal/cloud/cloudserver/auth_endpoint.go` | Modified | `handleAuth` passes `s.jwtTTL`; fix "7-day JWT" comment |
| `cmd/engram/cloud.go` | Modified | Read+clamp `ENGRAM_JWT_TTL`; start `Watch` goroutine |
| `cmd/engram/login.go` | Modified | Fix **second** 604800 at L397 |
| `cmd/engram/main.go` | Modified | `warnIfSessionExpiringSoon` in `tryStartAutosync` |
| `internal/cloud/users/loader.go` | New | `Watch(ctx context.Context)` method |
| `go.mod` | Modified | Promote fsnotify v1.7.0 indirect → direct |
| `jwt_test.go`, `loader_test.go`, `expiry_test.go` | Test | Update `wantExp` (604800), watcher-reload test, warning test |
| EngramCloud `oauth2-proxy.cfg`, `docker-compose.yml` | Modified | D5 companion deploy config |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| JWT shorter than 8h dashboard envelope | Med | D2 24h floor clamp + documented |
| Atomic-rename missed by watcher | Med | Watch **parent dir**, filter on filename; catches `Create`/`IN_MOVED_TO` |
| Double-fire (callback + watcher) after dashboard write | Low | Reload is idempotent; optional 100ms debounce |
| Hidden second constant (login.go:397) missed | Med | Explicitly called out in D1 + affected areas |
| jwt_test.go L56 `wantExp` breaks (RED) | High | Expected under strict TDD; fix in GREEN phase |

## Rollback Plan

Fork code: revert the Go change (single PR target). `ENGRAM_JWT_TTL` unset → 90d default; behavior degrades gracefully to the prior model minus the hardcoded 7d. **EngramCloud config (D5) rollback**: restore `oauth2-proxy.cfg` `cookie_expire` to 168h and remove `ENGRAM_JWT_TTL` from `docker-compose.yml`, then redeploy the cloud service — config-only, no data migration, safe to revert independently of the fork PR.

## Dependencies

- Strict TDD active for Go: `go test ./...`, RED-GREEN-REFACTOR.
- fsnotify v1.7.0 already vendored (indirect) — promotion only, no download/upgrade.
- Delivery: `ask-on-risk`, chain `stacked-to-main`; target **1 PR under 400 lines**.

## Success Criteria

- [ ] `ENGRAM_JWT_TTL` controls JWT lifetime; default 90d; values <24h clamped to 24h.
- [ ] Both hardcoded 604800 sites removed (jwt.go const + login.go:397).
- [ ] Editing/atomic-renaming `users.yaml` triggers live `Reload()` with no SIGHUP.
- [ ] Revoked user is denied on the next sync without a server restart.
- [ ] `serve`/`mcp` startup prints a stderr warning when the session expires in ≤7 days.
- [ ] EngramCloud `cookie_expire`=2160h and `ENGRAM_JWT_TTL` set (D5).

## First Slice Boundary

Single PR if it fits under 400 lines: D1+D2 (TTL) + D3 (watcher) + D4 (warning) in the fork, with D5 as the companion deploy-config commit in EngramCloud. If the diff exceeds 400 lines, split D3 (watcher) into a second stacked PR.
