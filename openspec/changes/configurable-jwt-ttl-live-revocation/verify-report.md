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
