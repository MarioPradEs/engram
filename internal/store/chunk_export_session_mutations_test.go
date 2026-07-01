package store

import (
	"database/sql"
	"testing"
)

// TestListPendingSyncMutationsForExportAfterSeq_IncludesSessionExcludesPrompt
// verifies that the dedicated cloud-export query method includes session upsert
// mutations in its result set while excluding prompt mutations.
//
// Context: the cloud chunk export path sends chunk.Mutations to the server;
// validateChunkSessionReferences reads session-upsert entries from chunk.Mutations
// to build the set of known session IDs when knownSessionIDs is empty (first push
// to a new project). If session mutations are absent, the server returns HTTP 400
// "observations[N] references missing session_id". Prompts must remain excluded —
// they are local-only by company policy.
//
// Contrast with ListPendingSyncMutationsAfterSeq (mutation-push API path) which
// correctly excludes BOTH sessions and prompts — that method is intentionally
// unchanged by this fix.
func TestListPendingSyncMutationsForExportAfterSeq_IncludesSessionExcludesPrompt(t *testing.T) {
	tests := []struct {
		name        string
		wantSession bool
		wantObs     bool
		wantPrompt  bool
	}{
		{
			name:        "session included prompt excluded obs included",
			wantSession: true,
			wantObs:     true,
			wantPrompt:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)

			const (
				project   = "export-method-test-proj"
				sessionID = "manual-save-export-method-test"
				directory = "/tmp/export-method-test"
			)

			if err := s.EnrollProject(project); err != nil {
				t.Fatalf("EnrollProject: %v", err)
			}
			if err := s.CreateSession(sessionID, project, directory); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if _, err := s.AddObservation(AddObservationParams{
				SessionID: sessionID,
				Type:      "decision",
				Title:     "export test observation",
				Content:   "content for export test",
				Project:   project,
				Scope:     "project",
			}); err != nil {
				t.Fatalf("AddObservation: %v", err)
			}
			// Enqueue a prompt mutation directly to confirm it is excluded.
			promptSyncID := "export-method-prompt-test"
			proj := project
			err := s.withTx(func(tx *sql.Tx) error {
				return s.enqueueSyncMutationTx(tx, SyncEntityPrompt, promptSyncID, SyncOpUpsert, syncPromptPayload{
					SyncID:    promptSyncID,
					SessionID: sessionID,
					Project:   &proj,
				})
			})
			if err != nil {
				t.Fatalf("enqueue prompt mutation: %v", err)
			}

			mutations, err := s.ListPendingSyncMutationsForExportAfterSeq(DefaultSyncTargetKey, 0, 100)
			if err != nil {
				t.Fatalf("ListPendingSyncMutationsForExportAfterSeq: %v", err)
			}

			sessionCount, obsCount, promptCount := 0, 0, 0
			for _, m := range mutations {
				switch m.Entity {
				case SyncEntitySession:
					sessionCount++
				case SyncEntityObservation:
					obsCount++
				case SyncEntityPrompt:
					promptCount++
				}
			}

			if tt.wantSession && sessionCount == 0 {
				t.Error("ListPendingSyncMutationsForExportAfterSeq must include session mutations (needed by server validateChunkSessionReferences), got 0")
			}
			if !tt.wantSession && sessionCount > 0 {
				t.Errorf("ListPendingSyncMutationsForExportAfterSeq must not include session mutations, got %d", sessionCount)
			}
			if tt.wantObs && obsCount == 0 {
				t.Error("ListPendingSyncMutationsForExportAfterSeq must include observation mutations, got 0")
			}
			if tt.wantPrompt && promptCount == 0 {
				t.Error("ListPendingSyncMutationsForExportAfterSeq must include prompt mutations, got 0")
			}
			if !tt.wantPrompt && promptCount > 0 {
				t.Errorf("ListPendingSyncMutationsForExportAfterSeq must exclude prompt mutations (local-only), got %d", promptCount)
			}
		})
	}
}

// TestListPendingSyncMutationsForExportAfterSeq_PaginationAdvances verifies that
// the new export method handles pagination correctly (same as AfterSeq sibling).
func TestListPendingSyncMutationsForExportAfterSeq_PaginationAdvances(t *testing.T) {
	s := newTestStore(t)

	const project = "export-pagination-proj"
	const sessionID = "export-pagination-sess"

	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	if err := s.CreateSession(sessionID, project, "/tmp/export-pagination"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Add two observations to ensure multiple rows.
	for i := 0; i < 2; i++ {
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: sessionID,
			Type:      "manual",
			Title:     "pagination obs",
			Content:   "content",
			Project:   project,
			Scope:     "project",
		}); err != nil {
			t.Fatalf("AddObservation %d: %v", i, err)
		}
	}

	// Fetch all mutations.
	all, err := s.ListPendingSyncMutationsForExportAfterSeq(DefaultSyncTargetKey, 0, 100)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("expected mutations but got none")
	}

	// Fetch with afterSeq = first seq — must skip the first row.
	firstSeq := all[0].Seq
	rest, err := s.ListPendingSyncMutationsForExportAfterSeq(DefaultSyncTargetKey, firstSeq, 100)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(rest) >= len(all) {
		t.Errorf("expected fewer rows after seq %d, got %d (same as all=%d)", firstSeq, len(rest), len(all))
	}
	for _, m := range rest {
		if m.Seq <= firstSeq {
			t.Errorf("returned row with seq %d <= afterSeq %d", m.Seq, firstSeq)
		}
	}
}

// TestListPendingSyncMutationsAfterSeq_RegressionGuard_StillExcludesBothEntities
// ensures the PUBLIC mutation-push API method (ListPendingSyncMutationsAfterSeq)
// still excludes both session and prompt entities after the export-path fix.
// This is the regression guard for the mutation-push / UI path.
func TestListPendingSyncMutationsAfterSeq_RegressionGuard_StillExcludesBothEntities(t *testing.T) {
	s := newTestStore(t)

	const project = "regression-guard-proj"

	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Insert session and prompt rows directly to confirm they are filtered.
	insert := func(entity, key string) {
		t.Helper()
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey, entity, key, SyncOpUpsert, `{}`, SyncSourceLocal, project,
		); err != nil {
			t.Fatalf("direct insert %s/%s: %v", entity, key, err)
		}
	}
	insert(SyncEntitySession, "rg-session-1")
	insert(SyncEntityPrompt, "rg-prompt-1")
	insert(SyncEntityObservation, "rg-obs-1")

	mutations, err := s.ListPendingSyncMutationsAfterSeq(DefaultSyncTargetKey, 0, 100)
	if err != nil {
		t.Fatalf("ListPendingSyncMutationsAfterSeq: %v", err)
	}

	for _, m := range mutations {
		if m.Entity == SyncEntitySession {
			t.Errorf("regression: ListPendingSyncMutationsAfterSeq (mutation-push API) must still exclude session mutations, got entity_key=%s", m.EntityKey)
		}
		if m.Entity == SyncEntityPrompt {
			t.Errorf("regression: ListPendingSyncMutationsAfterSeq (mutation-push API) must still exclude prompt mutations, got entity_key=%s", m.EntityKey)
		}
	}
}
