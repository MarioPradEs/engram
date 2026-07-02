# Self-Service Share Specification

## Purpose

Defines the triage "share with Viva" and "stop sharing" actions that let a user
enroll or un-enroll the current working directory's project from cloud sync
entirely from triage, without admin mediation.

---

## Requirements

### Requirement: Triage Share Action

The triage server MUST expose a `POST /project/{name}/share` route that, when
invoked for the cwd project, atomically:

1. Writes `default_scope=shared` to `.engram/config.json` for the cwd project.
2. Client-enrolls the project via `store.EnrollProject(project)`.
3. Calls the server-side self-service enroll endpoint with the caller's JWT.

If any step fails the handler MUST abort, roll back any step already applied,
return a clear error message, and leave the project in its pre-share state.

The route MUST enforce the cwd-project boundary (Option A): `name` MUST equal
`s.cwdProject`; requests for other projects MUST be rejected with HTTP 400.

#### Scenario: Happy path — share a folder

- GIVEN the user is logged in (valid `credentials.json` JWT available)
- AND the cwd project is enrolled-eligible (non-empty, non-"personal")
- AND the project is not already enrolled
- WHEN the user clicks "Share with Viva" in triage
- THEN `POST /project/{name}/share` executes all three steps atomically
- AND the triage UI shows a success confirmation
- AND subsequent observations in that folder sync to the cloud

#### Scenario: Not logged in at share time

- GIVEN no valid JWT is present in `credentials.json` (absent or expired)
- WHEN the user triggers "Share with Viva"
- THEN the handler returns a user-visible error: "Not logged in — please run engram auth login first"
- AND no local enrollment or config change is written
- AND the project remains unenrolled

#### Scenario: Server unreachable mid-share

- GIVEN local enrollment succeeds (step 2 written to SQLite)
- AND `default_scope=shared` written to `config.json` (step 1)
- BUT the HTTP call to the server-side enroll endpoint times out or returns an error
- THEN the handler rolls back: removes the client enrollment, reverts `default_scope` to its prior value
- AND returns a user-visible error describing the failure
- AND the project is left in its pre-share state (unenrolled, prior scope)

#### Scenario: Project already enrolled (idempotent re-share)

- GIVEN the project is already enrolled both client-side and server-side
- WHEN the user triggers "Share with Viva" again
- THEN the handler returns success (idempotent)
- AND no duplicate enrollment entries are created

#### Scenario: cwd boundary enforcement

- GIVEN the triage server's `cwdProject` is "android-game-perf-tool-desktop"
- WHEN a request arrives for `POST /project/other-project/share`
- THEN the handler returns HTTP 400 with "project mismatch — share only applies to the current folder's project"
- AND no enrollment or config change is written

---

### Requirement: Triage Unshare Action

The triage server MUST expose a `POST /project/{name}/unshare` route that, when
invoked for the cwd project:

1. Removes the project from the client-side enrollment (`sync_enrolled_projects`).
2. Calls the server-side self-service un-enroll endpoint with the caller's JWT.
3. Reverts `default_scope` in `.engram/config.json` to `private`.

The unshare action MUST be atomic with the same rollback guarantee as share.
Already-synced observations MUST NOT be deleted from the cloud; the action only
prevents future sync for this project.

The route MUST enforce the cwd-project boundary; `name` MUST equal `s.cwdProject`.

#### Scenario: Happy path — stop sharing a folder

- GIVEN the cwd project is currently enrolled and `default_scope=shared`
- AND the user is logged in
- WHEN the user clicks "Stop Sharing" in triage
- THEN the project is removed from `sync_enrolled_projects`
- AND the server-side enrolled list for this user no longer contains this project
- AND `.engram/config.json` `default_scope` is set to `private`
- AND the triage UI shows a confirmation: "Folder unshared — already-synced data remains in the cloud"

#### Scenario: Unsharing stops future sync but preserves existing cloud data

- GIVEN observations O1, O2, O3 were previously synced to the cloud under project P
- AND the user unshares project P
- WHEN the next autosync push runs
- THEN no new mutations for project P are pushed
- AND observations O1, O2, O3 remain accessible in the cloud (not deleted)

#### Scenario: Unshare when already unshared (idempotent)

- GIVEN the project is already unenrolled
- WHEN the user triggers "Stop Sharing"
- THEN the handler returns success (idempotent)
- AND no error is surfaced to the user

---

### Requirement: Enrollment Store Seam in Triage Server

The `triage.Server` struct MUST depend on an `EnrollmentStore` interface
(not directly on `*store.Store`) to allow enrollment operations to be injected
and independently tested.

The interface MUST expose at minimum:
- `EnrollProject(project string) error`
- `UnenrollProject(project string) error` (if unshare is in scope)

#### Scenario: Enrollment store is a required dependency at triage startup

- GIVEN `cmdTriage` is wiring the triage server
- WHEN the server is constructed without an `EnrollmentStore`
- THEN the constructor returns an error or panics with a clear message
- AND the triage server does not start

---

### Requirement: JWT Wiring at Triage Startup

The `cmdTriage` startup MUST read the user's credentials (JWT) from
`credentials.json` at the configured `credDir` before the triage server starts.

If the credentials file is absent or expired, the triage server MUST start but
MUST surface a visible warning in the triage UI indicating "Not logged in — share
actions will fail until you run engram auth login."

#### Scenario: JWT absent at triage startup — warning shown, server still starts

- GIVEN `credentials.json` is absent or contains an expired token
- WHEN the user starts the triage server
- THEN the server starts successfully
- AND the triage dashboard displays a persistent warning: "Not logged in"
- AND all non-share routes function normally

#### Scenario: JWT present — share actions available

- GIVEN `credentials.json` is present with a valid, non-expired token
- WHEN the triage server starts
- THEN no credential warning is shown
- AND share/unshare routes are available
