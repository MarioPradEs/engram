# Self-Service Server Enrollment Specification

## Purpose

Defines the server-side HTTP endpoint that allows an authenticated user to add
or remove their OWN project enrollments in `users.yaml`, without admin
mediation and without touching any other user's data.

---

## Requirements

### Requirement: POST /user/enrolled-projects — Add Enrollment

The cloud server MUST expose `POST /user/enrolled-projects` authenticated via
JWT Bearer (`withAuth` middleware).

When called with a valid JWT and a `project` name in the request body:
1. The handler MUST read the authenticated user's `Principal` from `users.yaml`.
2. MUST append the project to the user's `Enrolled` list if not already present.
3. MUST write the updated `users.yaml` atomically (write-rename pattern).
4. MUST reload the in-memory user directory after a successful write.
5. MUST return HTTP 200 on success.

The handler MUST NOT read or modify any other user's `Principal`.

#### Scenario: Happy path — user self-enrolls a project

- GIVEN user alice@viva.com is authenticated (valid JWT)
- AND `"android-game-perf-tool-desktop"` is NOT in alice's `Enrolled` list
- WHEN `POST /user/enrolled-projects` is called with `{ "project": "android-game-perf-tool-desktop" }`
- THEN alice's `Enrolled` in `users.yaml` now contains `"android-game-perf-tool-desktop"`
- AND the in-memory user directory reflects the change for subsequent requests
- AND the response is HTTP 200

#### Scenario: Project already enrolled (idempotent)

- GIVEN `"android-game-perf-tool-desktop"` IS already in alice's `Enrolled` list
- WHEN `POST /user/enrolled-projects` is called with the same project
- THEN the handler returns HTTP 200
- AND no duplicate entry is created in `Enrolled`

#### Scenario: Unauthenticated request is rejected

- GIVEN no `Authorization: Bearer` header is present
- WHEN `POST /user/enrolled-projects` is called
- THEN the server returns HTTP 401
- AND `users.yaml` is not modified

#### Scenario: Invalid JWT is rejected

- GIVEN the `Authorization: Bearer` token is malformed or expired
- WHEN `POST /user/enrolled-projects` is called
- THEN the server returns HTTP 401
- AND `users.yaml` is not modified

#### Scenario: Empty or invalid project name is rejected

- GIVEN alice is authenticated
- WHEN `POST /user/enrolled-projects` is called with `{ "project": "" }`
- THEN the server returns HTTP 400 with a descriptive error
- AND `users.yaml` is not modified

---

### Requirement: DELETE /user/enrolled-projects — Remove Enrollment

The cloud server MUST expose `DELETE /user/enrolled-projects` (or equivalent
`POST /user/enrolled-projects/remove`) authenticated via JWT Bearer.

When called with a valid JWT and a `project` name:
1. MUST remove the project from the authenticated user's `Enrolled` list.
2. MUST write the updated `users.yaml` atomically.
3. MUST reload the in-memory user directory.
4. MUST return HTTP 200 on success.

Already-synced cloud observations for this project MUST NOT be deleted from the
server; un-enrolling only prevents future push eligibility from the client.

#### Scenario: Happy path — user removes a project enrollment

- GIVEN `"android-game-perf-tool-desktop"` IS in alice's `Enrolled` list
- WHEN `DELETE /user/enrolled-projects` is called with `{ "project": "android-game-perf-tool-desktop" }`
- THEN alice's `Enrolled` no longer contains `"android-game-perf-tool-desktop"`
- AND `users.yaml` is written atomically
- AND the in-memory directory is reloaded
- AND the response is HTTP 200

#### Scenario: Remove project not in enrolled list (idempotent)

- GIVEN `"non-existent-project"` is NOT in alice's `Enrolled`
- WHEN `DELETE /user/enrolled-projects` is called with that project
- THEN the handler returns HTTP 200 (no-op)
- AND `users.yaml` is not modified unnecessarily

---

### Requirement: Concurrent-Safe Write to users.yaml

The `users.yaml` write path for self-service enrollment MUST be protected by a
server-scoped write mutex to prevent data loss from concurrent enroll/un-enroll
operations.

The mutex MUST cover the full read-modify-write-reload cycle.

#### Scenario: Concurrent enrollments from two users

- GIVEN user alice and user bob both send `POST /user/enrolled-projects` simultaneously
- WHEN both requests execute concurrently
- THEN both users' enrollments are persisted in `users.yaml`
- AND neither user's change overwrites the other's

#### Scenario: Concurrent admin edit and self-service enroll

- GIVEN an admin is editing a user's `Enrolled` list via the admin dashboard
- AND the same user simultaneously calls `POST /user/enrolled-projects`
- WHEN both writes complete
- THEN `users.yaml` reflects both changes without corruption
- AND the in-memory directory is in a consistent state

---

### Requirement: Self-Service Scope Boundary

The self-service endpoint MUST enforce that a user can ONLY modify their OWN
`Enrolled` list. The endpoint MUST derive the target user identity from the
authenticated JWT principal, NOT from any user-supplied parameter.

#### Scenario: Attempt to enroll a project for another user is rejected

- GIVEN alice is authenticated
- AND the request body includes a target user email `{ "project": "foo", "as_user": "bob@viva.com" }`
- WHEN the handler executes
- THEN the handler ignores the `as_user` field and modifies only alice's enrollment
- AND bob's enrollment is unchanged
