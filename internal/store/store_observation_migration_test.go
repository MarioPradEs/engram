package store

import (
	"database/sql"
	"testing"
)

// TestObservationsClassifiedByV2Column is an integration test (NOT -short)
// that verifies the observations.classified_by_v2 column is present and
// defaults to 0 after migrate() runs.
func TestObservationsClassifiedByV2Column(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	t.Parallel()

	s := newTestStore(t)

	// Column must exist with default 0.
	var colCount int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_xinfo('observations') WHERE name = 'classified_by_v2'
	`).Scan(&colCount)
	if err != nil {
		t.Fatalf("pragma_table_xinfo: %v", err)
	}
	if colCount == 0 {
		t.Fatal("observations.classified_by_v2 column not found after migrate()")
	}

	// Insert a row without providing classified_by_v2; it should default to 0.
	if err := s.CreateSession("test-sess", "test-project", t.TempDir()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "test-sess",
		Type:      "manual",
		Title:     "test obs",
		Content:   "content",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	var v2col sql.NullInt64
	err = s.db.QueryRow(`SELECT classified_by_v2 FROM observations WHERE id = ?`, obsID).Scan(&v2col)
	if err != nil {
		t.Fatalf("SELECT classified_by_v2: %v", err)
	}
	if v2col.Valid && v2col.Int64 != 0 {
		t.Errorf("expected classified_by_v2=0 by default, got %d", v2col.Int64)
	}
}

// TestObservationsClassifiedByV2IdempotentMigrate verifies that running
// New() on a store that already has the classified_by_v2 column is a no-op
// (idempotent migration via addColumnIfNotExists).
func TestObservationsClassifiedByV2IdempotentMigrate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	t.Parallel()

	s := newTestStore(t)

	// Verify the column is already present (migrate() should have added it).
	var colCount int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_xinfo('observations') WHERE name = 'classified_by_v2'
	`).Scan(&colCount)
	if err != nil {
		t.Fatalf("pragma check: %v", err)
	}
	if colCount == 0 {
		t.Fatal("classified_by_v2 column expected to exist before idempotency test")
	}

	// Opening the store again on the same DataDir (re-running migrate) must not fail.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = s.cfg.DataDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("re-New (idempotent migrate): %v", err)
	}
	defer s2.Close()
}
