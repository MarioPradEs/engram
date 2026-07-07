# Configurable JWT TTL Specification

## Change metadata

- Change: configurable-jwt-ttl-live-revocation
- Capability: configurable-jwt-ttl
- Kind: ADDITIVE (new behavior for JWT lifetime configuration)
- REQ range: REQ-001 through REQ-002

## Purpose

JWT lifetime is now configurable via the `ENGRAM_JWT_TTL` environment variable with a sensible default of 90 days (7776000 seconds). A minimum of 24 hours guards against tokens shorter than the dashboard session envelope. All hardcoded `604800` second constants have been removed.

---

## ADDED Requirements

### REQ-001: MintJWT Accepts Explicit TTL Parameter

The `MintJWT` function MUST accept a `ttl time.Duration` parameter and compute the expiration claim as `exp = iat + int64(ttl.Seconds())`. The hardcoded `jwtExpSeconds` package constant MUST be removed from `internal/cloud/auth/jwt.go`. All call sites that previously used the hardcoded constant MUST be updated to accept and pass the configured TTL.

**Scenarios**:

- **Default TTL when ENGRAM_JWT_TTL is unset**: GIVEN `ENGRAM_JWT_TTL` is not present in the environment, WHEN the server resolves the TTL and calls `MintJWT`, THEN `claims.Exp == now.Unix() + 7776000` (90 days in seconds).
- **Custom TTL is honored**: GIVEN `ENGRAM_JWT_TTL=48h`, WHEN `MintJWT` is called with the resolved TTL, THEN `claims.Exp == now.Unix() + 172800` (48 hours in seconds).
- **login.go uses configured TTL**: GIVEN the CLI authentication path in `login.go:397` previously hardcoded 604800, WHEN the TTL is resolved and passed to `MintJWT`, THEN `claims.Exp = now.Unix() + int64(ttl.Seconds())` for the configured TTL, NOT a hardcoded 604800.
- **Test expectations updated for 90-day default**: GIVEN existing tests like `jwt_test.go:56`, WHEN the test is run with the new TTL signature, THEN assertions on `wantExp` use the 90-day default (7776000 seconds).
- **Token expiry tested at 91+ days**: GIVEN `TestVerifyJWT_Expired`, WHEN a token is minted with TTL=90d and verified at `now.Add(91*24*time.Hour)`, THEN `VerifyJWT` returns an expiry error.

---

### REQ-002: Minimum-TTL Guard with Environment Parsing

The system MUST read `ENGRAM_JWT_TTL` as a `time.Duration` string, clamp values below 24 hours to exactly 24 hours (with a warning to stderr), and fall back to the 90-day default if parsing fails (with a warning to stderr). The system MUST NOT refuse to start on a malformed or sub-minimum TTL value.

**Scenarios**:

- **Sub-24h value is clamped to 24h with warning**: GIVEN `ENGRAM_JWT_TTL=1h`, WHEN the server initializes, THEN the effective TTL is 24 hours (86400 seconds), AND a warning message is written to stderr, AND the server continues to start normally.
- **Value at or above 24h is honored as-is**: GIVEN `ENGRAM_JWT_TTL=48h`, WHEN the server initializes, THEN the effective TTL is 48 hours, AND no warning is written to stderr.
- **Unparseable value falls back to 90d default**: GIVEN `ENGRAM_JWT_TTL=notaduration`, WHEN the server initializes, THEN the effective TTL is 90 days (7776000 seconds), AND a warning message is written to stderr.
- **Empty or missing value uses 90d default silently**: GIVEN `ENGRAM_JWT_TTL` is not set, WHEN the server initializes, THEN the effective TTL is 90 days, AND no warning is written to stderr.
- **User-specified value within valid range is used without warning**: GIVEN `ENGRAM_JWT_TTL=2160h` (exactly 90 days), WHEN the server initializes, THEN the effective TTL is 2160 hours, AND no warning is written to stderr.

---

## Test Seam Summary

| REQ | Test name(s) |
|-----|-------------|
| REQ-001 | `TestMintVerifyJWT`, `TestVerifyJWT_Expired`, `TestMintJWT_SecretTooShort`, `TestAuthEndpointLoopbackFlow_returns302_withToken`, `TestLoginHappyPath` |
| REQ-002 | `TestResolveJWTTTL_empty_env_uses_default_no_warn`, `TestResolveJWTTTL_2160h_honored_no_warn`, `TestResolveJWTTTL_sub_minimum_clamped_to_24h_with_warn`, `TestResolveJWTTTL_unparseable_falls_back_to_default_with_warn`, `TestResolveJWTTTL_exactly_24h_honored_no_warn` |
