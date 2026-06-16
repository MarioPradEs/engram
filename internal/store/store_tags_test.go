package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
)

// ─── WU-B1: Migration column exists + idempotency ────────────────────────────

// TestTagsJSON_Migration verifies that migrate() adds a tags_json TEXT column
// to the observations table, and that calling migrate() again on an already-
// migrated DB is idempotent (no duplicate column, no error).
func TestTagsJSON_Migration(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	// Column should exist after New() (which calls migrate()).
	rows, err := s.db.Query("PRAGMA table_info(observations)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	var found bool
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan pragma row: %v", err)
		}
		if name == "tags_json" {
			found = true
			if typ != "TEXT" {
				t.Errorf("tags_json column type = %q, want %q", typ, "TEXT")
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pragma rows: %v", err)
	}
	if !found {
		t.Fatal("tags_json column not found in observations table after migrate()")
	}

	// Idempotency: calling migrate() again must not error or add a duplicate.
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate() call: %v", err)
	}

	// Confirm still exactly one tags_json column.
	rows2, err := s.db.Query("PRAGMA table_info(observations)")
	if err != nil {
		t.Fatalf("PRAGMA table_info (second): %v", err)
	}
	defer rows2.Close()

	count := 0
	for rows2.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows2.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan pragma row (second): %v", err)
		}
		if name == "tags_json" {
			count++
		}
	}
	if err := rows2.Err(); err != nil {
		t.Fatalf("pragma rows (second): %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 tags_json column after second migrate(), got %d", count)
	}
}

// ─── WU-B3: Backfill correctness ─────────────────────────────────────────────

// TestTagsJSON_Backfill verifies the one-time backfill UPDATE that recovers
// tags from sync_mutations.payload into observations.tags_json.
//
// Pre-migration already-personal observations (no sync_mutations row) remain
// NULL — accepted limitation per REQ-57 / Scenario B-15.
func TestTagsJSON_Backfill(t *testing.T) {
	t.Parallel()

	// We need a raw DB to seed data before migrate() runs, reusing the pattern
	// from store_migration_test.go (newTestStoreWithLegacySchema).
	// Here we use a simpler approach: open the store (which runs migrate),
	// then directly write raw rows simulating a "pre-backfill" state by
	// inserting obs with tags_json=NULL and a matching sync_mutations row.
	//
	// Because the backfill is idempotent (WHERE tags_json IS NULL), we can
	// set tags_json back to NULL after insert and re-trigger via migrate().

	t.Run("synced obs recovered", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)

		// Insert a session required by FK.
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('ses-backfill-1', 'alpha', '/tmp/alpha')`,
		); err != nil {
			t.Fatalf("insert session: %v", err)
		}

		syncID := "obs-backfill-sync-001"
		wantTagsJSON := `{"juego":"game-x","tipo":"decision"}`

		// Insert observation with sync_id but tags_json=NULL.
		if _, err := s.db.Exec(
			`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, updated_at)
			 VALUES (?, 'ses-backfill-1', 'manual', 'Backfill Test', 'content', 'alpha', 'project', datetime('now'))`,
			syncID,
		); err != nil {
			t.Fatalf("insert obs: %v", err)
		}

		// Insert a matching sync_mutations row whose payload has $.tags.
		payload := fmt.Sprintf(`{"sync_id":%q,"type":"manual","title":"Backfill Test","content":"content","scope":"project","tags":{"juego":"game-x","tipo":"decision"}}`, syncID)
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, project)
			 VALUES ('cloud', 'observation', ?, 'upsert', ?, 'alpha')`,
			syncID, payload,
		); err != nil {
			t.Fatalf("insert sync_mutation: %v", err)
		}

		// Force tags_json back to NULL to simulate pre-backfill state.
		if _, err := s.db.Exec(
			`UPDATE observations SET tags_json = NULL WHERE sync_id = ?`, syncID,
		); err != nil {
			t.Fatalf("reset tags_json to NULL: %v", err)
		}

		// Re-run migrate() to trigger the backfill UPDATE.
		if err := s.migrate(); err != nil {
			t.Fatalf("migrate (backfill pass): %v", err)
		}

		// Assert tags_json is now populated.
		var tagsJSON sql.NullString
		if err := s.db.QueryRow(
			`SELECT tags_json FROM observations WHERE sync_id = ?`, syncID,
		).Scan(&tagsJSON); err != nil {
			t.Fatalf("select tags_json: %v", err)
		}
		if !tagsJSON.Valid {
			t.Fatal("tags_json is still NULL after backfill; expected recovery from sync_mutations")
		}

		// Verify the JSON contains the expected tags (may differ in key order).
		var got map[string]string
		if err := json.Unmarshal([]byte(tagsJSON.String), &got); err != nil {
			t.Fatalf("unmarshal tags_json %q: %v", tagsJSON.String, err)
		}
		want := map[string]string{"juego": "game-x", "tipo": "decision"}
		for k, wv := range want {
			if gv := got[k]; gv != wv {
				t.Errorf("tags_json[%q] = %q, want %q; full=%s", k, gv, wv, tagsJSON.String)
			}
		}
		_ = wantTagsJSON // reference to suppress unused-var warning
	})

	t.Run("already-personal stays NULL", func(t *testing.T) {
		// Pre-migration already-personal observations (no sync_mutations row) remain
		// NULL — accepted limitation per REQ-57 / Scenario B-15.
		t.Parallel()

		s := newTestStore(t)

		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('ses-backfill-2', 'alpha', '/tmp/alpha')`,
		); err != nil {
			t.Fatalf("insert session: %v", err)
		}

		// Insert obs with sync_id=NULL (never synced, pure personal) — no mutation row.
		if _, err := s.db.Exec(
			`INSERT INTO observations (session_id, type, title, content, project, scope, updated_at)
			 VALUES ('ses-backfill-2', 'manual', 'Personal Only', 'personal content', 'alpha', 'personal', datetime('now'))`,
		); err != nil {
			t.Fatalf("insert personal obs: %v", err)
		}

		// Run migrate() — backfill should NOT touch this row (no sync_id, no mutation).
		if err := s.migrate(); err != nil {
			t.Fatalf("migrate: %v", err)
		}

		var tagsJSON sql.NullString
		if err := s.db.QueryRow(
			`SELECT tags_json FROM observations WHERE title = 'Personal Only' AND project = 'alpha'`,
		).Scan(&tagsJSON); err != nil {
			t.Fatalf("select tags_json for personal obs: %v", err)
		}
		if tagsJSON.Valid {
			t.Errorf("expected tags_json=NULL for already-personal obs with no sync_mutations row, got %q", tagsJSON.String)
		}
	})

	t.Run("backfill is idempotent", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)

		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('ses-backfill-3', 'alpha', '/tmp/alpha')`,
		); err != nil {
			t.Fatalf("insert session: %v", err)
		}

		syncID := "obs-backfill-sync-idem"
		payload := fmt.Sprintf(`{"sync_id":%q,"type":"manual","title":"Idempotent","content":"c","scope":"project","tags":{"juego":"game-z"}}`, syncID)

		if _, err := s.db.Exec(
			`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, updated_at)
			 VALUES (?, 'ses-backfill-3', 'manual', 'Idempotent', 'c', 'alpha', 'project', datetime('now'))`,
			syncID,
		); err != nil {
			t.Fatalf("insert obs: %v", err)
		}
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, project)
			 VALUES ('cloud', 'observation', ?, 'upsert', ?, 'alpha')`,
			syncID, payload,
		); err != nil {
			t.Fatalf("insert sync_mutation: %v", err)
		}

		// Reset tags_json to NULL, then run migrate twice.
		if _, err := s.db.Exec(`UPDATE observations SET tags_json = NULL WHERE sync_id = ?`, syncID); err != nil {
			t.Fatalf("reset tags_json: %v", err)
		}

		// First backfill pass.
		if err := s.migrate(); err != nil {
			t.Fatalf("migrate pass 1: %v", err)
		}

		var firstValue sql.NullString
		if err := s.db.QueryRow(`SELECT tags_json FROM observations WHERE sync_id = ?`, syncID).Scan(&firstValue); err != nil {
			t.Fatalf("select after first pass: %v", err)
		}
		if !firstValue.Valid {
			t.Fatal("tags_json should be set after first backfill pass")
		}

		// Second backfill pass — must not clobber the already-filled row.
		if err := s.migrate(); err != nil {
			t.Fatalf("migrate pass 2: %v", err)
		}

		var secondValue sql.NullString
		if err := s.db.QueryRow(`SELECT tags_json FROM observations WHERE sync_id = ?`, syncID).Scan(&secondValue); err != nil {
			t.Fatalf("select after second pass: %v", err)
		}
		if secondValue.String != firstValue.String {
			t.Errorf("second migrate() clobbered tags_json: first=%q second=%q", firstValue.String, secondValue.String)
		}
	})
}

// ─── WU-B4: Write-path population in AddObservation ──────────────────────────

// TestTagsJSON_WritePath verifies that AddObservation populates the tags_json
// column in all three write branches (new insert, topic-key revision, dedupe).
func TestTagsJSON_WritePath(t *testing.T) {
	t.Parallel()

	t.Run("tagged insert populates tags_json flat", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-wp-1", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		id, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-wp-1",
			Type:      "manual",
			Title:     "Tagged Insert",
			Content:   "content wp-1",
			Project:   "alpha",
			Scope:     "project",
			Tags:      map[string]string{"juego": "game-x", "tipo": "decision"},
		})
		if err != nil {
			t.Fatalf("AddObservation: %v", err)
		}

		var tagsJSON sql.NullString
		if err := s.db.QueryRow(`SELECT tags_json FROM observations WHERE id = ?`, id).Scan(&tagsJSON); err != nil {
			t.Fatalf("select tags_json: %v", err)
		}
		if !tagsJSON.Valid {
			t.Fatal("tags_json is NULL after tagged insert; want flat JSON")
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(tagsJSON.String), &got); err != nil {
			t.Fatalf("unmarshal tags_json: %v", err)
		}
		want := map[string]string{"juego": "game-x", "tipo": "decision"}
		for k, wv := range want {
			if gv := got[k]; gv != wv {
				t.Errorf("tags_json[%q] = %q, want %q; full=%s", k, gv, wv, tagsJSON.String)
			}
		}
		// Verify it's flat (not nested under $.tags).
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(tagsJSON.String), &raw); err != nil {
			t.Fatalf("unmarshal raw: %v", err)
		}
		if _, nested := raw["tags"]; nested {
			t.Errorf("tags_json must be flat (no nested 'tags' key), got %s", tagsJSON.String)
		}
	})

	t.Run("untagged insert leaves tags_json NULL", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-wp-2", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		id, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-wp-2",
			Type:      "manual",
			Title:     "Untagged Insert",
			Content:   "content wp-2",
			Project:   "alpha",
			Scope:     "project",
			// No Tags field.
		})
		if err != nil {
			t.Fatalf("AddObservation: %v", err)
		}

		var tagsJSON sql.NullString
		if err := s.db.QueryRow(`SELECT tags_json FROM observations WHERE id = ?`, id).Scan(&tagsJSON); err != nil {
			t.Fatalf("select tags_json: %v", err)
		}
		if tagsJSON.Valid {
			t.Errorf("expected tags_json=NULL for untagged obs, got %q", tagsJSON.String)
		}
	})

	t.Run("personal-scope obs also gets tags_json", func(t *testing.T) {
		// Key invariant of decision #964: ALL scopes (including personal) get tags_json populated.
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-wp-3", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		id, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-wp-3",
			Type:      "manual",
			Title:     "Personal Tagged",
			Content:   "content wp-3",
			Project:   "alpha",
			Scope:     "personal",
			Tags:      map[string]string{"juego": "game-personal"},
		})
		if err != nil {
			t.Fatalf("AddObservation personal: %v", err)
		}

		var tagsJSON sql.NullString
		if err := s.db.QueryRow(`SELECT tags_json FROM observations WHERE id = ?`, id).Scan(&tagsJSON); err != nil {
			t.Fatalf("select tags_json: %v", err)
		}
		if !tagsJSON.Valid {
			t.Fatal("tags_json is NULL for personal-scope tagged obs; key invariant violated (decision #964)")
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(tagsJSON.String), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["juego"] != "game-personal" {
			t.Errorf(`tags_json["juego"] = %q, want "game-personal"`, got["juego"])
		}
	})

	t.Run("topic-key revision refreshes tags_json", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-wp-4", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// First insert: topic key k1, juego=game-x.
		id1, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-wp-4",
			Type:      "manual",
			Title:     "Topic Rev",
			Content:   "content v1",
			Project:   "alpha",
			Scope:     "project",
			TopicKey:  "k1-wp4",
			Tags:      map[string]string{"juego": "game-x"},
		})
		if err != nil {
			t.Fatalf("AddObservation v1: %v", err)
		}

		// Second insert: same topic key, juego=game-y (revision).
		id2, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-wp-4",
			Type:      "manual",
			Title:     "Topic Rev",
			Content:   "content v2",
			Project:   "alpha",
			Scope:     "project",
			TopicKey:  "k1-wp4",
			Tags:      map[string]string{"juego": "game-y"},
		})
		if err != nil {
			t.Fatalf("AddObservation v2: %v", err)
		}

		// Topic-key revision reuses the same row; ids should match.
		if id1 != id2 {
			t.Errorf("topic-key revision: got different IDs %d and %d; expected same row", id1, id2)
		}

		var tagsJSON sql.NullString
		if err := s.db.QueryRow(`SELECT tags_json FROM observations WHERE id = ?`, id1).Scan(&tagsJSON); err != nil {
			t.Fatalf("select tags_json: %v", err)
		}
		if !tagsJSON.Valid {
			t.Fatal("tags_json is NULL after topic-key revision")
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(tagsJSON.String), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["juego"] != "game-y" {
			t.Errorf(`after revision tags_json["juego"] = %q, want "game-y"`, got["juego"])
		}
	})

	t.Run("dedupe COALESCE preserves tags", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-wp-5", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// First insert: with tags.
		id1, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-wp-5",
			Type:      "manual",
			Title:     "Dedupe Tags",
			Content:   "dedupe content wp5",
			Project:   "alpha",
			Scope:     "project",
			Tags:      map[string]string{"juego": "game-dedupe"},
		})
		if err != nil {
			t.Fatalf("AddObservation first: %v", err)
		}

		// Second insert: same content (dedupe hit), NO tags — must not wipe existing tags_json.
		id2, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-wp-5",
			Type:      "manual",
			Title:     "Dedupe Tags",
			Content:   "dedupe content wp5",
			Project:   "alpha",
			Scope:     "project",
			// No Tags — simulates tagless duplicate.
		})
		if err != nil {
			t.Fatalf("AddObservation second (dedupe): %v", err)
		}

		// Should be same row.
		if id1 != id2 {
			t.Errorf("expected dedupe to reuse row %d, got %d", id1, id2)
		}

		var tagsJSON sql.NullString
		if err := s.db.QueryRow(`SELECT tags_json FROM observations WHERE id = ?`, id1).Scan(&tagsJSON); err != nil {
			t.Fatalf("select tags_json: %v", err)
		}
		if !tagsJSON.Valid {
			t.Fatal("tags_json became NULL after tagless dedupe hit; COALESCE should have preserved the original tags")
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(tagsJSON.String), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["juego"] != "game-dedupe" {
			t.Errorf(`after dedupe tags_json["juego"] = %q, want "game-dedupe"`, got["juego"])
		}
	})
}

// ─── WU-B5: ObservationsByTag + DistinctTagValues (RED tests) ─────────────────

// TestObservationsByTag verifies the ObservationsByTag store method.
func TestObservationsByTag(t *testing.T) {
	t.Parallel()

	t.Run("match returns correct observations", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-bt-1", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// Seed obs A (juego=game-x), B (juego=game-y), C (no tags).
		_, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-bt-1", Type: "manual", Title: "Obs A",
			Content: "content A", Project: "alpha", Scope: "project",
			Tags: map[string]string{"juego": "game-x"},
		})
		if err != nil {
			t.Fatalf("AddObservation A: %v", err)
		}
		_, err = s.AddObservation(AddObservationParams{
			SessionID: "sess-bt-1", Type: "manual", Title: "Obs B",
			Content: "content B", Project: "alpha", Scope: "project",
			Tags: map[string]string{"juego": "game-y"},
		})
		if err != nil {
			t.Fatalf("AddObservation B: %v", err)
		}
		_, err = s.AddObservation(AddObservationParams{
			SessionID: "sess-bt-1", Type: "manual", Title: "Obs C",
			Content: "content C", Project: "alpha", Scope: "project",
			// No tags.
		})
		if err != nil {
			t.Fatalf("AddObservation C: %v", err)
		}

		results, err := s.ObservationsByTag("alpha", "juego", "game-x", 100)
		if err != nil {
			t.Fatalf("ObservationsByTag: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("want 1 result, got %d: %+v", len(results), results)
		}
		if results[0].Title != "Obs A" {
			t.Errorf("result[0].Title = %q, want %q", results[0].Title, "Obs A")
		}
	})

	t.Run("NULL tags_json excluded", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-bt-null", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-bt-null", Type: "manual", Title: "No Tags",
			Content: "content", Project: "alpha", Scope: "project",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}

		results, err := s.ObservationsByTag("alpha", "juego", "anything", 100)
		if err != nil {
			t.Fatalf("ObservationsByTag: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for untagged obs, got %d", len(results))
		}
	})

	t.Run("personal-scope obs included when tagged", func(t *testing.T) {
		// Scenario B-08b: personal obs with tags must be queryable.
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-bt-personal", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-bt-personal", Type: "manual", Title: "Personal Tagged",
			Content: "content personal", Project: "alpha", Scope: "personal",
			Tags: map[string]string{"juego": "game-x"},
		}); err != nil {
			t.Fatalf("AddObservation personal: %v", err)
		}

		results, err := s.ObservationsByTag("alpha", "juego", "game-x", 100)
		if err != nil {
			t.Fatalf("ObservationsByTag: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected personal-scope tagged obs to be returned by ObservationsByTag")
		}
	})

	t.Run("special chars in value handled via bound param", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-bt-special", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		specialVal := `game'"x%{}`
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-bt-special", Type: "manual", Title: "Special Chars",
			Content: "content special", Project: "alpha", Scope: "project",
			Tags: map[string]string{"juego": specialVal},
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}

		// Must not error even with special chars in value (bound param prevents injection).
		results, err := s.ObservationsByTag("alpha", "juego", specialVal, 100)
		if err != nil {
			t.Fatalf("ObservationsByTag with special chars: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("expected 1 result for exact special-char match, got %d", len(results))
		}
	})

	t.Run("limit honored", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-bt-limit", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		for i := 0; i < 5; i++ {
			if _, err := s.AddObservation(AddObservationParams{
				SessionID: "sess-bt-limit", Type: "manual",
				Title:   fmt.Sprintf("Limit Obs %d", i),
				Content: fmt.Sprintf("content limit %d unique", i),
				Project: "alpha", Scope: "project",
				Tags: map[string]string{"juego": "game-limit"},
			}); err != nil {
				t.Fatalf("AddObservation %d: %v", i, err)
			}
		}

		results, err := s.ObservationsByTag("alpha", "juego", "game-limit", 3)
		if err != nil {
			t.Fatalf("ObservationsByTag: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("expected 3 results (limit=3), got %d", len(results))
		}
	})

	t.Run("invalid facet rejected by allow-list", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		_, err := s.ObservationsByTag("alpha", "departamento", "x", 10)
		if err == nil {
			t.Fatal("expected error for facet 'departamento' (not in allow-list {juego,tipo}), got nil")
		}
	})

	t.Run("cross-project isolation", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-bt-beta", "beta", "/tmp/beta"); err != nil {
			t.Fatalf("CreateSession beta: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-bt-beta", Type: "manual", Title: "Beta Obs",
			Content: "content beta", Project: "beta", Scope: "project",
			Tags: map[string]string{"juego": "game-x"},
		}); err != nil {
			t.Fatalf("AddObservation beta: %v", err)
		}

		// Query for alpha — must not return beta obs.
		results, err := s.ObservationsByTag("alpha", "juego", "game-x", 100)
		if err != nil {
			t.Fatalf("ObservationsByTag: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("cross-project: expected 0 results for alpha, got %d (beta obs leaked)", len(results))
		}
	})
}

// TestDistinctTagValues verifies the DistinctTagValues store method.
func TestDistinctTagValues(t *testing.T) {
	t.Parallel()

	t.Run("returns distinct sorted values", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-dv-1", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		for _, tags := range []map[string]string{
			{"juego": "game-x"},
			{"juego": "game-y"},
			{"juego": "game-x"}, // duplicate
		} {
			tags := tags
			if _, err := s.AddObservation(AddObservationParams{
				SessionID: "sess-dv-1", Type: "manual",
				Title:   fmt.Sprintf("DV Obs %v", tags),
				Content: fmt.Sprintf("content dv %v unique", tags),
				Project: "alpha", Scope: "project",
				Tags: tags,
			}); err != nil {
				t.Fatalf("AddObservation: %v", err)
			}
		}

		values, err := s.DistinctTagValues("alpha", "juego")
		if err != nil {
			t.Fatalf("DistinctTagValues: %v", err)
		}
		if len(values) != 2 {
			t.Fatalf("want 2 distinct values, got %d: %v", len(values), values)
		}
		// Should be sorted.
		if values[0] != "game-x" || values[1] != "game-y" {
			t.Errorf("want [game-x game-y] sorted, got %v", values)
		}
	})

	t.Run("empty project returns empty slice", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		values, err := s.DistinctTagValues("nonexistent-project", "juego")
		if err != nil {
			t.Fatalf("DistinctTagValues: %v", err)
		}
		if values == nil {
			t.Error("expected empty slice (not nil) for project with no tagged obs")
		}
		if len(values) != 0 {
			t.Errorf("expected 0 values, got %d: %v", len(values), values)
		}
	})

	t.Run("NULL tags_json excluded", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-dv-null", "alpha", "/tmp/alpha"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-dv-null", Type: "manual", Title: "No Tags DV",
			Content: "content dv null", Project: "alpha", Scope: "project",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}

		values, err := s.DistinctTagValues("alpha", "juego")
		if err != nil {
			t.Fatalf("DistinctTagValues: %v", err)
		}
		if len(values) != 0 {
			t.Errorf("expected 0 values (untagged obs excluded), got %d: %v", len(values), values)
		}
	})

	t.Run("empty string tags_json value excluded", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		// Insert a raw observation with tags_json containing an empty string value.
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('ses-dv-empty', 'alpha', '/tmp/alpha')`,
		); err != nil {
			t.Fatalf("insert session: %v", err)
		}
		if _, err := s.db.Exec(
			`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, tags_json, updated_at)
			 VALUES ('obs-dv-empty-001', 'ses-dv-empty', 'manual', 'Empty Val', 'content', 'alpha', 'project', '{"juego":""}', datetime('now'))`,
		); err != nil {
			t.Fatalf("insert obs with empty juego: %v", err)
		}

		values, err := s.DistinctTagValues("alpha", "juego")
		if err != nil {
			t.Fatalf("DistinctTagValues: %v", err)
		}
		for _, v := range values {
			if v == "" {
				t.Error("empty string value should be excluded from DistinctTagValues")
			}
		}
	})

	t.Run("invalid facet rejected", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		_, err := s.DistinctTagValues("alpha", "proyecto")
		if err == nil {
			t.Fatal("expected error for facet 'proyecto' (not in allow-list {juego,tipo}), got nil")
		}
	})

	t.Run("project isolation", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		if err := s.CreateSession("sess-dv-beta", "beta", "/tmp/beta"); err != nil {
			t.Fatalf("CreateSession beta: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "sess-dv-beta", Type: "manual", Title: "Beta DV",
			Content: "content beta dv", Project: "beta", Scope: "project",
			Tags: map[string]string{"juego": "game-beta"},
		}); err != nil {
			t.Fatalf("AddObservation beta: %v", err)
		}

		values, err := s.DistinctTagValues("alpha", "juego")
		if err != nil {
			t.Fatalf("DistinctTagValues alpha: %v", err)
		}
		for _, v := range values {
			if v == "game-beta" {
				t.Errorf("beta obs juego value leaked into alpha DistinctTagValues")
			}
		}
	})
}
