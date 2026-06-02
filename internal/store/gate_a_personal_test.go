package store

import (
	"database/sql"
	"testing"
	"time"
)

// TestGateAPersonalObservationNotEnqueued verifies that Gate A silently skips
// enqueueing sync mutations for observations with scope=personal.
//
// Gate A (spec: scope-enforcement S11) is the PRIMARY defense: personal
// observations must NEVER appear in sync_mutations destined for the cloud
// target. The server's Gate B is defense-in-depth only.
func TestGateAPersonalObservationNotEnqueued(t *testing.T) {
	s := newTestStore(t)

	cases := []struct {
		name      string
		scope     string
		wantInDB  bool // should a sync_mutations row appear?
	}{
		{"personal never enqueued", "personal", false},
		{"project enqueued", "project", true},
		{"department enqueued", "department", true},
		{"team enqueued", "team", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Enqueue a synthetic observation mutation directly via enqueueSyncMutationTx.
			syncID := "gate-a-" + tc.scope + "-" + t.Name()
			payload := syncObservationPayload{
				SyncID:    syncID,
				SessionID: "sess-gate-a",
				Type:      "manual",
				Title:     "Gate A Test",
				Content:   "content",
				Scope:     tc.scope,
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			}

			var enqueueErr error
			err := s.withTx(func(tx *sql.Tx) error {
				enqueueErr = s.enqueueSyncMutationTx(tx, SyncEntityObservation, syncID, SyncOpUpsert, payload)
				return enqueueErr
			})
			if err != nil {
				t.Fatalf("withTx: %v", err)
			}
			if enqueueErr != nil {
				t.Fatalf("enqueueSyncMutationTx(%q): %v", tc.scope, enqueueErr)
			}

			// Check whether the mutation was actually inserted.
			var count int
			row := s.db.QueryRow(
				`SELECT COUNT(*) FROM sync_mutations WHERE entity_key = ?`, syncID,
			)
			if err := row.Scan(&count); err != nil {
				t.Fatalf("count sync_mutations: %v", err)
			}

			if tc.wantInDB && count == 0 {
				t.Errorf("scope=%q: expected sync mutation to be enqueued, but found 0 rows", tc.scope)
			}
			if !tc.wantInDB && count > 0 {
				t.Errorf("scope=%q: expected personal observation to NOT be enqueued, but found %d row(s)", tc.scope, count)
			}
		})
	}
}
