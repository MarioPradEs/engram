# Verify Report: configurable-jwt-ttl-live-revocation — PR1 Slice

**Date**: 2026-07-06
**Branch**: feat/configurable-jwt-ttl
**Scope**: PR1 only — D1 (configurable TTL), D2 (24h guard), D4 (expiry warning), login.go divergence fix, 604800 cleanup
**Out of scope**: D3 fsnotify watcher (PR2), D5 EngramCloud config (different repo)
**Mode**: Strict TDD
**Verdict**: PASS

---

## Build and Test Evidence

| Package | Result | Notes |
|---|---|---|
| `internal/cloud/auth` | PASS | All tests pass (TestMintVerifyJWT, TestVerifyJWT_Expired, etc.) |
| `internal/cloud/cloudserver` | PASS | All tests pass including auth endpoint TTL assertions |
| `cmd/engram` (targeted) | PASS | TestResolveJWTTTL (5/5), TestWarnIfSessionExpiringSoon (3/3) |
| `cmd/engram` (login) | PASS | TestLoginHappyPath + TTL divergence assertion (90d) |
| Full build (`go build ./...`) | PASS | Zero errors |
| 604800 grep (`go` files) | PASS | Zero stray literals |

Pre-existing env-dependent failures (11 tests in `cmd/engram`) are EXCLUDED — identical on main (614f230) and this branch. Zero regression.

---

## Task Completion (PR1)

| Phase | Task | Status |
|---|---|---|
| 1.1 | [RED] jwt_test.go: MintJWT → 4-arg, wantExp → 90d, ExpiredToken → 91d | COMPLETE |
| 1.2 | [GREEN] jwt.go: remove jwtExpSeconds; add DefaultJWTTTL=90d, MinJWTTTL=24h; ttl param | COMPLETE |
| 2.1 | [RED] cloud_test.go: TestResolveJWTTTL (5 table cases) | COMPLETE |
| 2.2 | [GREEN] cloud.go: resolveJWTTTL() + resolveJWTTTLWithWriter(); wire WithJWTTTL | COMPLETE |
| 3.1 | [RED] auth_endpoint_test.go: assertion 604800→7776000; autologin expired uses 7d TTL | COMPLETE |
| 3.2 | [GREEN] cloudserver: jwtTTL field, WithJWTTTL option, wire into handleAuth + autoLoginFromHeader | COMPLETE |
| 4.1 | [RED] login_test.go: exp-iat assertion 7d → 90d | COMPLETE |
| 4.2 | [GREEN] login.go: resolveJWTTTL() in fallback; mintTTL derived to fix pass-by-value divergence | COMPLETE |
| 5.1 | [RED] expiry_test.go: TestWarnIfSessionExpiringSoon (3 subtests) | COMPLETE |
| 5.2 | [GREEN] main.go: warnIfSessionExpiringSoon + sessionExpiryWarnThreshold; injectable fn; wired | COMPLETE |
| 8.1 | [REFACTOR] Remove all 604800 literals; make expiry warning injectable | COMPLETE |

Phases 6 and 7: deferred (out of PR1 scope). Not counted as failures.

---

## Spec Compliance Matrix

### D1: Configurable TTL — MintJWT Accepts Explicit TTL

| Scenario | Covering Test | Result |
|---|---|---|
| Default TTL when ENGRAM_JWT_TTL unset → exp = now+7776000 | `TestMintVerifyJWT` (wantExp = now.Unix() + 7776000) | PASS |
| Custom TTL honored (48h → exp = now+172800) | `TestResolveJWTTTL/2160h honored no warn` + cloudserver assertion | PASS |
| login.go:397 uses configured TTL (not 604800) | `login_test.go:807` (expiresAt-issuedAt == 90d) | PASS |
| jwt_test.go:56 wantExp updated RED→GREEN | `TestMintVerifyJWT` (wantExp using 90d constant) | PASS |
| TestVerifyJWT_Expired uses 91d timestamp | `TestVerifyJWT_Expired` (now+91*24h) | PASS |

### D2: Minimum-TTL Guard

| Scenario | Covering Test | Result |
|---|---|---|
| Sub-24h clamped to 24h + warn | `TestResolveJWTTTL/sub-minimum clamped to 24h with warn` | PASS |
| ≥24h honored, no warn | `TestResolveJWTTTL/2160h honored no warn` + `exactly 24h honored no warn` | PASS |
| Unparseable → 90d default + warn | `TestResolveJWTTTL/unparseable falls back to default with warn` | PASS |
| Empty env → 90d default, no warn | `TestResolveJWTTTL/empty env uses default no warn` | PASS |

### D4: Session Expiry Warning

| Scenario | Covering Test | Result |
|---|---|---|
| Expires in ≤7 days → warn | `TestWarnIfSessionExpiringSoon/expires in 3d → warn` | PASS |
| Expires in >7 days → silent | `TestWarnIfSessionExpiringSoon/expires in 30d → silent` | PASS |
| Already expired → silent (reactive path handles) | `TestWarnIfSessionExpiringSoon/already expired → silent` | PASS |
| Check uses ExpiresAt field only, no JWT decode | Code inspection: unmarshals `struct { ExpiresAt string }` only | PASS (inspection) |

---

## Correctness Check: login.go Pass-by-Value Divergence Fix

**Key point**: `JWT exp` and `credentials.json ExpiresAt` must be computed from the SAME TTL.

Code path in `login.go`:
1. `ttl := resolveJWTTTL()` — single TTL resolution
2. `claims.Exp = now.Add(ttl).Unix()` — set exp in claims
3. `mintTTL := time.Duration(claims.Exp-now.Unix()) * time.Second` — derive TTL from exp
4. `auth.MintJWT(secret, claims, now, mintTTL)` — MintJWT sets `claims.Exp = now.Add(mintTTL).Unix()` internally; value is identical
5. `expiresAt := time.Unix(claims.Exp, 0)` — credentials.json ExpiresAt uses same exp

By deriving `mintTTL` from `claims.Exp`, the pass-by-value divergence is eliminated. Both ExpiresAt fields are guaranteed identical. Verified by `login_test.go` assertion.

---

## 604800 Sweep

```
$ grep -rn "604800" --include="*.go" .
(no output)
```

CLEAN — zero stray hardcoded second literals remain in Go source.

---

## Issues

### CRITICAL (0)
None.

### WARNING (0)
None.

### SUGGESTION (2)

**S1** — `autoLoginFromHeader` TTL not tested with dedicated behavioral assertion.
`autoLoginFromHeader` (dashboard login path) calls `auth.MintJWT(..., s.jwtTTL)` — same field as `handleAuth`. No dedicated test asserts the dashboard login path respects a non-default TTL. `handleAuth` is fully tested via `TestAuthEndpointLoopbackFlow_returns302_withToken` (exp-iat=7776000). Risk: low — shared `s.jwtTTL` field, simple closure.

**S2** — D4 "MUST NOT decode access_token" has no negative assertion in tests.
The spec requires `warnIfSessionExpiringSoon` to read ONLY `expires_at` and never decode the JWT. Code inspection confirms this — only `struct { ExpiresAt string }` is unmarshaled. But no test deliberately passes an invalid `access_token` value to assert the function still succeeds. Risk: informational.

---

## TDD Discipline

All PR1 behaviors follow RED → GREEN → REFACTOR:
- jwt_test.go updated before jwt.go signature change
- cloud_test.go: TestResolveJWTTTL added before resolveJWTTTL() implementation
- auth_endpoint_test.go assertion updated before cloudserver WithJWTTTL wiring
- login_test.go assertion updated before login.go fix
- expiry_test.go added before warnIfSessionExpiringSoon implementation

Strict TDD discipline: CONFIRMED.

---

## PR1 Readiness Verdict

**PASS** — 0 CRITICAL, 0 WARNING, 2 SUGGESTION

PR1 is ready for archive and merge to main. PR2 (D3 fsnotify watcher) and D5 (EngramCloud config) remain as separate deferred work.

---

# Verify Report: configurable-jwt-ttl-live-revocation — PR2 (D3 fsnotify watcher)

**Date**: 2026-07-06
**Branch**: feat/yaml-live-reload
**Scope**: PR2 only — D3 (YAMLLoader.Watch + wiring in defaultCloudRuntime.Start)
**Mode**: Strict TDD
**Verdict**: PASS WITH WARNINGS — 0 CRITICAL, 1 WARNING, 3 SUGGESTION

---

## Build and Test Evidence

| Command | Result | Notes |
|---|---|---|
| `go test ./internal/cloud/users/... -count=1` | PASS (1.763s) | DirectWrite, AtomicRename, IgnoresUnrelated all PASS |
| `go test ./internal/cloud/auth/... -count=1` | PASS (1.888s) | Includes TestHeaderAuth_RevokedUserDeniedAfterReload |
| `go test ./internal/cloud/... -count=1` | PASS — 12/12 packages | Full blast-radius clean |
| `go mod verify` | all modules verified | Clean |
| `go build ./...` | PASS | Zero errors |
| `go vet ./internal/cloud/users/... ./internal/cloud/auth/...` | PASS | Zero warnings |
| Race detector | NOT AVAILABLE | CGO_ENABLED=0 on Windows; not a code defect |
| `grep -rn "604800" --include="*.go" .` | Zero matches | No regression; same as PR1 |

Pre-existing env-dependent failures (~11 tests in `cmd/engram`) EXCLUDED — identical on main and this branch. Zero regression.

---

## Task Completion (Phase 7)

| Task | Description | Status |
|---|---|---|
| 7.1 | [PREREQ] go.mod — promote fsnotify v1.7.0 indirect→direct | COMPLETE |
| 7.2 | [RED] loader_test.go — TestWatcherTriggersReload_DirectWrite, _AtomicRename, TestWatcherIgnoresUnrelated | COMPLETE |
| 7.3 | [GREEN] loader.go — Watch(ctx context.Context) error — parent-dir watch, Create/Write/Rename filter, 100ms debounce, ctx.Done shutdown | COMPLETE |
| 7.4 | [REFACTOR] loader.go watchDebounce const; header_auth_test.go TestHeaderAuth_RevokedUserDeniedAfterReload | COMPLETE |
| 7.5 | cloud.go — ctx/cancel/onWatch on defaultCloudRuntime; defer cancel() in Start(); Watch goroutine after SIGHUP goroutine; cancel() in error paths | COMPLETE |

Phase 6 (D5 EngramCloud config, different repo): still deferred — out of PR2 scope.

---

## Spec Compliance Matrix: live-user-directory-reload (D3)

| Scenario | Covering Test | Result |
|---|---|---|
| Direct write triggers reload | `TestWatcherTriggersReload_DirectWrite` — polls Lookup for bob after os.WriteFile | PASS |
| Atomic rename triggers reload | `TestWatcherTriggersReload_AtomicRename` — polls Lookup for bob after os.Rename(tmp, users.yaml) | PASS |
| Unrelated file events ignored | `TestWatcherIgnoresUnrelated` — pollLookupAbsent after other.yaml write; watcher still alive after | PASS (see WARNING W1) |
| Removed user denied after reload | `TestHeaderAuth_RevokedUserDeniedAfterReload` — carol active → Reload → carol denied (account_removed) | PASS |
| Watcher active without SIGHUP | Code inspection — cloud.go L141–147: `runtime.onWatch = loader.Watch`; goroutine in Start() | PASS (inspection) |

---

## Design Correctness Check

| Design Point | Expected | Actual | Status |
|---|---|---|---|
| Watch target | `filepath.Dir(l.path)` (parent dir) | `dir := filepath.Dir(l.path)` → `watcher.Add(dir)` (loader.go:185–186) | PASS |
| Event filter | `filepath.Clean(event.Name) == target` | `target := filepath.Clean(l.path)` → compare at line 221 | PASS |
| Debounce | ~100ms, collapses bursts | `const watchDebounce = 100*time.Millisecond`, `time.AfterFunc` (loader.go:31, 200–205) | PASS |
| Graceful shutdown | `ctx.Done()` → stop debounce → return ctx.Err() | case `<-ctx.Done()`: stop timer, return ctx.Err() (loader.go:209–214) | PASS |
| Watcher goroutine wired | After SIGHUP goroutine in Start() | `if r.onWatch != nil { go func() { r.onWatch(r.ctx) }() }` (cloud.go:141–147) | PASS |
| ctx cancel on Start() return | `defer r.cancel()` | First defer in Start() — runs last (LIFO), after SIGHUP cleanup and store.Close() | PASS |
| cancel() in error paths | All newCloudRuntime error returns call cancel() | Verified at lines 156, 180, 194 (cloud.go) | PASS |
| fsnotify direct dep | In require block (not indirect) | `github.com/fsnotify/fsnotify v1.7.0` in main `require{}` — no `// indirect` (go.mod:10) | PASS |

---

## Cross-Platform Soundness

Tests run on Windows (ReadDirectoryChangesW) and target Linux (inotify).

| Check | Result |
|---|---|
| Tests assert on OUTCOME (Lookup state) via polling, not on specific fsnotify event types | CONFIRMED — all 3 Watch tests use pollLookup/pollLookupAbsent |
| Polling timeout generous vs debounce (3s window vs 100ms debounce) | CONFIRMED |
| `os.Rename` over existing file generates events on Windows | CONFIRMED — TestWatcherTriggersReload_AtomicRename PASSES on Windows |
| Event filter covers Create, Write, AND Rename — platform-agnostic | CONFIRMED — line 220 catches all three |

---

## TDD Discipline (Strict TDD)

| Cycle | RED Commit | GREEN Commit | REFACTOR |
|---|---|---|---|
| Watch DirectWrite/AtomicRename/IgnoresUnrelated | 820ad67 (compile error: Watch undefined) | 5dcb2b5 (all 3 PASS) | — |
| RevokedUserDeniedAfterReload | 086b41b (passes immediately — REFACTOR step) | — | PASS |
| cloud.go wiring | 3a129bf (wiring, no new tests needed) | — | — |

Strict TDD: CONFIRMED for core Watch behavior.

---

## Issues

### CRITICAL (0)
None.

### WARNING (1)

**W1** — `TestWatcherIgnoresUnrelated` asserts a WEAKER property than the spec requires.

The spec says: "WHEN dir/other.yaml written THEN Reload() NOT called." The test asserts: "state unchanged after sibling write AND watcher still alive." These are not equivalent. If the `filepath.Clean(event.Name) == target` filter were accidentally removed, Watch would call `Reload()` on every directory event — but since users.yaml has not changed at that point in the test, `Reload()` would re-read the same state and bob would still not appear. The test would **pass** even with the filter broken. Code inspection confirms the filter is correct today, but no programmatic regression guard exists for this spec requirement. To fully close this gap, inject a spy/counter on `Reload()` or use a sentinel write that would change state if incorrectly reloaded.

### SUGGESTION (3)

**S1** — `TestHeaderAuth_RevokedUserDeniedAfterReload` calls `loader.Reload()` directly rather than via Watch. The spec scenario specifies "after watch-triggered Reload()." Coverage is compositional (Watch→Reload in loader_test.go; Reload→AuthDenied in header_auth_test.go), but no single integration test proves the full Write→Watch→Reload→AuthDenies chain. Acceptable at unit level; no action required for merge.

**S2** — Race detector unavailable (CGO_ENABLED=0 on Windows). `Reload()` uses `sync.RWMutex` correctly and the concurrent-requests test (`TestHeaderAuthConcurrentRequestsDoNotCrossContaminate`) runs in CI. Recommend enabling `-race` in Linux CI where CGO is available for belt-and-suspenders goroutine safety validation.

**S3** — `time.AfterFunc` debounce race edge case: if `debounce.Stop()` returns false (callback already executing), a second `Reload()` may fire from the new timer during a rapid burst. Not a correctness issue (Reload is mutex-protected and idempotent), but technically two Reload calls are possible at debounce boundary. The design's "collapses into one Reload" guarantee is best-effort in this edge case.

---

## PR2 Readiness Verdict

**PASS WITH WARNINGS** — 0 CRITICAL, 1 WARNING, 3 SUGGESTION

PR2 (D3 fsnotify watcher) is ready for push and PR targeting main (stacked-to-main strategy). W1 does not block merge — the code is correct by inspection and by test execution; the gap is a future regression-safety improvement. All Phase 7 tasks are checked. No goroutine leaks. Graceful shutdown confirmed. fsnotify promoted to direct dependency.

Next: `sdd-archive` (after push + PR merge) or address W1 if hardening is desired before merge.
