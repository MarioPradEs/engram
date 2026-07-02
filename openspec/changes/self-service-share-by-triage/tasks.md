# Tasks: Self-Service Share-by-Triage

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | 650–850 |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR1 = Piece 3 (private-by-default foundation + orphan migration) → PR2 = Piece 1 (triage share/unshare/reassign) → PR3 = Piece 2 (self-service server endpoint) |
| Delivery strategy | ask-on-risk |
| Chain strategy | pending (decision needed before apply) |

Decision needed before apply: Yes
Chained PRs recommended: Yes
Chain strategy: pending
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| WU-A | D1 project resolution + D2/D3/D4 orphan migration | PR 1 | Unblocks Sergio; base = main; no UI dependency |
| WU-B | D5 triage share/unshare + D6 reassign + EnrollmentStore seam | PR 2 | base = PR1 branch; needs `make templ` before tests |
| WU-C | D7 server self-service enroll/unenroll + D8 mutex | PR 3 | base = PR2 branch; cloud-server only |

---

## Slice 1 — Piece 3: Private-by-Default Foundation + Orphan Migration
> Spec domains: `private-by-default`, `sync-enrollment-gate`
> Unblocks QA user Sergio (BUG #955). No UI, no network.

### Phase 1: D1 Project Resolution Helper (TDD — RED first)

- [x] 1.1 **RED** `cmd/engram/main_test.go` (or new `cmd/engram/project_resolve_test.go`): table-driven unit test for `resolveProjectName(envProject, detected, envDefault string) string` covering all 4 precedence cases from spec: explicit env → detectProject result → ENGRAM_DEFAULT_PROJECT → "personal". `go test ./cmd/engram/...` must FAIL (function absent).
- [x] 1.2 **GREEN** `cmd/engram/main.go`: implement `resolveProjectName(envProject, detected, envDefault string) string` — returns first non-empty of the three args, then "personal". Wire into `resolveServeSyncStatusProject` (~L812) and `cmdMCP` (~L875). `go test ./cmd/engram/...` must PASS.
- [x] 1.3 **GREEN (wire triage)** `cmd/engram/triage.go` `newTriageServer`: call `resolveProjectName(os.Getenv("ENGRAM_PROJECT"), detectProject(cwd), os.Getenv("ENGRAM_DEFAULT_PROJECT"))` to set `cwdProject`. Remove any direct `""` fallback path.

### Phase 2: D3/D4 Orphan Migration in Store (TDD — RED first)

- [x] 2.1 **RED** `internal/store/store_migrate_test.go` (new file): unit test `TestMigrateEmptyProjectToPersonal` seeding 5 observations + 3 `sync_mutations` rows with `project=''`; asserts (a) all rows now have `project='personal'` in entity table, (b) `sync_mutations.project='personal'`, (c) `json_extract(payload,'$.project')='personal'` (D4 dual-write), (d) second run is no-op. `go test ./internal/store/...` must FAIL.
- [x] 2.2 **GREEN** `internal/store/store.go`: implement `MigrateEmptyProjectToPersonal(target string) (*MigrateResult, error)`. One transaction: UPDATE observations/memory_mutations/pending_sync_mutations WHERE project=''; UPDATE sync_mutations.project column AND `json_set(payload,'$.project',target)` WHERE project=''; call `backfillProjectSyncMutationsTx(target)`. Record completion in a migration-status key. Do NOT touch L3958 gate (D2). `go test ./internal/store/...` must PASS.
- [x] 2.3 **RED** `internal/store/store_migrate_test.go`: add `TestMigrateEmptyProjectToPersonal_Idempotent` — run migration twice, assert count of modified rows on second run == 0.
- [x] 2.4 **GREEN** Ensure completion-check in `MigrateEmptyProjectToPersonal` returns early with `MigrateResult{AlreadyDone: true}` on second run. `go test ./internal/store/...` must PASS.

### Phase 3: D3 Migration Trigger Wiring

- [x] 3.1 `cmd/engram/triage.go` `cmdTriage`: after opening the store and before starting the HTTP server, call `store.MigrateEmptyProjectToPersonal("personal")` best-effort (log warning on error, do not abort). Add test in `cmd/engram/triage_test.go` asserting migration is invoked at triage startup using a fake store.
- [x] 3.2 `cmd/engram/doctor.go`: wire `store.MigrateEmptyProjectToPersonal("personal")` in `runDoctor` (best-effort). Add unit test in `cmd/engram/doctor_test.go`.

### Phase 4: D2 SQL Gate Fix (sync-enrollment-gate domain)

- [x] 4.1 **RED** `internal/store/store_test.go` (or dedicated `store_sync_gate_test.go`): test `TestListPendingSyncMutations_EmptyProjectBlocked` — seed mutation with `project=''`, assert it does NOT appear in `ListPendingSyncMutations` result.
- [x] 4.2 **RED** `internal/store/store_test.go`: test `TestCountPendingNonEnrolledSyncMutations_IncludesEmptyProject` — seed 10 mutations with `project=''`, assert count >= 10.
- [x] 4.3 **GREEN** `internal/store/store.go`: remove `OR sm.project = ''` from `ListPendingSyncMutations` query (the clause that caused BUG #955). Ensure `CountPendingNonEnrolledSyncMutations` WHERE does NOT exclude `project=''`. Both tests must PASS.

---

## Slice 2 — Piece 1: Triage Share / Unshare / Reassign
> Spec domains: `self-service-share`
> Depends on Slice 1 (EnrollmentStore method signatures available).
> NOTE: run `make templ` BEFORE `go test ./internal/triage/...` after any `.templ` change.

### Phase 5: EnrollmentStore Interface + Store Adapter (TDD — RED first)

- [x] 5.1 **RED** `internal/triage/handlers_test.go`: add `TestEnrollmentStoreInterface` — compile-time check that `*store.StoreAdapter` (or `*store.Store`) satisfies a declared `EnrollmentStore` interface with `EnrollProject(string) error` and `UnenrollProject(string) error`.
- [x] 5.2 **GREEN** `internal/triage/handlers.go`: declare `EnrollmentStore` interface. Confirm `*store.StoreAdapter` satisfies it (add methods to `StoreAdapter` in `internal/triage/server.go` if absent). Test must PASS.
- [x] 5.3 **GREEN** `internal/triage/server.go` `Server` struct: add fields `enrollStore EnrollmentStore` and `serverEnrollFn func(project, bearerToken string) error`. Add `WithEnrollmentStore(s EnrollmentStore)` and `WithServerEnrollFn(fn func(string,string) error)` setters.

### Phase 6: Share Templ Fragment (templ codegen required)

- [x] 6.1 `internal/triage/share.templ` (new file): create templ component `SharePanel(projectName string, isShared bool)` rendering Share/Unshare button and status label. **Run `make templ` immediately after saving** to generate `share_templ.go`.
- [x] 6.2 `internal/triage/golden_test.go`: add golden snapshot test for `SharePanel` rendered output. Run `make templ` then `go test ./internal/triage/...` to generate baseline.

### Phase 7: handleShareProject (TDD — RED first)

- [x] 7.1 **RED** `internal/triage/handlers_test.go`: `TestHandleShareProject_HappyPath` — fake `EnrollmentStore` (no error) + fake `serverEnrollFn` (no error); POST `/project/{cwdProject}/share`; assert HTTP 200, `EnrollProject` called once, `WriteProjectDefaultScope` sets "shared".
- [x] 7.2 **RED** `internal/triage/handlers_test.go`: `TestHandleShareProject_ServerEnrollFailure` — fake `serverEnrollFn` returns error; assert HTTP 4xx, `EnrollProject` NOT called, scope unchanged (rollback per D9: server enroll FIRST).
- [x] 7.3 **RED** `internal/triage/handlers_test.go`: `TestHandleShareProject_CwdBoundary` — request for a different project name; assert HTTP 400 "project mismatch", nothing called.
- [x] 7.4 **RED** `internal/triage/handlers_test.go`: `TestHandleShareProject_NoJWT` — empty bearer token passed to `serverEnrollFn`; assert error propagated, HTTP 4xx "not logged in".
- [x] 7.5 **GREEN** `internal/triage/handlers.go`: implement `handleShareProject` — (1) enforce `name == s.cwdProject`; (2) call `s.serverEnrollFn(project, bearerToken)` first; (3) on success call `s.enrollStore.EnrollProject(project)`; (4) call `WriteProjectDefaultScope(cwdDir, "shared")`; on step ≥ 3 failure: `UnenrollProject` + revert scope + return error. All 4 tests must PASS. Run `make templ` then `go test ./internal/triage/...`.
- [x] 7.6 `internal/triage/server.go`: register `POST /project/{name}/share` → `handleShareProject`.

### Phase 8: handleUnshareProject (TDD — RED first)

- [x] 8.1 **RED** `internal/triage/handlers_test.go`: `TestHandleUnshareProject_HappyPath` — assert `UnenrollProject` called, server un-enroll called, scope reverted to "private", HTTP 200.
- [x] 8.2 **RED** `internal/triage/handlers_test.go`: `TestHandleUnshareProject_Idempotent` — project already unenrolled; assert HTTP 200, no error.
- [x] 8.3 **GREEN** `internal/triage/handlers.go`: implement `handleUnshareProject` — reverse of share; preserve cloud data (no delete). Register `POST /project/{name}/unshare`. Run `make templ` then `go test ./internal/triage/...`.

### Phase 9: D6 handleReassign (TDD — RED first)

- [x] 9.1 **RED** `internal/store/store_test.go`: `TestReassignProject_FromPersonal` — seed observations with `project='personal'`; call `ReassignProject("personal", "target-project")`; assert all rows moved + backfill enqueued.
- [x] 9.2 **GREEN** `internal/store/store.go`: implement `ReassignProject(source, canonical string) (*MergeResult, error)` — handles `source=="personal"` unlike existing `MergeProjects`; includes D4 dual-write for payload. `go test ./internal/store/...` must PASS.
- [x] 9.3 **RED** `internal/triage/handlers_test.go`: `TestHandleReassign_HappyPath` — fake `MutableTriageStore` with `ReassignProject`; POST `/project/{name}/reassign` body `{"from":"personal"}`; assert HTTP 200 and reassign called.
- [x] 9.4 **GREEN** `internal/triage/handlers.go`: implement `handleReassign`. Register `POST /project/{name}/reassign`. Run `make templ` then `go test ./internal/triage/...`.

### Phase 10: JWT Wiring in cmdTriage (D9)

- [x] 10.1 `cmd/engram/triage.go` `newTriageServer`: read JWT via `readCredentialsToken(credDir)` (login.go:177); build `serverEnrollFn` closure (HTTP POST to cloud URL + bearer). If token empty/expired, set `serverEnrollFn` to func returning "not logged in" error. Pass to `WithServerEnrollFn`.
- [x] 10.2 `cmd/engram/triage_test.go`: test that triage server starts without error when credentials.json absent (JWT warning path) and that `serverEnrollFn` returns "not logged in" error for empty token.

---

## Slice 3 — Piece 2: Self-Service Server Endpoint
> Spec domain: `self-service-server-enroll`
> Depends on Slice 2 (cloudserver route registration pattern established).

### Phase 11: enrollMu Mutex + Handler Skeleton (TDD — RED first)

- [ ] 11.1 **RED** `internal/cloud/cloudserver/self_enroll_test.go` (new file): `TestSelfEnrollProject_HappyPath` — httptest server with real handler; alice authenticated; POST `/user/enrolled-projects` `{"project":"foo"}`; assert HTTP 200 and alice's `Enrolled` contains "foo".
- [ ] 11.2 **RED** `internal/cloud/cloudserver/self_enroll_test.go`: `TestSelfEnrollProject_Idempotent` — POST same project twice; assert HTTP 200, no duplicate in `Enrolled`.
- [ ] 11.3 **RED** `internal/cloud/cloudserver/self_enroll_test.go`: `TestSelfEnrollProject_Unauthenticated` — no Authorization header; assert HTTP 401, users.yaml unmodified.
- [ ] 11.4 **RED** `internal/cloud/cloudserver/self_enroll_test.go`: `TestSelfEnrollProject_EmptyProject` — body `{"project":""}`; assert HTTP 400.
- [ ] 11.5 **GREEN** `internal/cloud/cloudserver/self_enroll.go` (new file): declare `enrollMu sync.Mutex` at package level; implement `handleSelfEnrollProject` — `withAuth` enforces JWT; derive email from `Attribution(ctx).UserEmail`; `enrollMu.Lock/Unlock` wraps Lookup→append(dedup)→MarshalPrincipals→WriteAtomic→RunLocalGitCommit→UserReload. All 4 tests must PASS. `go test ./internal/cloud/cloudserver/...`.

### Phase 12: handleSelfUnenrollProject (TDD — RED first)

- [ ] 12.1 **RED** `internal/cloud/cloudserver/self_enroll_test.go`: `TestSelfUnenrollProject_HappyPath` — DELETE `/user/enrolled-projects`; assert project removed from Enrolled, HTTP 200.
- [ ] 12.2 **RED** `internal/cloud/cloudserver/self_enroll_test.go`: `TestSelfUnenrollProject_Idempotent` — DELETE project not in list; assert HTTP 200, no-op.
- [ ] 12.3 **GREEN** `internal/cloud/cloudserver/self_enroll.go`: implement `handleSelfUnenrollProject` — same `enrollMu` guard; remove project from slice; WriteAtomic; existing cloud observations NOT deleted. Both tests must PASS.

### Phase 13: Concurrent-Safety Test

- [ ] 13.1 **RED** `internal/cloud/cloudserver/self_enroll_test.go`: `TestSelfEnroll_Concurrent` — launch 10 goroutines, each POST a unique project for alice under `enrollMu`; assert final `Enrolled` length == 10 with no duplicates.
- [ ] 13.2 **GREEN** Confirm `enrollMu` scope in `self_enroll.go` covers full read-modify-write-reload cycle. `go test -race ./internal/cloud/cloudserver/...` must PASS.

### Phase 14: Route Registration

- [ ] 14.1 `internal/cloud/cloudserver/cloudserver.go` `routes()`: register `POST /user/enrolled-projects` → `withAuth(handleSelfEnrollProject)` and `DELETE /user/enrolled-projects` → `withAuth(handleSelfUnenrollProject)`.
- [ ] 14.2 `internal/cloud/cloudserver/cloudserver_test.go`: add smoke route test asserting both paths exist and return 401 without credentials.

### Phase 15: Scope Boundary Test (self-service only modifies own enrollment)

- [ ] 15.1 `internal/cloud/cloudserver/self_enroll_test.go`: `TestSelfEnroll_ScopeBoundary` — alice authenticated; body includes `"as_user":"bob@viva.com"`; assert handler ignores field, modifies only alice's Enrolled, bob's Enrolled unchanged.

---

## Slice Cross-Cut: Integration Smoke

- [ ] 16.1 `cmd/engram/autosync_e2e_test.go` (or new `cmd/engram/private_by_default_e2e_test.go`): end-to-end test asserting that after `MigrateEmptyProjectToPersonal`, no orphan mutations appear in `ListPendingSyncMutations`, and `CountPendingNonEnrolledSyncMutations` captures them.
- [ ] 16.2 Full `go test ./...` green run (reminder: run `make templ` first if any `.templ` file was modified). Fix any compilation breaks introduced by interface additions.
