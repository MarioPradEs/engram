# Session Expiry Warning Specification

## Change metadata

- Change: configurable-jwt-ttl-live-revocation
- Capability: session-expiry-warning
- Kind: ADDITIVE (new proactive user advisory at startup)
- REQ range: REQ-008 through REQ-009

## Purpose

At `serve` and `mcp` startup, the CLI proactively warns the user to stderr when the stored session's expiration is approaching (within 7 days). The function reads the `ExpiresAt` field from `credentials.json` directly — no JWT decoding is performed. This is a non-fatal advisory that informs operators of upcoming credential renewal needs.

---

## ADDED Requirements

### REQ-008: Proactive Expiry Warning at Startup

The system MUST call `warnIfSessionExpiringSoon` from the startup path shared by both `serve` and `mcp` commands (typically `tryStartAutosync` or an equivalent function called before the main server loop). When the `credentials.json` file's `expires_at` field is within 7 days of the current time, the function MUST write a warning message to stderr (or a provided io.Writer). When `expires_at` is more than 7 days away, the function MUST NOT write any output. The function MUST NOT return an error or block startup; it is purely advisory.

**Scenarios**:

- **Session expires in ≤7 days — warning printed**: GIVEN `credentials.json` has `expires_at = now + 3 days` (RFC3339 format), WHEN `warnIfSessionExpiringSoon` is called at startup, THEN a warning message is written to stderr mentioning the imminent expiry, AND startup continues normally.
- **Session expires in >7 days — no warning**: GIVEN `credentials.json` has `expires_at = now + 30 days`, WHEN `warnIfSessionExpiringSoon` is called at startup, THEN nothing is written to stderr and the function returns silently.
- **Session already expired — reactive path applies**: GIVEN `credentials.json` has `expires_at` in the past, WHEN the startup proceeds, THEN `warnIfSessionExpiringSoon` does NOT write a proactive warning (the existing `errCredentialsExpired` reactive error path produces an error message instead).
- **No credentials file — function handles gracefully**: GIVEN `credentials.json` does not exist, WHEN `warnIfSessionExpiringSoon` is called, THEN the function does not crash; it either returns silently or logs a debug message and continues.

---

### REQ-009: No JWT Token Decoding

The `warnIfSessionExpiringSoon` function MUST read and parse only the `expires_at` field from `credentials.json` (the RFC3339 timestamp stored as a string field on the `credentialFile` struct). It MUST NOT decode, validate, or verify the JWT in the `access_token` field. It MUST NOT inspect JWT claims, signatures, or expiration claims; it relies solely on the explicitly stored `expires_at` metadata.

**Scenarios**:

- **Warning uses ExpiresAt field, not JWT exp claim**: GIVEN a valid `credentials.json` with `expires_at = now + 2 days` and a JWT with a different `exp` claim, WHEN `warnIfSessionExpiringSoon` runs, THEN the 7-day threshold check is based solely on parsing `expires_at`, and the JWT is left untouched.
- **Invalid JWT does not break the warning**: GIVEN `credentials.json` with a malformed or expired JWT in `access_token` but a valid `expires_at` field, WHEN `warnIfSessionExpiringSoon` is called, THEN the function reads and checks only `expires_at`, ignoring the JWT, and produces the appropriate warning.

---

## Test Seam Summary

| REQ | Test name(s) |
|-----|-------------|
| REQ-008 | `TestWarnIfSessionExpiringSoon_Expires3d`, `TestWarnIfSessionExpiringSoon_30d`, `TestWarnIfSessionExpiringSoon_AlreadyExpired` |
| REQ-009 | `TestWarnIfSessionExpiringSoon_*` (code inspection verifies no JWT decode; no invalid-JWT test needed beyond normal test hygiene) |
