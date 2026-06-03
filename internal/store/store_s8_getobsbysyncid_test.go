package store

import (
	"testing"
)

// TestGetObservationBySyncID_ClassifiedByV2RoundTrip verifies S8:
// GetObservationBySyncID must include classified_by_v2 in its SELECT and scan
// so the field round-trips correctly. Without the fix the field is always false
// regardless of what is stored in the DB.
func TestGetObservationBySyncID_ClassifiedByV2RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB round-trip test in short mode")
	}
	s := newTestStore(t)
	defer s.Close()

	// Create a minimal session so the FK constraint is satisfied.
	sessionID := "sess-s8"
	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		sessionID, "test-project", "/tmp",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Insert an observation with classified_by_v2 = 1 via direct SQL so we
	// control the column value precisely (bypasses AddObservation defaults).
	syncID := "obs-s8-001"
	_, err := s.db.Exec(
		`INSERT INTO observations
			(sync_id, session_id, type, title, content, scope, normalized_hash,
			 revision_count, duplicate_count, last_seen_at, updated_at, classified_by_v2)
		 VALUES (?, ?, 'manual', 'S8 title', 'S8 content', 'project', 'h-s8-001',
		         1, 1, datetime('now'), datetime('now'), 1)`,
		syncID, sessionID,
	)
	if err != nil {
		t.Fatalf("insert observation with classified_by_v2=1: %v", err)
	}

	// Read back via the PUBLIC GetObservationBySyncID method (the S8 gap).
	obs, err := s.GetObservationBySyncID(syncID)
	if err != nil {
		t.Fatalf("GetObservationBySyncID: %v", err)
	}
	if obs == nil {
		t.Fatal("GetObservationBySyncID: returned nil observation")
	}

	// S8 assertion: classified_by_v2 must survive the round-trip.
	if !obs.ClassifiedByV2 {
		t.Errorf("GetObservationBySyncID: ClassifiedByV2 should be true after DB round-trip, got false (S8 fix not applied)")
	}
}

// TestGetObservationBySyncID_ClassifiedByV2FalseWhenUnset verifies that
// GetObservationBySyncID returns ClassifiedByV2=false when the column is 0.
// This ensures the fix does not invert the default behavior.
func TestGetObservationBySyncID_ClassifiedByV2FalseWhenUnset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DB round-trip test in short mode")
	}
	s := newTestStore(t)
	defer s.Close()

	sessionID := "sess-s8-false"
	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		sessionID, "test-project", "/tmp",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Insert observation with classified_by_v2 = 0 (legacy/default).
	syncID := "obs-s8-002"
	_, err := s.db.Exec(
		`INSERT INTO observations
			(sync_id, session_id, type, title, content, scope, normalized_hash,
			 revision_count, duplicate_count, last_seen_at, updated_at, classified_by_v2)
		 VALUES (?, ?, 'manual', 'S8 unset', 'content', 'project', 'h-s8-002',
		         1, 1, datetime('now'), datetime('now'), 0)`,
		syncID, sessionID,
	)
	if err != nil {
		t.Fatalf("insert observation with classified_by_v2=0: %v", err)
	}

	obs, err := s.GetObservationBySyncID(syncID)
	if err != nil {
		t.Fatalf("GetObservationBySyncID: %v", err)
	}
	if obs.ClassifiedByV2 {
		t.Errorf("GetObservationBySyncID: ClassifiedByV2 should be false when column is 0, got true")
	}
}
