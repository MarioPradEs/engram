package store

import (
	"database/sql"
	"testing"
	"time"
)

// ─── Chunk/snapshot cloud-export gate (ExportProjectForCloud) ─────────────────
//
// Gate C: the cloud snapshot export path (ExportProjectForCloud) must strip
// sessions and prompts from the exported payload. The shared team brain holds
// ONLY observations (+ relations). Sessions are local runtime context;
// prompts are raw user input — both are local-only by company policy.

// TestChunkExportGateCloudExportStripsSessionsAndPrompts verifies that
// ExportProjectForCloud returns Sessions=0 and Prompts=0 even when the project
// has sessions and prompts, and that all non-deleted observations are included.
func TestChunkExportGateCloudExportStripsSessionsAndPrompts(t *testing.T) {
	s := newTestStore(t)

	proj := "cloud-gate-proj"
	if err := s.CreateSession("cloud-gate-sess", proj, "/tmp/cloud-gate"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "cloud-gate-sess",
		Type:      "decision",
		Title:     "cloud gate obs",
		Content:   "observations must pass through",
		Project:   proj,
		Scope:     "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "cloud-gate-sess",
		Content:   "user prompt — local-only",
		Project:   proj,
	}); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}

	data, err := s.ExportProjectForCloud(proj)
	if err != nil {
		t.Fatalf("ExportProjectForCloud: %v", err)
	}

	// Gate C: sessions must be stripped.
	if len(data.Sessions) != 0 {
		t.Errorf("chunk-export gate: ExportProjectForCloud must return 0 sessions, got %d — sessions are local-only", len(data.Sessions))
	}
	// Gate C: prompts must be stripped.
	if len(data.Prompts) != 0 {
		t.Errorf("chunk-export gate: ExportProjectForCloud must return 0 prompts, got %d — prompts are local-only", len(data.Prompts))
	}
	// Observations must pass through.
	if len(data.Observations) != 1 {
		t.Errorf("chunk-export gate: expected 1 observation, got %d — observations must not be stripped", len(data.Observations))
	}
}

// TestChunkExportGateLocalExportPreservesSessionsAndPrompts verifies that the
// local full-export path (ExportProject, used by the /export HTTP handler) still
// includes sessions and prompts. This is the regression guard.
func TestChunkExportGateLocalExportPreservesSessionsAndPrompts(t *testing.T) {
	s := newTestStore(t)

	proj := "local-gate-proj"
	if err := s.CreateSession("local-gate-sess", proj, "/tmp/local-gate"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "local-gate-sess",
		Type:      "note",
		Title:     "local export obs",
		Content:   "local export must include everything",
		Project:   proj,
		Scope:     "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "local-gate-sess",
		Content:   "user prompt — must be in local export",
		Project:   proj,
	}); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}

	data, err := s.ExportProject(proj)
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}

	// Regression guard: local export must still include sessions.
	if len(data.Sessions) == 0 {
		t.Error("local-export regression: ExportProject must include sessions but returned 0")
	}
	// Regression guard: local export must still include prompts.
	if len(data.Prompts) == 0 {
		t.Error("local-export regression: ExportProject must include prompts but returned 0")
	}
	if len(data.Observations) == 0 {
		t.Error("local-export regression: ExportProject must include observations but returned 0")
	}
}

// TestChunkExportGateCloudExportMultipleObservations verifies that all
// non-deleted observations for the project are returned by ExportProjectForCloud,
// even when sessions and prompts exist alongside them.
func TestChunkExportGateCloudExportMultipleObservations(t *testing.T) {
	s := newTestStore(t)

	proj := "cloud-multi-obs-proj"
	if err := s.CreateSession("cloud-multi-sess", proj, "/tmp/cloud-multi"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "cloud-multi-sess",
			Type:      "manual",
			Title:     "obs",
			Content:   "content",
			Project:   proj,
			Scope:     "project",
		}); err != nil {
			t.Fatalf("AddObservation %d: %v", i, err)
		}
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "cloud-multi-sess",
		Content:   "prompt",
		Project:   proj,
	}); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}

	data, err := s.ExportProjectForCloud(proj)
	if err != nil {
		t.Fatalf("ExportProjectForCloud: %v", err)
	}

	if len(data.Sessions) != 0 {
		t.Errorf("cloud export gate: expected 0 sessions, got %d", len(data.Sessions))
	}
	if len(data.Prompts) != 0 {
		t.Errorf("cloud export gate: expected 0 prompts, got %d", len(data.Prompts))
	}
	// Dedupe may collapse identical observations, so we just need at least 1.
	if len(data.Observations) == 0 {
		t.Error("cloud export gate: expected observations to be present, got 0")
	}
}

// TestLocalOnlyEntityFilterPromptNotExportedToCloud verifies that the
// local-only entity filter (prompts/sessions are never exported to cloud)
// prevents prompt mutations from being returned by ListPendingSyncMutations
// (the cloud-export path). Prompts are raw user context, local-only by company
// policy — the shared team brain holds only observations (+ relations).
//
// The row IS stored in sync_mutations for local machinery (repair, backfill),
// but is invisible to the export selector.
func TestLocalOnlyEntityFilterPromptNotExportedToCloud(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("local-filter-prompt-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	syncID := "local-filter-prompt-test"
	proj := "local-filter-prompt-proj"
	payload := syncPromptPayload{
		SyncID:    syncID,
		SessionID: "sess-local-filter-prompt",
		Project:   &proj,
	}

	var enqueueErr error
	err := s.withTx(func(tx *sql.Tx) error {
		enqueueErr = s.enqueueSyncMutationTx(tx, SyncEntityPrompt, syncID, SyncOpUpsert, payload)
		return enqueueErr
	})
	if err != nil {
		t.Fatalf("withTx: %v", err)
	}
	if enqueueErr != nil {
		t.Fatalf("enqueueSyncMutationTx(prompt): %v", enqueueErr)
	}

	// The row must exist in sync_mutations (local machinery still needs it).
	var count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
		SyncEntityPrompt, syncID,
	).Scan(&count); err != nil {
		t.Fatalf("count sync_mutations for prompt: %v", err)
	}
	if count == 0 {
		t.Errorf("prompt mutation row must exist in sync_mutations for local machinery, got 0")
	}

	// But ListPendingSyncMutations (the cloud-export selector) must not return it.
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	for _, m := range mutations {
		if m.Entity == SyncEntityPrompt && m.EntityKey == syncID {
			t.Errorf("local-only entity filter: prompt mutation (key=%s) must not be exported to cloud via ListPendingSyncMutations", syncID)
		}
	}
}

// TestLocalOnlyEntityFilterSessionNotExportedToCloud verifies that the
// local-only entity filter (prompts/sessions are never exported to cloud)
// prevents session mutations from being returned by ListPendingSyncMutations.
// Sessions are local runtime context, local-only by company policy.
//
// The row IS stored in sync_mutations for local machinery (repair, backfill,
// delete propagation), but is invisible to the export selector.
func TestLocalOnlyEntityFilterSessionNotExportedToCloud(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("local-filter-sess-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	syncID := "local-filter-session-test"
	payload := syncSessionPayload{
		ID:      syncID,
		Project: "local-filter-sess-proj",
	}

	var enqueueErr error
	err := s.withTx(func(tx *sql.Tx) error {
		enqueueErr = s.enqueueSyncMutationTx(tx, SyncEntitySession, syncID, SyncOpUpsert, payload)
		return enqueueErr
	})
	if err != nil {
		t.Fatalf("withTx: %v", err)
	}
	if enqueueErr != nil {
		t.Fatalf("enqueueSyncMutationTx(session): %v", enqueueErr)
	}

	// The row must exist in sync_mutations for local machinery.
	var count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
		SyncEntitySession, syncID,
	).Scan(&count); err != nil {
		t.Fatalf("count sync_mutations for session: %v", err)
	}
	if count == 0 {
		t.Errorf("session mutation row must exist in sync_mutations for local machinery, got 0")
	}

	// But ListPendingSyncMutations (the cloud-export selector) must not return it.
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	for _, m := range mutations {
		if m.Entity == SyncEntitySession && m.EntityKey == syncID {
			t.Errorf("local-only entity filter: session mutation (key=%s) must not be exported to cloud via ListPendingSyncMutations", syncID)
		}
	}
}

// TestLocalOnlyEntityFilterObservationAndRelationStillEnqueue verifies that
// the local-only entity filter does NOT over-filter: non-personal observations
// and relations must still be enqueued and returned by ListPendingSyncMutations.
func TestLocalOnlyEntityFilterObservationAndRelationStillEnqueue(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("local-filter-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	obsSyncID := "local-filter-obs-test"
	filterProj := "local-filter-proj"
	obsPayload := syncObservationPayload{
		SyncID:    obsSyncID,
		SessionID: "sess-local-filter-obs",
		Project:   &filterProj,
		Scope:     "project",
		Type:      "manual",
		Title:     "local-only filter obs",
		Content:   "content",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	relSyncID := "local-filter-rel-test"
	relPayload := syncRelationPayload{
		SyncID:    relSyncID,
		SourceID:  obsSyncID,
		TargetID:  obsSyncID, // self-relation, valid for this test
		Relation:  "related",
		Project:   "local-filter-proj",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	err := s.withTx(func(tx *sql.Tx) error {
		if err := s.enqueueSyncMutationTx(tx, SyncEntityObservation, obsSyncID, SyncOpUpsert, obsPayload); err != nil {
			return err
		}
		return s.enqueueSyncMutationTx(tx, SyncEntityRelation, relSyncID, SyncOpUpsert, relPayload)
	})
	if err != nil {
		t.Fatalf("withTx: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}

	foundObs, foundRel := false, false
	for _, m := range mutations {
		if m.Entity == SyncEntityObservation && m.EntityKey == obsSyncID {
			foundObs = true
		}
		if m.Entity == SyncEntityRelation && m.EntityKey == relSyncID {
			foundRel = true
		}
	}
	if !foundObs {
		t.Error("project-scoped observation must be returned by ListPendingSyncMutations but was not")
	}
	if !foundRel {
		t.Error("relation must be returned by ListPendingSyncMutations but was not")
	}
}

// TestGateAPersonalObservationRegressionGuard ensures that Gate A (personal
// observation filtering) is still intact after local-only entity filter changes.
func TestGateAPersonalObservationRegressionGuard(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("gate-reg-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	personalSyncID := "gate-reg-personal"
	regProj := "gate-reg-proj"
	personalPayload := syncObservationPayload{
		SyncID:    personalSyncID,
		SessionID: "sess-gate-reg",
		Project:   &regProj,
		Scope:     "personal",
		Type:      "manual",
		Title:     "Personal obs",
		Content:   "secret",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	err := s.withTx(func(tx *sql.Tx) error {
		return s.enqueueSyncMutationTx(tx, SyncEntityObservation, personalSyncID, SyncOpUpsert, personalPayload)
	})
	if err != nil {
		t.Fatalf("withTx: %v", err)
	}

	var count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
		SyncEntityObservation, personalSyncID,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count > 0 {
		t.Errorf("Gate A regression: personal observation was enqueued (%d row(s)) — Gate A must NOT have been removed", count)
	}
}

// TestLocalOnlyEntityFilterListPendingExcludesPromptAndSession verifies that
// ListPendingSyncMutations does not return prompt or session rows even when
// they were directly inserted into sync_mutations (representing ~2860 legacy rows).
func TestLocalOnlyEntityFilterListPendingExcludesPromptAndSession(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("list-excl-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Insert rows directly — bypassing enqueueSyncMutationTx — to simulate
	// existing pending rows in the database.
	insert := func(entity, key string) {
		t.Helper()
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey, entity, key, SyncOpUpsert, `{}`, SyncSourceLocal, "list-excl-proj",
		); err != nil {
			t.Fatalf("direct insert %s/%s: %v", entity, key, err)
		}
	}

	insert(SyncEntityPrompt, "legacy-prompt-1")
	insert(SyncEntitySession, "legacy-session-1")
	insert(SyncEntityObservation, "legacy-obs-1")

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}

	for _, m := range mutations {
		if m.Entity == SyncEntityPrompt {
			t.Errorf("ListPendingSyncMutations returned a prompt row (key=%s) — must be excluded by local-only entity filter", m.EntityKey)
		}
		if m.Entity == SyncEntitySession {
			t.Errorf("ListPendingSyncMutations returned a session row (key=%s) — must be excluded by local-only entity filter", m.EntityKey)
		}
	}

	foundObs := false
	for _, m := range mutations {
		if m.Entity == SyncEntityObservation && m.EntityKey == "legacy-obs-1" {
			foundObs = true
		}
	}
	if !foundObs {
		t.Error("ListPendingSyncMutations must still return enrolled observation rows")
	}
}

// TestLocalOnlyEntityFilterListAfterSeqExcludesPromptAndSession verifies that
// ListPendingSyncMutationsAfterSeq also excludes prompt and session rows.
func TestLocalOnlyEntityFilterListAfterSeqExcludesPromptAndSession(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("afterseq-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	insert := func(entity, key string) int64 {
		t.Helper()
		res, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey, entity, key, SyncOpUpsert, `{}`, SyncSourceLocal, "afterseq-proj",
		)
		if err != nil {
			t.Fatalf("direct insert %s/%s: %v", entity, key, err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	insert(SyncEntityPrompt, "afterseq-prompt-1")
	insert(SyncEntitySession, "afterseq-session-1")
	insert(SyncEntityObservation, "afterseq-obs-1")

	mutations, err := s.ListPendingSyncMutationsAfterSeq(DefaultSyncTargetKey, 0, 100)
	if err != nil {
		t.Fatalf("ListPendingSyncMutationsAfterSeq: %v", err)
	}

	for _, m := range mutations {
		if m.Entity == SyncEntityPrompt {
			t.Errorf("ListPendingSyncMutationsAfterSeq returned a prompt row (key=%s) — must be excluded by local-only entity filter", m.EntityKey)
		}
		if m.Entity == SyncEntitySession {
			t.Errorf("ListPendingSyncMutationsAfterSeq returned a session row (key=%s) — must be excluded by local-only entity filter", m.EntityKey)
		}
	}

	foundObs := false
	for _, m := range mutations {
		if m.Entity == SyncEntityObservation && m.EntityKey == "afterseq-obs-1" {
			foundObs = true
		}
	}
	if !foundObs {
		t.Error("ListPendingSyncMutationsAfterSeq must still return enrolled observation rows")
	}
}

// TestLocalOnlyEntityFilterCountNonEnrolledExcludesPromptAndSession verifies
// that CountPendingNonEnrolledSyncMutations does not count prompt or session
// rows, so they do not block sync readiness checks.
func TestLocalOnlyEntityFilterCountNonEnrolledExcludesPromptAndSession(t *testing.T) {
	s := newTestStore(t)

	// Insert prompt and session rows for a non-enrolled project.
	insert := func(entity, key, project string) {
		t.Helper()
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey, entity, key, SyncOpUpsert, `{}`, SyncSourceLocal, project,
		); err != nil {
			t.Fatalf("direct insert %s/%s: %v", entity, key, err)
		}
	}

	// Non-enrolled project with only prompt and session rows.
	insert(SyncEntityPrompt, "count-prompt-1", "non-enrolled-proj")
	insert(SyncEntitySession, "count-session-1", "non-enrolled-proj")

	counts, err := s.CountPendingNonEnrolledSyncMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("CountPendingNonEnrolledSyncMutations: %v", err)
	}

	for _, c := range counts {
		if c.Project == "non-enrolled-proj" {
			t.Errorf("CountPendingNonEnrolledSyncMutations counted %d prompt/session row(s) for non-enrolled-proj — must exclude them (local-only entity filter)", c.Count)
		}
	}
}

// TestLocalOnlyEntityFilterAckSeqsReachesHealthy verifies that after acking
// all observation/relation rows, the lifecycle reaches SyncLifecycleHealthy
// even when only prompt/session rows remain unacked. Prompt/session rows are
// never pushed to the cloud and must not count in the remaining-unacked tally
// that determines lifecycle state.
func TestLocalOnlyEntityFilterAckSeqsReachesHealthy(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("ack-lifecycle-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Insert an observation row (will be acked) and a prompt + session row
	// (local-only, never acked by cloud machinery).
	insert := func(entity, key string) int64 {
		t.Helper()
		res, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey, entity, key, SyncOpUpsert, `{}`, SyncSourceLocal, "ack-lifecycle-proj",
		)
		if err != nil {
			t.Fatalf("direct insert %s/%s: %v", entity, key, err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	obsSeq := insert(SyncEntityObservation, "ack-obs-1")
	insert(SyncEntityPrompt, "ack-prompt-1")    // never acked — local-only
	insert(SyncEntitySession, "ack-session-1")  // never acked — local-only

	// Ack only the observation row.
	if err := s.AckSyncMutationSeqs(DefaultSyncTargetKey, []int64{obsSeq}); err != nil {
		t.Fatalf("AckSyncMutationSeqs: %v", err)
	}

	// Lifecycle must be Healthy: prompt/session rows must not count in the
	// remaining-unacked total (local-only entity filter on the ack path).
	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if state.Lifecycle != SyncLifecycleHealthy {
		t.Errorf("expected lifecycle %q after acking observation, got %q — prompt/session rows must not block ack lifecycle (local-only entity filter)", SyncLifecycleHealthy, state.Lifecycle)
	}
}
