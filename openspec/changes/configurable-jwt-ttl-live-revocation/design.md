# Design: Configurable JWT TTL + Live users.yaml Revocation

## Technical Approach

Turn the hidden `jwtExpSeconds` constant into an explicit `time.Duration` seam threaded
the same way the existing `now time.Time` test seam is. Read+clamp `ENGRAM_JWT_TTL` once
in a shared `cmd/engram` helper so both the server mint path (`handleAuth`,
`autoLoginFromHeader`) and the CLI client-mint path (`login.go`) draw from one source.
Live revocation stops depending on manual SIGHUP: `YAMLLoader` owns an fsnotify watcher on
its parent directory. The silent-expiry gap closes with a startup stderr warning that reads
the already-present `credentials.json` `ExpiresAt`. Authorization model is unchanged.

## Architecture Decisions

| # | Decision | Choice | Rejected | Rationale |
|---|----------|--------|----------|-----------|
| D1a | TTL delivery | Add `ttl time.Duration` param to `MintJWT`; default fallback when `ttl<=0` | Read env inside `MintJWT` | Matches `now time.Time` seam; testable without env mutation; keeps `auth` pkg free of `os` |
| D1b | Env format | Duration string via `time.ParseDuration` (`2160h`=90d) | Raw seconds | Consistent with companion `oauth2-proxy.cfg cookie_expire=2160h`; stdlib parse; readable. Caveat: no `d` unit — document `2160h` |
| D1c | Default/floor location | `auth.DefaultJWTTTL = 90*24*time.Hour`, `auth.MinJWTTTL = 24*time.Hour` in `jwt.go` | Constants in `cmd/engram` | `auth` owns JWT lifetime domain; both CLI and server import it |
| D2 | Clamp site | `resolveJWTTTL()` in `cmd/engram/cloud.go` (shared by serve + login) | Clamp inside `MintJWT` | One parse/clamp; both mint paths get a valid TTL; env logic stays out of `auth` |
| D3a | Watcher owner | `YAMLLoader.Watch(ctx) error` method | Wire externally in runtime | Encapsulated; loader owns `path`; isolated test; matches `obsidian.Watcher.Run(ctx)` |
| D3b | Watch target | Parent dir `filepath.Dir(l.path)`, filter `event.Name==l.path` | Watch the file directly | Admin `WriteAtomic` uses `os.Rename` → fires `Create`/`IN_MOVED_TO` on the dir, not `Write` on the file |
| D4 | Warning site | `warnIfSessionExpiringSoon(credDir, 168h)` in `tryStartAutosync` | Warn on every explicit sync | Fires once at `serve`/`mcp` start; visible; low noise |

## Data Flow

    ENGRAM_JWT_TTL ─→ resolveJWTTTL() ──clamp≥24h──┬─→ WithJWTTTL → s.jwtTTL ─→ handleAuth / autoLoginFromHeader ─→ MintJWT(...,ttl)
    (unset/bad→90d+warn)                           └─→ login.go: claims.Exp + MintJWT(...,ttl)   (one TTL, no divergence)

    users.yaml edit / atomic-rename ─→ fsnotify(parent dir) ─→ 100ms debounce ─→ loader.Reload() ─→ next /sync Lookup denies revoked user
    (SIGHUP goroutine stays as container fallback; WithUserDirectoryReload callback stays — both idempotent)

    serve/mcp start ─→ tryStartAutosync ─→ warnIfSessionExpiringSoon(credDir,7d) ─→ reads credentials.json ExpiresAt ─→ stderr if ≤7d

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/cloud/auth/jwt.go` | Modify | `MintJWT(secret,claims,now,ttl)`; `claims.Exp=now.Add(ttl).Unix()`; `if ttl<=0 {ttl=DefaultJWTTTL}`; add `DefaultJWTTTL`,`MinJWTTTL`; drop `jwtExpSeconds` |
| `internal/cloud/cloudserver/cloudserver.go` | Modify | `jwtTTL time.Duration` field; `autoLoginFromHeader` (L414) passes `s.jwtTTL` |
| `internal/cloud/cloudserver/auth_endpoint.go` | Modify | `WithJWTTTL(d)` option; `handleAuth` (L100) passes `s.jwtTTL`; fix "7-day JWT" comments |
| `cmd/engram/cloud.go` | Modify | `resolveJWTTTL()` (parse+clamp+warn); pass `WithJWTTTL`; `go loader.Watch(ctx)` in `Start()`; ctx+cancel on runtime |
| `cmd/engram/login.go` | Modify | L396: `claims.Exp=now.Add(resolveJWTTTL()).Unix()`; L404 pass ttl; kill hardcoded 604800 |
| `cmd/engram/main.go` | Modify | `warnIfSessionExpiringSoon` + `sessionExpiryWarnThreshold=168h`; call in `tryStartAutosync` |
| `internal/cloud/users/loader.go` | Modify | `Watch(ctx context.Context) error` (fsnotify, dir watch, filename filter, 100ms debounce) |
| `go.mod` | Modify | Promote `fsnotify v1.7.0` indirect → direct (`go get github.com/fsnotify/fsnotify`) |
| `jwt_test.go`,`loader_test.go`,`expiry_test.go` | Test | New-signature exp assert; `TestWatcherTriggersReload`; warning test |
| EngramCloud `oauth2-proxy.cfg`,`docker-compose.yml` | Modify (D5) | `cookie_expire` 168h→2160h; set `ENGRAM_JWT_TTL` |

## Interfaces / Contracts

```go
// auth/jwt.go
const DefaultJWTTTL = 90 * 24 * time.Hour
const MinJWTTTL     = 24 * time.Hour            // floor: > 8h dashboard outer envelope
func MintJWT(secret string, claims JWTClaims, now time.Time, ttl time.Duration) (string, error)

// cloudserver: WithJWTTTL(d time.Duration) Option  → s.jwtTTL

// cmd/engram/cloud.go
func resolveJWTTTL() time.Duration // env empty→Default(no warn); parse-err→Default+warn; <Min→Min+warn (stderr)

// users/loader.go
func (l *YAMLLoader) Watch(ctx context.Context) error // watch Dir(path); Create|Write|Rename & Name==path; 100ms debounce→Reload

// cmd/engram: warnIfSessionExpiringSoon(credDir string, threshold time.Duration) // reads credentials.json ExpiresAt (RFC3339)
```

## Testing Strategy (RED → GREEN → REFACTOR)

| Area | RED (write/adjust test first) | GREEN |
|------|-------------------------------|-------|
| A. TTL | `jwt_test.go:56` — update to new signature + `wantExp:=now.Add(ttl).Unix()`; won't compile / fails against old const | Change `MintJWT` sig + use ttl; update 3 callers (auth_endpoint L100, cloudserver L414, login L404) |
| A. Clamp | New `resolveJWTTTL` unit test: `12h→24h`, `garbage→Default`, `2160h→2160h` | Implement parse+clamp+warn |
| B. Watcher | `loader_test.go` `TestWatcherTriggersReload`: temp dir, atomic-rename new users.yaml, assert `Lookup` reflects change | Implement `Watch(ctx)` + wire goroutine |
| C. Warning | `expiry_test.go`: ExpiresAt=now+3d ⇒ stderr warns; now+30d ⇒ silent | Implement `warnIfSessionExpiringSoon` + call in `tryStartAutosync` |

REFACTOR: dedupe day-formatting helper; confirm single 604800 source removed.

## Risks

| Risk | Mitigation |
|------|-----------|
| JWT < 8h dashboard outer envelope | `MinJWTTTL=24h` floor clamp + WARN; documented |
| Atomic rename missed | Watch parent dir; match `filepath.Clean(Name)==path`; handle `Create` |
| Double-fire (callback + watcher) | `Reload()` idempotent + 100ms debounce timer |
| Hidden 2nd constant (`login.go:396`) | Single `resolveJWTTTL()` source; explicit callout |
| `claims` pass-by-value → credentials.json `ExpiresAt` diverges from token | Compute ttl once; use for both `claims.Exp` and `MintJWT` |
| `time.ParseDuration` has no `d` unit | Document 90d=`2160h`; unparseable→Default+warn |
| Watcher goroutine leak | `ctx` cancel on `Start()` return (autosync/obsidian pattern) |
| Strict-TDD RED expected at `jwt_test.go:56` | Expected — fix only in GREEN, do not pre-patch |

## Migration / Rollout

Fork: single PR (D1+D2+D3+D4). If diff >400 lines, split D3 (watcher) into a stacked
second PR. `ENGRAM_JWT_TTL` unset → 90d default (graceful).

D5 rollback (EngramCloud, config-only, no data migration, independent of fork PR):
restore `oauth2-proxy.cfg cookie_expire` 2160h→168h, remove `ENGRAM_JWT_TTL` from
`docker-compose.yml`, redeploy the cloud service.

## Open Questions

- [ ] None — clamp behavior, debounce, and PR split resolved with approved defaults.
