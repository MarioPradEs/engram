package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// ─── Brain-graph data-gap fix — attribution round-trip tests ─────────────────
//
// These tests verify that user_email / user_name / department / user_deleted
// columns exist on the local SQLite observations table AND that they are
// correctly persisted and retrieved through the store, including via the
// sync-pull (applyObservationUpsertTx) path.
//
// Run: go test ./internal/store/... -run TestAttribution

// TestAttributionColumns_ExistAfterMigrate verifies that the four attribution
// columns are present in the observations table after migrate() runs.
func TestAttributionColumns_ExistAfterMigrate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	t.Parallel()

	s := newTestStore(t)

	wantCols := []string{"user_email", "user_name", "department", "user_deleted"}
	for _, col := range wantCols {
		var count int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_xinfo('observations') WHERE name = ?`, col,
		).Scan(&count); err != nil {
			t.Fatalf("pragma_table_xinfo for %q: %v", col, err)
		}
		if count == 0 {
			t.Errorf("observations.%s column not found after migrate()", col)
		}
	}
}

// TestAttributionColumns_DefaultEmptyOnLegacyRow verifies that a row created
// before attribution columns existed reads back with empty string / false for
// the attribution fields — no scan error.
func TestAttributionColumns_DefaultEmptyOnLegacyRow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	t.Parallel()

	s := newTestStore(t)

	if err := s.CreateSession("ses-attr-legacy", "attr-proj", t.TempDir()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Insert directly via SQL without providing attribution columns (legacy path).
	if _, err := s.db.Exec(`
		INSERT INTO observations (sync_id, session_id, type, title, content, scope, revision_count, duplicate_count, updated_at)
		VALUES ('obs-legacy-attr', 'ses-attr-legacy', 'manual', 'Legacy row', 'no attribution', 'project', 1, 1, datetime('now'))
	`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	obs, err := s.GetObservationBySyncID("obs-legacy-attr")
	if err != nil {
		t.Fatalf("GetObservationBySyncID: %v", err)
	}

	if obs.UserEmail != "" {
		t.Errorf("UserEmail: want empty, got %q", obs.UserEmail)
	}
	if obs.UserName != "" {
		t.Errorf("UserName: want empty, got %q", obs.UserName)
	}
	if obs.Department != "" {
		t.Errorf("Department: want empty, got %q", obs.Department)
	}
	if obs.UserDeleted {
		t.Errorf("UserDeleted: want false, got true")
	}
}

// TestAttributionRoundTrip_SyncPull verifies that when a sync-pull mutation
// carries UserEmail / UserName / Department / UserDeleted, the upsert persists
// them and GetObservationBySyncID returns them populated.
func TestAttributionRoundTrip_SyncPull(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	t.Parallel()

	s := newTestStore(t)

	if err := s.CreateSession("ses-attr-pull", "attr-proj", t.TempDir()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	payload := syncObservationPayload{
		SyncID:         "obs-attr-pull-001",
		SessionID:      "ses-attr-pull",
		Type:           "decision",
		Title:          "Attribution test",
		Content:        "This observation carries attribution.",
		Scope:          "project",
		RevisionCount:  1,
		DuplicateCount: 1,
		CreatedAt:      "2026-06-18 12:00:00",
		UpdatedAt:      "2026-06-18 12:00:00",
		UserEmail:      "alice@vivastudios.com",
		UserName:       "Alice",
		Department:     "dev",
		UserDeleted:    false,
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	mutation := SyncMutation{
		Entity:    SyncEntityObservation,
		EntityKey: payload.SyncID,
		Op:        SyncOpUpsert,
		Payload:   string(rawPayload),
		Source:    SyncSourceRemote,
	}

	// Apply as a pulled mutation (mimics sync pull path).
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.applyPulledMutationTx(tx, mutation)
	}); err != nil {
		t.Fatalf("applyPulledMutationTx: %v", err)
	}

	obs, err := s.GetObservationBySyncID("obs-attr-pull-001")
	if err != nil {
		t.Fatalf("GetObservationBySyncID: %v", err)
	}

	if obs.UserEmail != "alice@vivastudios.com" {
		t.Errorf("UserEmail: want %q, got %q", "alice@vivastudios.com", obs.UserEmail)
	}
	if obs.UserName != "Alice" {
		t.Errorf("UserName: want %q, got %q", "Alice", obs.UserName)
	}
	if obs.Department != "dev" {
		t.Errorf("Department: want %q, got %q", "dev", obs.Department)
	}
	if obs.UserDeleted {
		t.Errorf("UserDeleted: want false, got true")
	}
}

// TestAttributionRoundTrip_UpdatePreservesAttribution verifies that a second
// sync-pull upsert (UPDATE path) for an existing observation preserves/updates
// attribution fields correctly.
func TestAttributionRoundTrip_UpdatePreservesAttribution(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	t.Parallel()

	s := newTestStore(t)

	if err := s.CreateSession("ses-attr-upd", "attr-proj", t.TempDir()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// INSERT via upsert first.
	first := syncObservationPayload{
		SyncID:         "obs-attr-upd-001",
		SessionID:      "ses-attr-upd",
		Type:           "manual",
		Title:          "Update attribution test",
		Content:        "Initial content.",
		Scope:          "project",
		RevisionCount:  1,
		DuplicateCount: 1,
		CreatedAt:      "2026-06-18 10:00:00",
		UpdatedAt:      "2026-06-18 10:00:00",
		UserEmail:      "bob@vivastudios.com",
		UserName:       "Bob",
		Department:     "art",
	}
	applyObsPayload(t, s, first)

	// UPDATE via second upsert — new content, updated attribution (UserDeleted now true).
	second := first
	second.Content = "Updated content."
	second.RevisionCount = 2
	second.UpdatedAt = "2026-06-18 11:00:00"
	second.UserDeleted = true
	applyObsPayload(t, s, second)

	obs, err := s.GetObservationBySyncID("obs-attr-upd-001")
	if err != nil {
		t.Fatalf("GetObservationBySyncID (after update): %v", err)
	}

	if obs.Content != "Updated content." {
		t.Errorf("Content: want %q, got %q", "Updated content.", obs.Content)
	}
	if obs.UserEmail != "bob@vivastudios.com" {
		t.Errorf("UserEmail: want %q, got %q", "bob@vivastudios.com", obs.UserEmail)
	}
	if obs.Department != "art" {
		t.Errorf("Department: want %q, got %q", "art", obs.Department)
	}
	if !obs.UserDeleted {
		t.Error("UserDeleted: want true, got false")
	}
}

// TestAttributionColumns_IdempotentMigrate verifies that running New() on a
// store that already has attribution columns is a no-op (no error).
func TestAttributionColumns_IdempotentMigrate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	t.Parallel()

	s := newTestStore(t)

	// Re-open the same DataDir — migrate() must not error.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = s.cfg.DataDir
	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("re-New (idempotent attribution migration): %v", err)
	}
	defer s2.Close()
}

// TestAttributionColumns_PreExistingSchemaUpgrade verifies that the attribution
// columns are added by migrate() when starting from a pre-attribution schema
// (i.e., the observations table does NOT yet have user_email/department).
func TestAttributionColumns_PreExistingSchemaUpgrade(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")

	// Build a legacy DB that does NOT have user_email / department columns.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec("PRAGMA journal_mode = WAL"); err != nil {
		raw.Close()
		t.Fatalf("WAL pragma: %v", err)
	}
	// Use the post-memory-conflict-surfacing schema as the "old" schema
	// (no user_email / department in that DDL).
	if _, err := raw.Exec(legacyDDLPostMemoryConflictSurfacing); err != nil {
		raw.Close()
		t.Fatalf("apply legacy DDL: %v", err)
	}

	// Confirm the columns are absent BEFORE migrate.
	for _, col := range []string{"user_email", "department"} {
		var count int
		if err := raw.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_xinfo('observations') WHERE name = ?`, col,
		).Scan(&count); err != nil {
			raw.Close()
			t.Fatalf("pre-migrate pragma for %q: %v", col, err)
		}
		if count != 0 {
			raw.Close()
			t.Fatalf("column %q unexpectedly present in legacy schema (test setup error)", col)
		}
	}

	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	// Now open via New() — this runs migrate() and MUST add the columns.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New(cfg) on legacy db: %v", err)
	}
	defer s.Close()

	for _, col := range []string{"user_email", "user_name", "department", "user_deleted"} {
		var count int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_xinfo('observations') WHERE name = ?`, col,
		).Scan(&count); err != nil {
			t.Fatalf("post-migrate pragma for %q: %v", col, err)
		}
		if count == 0 {
			t.Errorf("observations.%s column missing after migrate() on legacy schema", col)
		}
	}
}

// ─── helper ──────────────────────────────────────────────────────────────────

// applyObsPayload marshals a syncObservationPayload and applies it as a
// pulled upsert mutation inside a transaction.
func applyObsPayload(t *testing.T, s *Store, payload syncObservationPayload) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("applyObsPayload: marshal: %v", err)
	}
	mutation := SyncMutation{
		Entity:    SyncEntityObservation,
		EntityKey: payload.SyncID,
		Op:        SyncOpUpsert,
		Payload:   string(raw),
		Source:    SyncSourceRemote,
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		return s.applyPulledMutationTx(tx, mutation)
	}); err != nil {
		t.Fatalf("applyObsPayload: applyPulledMutationTx: %v", err)
	}
}
