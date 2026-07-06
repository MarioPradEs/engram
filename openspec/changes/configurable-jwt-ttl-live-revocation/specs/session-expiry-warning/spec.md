# Session Expiry Warning Specification (D4)

## Purpose

At `serve` and `mcp` startup, the CLI proactively warns on stderr when the stored
session expires within 7 days. The function reads `ExpiresAt` (RFC3339 string) from
`credentials.json` directly — no JWT decode. This is a non-fatal advisory only.

## Requirements

### Requirement: Proactive Expiry Warning at Startup

The system MUST call `warnIfSessionExpiringSoon(w io.Writer, now time.Time)` from
`tryStartAutosync` (or the equivalent startup path shared by `serve` and `mcp`).
When `credentials.json` `expires_at` is within 7 days of `now`, the function MUST
write a warning to `w` (stderr in production). When `expires_at` is more than 7 days
away, the function MUST NOT write any output. The function MUST NOT return an error or
block startup.

#### Scenario: Session expires in ≤7 days — warning printed

- GIVEN `credentials.json` has `expires_at = now + 3 days` (RFC3339)
- WHEN `tryStartAutosync` calls `warnIfSessionExpiringSoon`
- THEN a warning message is written to stderr mentioning the imminent expiry
- AND startup continues normally

#### Scenario: Session expires in >7 days — no warning

- GIVEN `credentials.json` has `expires_at = now + 30 days`
- WHEN `tryStartAutosync` calls `warnIfSessionExpiringSoon`
- THEN nothing is written to stderr

#### Scenario: Session already expired — reactive path applies, not proactive warning

- GIVEN `credentials.json` has `expires_at` in the past
- WHEN the startup proceeds
- THEN `warnIfSessionExpiringSoon` does NOT write a proactive warning
- AND the existing `errCredentialsExpired` reactive path produces
  `"Session expired. Run 'engram login' to re-authenticate."`

### Requirement: No JWT Decode

`warnIfSessionExpiringSoon` MUST read only `credentials.json` `expires_at` (the
RFC3339 string field on `credentialFile`). It MUST NOT decode or verify the JWT in
`access_token`.

#### Scenario: Warning uses ExpiresAt field, not JWT exp claim

- GIVEN a valid `credentials.json` with `expires_at = now + 2 days`
- WHEN `warnIfSessionExpiringSoon` runs
- THEN the 7-day check is based solely on parsing `expires_at`, not on decoding the JWT

## Test File Targets

| Test | File | Type |
|------|------|------|
| Warn when expires in 3 days | `cmd/engram/expiry_test.go` | New test |
| Silent when expires in 30 days | `cmd/engram/expiry_test.go` | New test |
| No proactive warn when already expired | `cmd/engram/expiry_test.go` | New test |

Note: The two existing tests in `expiry_test.go` cover `cloudSyncFailureMessage` (the
*reactive* 401 path). New tests cover `warnIfSessionExpiringSoon` (the *proactive*
startup path) and MUST use an injected `io.Writer` and `now time.Time` for
determinism — no real filesystem or clock dependency.
