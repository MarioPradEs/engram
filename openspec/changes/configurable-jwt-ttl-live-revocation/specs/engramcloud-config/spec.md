# EngramCloud Config Alignment Specification (D5)

## Purpose

Deploy config in the EngramCloud repository is updated to align with the 90-day JWT
default. This spec contains acceptance criteria only — not TDD-tested. Verification
is manual at deploy time.

## Requirements

### Requirement: oauth2-proxy Cookie Lifetime Aligned to 90d

`cookie_expire` in `oauth2-proxy.cfg` MUST be `2160h` (90 days). The previous value
was `168h` (7 days). The outer session envelope governs how long the browser cookie
survives; setting it shorter than the JWT would cause premature dashboard logouts.

#### Acceptance Criterion: cookie_expire value

- GIVEN the EngramCloud repository at `oauth2-proxy.cfg`
- WHEN the file is inspected
- THEN `cookie_expire = 2160h` is present

### Requirement: Cloud Service Declares ENGRAM_JWT_TTL

The cloud service entry in `docker-compose.yml` MUST declare `ENGRAM_JWT_TTL` in its
environment block, set to the 90-day equivalent (`7776000s` or `2160h`). This ensures
the Engram server process uses the same lifetime as the proxy cookie.

#### Acceptance Criterion: ENGRAM_JWT_TTL in compose environment

- GIVEN the EngramCloud repository at `docker-compose.yml`
- WHEN the cloud service `environment:` block is inspected
- THEN `ENGRAM_JWT_TTL` is set to `7776000s` (or an equivalent 90-day duration string)

## Non-Goals

- No automated test coverage for this spec (config files only).
- D5 can be applied and reverted independently of the fork's Go changes.
- The 8h dashboard session envelope (`oauth2-proxy` `session_cookie_minimal`) is NOT
  changed by this spec.
