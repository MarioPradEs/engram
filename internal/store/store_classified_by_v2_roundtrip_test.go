package store

import (
	"testing"
)

// TestClassifiedByV2RoundTripThroughGetObservationTx verifies that
// ClassifiedByV2 is included in the SELECT column list of getObservationTx
// and getObservationBySyncIDTx so the field survives a DB read cycle.
// Without the fix these functions omit classified_by_v2 from the SELECT and
// the field is always zero after a read.
//
// This is a pure-store unit test (no Postgres). We open a temp SQLite DB,
// apply the migration (which creates the classified_by_v2 column), insert a
// row with classified_by_v2=1, then read it back and assert the field is true.
func TestClassifiedByV2RoundTripThroughGetObservationTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB round-trip test in short mode")
	}
	s := newTestStore(t)
	defer s.Close()

	// Create a minimal session so the FK constraint is satisfied.
	sessionID := "sess-cv2"
	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		sessionID, "test-project", "/tmp",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Insert an observation with classified_by_v2 = 1.
	syncID := "obs-cv2-001"
	res, err := s.db.Exec(
		`INSERT INTO observations
			(sync_id, session_id, type, title, content, scope, normalized_hash,
			 revision_count, duplicate_count, last_seen_at, updated_at, classified_by_v2)
		 VALUES (?, ?, 'manual', 'T', 'C', 'project', 'h001', 1, 1, datetime('now'), datetime('now'), 1)`,
		syncID, sessionID,
	)
	if err != nil {
		t.Fatalf("insert observation: %v", err)
	}
	id, _ := res.LastInsertId()

	// Read via getObservationTx.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	obs, err := s.getObservationTx(tx, id)
	if err != nil {
		t.Fatalf("getObservationTx: %v", err)
	}
	if !obs.ClassifiedByV2 {
		t.Errorf("getObservationTx: ClassifiedByV2 should be true after DB round-trip, got false")
	}

	// Read via getObservationBySyncIDTx (include_deleted=true to match all states).
	obs2, err := s.getObservationBySyncIDTx(tx, syncID, true)
	if err != nil {
		t.Fatalf("getObservationBySyncIDTx: %v", err)
	}
	if !obs2.ClassifiedByV2 {
		t.Errorf("getObservationBySyncIDTx: ClassifiedByV2 should be true after DB round-trip, got false")
	}
}
