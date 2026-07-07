# Configurable JWT TTL Specification (D1 + D2)

## Purpose

JWT lifetime is driven by `ENGRAM_JWT_TTL` (default 90d = 7776000s). Both hardcoded
`604800` constants are removed. A 24h minimum floor guards against tokens shorter than
the dashboard session envelope.

## Requirements

### Requirement: MintJWT Accepts Explicit TTL

`MintJWT` MUST accept a `ttl time.Duration` parameter and set `exp = iat + int64(ttl.Seconds())`.
The `jwtExpSeconds = 604800` package-level constant MUST be removed from `jwt.go`.
The fallback at `login.go:397` (`claims.Exp = now.Unix() + 604800`) MUST be replaced with
the same configured TTL value.

(No previously existing spec — this is a full new requirement.)

#### Scenario: Default TTL when ENGRAM_JWT_TTL is unset

- GIVEN `ENGRAM_JWT_TTL` is not present in the environment
- WHEN the server resolves the TTL and calls `MintJWT(secret, claims, ttl, now)`
- THEN `claims.Exp == now.Unix() + 7776000`

#### Scenario: Custom TTL is honored

- GIVEN `ENGRAM_JWT_TTL=48h`
- WHEN `MintJWT` is called with the resolved (clamped) TTL
- THEN `claims.Exp == now.Unix() + 172800`

#### Scenario: login.go:397 placeholder uses configured TTL (not 604800)

- GIVEN `claims.Exp == 0` in the CLI test/placeholder mint path
- WHEN `ENGRAM_JWT_TTL` resolves to 90d (7776000s)
- THEN `claims.Exp = now.Unix() + 7776000`, NOT `now.Unix() + 604800`

#### Scenario: jwt_test.go:56 wantExp updated (RED → GREEN)

- GIVEN the existing `TestMintVerifyJWT` calls `MintJWT` with the new `ttl` argument set to 90d
- WHEN `wantExp` is evaluated
- THEN the assertion reads `now.Unix() + int64((90*24*time.Hour).Seconds())`, not `now.Unix() + 604800`

#### Scenario: TestVerifyJWT_Expired uses post-90d timestamp (RED → GREEN)

- GIVEN `TestVerifyJWT_Expired` mints a token with ttl=90d
- WHEN expiry is checked at `now.Add(91 * 24 * time.Hour)` (past 90d)
- THEN `VerifyJWT` returns an expiry error

---

### Requirement: Minimum-TTL Guard

The system MUST read `ENGRAM_JWT_TTL` in `cmd/engram/cloud.go`, parse it as `time.Duration`,
clamp values below 24h to exactly 24h, and write a warning to stderr. The system MUST NOT
return an error or refuse to start. Unparseable values MUST fall back to 90d with a
stderr warning.

#### Scenario: Sub-24h value is clamped to 24h

- GIVEN `ENGRAM_JWT_TTL=1h`
- WHEN the server initializes and reads the TTL
- THEN effective TTL is 24h (86400 seconds)
- AND a warning is written to stderr (startup continues normally)

#### Scenario: Value at or above 24h is honored as-is

- GIVEN `ENGRAM_JWT_TTL=48h`
- WHEN the server initializes
- THEN effective TTL is 48h; no warning is written to stderr

#### Scenario: Unparseable value falls back to 90d default

- GIVEN `ENGRAM_JWT_TTL=notaduration`
- WHEN the server initializes
- THEN effective TTL is 90d (7776000 seconds)
- AND a warning is written to stderr

## Test File Targets

| Test | File | Change |
|------|------|--------|
| `TestMintVerifyJWT` | `internal/cloud/auth/jwt_test.go:10` | Add `ttl` arg; update `wantExp` |
| `TestVerifyJWT_Expired` | `internal/cloud/auth/jwt_test.go:73` | Update expiry check to 91d |
| `TestMintJWT_SecretTooShort` | `internal/cloud/auth/jwt_test.go:65` | Add `ttl` arg (error fires before TTL use) |
| TTL guard tests | `cmd/engram/cloud_test.go` (new) | Cover D2 clamp + warn scenarios |
