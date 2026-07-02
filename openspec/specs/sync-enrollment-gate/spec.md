# Delta for Sync-Enrollment-Gate

## MODIFIED Requirements

### Requirement: Project Enrollment Gate for Push

The sync push path (`ListPendingSyncMutations`) MUST include a pending mutation
in a push batch if and only if the mutation's `project` is listed in
`sync_enrolled_projects`.

Mutations with `project=''` (empty string) MUST NOT bypass the enrollment gate.
The prior implicit behavior that treated `project=''` as always-enrolled IS
REMOVED.

After this change the gate query MUST NOT contain the clause
`OR sm.project = ''` (or any equivalent that makes empty-project mutations
unconditionally eligible for sync).

(Previously: `sm.project = ''` was always eligible — the OR clause meant all
empty-project mutations auto-synced without enrollment.)

#### Scenario: Empty-project mutation is blocked (BUG #955 fix)

- GIVEN a pending sync mutation exists with `project=''`
- AND `''` is NOT in `sync_enrolled_projects`
- WHEN `ListPendingSyncMutations` is called
- THEN the mutation is NOT returned in the result set
- AND the push batch does NOT include it

#### Scenario: Enrolled project mutation still syncs

- GIVEN a pending sync mutation exists with `project="android-game-perf-tool-desktop"`
- AND `"android-game-perf-tool-desktop"` IS in `sync_enrolled_projects`
- WHEN `ListPendingSyncMutations` is called
- THEN the mutation IS returned and included in the push batch

#### Scenario: Unenrolled non-empty project mutation is blocked (existing behavior, preserved)

- GIVEN a pending sync mutation exists with `project="my-secret-notes"`
- AND `"my-secret-notes"` is NOT in `sync_enrolled_projects`
- WHEN `ListPendingSyncMutations` is called
- THEN the mutation is NOT returned
- AND `CountPendingNonEnrolledSyncMutations` counts it

#### Scenario: After migration, no orphan mutations reach the push path

- GIVEN the orphan migration has completed (all former `project=''` mutations now have `project="personal"`)
- AND `"personal"` is NOT enrolled
- WHEN autosync push runs
- THEN zero mutations are pushed from the former-orphan set
- AND the QA user (Sergio) sees no unexpected data in the cloud from unenrolled mutations

---

## MODIFIED Requirements

### Requirement: CountPendingNonEnrolledSyncMutations Covers All Projects

The `CountPendingNonEnrolledSyncMutations` query MUST count pending mutations for
ALL unenrolled projects, including observations with `project=''`.

(Previously: the query had `WHERE sm.project != ''` which excluded empty-project
mutations from the count, hiding the backlog from operators.)

#### Scenario: Empty-project mutations appear in the non-enrolled count

- GIVEN 10 pending mutations with `project=''` exist
- AND `''` is not in `sync_enrolled_projects`
- WHEN `CountPendingNonEnrolledSyncMutations` is called
- THEN the result includes those 10 mutations in the count
- AND operators can see the full unenrolled backlog
