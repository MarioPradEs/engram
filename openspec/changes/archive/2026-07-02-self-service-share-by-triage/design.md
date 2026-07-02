# Design: Self-Service Share-by-Triage

## Technical Approach

Private by default + user-controlled sharing, built in order **Piece 3 → 1 → 2**. Piece 3 guarantees no new code ever produces `project=''` (the BUG #955 root) and migrates existing orphans to the private `personal` project. Piece 1 adds an atomic triage "share/unshare/reassign" action (client enroll + server enroll + `default_scope`). Piece 2 adds a JWT-authed self-service server endpoint that mutates only the caller's own `enrolled` list in `users.yaml`. All new collaborators are narrow interfaces/closures so units test without real listeners, HTTP, or git.

## Architecture Decisions

| # | Decision | Choice | Rejected | Rationale |
|---|----------|--------|----------|-----------|
| D1 | Project resolution precedence | `ENGRAM_PROJECT` (explicit override) > `detectProject(cwd)` > `ENGRAM_DEFAULT_PROJECT` (default `personal`) > `""` | Repurpose `ENGRAM_PROJECT` as fallback | Keeps `ENGRAM_PROJECT=viva` deployments working unchanged; fallback only fires when cwd resolves empty. New code never emits `""`. |
| D2 | BUG #955 SQL gate | Do NOT change `store.go:3958` gate; rely on no-new-empties + one-time migration | Tighten SQL to `sep.project IS NOT NULL` | Tightening breaks every existing `project=''` mutation (global settings, in-flight). Migration is reversible and leak-safe (lands in private `personal`). |
| D3 | Orphan migration trigger | Idempotent `MigrateEmptyProjectToPersonal(target)` store method, invoked once at `engram triage` launch (best-effort, logged, non-fatal) + exposed via `engram doctor` | Dedicated migrate-only command; auto-run in MCP hot path | Triage is the human-in-the-loop entry; doctor is the explicit repair path. Idempotent so re-running is safe. |
| D4 | Migration dual-write (column vs payload) | Update entity tables `project`, then update **both** pending `sync_mutations.project` column AND `json_set(payload,'$.project',...)` for `project=''` rows; then `backfillProjectSyncMutationsTx("personal")` | Column-only update | Push groups by `mut.Project` (manager.go:561/574) BUT the cloud-stored obs uses `payload.$.project`. Updating only the column would land the obs as `project=''` cloud-side. Must fix both. |
| D5 | Triage share action | New dedicated `POST /project/{name}/share` → `handleShareProject` (Option B), same cwd-project Option A gate as `handleClassify` | Extend `handleClassify` (Option A) | Avoids loading classify with network I/O + enrollment; explicit, testable, own confirmation UI. |
| D6 | Reassign orphans to real project | `POST /project/{name}/reassign` → store method `ReassignProject(source, canonical)` that DOES handle `source="personal"` (unlike `MergeProjects`, which skips empty/personal) + applies D4 dual-write | Reuse `MergeProjects` | `MergeProjects` continues on empty/non-canonical sources; reassign must move `personal`→shareable explicitly. |
| D7 | Server self-service endpoint | `POST /user/enrolled-projects` + `DELETE` variant in cloudserver `routes()`, wrapped with `withAuth`; caller email via `Attribution(ctx).UserEmail`; mutate only own `enrolled` | Admin webhook/queue | Fits existing JWT auth; reuses `users.MarshalPrincipals/WriteAtomic/RunLocalGitCommit` + `s.userReloadFn`. No admin gate — is-self is sufficient authz. |
| D8 | users.yaml write concurrency | Package-level `sync.Mutex` on `CloudServer` guarding read-`Lookup`→modify→`WriteAtomic`→`UserReload` | Optimistic file compare/retry | `WriteAtomic` prevents corruption but not lost updates across the read-modify-write window. A serializing mutex is the simplest correct fix; admin handlers should share it later. |
| D9 | JWT at triage (atomic failure) | Read `credentials.json` via `readCredentialsToken(credDir)` at `newTriageServer`; build `serverEnrollFn` closure. Share is atomic: server enroll FIRST → on success client `EnrollProject` + `WriteProjectDefaultScope`; any failure rolls back and reports "not logged in / share failed" — no half-state. | Best-effort client-first | Proposal requires atomic with clear feedback; server is the failure-prone hop, so gate on it first. |
| D10 | Per-project autosync target key | Keep global `cloud` target key; share does NOT create `cloud:project`. Enrollment gate (`sync_enrolled_projects`) already scopes which projects sync within the global target. | `cloud:project` per share | Manager is target-key scoped; existing model already filters by enrollment per project. No new target key needed for v1. |

## Data Flow

    Triage "Share folder" (Piece 1)
    Browser ──POST /project/{cwd}/share──▶ handleShareProject
         │  1. Option-A gate (name == cwdProject)
         │  2. serverEnrollFn(project, bearerToken) ──HTTP POST /user/enrolled-projects──▶ CloudServer
         │       (D9: server FIRST; on fail → 4xx "not logged in", NO local change)
         │  3. enrollStore.EnrollProject(project)        [client SQLite]
         │  4. WriteProjectDefaultScope(cwdDir,"shared") [config.json]
         └─ on any step≥3 failure → UnenrollProject + revert scope, report

    Server self-service (Piece 2)        Private-by-default (Piece 3)
    withAuth → mu.Lock →                 main.go resolve: ENGRAM_PROJECT ?
      Lookup(email) → append/remove        else detectProject(cwd) ?
      MarshalPrincipals → WriteAtomic      else ENGRAM_DEFAULT_PROJECT("personal")
      → RunLocalGitCommit → UserReload   triage launch → MigrateEmptyProjectToPersonal()
    → mu.Unlock                            (entity tables + sm.column + payload.$.project)

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `cmd/engram/main.go` (~812 `resolveServeSyncStatusProject`, ~875 `cmdMCP`) | Modify | Add `resolveProjectName()` helper implementing D1 precedence; replace inline env reads. |
| `cmd/engram/triage.go` (`newTriageServer`, `cmdTriage`) | Modify | Wire `EnrollmentStore`, `serverEnrollFn` closure (cloud URL + `readCredentialsToken`), call `MigrateEmptyProjectToPersonal` (D3). |
| `internal/store/store.go` | Modify | Add `MigrateEmptyProjectToPersonal(target string)` (D3/D4) and `ReassignProject(source, canonical string)` (D6). Do NOT touch L3958 gate (D2). |
| `internal/triage/server.go` | Modify | Add `EnrollmentStore` field + `serverEnrollFn` + setters; register `POST /project/{name}/share`, `/unshare`, `/reassign`. |
| `internal/triage/handlers.go` | Modify | Add `EnrollmentStore` interface + `handleShareProject`/`handleUnshareProject`/`handleReassign`. |
| `internal/triage/share.templ` | Create | Share/unshare/reassign UI fragment; run `make templ` before tests. |
| `internal/cloud/cloudserver/self_enroll.go` | Create | `handleSelfEnrollProject` (POST) + remove variant; `enrollMu sync.Mutex`; reuse `users.*` + `s.userReloadFn`. |
| `internal/cloud/cloudserver/cloudserver.go` (`routes`) | Modify | Register `POST`/`DELETE /user/enrolled-projects` behind `withAuth`. |

## Interfaces / Contracts

```go
// Piece 1 — triage seams (testable without store/HTTP)
type EnrollmentStore interface {
    EnrollProject(project string) error
    UnenrollProject(project string) error
}
type serverEnrollFn func(project, bearerToken string) error // nil-safe; "" token → "not logged in"

// Piece 3 — store
func (s *Store) MigrateEmptyProjectToPersonal(target string) (*MigrateResult, error) // idempotent
func (s *Store) ReassignProject(source, canonical string) (*MergeResult, error)       // handles source=="personal"

// Piece 2 — server: POST/DELETE /user/enrolled-projects {"project":"<name>"}
//   email := Attribution(r.Context()).UserEmail ; mutate only that principal's Enrolled
```

`*store.Store` already satisfies `EnrollmentStore` (`EnrollProject`/`UnenrollProject` exist). `serverEnrollFn` POSTs `{project}` with `Authorization: Bearer <token>` to the cloud URL.

## Testing Strategy

| Layer | What | Approach |
|-------|------|----------|
| Unit | D1 precedence matrix (env/cwd/default/empty) | Table test on `resolveProjectName`, inject `detectProject` + env. |
| Unit | `handleShareProject` atomicity (D9) | Fake `EnrollmentStore` + `serverEnrollFn` returning error → assert UnenrollProject called, scope reverted, 4xx body. |
| Unit | `MigrateEmptyProjectToPersonal` idempotency + D4 dual-write | Seed `project=''` obs + pending mutation; assert column AND `payload.$.project` == `personal`; run twice → same result. |
| Unit | `ReassignProject` from `personal` | Seed `personal` obs; assert moved + backfill enqueued. |
| Unit | Self-service endpoint | Fake auth (`Attribution`) + temp `users.yaml`; assert only caller's `enrolled` changes; concurrent goroutines under `enrollMu` → no lost update. |
| Integration | Share end-to-end | httptest cloudserver + real store; share → enrolled both sides + scope shared. |
| Golden | triage templ | `make templ` then `go test ./internal/triage/...`; new share fragment golden. |

## Migration / Rollout

One-time `MigrateEmptyProjectToPersonal("personal")` at triage launch (best-effort, logged) and via `engram doctor`. Idempotent (only acts on `project=''`); private destination so zero leak. Reverse-safe per Piece. `ENGRAM_PROJECT` override preserved.

## BUG #955 Resolution (Sergio block)

Root = non-git folders saving `project=''` which the gate auto-syncs. D1 stops new empties; D3/D4 migrate existing orphans (column + payload) into private `personal`, which is unenrolled → no longer auto-syncs. Gate SQL stays intact (D2). Sergio's poisoned batches drain to private state — unblocked.

## Open Questions

- [ ] Should admin member-management handlers (`dashboard`) share the same `enrollMu` to fully serialize all `users.yaml` writers? (Recommended follow-up; v1 mutex covers self-service path.)
- [ ] Git-commit-per-enroll noise accepted for v1; batching deferred.
