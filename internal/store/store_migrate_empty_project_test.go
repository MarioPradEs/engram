package store

import (
	"database/sql"
	"encoding/json"
	"testing"

	_ "modernc.org/sqlite"
)

// seedEmptyProjectData inserts sessions, observations, and pending
// sync_mutations with project='' into a freshly opened test store using
// raw SQL.  This simulates legacy data produced before the D1 fix.
//
// Returns the number of sync_mutation rows seeded.
func seedEmptyProjectData(t *testing.T, s *Store) int {
	t.Helper()

	// Create a session with project=''.
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('ses-orphan', '', '/tmp/nongit')`,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Create 5 observations with project=''.
	for i := 0; i < 5; i++ {
		syncID := "obs-orphan-" + string(rune('a'+i))
		payload := map[string]interface{}{
			"sync_id":    syncID,
			"session_id": "ses-orphan",
			"type":       "note",
			"title":      "orphan " + string(rune('a'+i)),
			"content":    "orphan content",
			"project":    "",
			"scope":      "project",
		}
		encoded, _ := json.Marshal(payload)
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO observations (sync_id, session_id, type, title, content, project, scope)
			 VALUES (?, 'ses-orphan', 'note', ?, 'orphan content', '', 'project')`,
			syncID, "orphan "+string(rune('a'+i)),
		); err != nil {
			t.Fatalf("seed observation %d: %v", i, err)
		}
		// Ensure sync_state row exists first.
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at)
			 VALUES (?, 'idle', datetime('now'))`, DefaultSyncTargetKey,
		); err != nil {
			t.Fatalf("seed sync_state: %v", err)
		}
		// Insert a pending sync_mutation row with project='' and payload.$.project=''.
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
			 VALUES (?, 'observation', ?, 'upsert', ?, 'local', '')`,
			DefaultSyncTargetKey, syncID, string(encoded),
		); err != nil {
			t.Fatalf("seed sync_mutation %d: %v", i, err)
		}
	}
	return 5
}

// TestMigrateEmptyProjectToPersonal verifies D3/D4 requirements:
//
//   (a) all observations/sessions with project='' gain project='personal'
//   (b) sync_mutations.project column is updated to 'personal'
//   (c) json_extract(payload,'$.project')='personal' (D4 dual-write)
//   (d) a second run is a no-op (idempotency — tested in separate function)
func TestMigrateEmptyProjectToPersonal(t *testing.T) {
	s := newTestStore(t)
	seeded := seedEmptyProjectData(t, s)
	if seeded == 0 {
		t.Fatal("no seed data — test is worthless")
	}

	result, err := s.MigrateEmptyProjectToPersonal("personal")
	if err != nil {
		t.Fatalf("MigrateEmptyProjectToPersonal: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil *OrphanMigrateResult")
	}

	// (a) Entity table: no observations with project='' should remain.
	var orphanCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE project = ''`).Scan(&orphanCount); err != nil {
		t.Fatalf("count orphan observations: %v", err)
	}
	if orphanCount != 0 {
		t.Errorf("expected 0 orphan observations after migration, got %d", orphanCount)
	}

	var personalObs int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE project = 'personal'`).Scan(&personalObs); err != nil {
		t.Fatalf("count personal observations: %v", err)
	}
	if personalObs != 5 {
		t.Errorf("expected 5 observations with project='personal', got %d", personalObs)
	}

	// (b) sync_mutations.project column updated.
	var smOrphan int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE project = '' AND acked_at IS NULL`,
	).Scan(&smOrphan); err != nil {
		t.Fatalf("count orphan sync_mutations: %v", err)
	}
	if smOrphan != 0 {
		t.Errorf("expected 0 orphan sync_mutations after migration, got %d", smOrphan)
	}

	var smPersonal int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE project = 'personal' AND acked_at IS NULL`,
	).Scan(&smPersonal); err != nil {
		t.Fatalf("count personal sync_mutations: %v", err)
	}
	if smPersonal == 0 {
		t.Error("expected sync_mutations.project = 'personal' after migration")
	}

	// (c) D4 dual-write: payload.$.project must also be 'personal'.
	rows, err := s.db.Query(
		`SELECT json_extract(payload, '$.project') FROM sync_mutations WHERE project = 'personal' AND acked_at IS NULL`,
	)
	if err != nil {
		t.Fatalf("query payload project: %v", err)
	}
	defer rows.Close()
	rowCount := 0
	for rows.Next() {
		rowCount++
		var payloadProject sql.NullString
		if err := rows.Scan(&payloadProject); err != nil {
			t.Fatalf("scan payload project: %v", err)
		}
		if !payloadProject.Valid || payloadProject.String != "personal" {
			t.Errorf("payload.$.project = %v; want 'personal'", payloadProject)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if rowCount == 0 {
		t.Error("expected at least one sync_mutation row to verify dual-write")
	}

	// Session should also be updated.
	var sesOrphan int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE project = ''`).Scan(&sesOrphan); err != nil {
		t.Fatalf("count orphan sessions: %v", err)
	}
	if sesOrphan != 0 {
		t.Errorf("expected 0 orphan sessions after migration, got %d", sesOrphan)
	}
}

// TestMigrateEmptyProjectToPersonal_Idempotent verifies task 2.3/2.4:
// running the migration twice must return AlreadyDone=true on the second run
// and must NOT modify any rows.
func TestMigrateEmptyProjectToPersonal_Idempotent(t *testing.T) {
	s := newTestStore(t)
	seedEmptyProjectData(t, s)

	// First run.
	r1, err := s.MigrateEmptyProjectToPersonal("personal")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if r1 == nil {
		t.Fatal("first run returned nil result")
	}
	if r1.AlreadyDone {
		t.Error("first run must not report AlreadyDone")
	}

	// Count rows after first run.
	var smPersonalAfterFirst int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE project = 'personal'`,
	).Scan(&smPersonalAfterFirst); err != nil {
		t.Fatalf("count after first run: %v", err)
	}

	// Second run — must be a no-op.
	r2, err := s.MigrateEmptyProjectToPersonal("personal")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if r2 == nil {
		t.Fatal("second run returned nil result")
	}
	if !r2.AlreadyDone {
		t.Error("second run must report AlreadyDone")
	}

	// Row count must be unchanged.
	var smPersonalAfterSecond int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE project = 'personal'`,
	).Scan(&smPersonalAfterSecond); err != nil {
		t.Fatalf("count after second run: %v", err)
	}
	if smPersonalAfterSecond != smPersonalAfterFirst {
		t.Errorf("second run modified rows: before=%d after=%d", smPersonalAfterFirst, smPersonalAfterSecond)
	}
}
