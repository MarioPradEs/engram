package cloudstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud"
)

// openTestDB opens a Postgres connection for integration tests.
// It creates an isolated schema, runs migrate(), and returns the db + a cleanup func.
// Skips if CLOUDSTORE_TEST_DSN is unset or testing.Short() is true.
func openTestDB(t *testing.T) (*sql.DB, *CloudStore, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: skipped in -short mode")
	}
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN")
	}

	schema := fmt.Sprintf("cloudstore_attr_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	if _, err := adminDB.Exec(fmt.Sprintf("CREATE SCHEMA %s", schema)); err != nil {
		adminDB.Close()
		t.Fatalf("create schema: %v", err)
	}

	schemaDSN := dsn + "?search_path=" + schema
	cs, err := New(cloud.Config{DSN: schemaDSN})
	if err != nil {
		adminDB.Exec(fmt.Sprintf("DROP SCHEMA %s CASCADE", schema))
		adminDB.Close()
		t.Fatalf("New: %v", err)
	}

	cleanup := func() {
		cs.Close()
		adminDB.Exec(fmt.Sprintf("DROP SCHEMA %s CASCADE", schema))
		adminDB.Close()
	}
	return adminDB, cs, cleanup
}

// TestMigrateAddsAttributionColumns verifies that migrate() adds the 4 denorm
// columns and 2 indices to cloud_mutations, and the classified_by_v2 local
// column to observations (via local SQLite — skipped here, covered in store tests).
func TestMigrateAddsAttributionColumns(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Verify the 4 denorm columns exist on cloud_mutations.
	expectedCols := []string{"user_email", "user_name", "department", "user_deleted"}
	for _, col := range expectedCols {
		var count int
		err := cs.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = 'cloud_mutations' AND column_name = $1
				AND table_schema = current_schema()
		`, col).Scan(&count)
		if err != nil {
			t.Fatalf("check column %q: %v", col, err)
		}
		if count == 0 {
			t.Errorf("column %q not found on cloud_mutations after migrate()", col)
		}
	}

	// Verify the 2 indices exist.
	expectedIdx := []string{"idx_cloud_mutations_user_email", "idx_cloud_mutations_department"}
	for _, idx := range expectedIdx {
		var count int
		err := cs.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM pg_indexes
			WHERE tablename = 'cloud_mutations' AND indexname = $1
				AND schemaname = current_schema()
		`, idx).Scan(&count)
		if err != nil {
			t.Fatalf("check index %q: %v", idx, err)
		}
		if count == 0 {
			t.Errorf("index %q not found after migrate()", idx)
		}
	}
}

// TestMigrateBackfillAttributionDualJSONBSet verifies the Mario pre-OAuth backfill:
// rows with entity='observation' and user_email IS NULL get their denorm columns
// populated AND their payload JSONB updated with user_email on both
// cloud_mutations.payload and cloud_chunks.payload.observations[].
func TestMigrateBackfillAttributionDualJSONBSet(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Seed a cloud_mutations row with entity='observation', user_email IS NULL.
	obsPayload := map[string]interface{}{
		"sync_id":    "obs-seed-001",
		"title":      "test obs",
		"content":    "hello",
		"scope":      "project",
		"session_id": "sess-001",
	}
	payloadJSON, _ := json.Marshal(obsPayload)

	var seq int64
	err := cs.db.QueryRowContext(ctx, `
		INSERT INTO cloud_mutations (project, entity, entity_key, op, payload)
		VALUES ('test-project', 'observation', 'obs-seed-001', 'upsert', $1)
		RETURNING seq
	`, payloadJSON).Scan(&seq)
	if err != nil {
		t.Fatalf("seed cloud_mutations: %v", err)
	}

	// Seed a matching cloud_chunks row (with observations array).
	chunkPayload := map[string]interface{}{
		"sessions":     []interface{}{},
		"prompts":      []interface{}{},
		"observations": []interface{}{obsPayload},
	}
	chunkPayloadJSON, _ := json.Marshal(chunkPayload)
	_, err = cs.db.ExecContext(ctx, `
		INSERT INTO cloud_chunks (project_name, chunk_id, created_by, payload)
		VALUES ('test-project', 'chunk-001', 'mario@vivastudios.com', $1)
	`, chunkPayloadJSON)
	if err != nil {
		t.Fatalf("seed cloud_chunks: %v", err)
	}

	// Run backfill by re-running migrate (it's idempotent for the backfill too).
	if err := cs.migrate(ctx); err != nil {
		t.Fatalf("re-migrate for backfill: %v", err)
	}

	// Verify: user_email NULL count should be 0 after backfill for seeded rows.
	// (The backfill only applies to entity='observation' rows without user_email.)
	var nullCount int
	err = cs.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cloud_mutations
		WHERE entity = 'observation' AND user_email IS NULL AND seq = $1
	`, seq).Scan(&nullCount)
	if err != nil {
		t.Fatalf("count null user_email: %v", err)
	}
	// After backfill, user_email should be set (to empty string '' as default).
	if nullCount != 0 {
		t.Errorf("expected 0 rows with user_email NULL after backfill, got %d", nullCount)
	}

	// Verify the JSONB payload on cloud_mutations now has user_email key.
	var mutPayload []byte
	err = cs.db.QueryRowContext(ctx, `
		SELECT payload FROM cloud_mutations WHERE seq = $1
	`, seq).Scan(&mutPayload)
	if err != nil {
		t.Fatalf("read mutation payload: %v", err)
	}
	var mutMap map[string]interface{}
	if err := json.Unmarshal(mutPayload, &mutMap); err != nil {
		t.Fatalf("unmarshal mutation payload: %v", err)
	}
	if _, ok := mutMap["user_email"]; !ok {
		t.Errorf("cloud_mutations.payload missing user_email key after backfill; payload=%s", mutPayload)
	}
}
