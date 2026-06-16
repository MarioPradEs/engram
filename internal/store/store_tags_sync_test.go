package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// applyObsMutationSeq applies an observation SyncMutation via ApplyPulledMutation
// with an explicit seq. seq must be > the store's current LastPulledSeq.
func applyObsMutationSeq(t *testing.T, s *Store, payload string, syncID string, seq int64) {
	t.Helper()
	if err := s.ensureSyncState(DefaultSyncTargetKey); err != nil {
		t.Fatalf("ensureSyncState: %v", err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{
		Seq:       seq,
		Entity:    SyncEntityObservation,
		EntityKey: syncID,
		Op:        SyncOpUpsert,
		Payload:   payload,
		Source:    SyncSourceRemote,
		TargetKey: DefaultSyncTargetKey,
	}); err != nil {
		t.Fatalf("ApplyPulledMutation (obs %s seq=%d): %v", syncID, seq, err)
	}
}

// readTagsJSONColumn reads the raw tags_json column value for an obs by sync_id.
func readTagsJSONColumn(t *testing.T, s *Store, syncID string) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.db.QueryRow(
		`SELECT tags_json FROM observations WHERE sync_id = ?`, syncID,
	).Scan(&v); err != nil {
		t.Fatalf("readTagsJSONColumn sync_id=%s: %v", syncID, err)
	}
	return v
}

// readTagsJSONColumnByID reads tags_json by numeric observation id.
func readTagsJSONColumnByID(t *testing.T, s *Store, id int64) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.db.QueryRow(
		`SELECT tags_json FROM observations WHERE id = ?`, id,
	).Scan(&v); err != nil {
		t.Fatalf("readTagsJSONColumnByID id=%d: %v", id, err)
	}
	return v
}

// ─── CRITICAL #1: sync-apply round-trip ───────────────────────────────────────

// TestSyncApply_TagsJSON_InsertBranch verifies that the sync-apply path writes
// tags_json on the INSERT branch (first time a sync_id is seen).
func TestSyncApply_TagsJSON_InsertBranch(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	if err := s.CreateSession("sess-sa-ins", "proj-sa", "/tmp/sa"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	syncID := "obs-sa-insert-001"
	payload := fmt.Sprintf(
		`{"sync_id":%q,"session_id":"sess-sa-ins","type":"manual","title":"Sync Insert Tagged","content":"content for sync insert","project":"proj-sa","scope":"project","tags":{"juego":"game-sync","tipo":"decision"}}`,
		syncID,
	)

	applyObsMutationSeq(t, s, payload, syncID, 1)

	// tags_json column must be populated.
	raw := readTagsJSONColumn(t, s, syncID)
	if !raw.Valid || raw.String == "" {
		t.Fatal("tags_json is NULL/empty after sync-apply INSERT; want populated JSON")
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(raw.String), &got); err != nil {
		t.Fatalf("unmarshal tags_json %q: %v", raw.String, err)
	}
	if got["juego"] != "game-sync" {
		t.Errorf("tags_json[juego] = %q, want %q", got["juego"], "game-sync")
	}
	if got["tipo"] != "decision" {
		t.Errorf("tags_json[tipo] = %q, want %q", got["tipo"], "decision")
	}

	// ObservationsByTag must find it after INSERT.
	results, err := s.ObservationsByTag("proj-sa", "juego", "game-sync", 100)
	if err != nil {
		t.Fatalf("ObservationsByTag: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ObservationsByTag: want 1 result after INSERT, got %d", len(results))
	}
	if results[0].Title != "Sync Insert Tagged" {
		t.Errorf("result[0].Title = %q, want %q", results[0].Title, "Sync Insert Tagged")
	}
	// Tags must be populated on read-back through queryObservations.
	if results[0].Tags["juego"] != "game-sync" {
		t.Errorf("result[0].Tags[juego] = %q, want %q", results[0].Tags["juego"], "game-sync")
	}
}

// TestSyncApply_TagsJSON_UpdateBranch verifies that the sync-apply path writes
// tags_json on the UPDATE branch (same sync_id applied twice, second with tags).
func TestSyncApply_TagsJSON_UpdateBranch(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	if err := s.CreateSession("sess-sa-upd", "proj-sa-upd", "/tmp/sa-upd"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	syncID := "obs-sa-update-001"

	// First apply: INSERT with no tags.
	payload1 := fmt.Sprintf(
		`{"sync_id":%q,"session_id":"sess-sa-upd","type":"manual","title":"Sync Update Test","content":"initial content","project":"proj-sa-upd","scope":"project"}`,
		syncID,
	)
	applyObsMutationSeq(t, s, payload1, syncID, 1)

	rawBefore := readTagsJSONColumn(t, s, syncID)
	if rawBefore.Valid {
		t.Fatalf("expected NULL tags_json after untagged INSERT, got %q", rawBefore.String)
	}

	// Second apply: UPDATE same sync_id now with tags.
	payload2 := fmt.Sprintf(
		`{"sync_id":%q,"session_id":"sess-sa-upd","type":"manual","title":"Sync Update Test","content":"updated content","project":"proj-sa-upd","scope":"project","tags":{"juego":"game-updated"}}`,
		syncID,
	)
	applyObsMutationSeq(t, s, payload2, syncID, 2)

	rawAfter := readTagsJSONColumn(t, s, syncID)
	if !rawAfter.Valid || rawAfter.String == "" {
		t.Fatal("tags_json is NULL/empty after sync-apply UPDATE with tags; want populated JSON")
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(rawAfter.String), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["juego"] != "game-updated" {
		t.Errorf("tags_json[juego] = %q, want %q", got["juego"], "game-updated")
	}

	// ObservationsByTag must find it after UPDATE.
	results, err := s.ObservationsByTag("proj-sa-upd", "juego", "game-updated", 100)
	if err != nil {
		t.Fatalf("ObservationsByTag: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ObservationsByTag: want 1 result after UPDATE, got %d", len(results))
	}
}

// ─── CRITICAL #2: read-back + UpdateObservation payload preservation ──────────

// TestReadBack_TagsPopulated verifies that after inserting a tagged observation,
// subsequent reads via RecentObservations/queryObservations return Tags populated.
func TestReadBack_TagsPopulated(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	if err := s.CreateSession("sess-rb-1", "proj-rb", "/tmp/rb"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	wantTags := map[string]string{"juego": "game-rb", "tipo": "pattern"}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-rb-1",
		Type:      "manual",
		Title:     "Read-back Tagged",
		Content:   "read-back content",
		Project:   "proj-rb",
		Scope:     "project",
		Tags:      wantTags,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Read back via RecentObservations (uses queryObservations).
	recents, err := s.RecentObservations("proj-rb", "project", 10)
	if err != nil {
		t.Fatalf("RecentObservations: %v", err)
	}
	if len(recents) == 0 {
		t.Fatal("RecentObservations returned no results")
	}
	var found bool
	for _, o := range recents {
		if o.ID == id {
			found = true
			if o.Tags["juego"] != "game-rb" {
				t.Errorf("RecentObservations: Tags[juego] = %q, want %q", o.Tags["juego"], "game-rb")
			}
			if o.Tags["tipo"] != "pattern" {
				t.Errorf("RecentObservations: Tags[tipo] = %q, want %q", o.Tags["tipo"], "pattern")
			}
		}
	}
	if !found {
		t.Errorf("observation id=%d not found in RecentObservations", id)
	}
}

// TestUpdateObservation_PayloadPreservesTags verifies that UpdateObservation
// reads the current tags_json back into obs.Tags so the enqueued
// sync_mutations payload carries the tags (not nil). This is the key regression
// fixed by Critical #2: before this fix, getObservationTx returned Tags=nil so
// every scope toggle enqueued a tagless mutation that wipes tags on the cloud.
func TestUpdateObservation_PayloadPreservesTags(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	if err := s.CreateSession("sess-upd-tags", "proj-upd-tags", "/tmp/upd"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	wantTags := map[string]string{"juego": "game-upd", "tipo": "decision"}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-upd-tags",
		Type:      "manual",
		Title:     "Update Preserve Tags",
		Content:   "original content",
		Project:   "proj-upd-tags",
		Scope:     "project",
		Tags:      wantTags,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// UpdateObservation — change content only, no tags in update params.
	newContent := "updated content for payload test"
	updated, err := s.UpdateObservation(id, UpdateObservationParams{
		Content: &newContent,
	})
	if err != nil {
		t.Fatalf("UpdateObservation: %v", err)
	}

	// The returned observation must have Tags populated (read from getObservationTx).
	if updated.Tags["juego"] != "game-upd" {
		t.Errorf("UpdateObservation return: Tags[juego] = %q, want %q", updated.Tags["juego"], "game-upd")
	}
	if updated.Tags["tipo"] != "decision" {
		t.Errorf("UpdateObservation return: Tags[tipo] = %q, want %q", updated.Tags["tipo"], "decision")
	}

	// Verify the enqueued sync_mutations payload carries the tags.
	// Without Critical #2, this would be absent because Tags was nil.
	row := s.db.QueryRow(
		`SELECT payload FROM sync_mutations
		 WHERE entity = 'observation' AND entity_key = ?
		 ORDER BY seq DESC LIMIT 1`,
		updated.SyncID,
	)
	var payloadJSON string
	if err := row.Scan(&payloadJSON); err != nil {
		t.Fatalf("scan sync_mutations payload: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(payloadJSON), &m); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	raw, ok := m["tags"]
	if !ok {
		t.Fatalf("'tags' key missing from sync_mutations payload after UpdateObservation; payload=%s", payloadJSON)
	}
	tagsMap, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("'tags' not an object in payload; payload=%s", payloadJSON)
	}
	if tagsMap["juego"] != "game-upd" {
		t.Errorf("payload.tags[juego] = %v, want %q", tagsMap["juego"], "game-upd")
	}
}

// ─── UpdateObservation no-clobber of tags_json column ─────────────────────────

// TestUpdateObservation_NoClobberTagsJSON verifies that UpdateObservation
// does NOT write to the tags_json column. The UPDATE statement in
// UpdateObservation must not include tags_json in its SET clause.
func TestUpdateObservation_NoClobberTagsJSON(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	if err := s.CreateSession("sess-nc", "proj-nc", "/tmp/nc"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	wantTags := map[string]string{"juego": "game-nc"}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-nc",
		Type:      "manual",
		Title:     "No-Clobber Test",
		Content:   "original content nc",
		Project:   "proj-nc",
		Scope:     "project",
		Tags:      wantTags,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Capture raw tags_json before update.
	rawBefore := readTagsJSONColumnByID(t, s, id)
	if !rawBefore.Valid || rawBefore.String == "" {
		t.Fatal("tags_json not populated before UpdateObservation")
	}

	// UpdateObservation: change title only.
	newTitle := "No-Clobber Updated Title"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{
		Title: &newTitle,
	}); err != nil {
		t.Fatalf("UpdateObservation: %v", err)
	}

	// tags_json column must be unchanged.
	rawAfter := readTagsJSONColumnByID(t, s, id)
	if !rawAfter.Valid || rawAfter.String == "" {
		t.Fatal("tags_json became NULL/empty after UpdateObservation; no-clobber invariant broken")
	}
	if rawAfter.String != rawBefore.String {
		t.Errorf("tags_json changed after UpdateObservation: before=%q after=%q", rawBefore.String, rawAfter.String)
	}
}

// ─── ObservationsByTag limit clamp ────────────────────────────────────────────

// TestObservationsByTag_LimitClamp verifies that limit=0 does not result in 0
// rows (it should clamp to cfg.MaxContextResults like RecentObservations does).
func TestObservationsByTag_LimitClamp(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	if err := s.CreateSession("sess-lc", "proj-lc", "/tmp/lc"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Insert one tagged obs.
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-lc",
		Type:      "manual",
		Title:     "Limit Clamp Obs",
		Content:   "content lc unique",
		Project:   "proj-lc",
		Scope:     "project",
		Tags:      map[string]string{"juego": "game-lc"},
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// limit=0 must clamp to MaxContextResults, not return 0 rows.
	results, err := s.ObservationsByTag("proj-lc", "juego", "game-lc", 0)
	if err != nil {
		t.Fatalf("ObservationsByTag limit=0: %v", err)
	}
	if len(results) == 0 {
		t.Error("ObservationsByTag with limit=0 returned 0 results; limit should clamp to MaxContextResults")
	}
}

// ─── Backfill shape consistency ───────────────────────────────────────────────

// TestBackfill_ObservationsByTag verifies that after the backfill migration,
// ObservationsByTag returns observations whose tags_json was populated from
// sync_mutations rather than from an active AddObservation call.
func TestBackfill_ObservationsByTag(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	// Insert required session.
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('ses-bf-shape', 'proj-bf', '/tmp/bf')`,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	syncID := "obs-bf-shape-001"

	// Insert obs with sync_id but no tags_json (simulates pre-backfill state).
	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, updated_at)
		 VALUES (?, 'ses-bf-shape', 'manual', 'Backfill Shape', 'backfill content', 'proj-bf', 'project', datetime('now'))`,
		syncID,
	); err != nil {
		t.Fatalf("insert obs: %v", err)
	}

	// Insert sync_mutations row with tags in payload.
	payload := fmt.Sprintf(
		`{"sync_id":%q,"type":"manual","title":"Backfill Shape","content":"backfill content","scope":"project","tags":{"juego":"game-bf"}}`,
		syncID,
	)
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, project)
		 VALUES ('cloud', 'observation', ?, 'upsert', ?, 'proj-bf')`,
		syncID, payload,
	); err != nil {
		t.Fatalf("insert sync_mutation: %v", err)
	}

	// Null out tags_json to simulate pre-backfill state.
	if _, err := s.db.Exec(`UPDATE observations SET tags_json = NULL WHERE sync_id = ?`, syncID); err != nil {
		t.Fatalf("nullify tags_json: %v", err)
	}

	// Trigger backfill by running migrate().
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate (backfill): %v", err)
	}

	// ObservationsByTag must now find the backfilled obs.
	results, err := s.ObservationsByTag("proj-bf", "juego", "game-bf", 100)
	if err != nil {
		t.Fatalf("ObservationsByTag after backfill: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("ObservationsByTag returned 0 results after backfill; expected the backfilled obs to be found")
	}
	// Tags must also be populated on read-back.
	if results[0].Tags["juego"] != "game-bf" {
		t.Errorf("result Tags[juego] = %q, want %q", results[0].Tags["juego"], "game-bf")
	}
}

// ─── Dedupe untagged→tagged via sync apply ─────────────────────────────────────

// TestSyncApply_DedupeUntaggedToTagged verifies the COALESCE(?, tags_json)
// behavior in the sync-apply UPDATE branch:
//   - First apply with no tags → NULL tags_json.
//   - Second apply with tags → tags_json populated (new non-null wins).
//   - Third apply with different tags → tags_json replaced.
func TestSyncApply_DedupeUntaggedToTagged(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	if err := s.CreateSession("sess-dt", "proj-dt", "/tmp/dt"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	syncID := "obs-dt-001"

	// First apply: INSERT with no tags.
	applyObsMutationSeq(t, s, fmt.Sprintf(
		`{"sync_id":%q,"session_id":"sess-dt","type":"manual","title":"Dedupe Tag Test","content":"content dt","project":"proj-dt","scope":"project"}`,
		syncID,
	), syncID, 1)

	rawBefore := readTagsJSONColumn(t, s, syncID)
	if rawBefore.Valid {
		t.Fatalf("expected NULL tags_json after untagged INSERT, got %q", rawBefore.String)
	}

	// Second apply: UPDATE same sync_id with tags — new non-null wins via COALESCE.
	applyObsMutationSeq(t, s, fmt.Sprintf(
		`{"sync_id":%q,"session_id":"sess-dt","type":"manual","title":"Dedupe Tag Test","content":"content dt v2","project":"proj-dt","scope":"project","tags":{"juego":"game-dt"}}`,
		syncID,
	), syncID, 2)

	rawAfter := readTagsJSONColumn(t, s, syncID)
	if !rawAfter.Valid || rawAfter.String == "" {
		t.Fatal("tags_json is NULL after untagged→tagged update; COALESCE new-non-null should win")
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(rawAfter.String), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["juego"] != "game-dt" {
		t.Errorf("tags_json[juego] = %q, want %q", got["juego"], "game-dt")
	}

	// Third apply: same sync_id, different tags (tagged→different-tagged replaces).
	applyObsMutationSeq(t, s, fmt.Sprintf(
		`{"sync_id":%q,"session_id":"sess-dt","type":"manual","title":"Dedupe Tag Test","content":"content dt v3","project":"proj-dt","scope":"project","tags":{"juego":"game-dt-v2"}}`,
		syncID,
	), syncID, 3)

	rawFinal := readTagsJSONColumn(t, s, syncID)
	if !rawFinal.Valid {
		t.Fatal("tags_json is NULL after tagged→different-tagged replace")
	}
	var gotFinal map[string]string
	if err := json.Unmarshal([]byte(rawFinal.String), &gotFinal); err != nil {
		t.Fatalf("unmarshal final: %v", err)
	}
	if gotFinal["juego"] != "game-dt-v2" {
		t.Errorf("after replace tags_json[juego] = %q, want %q", gotFinal["juego"], "game-dt-v2")
	}
}
