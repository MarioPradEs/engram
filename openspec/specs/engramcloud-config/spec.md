# EngramCloud Config Alignment Specification

## Change metadata

- Change: configurable-jwt-ttl-live-revocation
- Capability: engramcloud-config
- Kind: ADDITIVE (deployment configuration that aligns with JWT default)
- REQ range: REQ-003 through REQ-004

## Purpose

EngramCloud deployment configuration is aligned with the new 90-day JWT default to ensure the outer session envelope (oauth2-proxy cookie lifetime) does not expire before the inner JWT token. This spec contains acceptance criteria only — not TDD-tested in the main repository. Verification is manual at deploy time.

---

## ADDED Requirements

### REQ-003: oauth2-proxy Cookie Lifetime Aligned to 90 Days

The `oauth2-proxy.cfg` configuration file in the EngramCloud repository MUST set `cookie_expire` to `2160h` (90 days). The previous value of `168h` (7 days) was shorter than the JWT lifetime and caused premature browser session expirations.

**Acceptance Criteria**:

- GIVEN the EngramCloud repository's `oauth2-proxy/oauth2-proxy.cfg` file
- WHEN the file is inspected
- THEN `cookie_expire = 2160h` is present and the previous value `168h` is removed

---

### REQ-004: Cloud Service Declares ENGRAM_JWT_TTL Environment Variable

The cloud service entry in `docker-compose.yml` MUST declare `ENGRAM_JWT_TTL` in its `environment:` block, set to the 90-day equivalent (either `7776000s` or `2160h`). This ensures the Engram server process resolves the same lifetime as the oauth2-proxy cookie, keeping both session envelopes synchronized.

**Acceptance Criteria**:

- GIVEN the EngramCloud repository's `docker-compose.yml` file
- WHEN the cloud service `environment:` block is inspected
- THEN `ENGRAM_JWT_TTL` is present and set to either `7776000s` or `2160h` (or equivalent 90-day duration string)

---

## Non-Goals

- No automated test coverage required for config files (verification is by file inspection and deploy-time observation).
- D5 configuration can be applied and reverted independently of the fork's Go code changes.
- The 8-hour dashboard session envelope (`oauth2-proxy` `session_cookie_minimal`) is NOT changed by this spec.
