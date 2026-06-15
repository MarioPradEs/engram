package store

import (
	"database/sql"
	"testing"
	"time"
)

// TestGateBPromptNotExportedToCloud verifies that Gate B prevents prompt
// mutations from being returned by ListPendingSyncMutations (the cloud-export
// path). Prompts are raw user context, local-only by company policy — the
// shared team brain holds only observations (+ relations).
//
// The row IS stored in sync_mutations for local machinery (repair, backfill),
// but is invisible to the export selector.
func TestGateBPromptNotExportedToCloud(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("gate-b-prompt-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	syncID := "gate-b-prompt-test"
	proj := "gate-b-prompt-proj"
	payload := syncPromptPayload{
		SyncID:    syncID,
		SessionID: "sess-gate-b-prompt",
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
			t.Errorf("Gate B: prompt mutation (key=%s) must not be exported to cloud via ListPendingSyncMutations", syncID)
		}
	}
}

// TestGateBSessionNotExportedToCloud verifies that Gate B prevents session
// mutations from being returned by ListPendingSyncMutations. Sessions are
// local runtime context, local-only by company policy.
//
// The row IS stored in sync_mutations for local machinery (repair, backfill,
// delete propagation), but is invisible to the export selector.
func TestGateBSessionNotExportedToCloud(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("gate-b-sess-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	syncID := "gate-b-session-test"
	payload := syncSessionPayload{
		ID:      syncID,
		Project: "gate-b-sess-proj",
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
			t.Errorf("Gate B: session mutation (key=%s) must not be exported to cloud via ListPendingSyncMutations", syncID)
		}
	}
}

// TestGateBObservationAndRelationStillEnqueue verifies that Gate B does NOT
// over-filter: non-personal observations and relations must still be enqueued
// and returned by ListPendingSyncMutations.
func TestGateBObservationAndRelationStillEnqueue(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("gate-b-proj"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	obsSyncID := "gate-b-obs-test"
	gateProj := "gate-b-proj"
	obsPayload := syncObservationPayload{
		SyncID:    obsSyncID,
		SessionID: "sess-gate-b-obs",
		Project:   &gateProj,
		Scope:     "project",
		Type:      "manual",
		Title:     "Gate B obs",
		Content:   "content",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	relSyncID := "gate-b-rel-test"
	relPayload := syncRelationPayload{
		SyncID:        relSyncID,
		SourceID:      obsSyncID,
		TargetID:      obsSyncID, // self-relation, valid for this test
		Relation:      "related",
		Project:       "gate-b-proj",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
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
// observation filtering) is still intact after Gate B changes.
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

// TestListPendingSyncMutationsExcludesPromptAndSession verifies that
// ListPendingSyncMutations does not return prompt or session rows even when
// they were directly inserted into sync_mutations (representing ~2860 legacy rows).
func TestListPendingSyncMutationsExcludesPromptAndSession(t *testing.T) {
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
			t.Errorf("ListPendingSyncMutations returned a prompt row (key=%s) — must be excluded", m.EntityKey)
		}
		if m.Entity == SyncEntitySession {
			t.Errorf("ListPendingSyncMutations returned a session row (key=%s) — must be excluded", m.EntityKey)
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

// TestListPendingSyncMutationsAfterSeqExcludesPromptAndSession verifies that
// ListPendingSyncMutationsAfterSeq also excludes prompt and session rows.
func TestListPendingSyncMutationsAfterSeqExcludesPromptAndSession(t *testing.T) {
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
			t.Errorf("ListPendingSyncMutationsAfterSeq returned a prompt row (key=%s) — must be excluded", m.EntityKey)
		}
		if m.Entity == SyncEntitySession {
			t.Errorf("ListPendingSyncMutationsAfterSeq returned a session row (key=%s) — must be excluded", m.EntityKey)
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

// TestCountPendingNonEnrolledSyncMutationsExcludesPromptAndSession verifies
// that CountPendingNonEnrolledSyncMutations does not count prompt or session
// rows, so they do not block sync readiness checks.
func TestCountPendingNonEnrolledSyncMutationsExcludesPromptAndSession(t *testing.T) {
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
			t.Errorf("CountPendingNonEnrolledSyncMutations counted %d prompt/session row(s) for non-enrolled-proj — must exclude them", c.Count)
		}
	}
}
