# Private-by-Default Specification

## Purpose

Defines the per-folder project model, the `ENGRAM_DEFAULT_PROJECT` fallback for
non-git folders, the one-time orphan migration from `project=''` to `"personal"`,
and the selective sharing path for personal/orphan observations.

---

## Requirements

### Requirement: Per-Folder Project Detection

The system MUST resolve each tool invocation's active project as follows (in
priority order):

1. `ENGRAM_PROJECT` env var — if non-empty, use it as-is (explicit override).
2. `detectProject(cwd)` — if returns a non-empty string, use it.
3. `ENGRAM_DEFAULT_PROJECT` env var — if non-empty, use it as the fallback.
4. Hard default: `"personal"` (if none of the above resolves to a non-empty value).

This resolution MUST apply in MCP tool calls (`cmdMCP`), sync status
(`resolveServeSyncStatusProject`), and triage startup (`cmdTriage`).

`ENGRAM_PROJECT` MUST remain a valid explicit override so that existing Viva
deployments (e.g. `ENGRAM_PROJECT=viva`) keep working unchanged.

#### Scenario: Work in a git-tracked folder

- GIVEN no `ENGRAM_PROJECT` env var is set
- AND `detectProject(cwd)` returns `"android-game-perf-tool-desktop"`
- WHEN the user saves an observation
- THEN the observation's `project` field is `"android-game-perf-tool-desktop"`

#### Scenario: Work in a non-git folder — ENGRAM_DEFAULT_PROJECT set

- GIVEN `ENGRAM_PROJECT` is not set
- AND `detectProject(cwd)` returns `""`
- AND `ENGRAM_DEFAULT_PROJECT=work-notes`
- WHEN the user saves an observation
- THEN the observation's `project` field is `"work-notes"`

#### Scenario: Work in a non-git folder — no env vars set

- GIVEN `ENGRAM_PROJECT` is not set
- AND `detectProject(cwd)` returns `""`
- AND `ENGRAM_DEFAULT_PROJECT` is not set
- WHEN the user saves an observation
- THEN the observation's `project` field is `"personal"`

#### Scenario: ENGRAM_PROJECT explicit override wins over cwd detection

- GIVEN `ENGRAM_PROJECT=viva`
- AND `detectProject(cwd)` returns `"android-game-perf-tool-desktop"`
- WHEN any MCP tool call executes
- THEN the observation's `project` field is `"viva"` (override wins)

#### Scenario: Fresh folder — nothing leaves the machine

- GIVEN the user creates a new folder and starts working
- AND no `ENGRAM_PROJECT` is set
- AND `detectProject(cwd)` returns `""` (non-git folder)
- THEN new observations are written with `project="personal"` (or ENGRAM_DEFAULT_PROJECT value)
- AND the `"personal"` project is NOT enrolled in `sync_enrolled_projects`
- AND no observations from this folder are pushed to the cloud

---

### Requirement: "personal" Project Is Private by Default

The `"personal"` project (and any project resolving from `ENGRAM_DEFAULT_PROJECT`
that is not explicitly enrolled) MUST NOT be enrolled at creation time. It MUST
remain local-only until the user explicitly shares it via triage or `engram enroll`.

#### Scenario: "personal" project is unenrolled at startup

- GIVEN the system resolves project to `"personal"`
- WHEN autosync push runs
- THEN no observations tagged `project="personal"` are included in the sync batch
- AND `CountPendingNonEnrolledSyncMutations` reports these mutations as pending-unenrolled (not silently skipped)

---

### Requirement: One-Time Orphan Migration

On a defined migration trigger (e.g., `engram doctor`, triage startup, or
first `cmdMCP` run after upgrade), the system MUST perform a one-time,
idempotent migration:

All mutations and observations with `project=''` (empty string) MUST be
reassigned to the `"personal"` project.

The migration MUST be idempotent: re-running it MUST produce the same result
without duplicating or corrupting data.

The migration MUST NOT sync these observations — after migration they belong to
`"personal"` which is private by default.

#### Scenario: Orphan migration runs once

- GIVEN 50 observations exist with `project=''`
- WHEN the migration runs for the first time
- THEN all 50 observations now have `project="personal"`
- AND no observation has `project=''` remaining
- AND the migration completion is recorded (so re-runs are no-ops)

#### Scenario: Orphan migration is idempotent

- GIVEN the migration has already completed
- WHEN the migration trigger runs again (e.g., restart, upgrade)
- THEN no observations are modified
- AND no errors are returned

#### Scenario: Orphan observations do NOT sync after migration

- GIVEN the migration completes (orphans → "personal")
- WHEN autosync push runs immediately after
- THEN none of the migrated observations are included in the push
- AND `"personal"` remains unenrolled

---

### Requirement: Selective Sharing of Personal/Orphan Observations

To share specific observations that landed in `"personal"` (or were formerly
`project=''`), the user MUST be able to reassign individual observations or a
bulk selection to a different, shareable project via triage.

Sharing the entire `"personal"` project as a bucket is NOT supported as a
first-class action (to avoid leaking genuinely-personal notes).

After reassignment, if the target project is enrolled, those observations MUST
sync in the next push cycle.

#### Scenario: User reassigns orphan observations to a shareable project

- GIVEN observations O1, O2, O3 have `project="personal"`
- AND the user selects O1, O2, O3 in triage and reassigns them to `"android-game-perf-tool-desktop"`
- AND `"android-game-perf-tool-desktop"` is enrolled
- WHEN autosync push runs
- THEN O1, O2, O3 are pushed to the cloud tagged under `"android-game-perf-tool-desktop"`
- AND the remaining observations in `"personal"` are NOT pushed

#### Scenario: Reassigned observations must belong to an enrolled project to sync

- GIVEN observations O4, O5 have `project="personal"`
- AND the user reassigns them to `"new-unenrolled-project"` (not enrolled)
- WHEN autosync push runs
- THEN O4, O5 are NOT pushed (new-unenrolled-project is not enrolled)
- AND no sync error is raised (standard unenrolled behavior)
