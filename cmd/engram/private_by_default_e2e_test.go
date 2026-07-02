package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	_ "modernc.org/sqlite"
)

// TestPrivateByDefault_OrphanMigrationSmokeTest is a cross-slice integration test
// (Piece 3 × Slice gate SQL × enrollment count) verifying the BUG #955 fix end-to-end:
//
//  1. Pre-migration: orphan (project='') mutations appear in ListPendingSyncMutations
//     via the SQL gate clause — this was the BUG #955 root cause.
//
//  2. Post-migration: MigrateEmptyProjectToPersonal moves orphan rows to
//     project='personal' AND acks them (FIX-2). No 'personal' mutations appear in
//     ListPendingSyncMutations (acked = not pending).
//
//  3. Post-migration: CountPendingNonEnrolledSyncMutations returns nothing for
//     'personal' because all migrated rows were acked — autosync stays clear.
//
// This is the autosync-safe invariant: after migration the local mutation journal
// is drained for the private bucket, unblocking Sergio (BUG #955).
func TestPrivateByDefault_OrphanMigrationSmokeTest(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	// Open the underlying SQLite file directly to seed raw orphan data.
	// This replicates the legacy state: observations saved before the D1 fix
	// had project='' because detectProject returned "" for non-git directories.
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	const targetKey = store.DefaultSyncTargetKey
	const orphanCount = 5

	// Ensure the sync_state row exists (required by sync_mutations FK).
	if _, err := db.Exec(`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at)
		VALUES (?, 'idle', datetime('now'))`, targetKey); err != nil {
		t.Fatalf("seed sync_state: %v", err)
	}

	// Seed orphan mutations with project=''.
	for i := 0; i < orphanCount; i++ {
		payload, _ := json.Marshal(map[string]any{
			"sync_id": fmt.Sprintf("orphan-%d", i),
			"project": "",
		})
		if _, err := db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
			 VALUES (?, 'observation', ?, 'upsert', ?, 'local', '')`,
			targetKey, fmt.Sprintf("orphan-%d", i), string(payload),
		); err != nil {
			t.Fatalf("seed orphan mutation %d: %v", i, err)
		}
	}

	// ── Step 1: Pre-migration state (BUG #955 reproduction) ─────────────────
	// The SQL gate `sm.project = ''` in ListPendingSyncMutations includes
	// orphan mutations in the sync batch — causing them to be pushed to the cloud.
	preMigMutations, err := s.ListPendingSyncMutations(targetKey, 100)
	if err != nil {
		t.Fatalf("pre-migration ListPendingSyncMutations: %v", err)
	}
	preMigOrphans := 0
	for _, m := range preMigMutations {
		if m.Project == "" {
			preMigOrphans++
		}
	}
	if preMigOrphans != orphanCount {
		t.Fatalf("pre-migration: expected %d orphan mutations (project='') in ListPendingSyncMutations, got %d", orphanCount, preMigOrphans)
	}

	// ── Step 2: Run migration ────────────────────────────────────────────────
	// D3/D4: moves project='' rows to project='personal' and acks them (FIX-2).
	result, err := s.MigrateEmptyProjectToPersonal("personal")
	if err != nil {
		t.Fatalf("MigrateEmptyProjectToPersonal: %v", err)
	}
	if result.AlreadyDone {
		t.Fatal("expected migration to run (not already done)")
	}
	if result.MutationsUpdated == 0 {
		t.Fatalf("expected MutationsUpdated > 0, got 0 (orphan rows not migrated)")
	}

	// ── Step 3: No orphan or 'personal' mutations in ListPendingSyncMutations ─
	// After migration: project='' rows become project='personal' and are acked.
	// The SQL gate `(sm.project = '' OR sep.project IS NOT NULL)` excludes them:
	//   - project='' is gone (now 'personal')
	//   - 'personal' is not enrolled (sep.project IS NULL)
	//   - the ack means acked_at IS NOT NULL → filtered out by WHERE acked_at IS NULL
	postMigMutations, err := s.ListPendingSyncMutations(targetKey, 100)
	if err != nil {
		t.Fatalf("post-migration ListPendingSyncMutations: %v", err)
	}
	for _, m := range postMigMutations {
		if m.Project == "" {
			t.Fatalf("post-migration: orphan mutation (project='') still in ListPendingSyncMutations: %+v", m)
		}
		if m.Project == "personal" {
			t.Fatalf("post-migration: 'personal' mutation must not appear in ListPendingSyncMutations (should be acked): %+v", m)
		}
	}

	// ── Step 4: CountPendingNonEnrolledSyncMutations is clear for 'personal' ──
	// FIX-2: migrated personal mutations are acked, so they do not appear in the
	// non-enrolled pending count. This prevents autosync from entering
	// blocked_unenrolled state after the migration runs at startup.
	counts, err := s.CountPendingNonEnrolledSyncMutations(targetKey)
	if err != nil {
		t.Fatalf("CountPendingNonEnrolledSyncMutations: %v", err)
	}
	for _, c := range counts {
		if c.Project == "personal" {
			t.Fatalf("post-migration: 'personal' must not appear in CountPendingNonEnrolledSyncMutations (should be acked), got count=%d", c.Count)
		}
	}
}
