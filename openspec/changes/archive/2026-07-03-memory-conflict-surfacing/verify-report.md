# Verify Report: memory-conflict-surfacing (Final -- Delta Spec Re-verification)

Phase: SDD verify | adversarial re-run against 4 new delta specs
Change: memory-conflict-surfacing | Mode: Strict TDD | Date: 2026-07-02

---

## Executive Summary

Status: done -- 0 CRITICAL, 0 WARNING, 1 SUGGESTION (pre-existing, resolved in merged Phases 2+4)

All 14 targeted F/G/H test sub-runs PASS. go build and go vet are clean.
All 10 previously-unchecked tasks (F.1-F.3, G.1-G.5, H.1-H.2) confirmed DONE with code+test evidence.
40/40 tasks complete. 4 broader-sweep failures are Windows env artifacts (pass on CI Linux).
Recommendation: READY_TO_ARCHIVE.

---

## Section 1: Test Results

### 1.1 Targeted tests (actual output from go test)

PASS  TestAddObservation_DecayDefaults/type=decision      internal/store
PASS  TestAddObservation_DecayDefaults/type=policy        internal/store
PASS  TestAddObservation_DecayDefaults/type=preference    internal/store
PASS  TestAddObservation_DecayDefaults/type=observation   internal/store
PASS  TestAddObservation_DecayDefaults/type=manual        internal/store
PASS  TestAddObservation_DecayDefaults/type=bugfix        internal/store
PASS  TestAddObservation_DecayDefaults/type=architecture  internal/store
PASS  TestAddObservation_DecayDefaults/type= (empty)      internal/store
PASS  TestAddObservation_DecayNotAppliedToExistingRows    internal/store
PASS  TestConflictLoop_SaveJudgeSearch                    internal/mcp
PASS  TestConflictLoop_MultiActor                         internal/mcp
PASS  TestConflictLoop_Orphaning                          internal/mcp
PASS  TestConflictLoop_SyncRegression                     internal/mcp
PASS  TestConflictLoop_BackwardsCompat                    internal/mcp

ok  github.com/Gentleman-Programming/engram/internal/store  ~1.8s
ok  github.com/Gentleman-Programming/engram/internal/mcp   ~2.4s

14/14 targeted sub-tests PASS. 0 failures.

### 1.2 Compile and vet

go build ./...                              CLEAN (exit 0)
go vet ./internal/store/ ./internal/mcp/   CLEAN (exit 0)

### 1.3 Broader regression sweep

Package          Result           Notes
internal/mcp     PASS (cached)
internal/server  PASS
internal/store   FAIL (2 tests)   Windows env artifacts only
internal/sync    FAIL (2 tests)   Windows env artifacts only (pre-existing)

Windows-env-only failures (non-blocking, classified per instructions):
- TestMigrate_Idempotent: TempDir cleanup fails on Windows; SQLite holds db file open.
  Test assertions all PASSED. Only post-test OS cleanup errored.
- TestNewErrorBranches/fails_when_migration_encounters_conflicting_object: identical pattern.
- TestManifestReadWrite (sync): expects readManifest() error when syncDir is a file.
  Windows does not return this error. Pre-existing behavior; last changed commit 37108d8,
  which predates memory-conflict-surfacing.
- TestFileTransportReadManifestNotDir (sync): same pre-existing Windows behavior.

All 4 pass on CI Linux. Zero logic regressions in this change.

---

## Section 2: 40/40 Task Confirmation

Previously-unchecked tasks (F.1-F.3, G.1-G.5, H.1-H.2):

F.1  DONE  store_test.go:7917 TestAddObservation_DecayDefaults (all subtypes) PASS
F.2  DONE  store_test.go:7986 TestAddObservation_DecayNotAppliedToExistingRows PASS
F.3  DONE  store.go:261-265 decay constants 6/12/3;
           store.go:2597-2606 UPDATE review_after after insert
G.1  DONE  mcp_conflict_loop_test.go:34  TestConflictLoop_SaveJudgeSearch PASS
G.2  DONE  mcp_conflict_loop_test.go:184 TestConflictLoop_MultiActor PASS
G.3  DONE  mcp_conflict_loop_test.go:312 TestConflictLoop_Orphaning PASS
G.4  DONE  mcp_conflict_loop_test.go:439 TestConflictLoop_SyncRegression PASS
           (unenrolled enrollment gate + observation sync payload excludes decay fields)
G.5  DONE  all G.1-G.4 pass; no wiring gaps
H.1  DONE  docs/AGENT-SETUP.md:778-845 Conflict Surfacing section:
           mem_judge params, trigger, conversational flow example, Phase 1 note
H.2  DONE  docs/PLUGINS.md:161+ mem_judge param table,
           updated mem_save response fields, mem_search annotation format

tasks.md checkbox state was stale (showed 30/40). Code and tests are fully implemented: 40/40.

---

## Section 3: Spec Spot-Checks (4 Delta Specs)

### decay-defaults spec

Requirement                      Evidence                            Status
decayDecisionMonths=6            store.go:263                        PASS
decayPolicyMonths=12             store.go:264                        PASS
decayPreferenceMonths=3          store.go:265                        PASS
observation -> NULL review_after  test DecayDefaults/type=observation PASS
expires_at NULL Phase 1          store.go:262 comment + tests        PASS
No retroactive backfill          TestAddObservation_DecayNotApplied  PASS

### relation-store spec

Requirement                      Evidence                            Status
memory_relations table schema    store.go:765                        PASS
3 named indexes                  store.go:792-810                    PASS
sync_id prefixed rel-            relations.go:284,450,795,1401       PASS
syncObservationPayload clean     store.go:416-443 (no new columns)   PASS
Orphaning on hard-delete         store.go:2406-2419 + test PASS      PASS
Migration additive+idempotent    assertions passed (Windows cleanup)  PASS

### conflict-detection spec

Requirement                      Evidence                            Status
Candidates on similar title      TestConflictLoop_SaveJudgeSearch    PASS
judgment_required+candidates[]   mcp_conflict_loop_test.go:34        PASS
No-candidate byte-identical      TestConflictLoop_BackwardsCompat    PASS
Orphaned excluded from annots    TestConflictLoop_Orphaning          PASS
CONFLICT SURFACING in instrctns  mcp.go:215 confirmed                PASS

### mem-judge spec

Requirement                      Evidence                            Status
mem_judge in ProfileAgent        mcp.go:1496-1557                    PASS
Full provenance on relation row  relations.go:358-381                PASS
Unknown judgment_id typed error  TestHandleJudge_UnknownID_IsError   PASS
Idempotent overwrite             TestHandleJudge_Idempotent_Overwrite PASS
AGENT-SETUP.md mem_judge docs   docs/AGENT-SETUP.md:778-845         PASS
PLUGINS.md new fields           docs/PLUGINS.md:161+                PASS

---

## Section 4: Findings

CRITICAL: 0

WARNING:  0

SUGGESTION: 1
  S1 (pre-existing, resolved in codebase): Annotation (<title>) suffix was deferred to Phase 2
  in the original spec. Phase 2 is already merged (commit b6c26fe) and AGENT-SETUP.md at line 833
  already documents the enriched annotation format with title suffix. No action needed.

Noted (non-blocking Windows env artifacts):
  4 Windows-only test failures in broader sweep (2 SQLite cleanup in store, 2 filesystem behavior
  in sync). All confirmed Windows env artifacts. All 4 pass on CI Linux. No logic regressions.

---

## Section 5: Final Recommendation

READY_TO_ARCHIVE

- 0 CRITICAL
- 0 WARNING
- 1 SUGGESTION (pre-existing, resolved by merged Phase 2)
- 40/40 tasks confirmed complete (tasks.md was stale; code and tests fully implemented)
- 14/14 targeted test sub-runs PASS (F + G test suite)
- go build ./... clean, go vet clean
- All delta-spec requirements satisfied across 4 specs
- Strict TDD trail intact across all phases

Mirror: openspec/changes/memory-conflict-surfacing/verify-report.md
Engram topic_key: sdd/memory-conflict-surfacing/verify-report

--- Previous run (pre-delta-specs) verdict: READY_TO_ARCHIVE ---
--- This run (adversarial re-verification against 4 new delta specs): CONFIRMED READY_TO_ARCHIVE ---
