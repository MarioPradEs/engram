# Verify Report: self-service-share-by-triage -- Batch 3 (Phases 11-16)

**Branch**: feat/share-by-triage-p2-server-enroll
**Base**: main at 158eb6a
**Verified**: 2026-07-02
**Verdict**: PASS -- 0 CRITICAL, 2 WARNING, 2 SUGGESTION

---

## Executive Summary

Batch 3 (Phases 11-16: server self-service enroll/unenroll + integration smoke) is
GREEN with no critical issues. Build clean, all 9 cloudserver tests pass (3.937s),
BUG #955 e2e smoke passes (0.06s). Every failing package on the branch was verified
against main and confirmed pre-existing. Batch 3 changed ONLY cloudserver files +
one new e2e test -- no regression possible in packages it does not touch.
Branch is PR-ready.

---

## Test Results

### Build

    go build ./... -- CLEAN (exit code 0)

### Cloudserver Package (Phases 11-14)

    go test ./internal/cloud/cloudserver/... -- PASS (3.937s, all tests green)

Self-enroll tests (all 9 pass):

| Test | Phase | Status |
|------|-------|--------|
| TestSelfEnrollProject_HappyPath | 11.1 | PASS |
| TestSelfEnrollProject_Idempotent | 11.2 | PASS |
| TestSelfEnrollProject_Unauthenticated | 11.3 (401 gate) | PASS |
| TestSelfEnrollProject_EmptyProject | 11.4 (400 gate) | PASS |
| TestSelfUnenrollProject_HappyPath | 12.1 | PASS |
| TestSelfUnenrollProject_Idempotent | 12.2 | PASS |
| TestSelfEnroll_Concurrent | 13.1 | PASS |
| TestSelfEnrollRoutes_Return401WithoutCredentials | 14.2 | PASS |
| TestSelfEnroll_ScopeBoundary | 15.1 | PASS |

### BUG #955 Integration Smoke (Phase 16.1)

    go test ./cmd/engram/ -run TestPrivateByDefault -- PASS (0.06s)
    TestPrivateByDefault_OrphanMigrationSmokeTest -- PASS

### Full Suite

    FAIL  internal/setup  (2 tests -- pre-existing)
    FAIL  internal/store  (2 tests -- pre-existing Windows SQLite file-lock)
    FAIL  internal/sync   (2 tests -- pre-existing)
    FAIL  cmd/engram      (11 tests -- pre-existing; TestPrivateByDefault PASSES)
    ok    internal/cloud/cloudserver  (3.937s)
    ok    internal/triage  (cached)

---

## Spec Requirement Verification

### REQ: POST /user/enrolled-projects

| Check | Result |
|-------|--------|
| Email from JWT not body | PASS -- callerEmail uses Attribution(ctx).UserEmail |
| as_user body field ignored | PASS -- parseEnrollBody reads only json project field; Decoder discards unknown fields |
| Dedup on append | PASS -- inner loop early-returns 200 when already enrolled |
| Atomic write to users.yaml | PASS -- users.WriteAtomic (write-rename pattern) |
| In-memory directory reload | PASS -- s.userReloadFn() in writePrincipalsAtomic |
| HTTP 200 success | PASS |
| HTTP 401 unauthenticated | PASS -- enforced by withAuth at route registration |
| HTTP 400 empty project | PASS -- parseEnrollBody validates |

### REQ: DELETE /user/enrolled-projects

| Check | Result |
|-------|--------|
| Removes project from Enrolled | PASS -- filter-copy approach |
| Idempotent (200 if absent) | PASS |
| Atomic write | PASS |
| Cloud observations NOT deleted | PASS -- no observation store calls |
| HTTP 200 | PASS |

### REQ: D8 Mutex (full read-modify-write-reload cycle)

enrollMu.Lock() is acquired BEFORE lister.List() and released via defer AFTER
writePrincipalsAtomic completes (RunLocalGitCommit and userReloadFn inside).
The complete cycle Lookup -> append/remove -> MarshalPrincipals -> WriteAtomic ->
RunLocalGitCommit -> UserReload is fully inside the critical section.

| Check | Result |
|-------|--------|
| Lock before Read (lister.List()) | PASS |
| Lock covers modify (dedup + append/remove) | PASS |
| Lock covers WriteAtomic | PASS |
| Lock covers RunLocalGitCommit | PASS |
| Lock covers userReloadFn | PASS |
| Concurrent test (10 goroutines, final len==10, no dup) | PASS |
| No window outside the lock | PASS |

### REQ: Route Registration

    s.mux.HandleFunc POST /user/enrolled-projects  => withAuth(handleSelfEnrollProject)
    s.mux.HandleFunc DELETE /user/enrolled-projects => withAuth(handleSelfUnenrollProject)

PASS -- both verbs wired through withAuth; 401 smoke test confirms enforcement.

### Phase Task Completion

All tasks 11.1-16.2 marked [x] in openspec tasks.md and engram artifact
sdd/self-service-share-by-triage/tasks. Matches 4 commits on branch.

---

## Findings

### WARNING-1: go test -race Not Run (CGO/GCC Absent on Windows 11)

go test -race requires CGO which requires GCC/MinGW, absent on this machine.
TestSelfEnroll_Concurrent validates mutex correctness without race detection:
10 goroutines each POST a unique project for alice under enrollMu; final
len(enrolled)==10 with no duplicates -- PASSES.

Race detection MUST be verified in CI on a Linux runner before merge.
Recommended: add go test -race ./internal/cloud/cloudserver/... to CI pipeline.

### WARNING-2: handleSelfUnenrollProject Always Writes for No-Op Unenroll

Spec: users.yaml is not modified unnecessarily (idempotent unenroll scenario).
The implementation calls writePrincipalsAtomic even when the project was not found
in the Enrolled list (filter produces identical slice), producing a same-content
write plus a non-fatal git commit attempt.

Location: internal/cloud/cloudserver/self_enroll.go:handleSelfUnenrollProject

Fix: add a boolean changed flag before the filter loop; skip writePrincipalsAtomic
when no removal occurred.

Test gap: TestSelfUnenrollProject_Idempotent only asserts HTTP 200; does not verify
that users.yaml is unmodified (e.g., via file mtime or content comparison).

Behavior is CORRECT (final state identical); issue is unnecessary I/O and git noise.

### SUGGESTION-1: Package-Level Mutex Before Admin Handler Integration

enrollMu is var enrollMu sync.Mutex at package scope (design D8 explicit choice).
Admin handlers do not yet share it (acknowledged in design open questions:
"Should admin member-management handlers share the same enrollMu?").
When admin handlers are added to the mutex scope, a package-level variable
serializes across ALL CloudServer instances in-process including test instances.
Promoting to CloudServer.enrollMu before that expansion is recommended.

### SUGGESTION-2: No Unit Test for Caller-Not-Found 500 Path

When callerEmail returns empty string (attrProvider not implemented by auth stub),
both handlers return HTTP 500 "could not determine caller identity" with no dedicated
unit test. Adding a stub attrProvider returning empty email would document the
expected behavior and prevent silent regressions if auth wiring changes.

---

## Pre-Existing vs Regression Verdict

Batch 3 diff (git diff --name-only main..HEAD):

    internal/cloud/cloudserver/self_enroll.go      (new)
    internal/cloud/cloudserver/self_enroll_test.go (new)
    internal/cloud/cloudserver/cloudserver.go      (modified -- 2 route lines)
    internal/cloud/cloudserver/cloudserver_test.go (modified -- 401 smoke test)
    cmd/engram/private_by_default_e2e_test.go      (new -- test-only, no prod code)
    openspec/changes/self-service-share-by-triage/ (docs only)

| Package | Branch Failures | Main Failures | Verdict |
|---------|----------------|---------------|---------|
| internal/store | TestMigrate_Idempotent, TestNewErrorBranches | SAME | PRE-EXISTING (Windows SQLite file-lock) |
| internal/sync | TestManifestReadWrite, TestFileTransportReadManifestNotDir | SAME | PRE-EXISTING |
| internal/setup | TestInstallCodexPluginCLIAbsent, TestClaudeCodeUserPromptHookDefers... | SAME (main has 2 more) | PRE-EXISTING |
| internal/cloud/dashboard | Same as main (Batch 3 does not touch dashboard) | TestDashboardLayoutHTMLStructure + 4 others | PRE-EXISTING |
| cmd/engram | 11 tests (TestCmdCloudStatus* TestCmdServe* TestCmdMCPAutosync* etc.) | SAME 11 tests + TestPrintUsage timeout | PRE-EXISTING |
| internal/cloud/cloudserver | ALL PASS | No failures | CLEAN |

No regressions introduced by Batch 3.

---

## PR Readiness

- Build: CLEAN
- Batch 3 target package (cloudserver): 100% green
- BUG #955 e2e smoke: PASS
- CRITICAL: 0
- WARNING: 2 (race detection must run in CI; unnecessary unenroll write)
- SUGGESTION: 2 (mutex scope promotion; missing 500-path unit test)
- Phase 11-16 tasks: all complete
- Branch is PR-READY pending CI race detection run on Linux
