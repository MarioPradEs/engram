package cloudstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

// TestMigrateCreatesClientVersionsTable asserts that after migrate() the table
// cloud_client_versions exists with the expected columns.
// Satisfies: REQ-CVR-09 (T-01 RED → T-02 GREEN)
func TestMigrateCreatesClientVersionsTable(t *testing.T) {
	dsn := testDSN(t)

	cs, err := New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New (migrate): %v", err)
	}
	defer cs.Close()

	// Verify table exists and has the expected columns.
	rows, err := cs.db.QueryContext(context.Background(), `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_name = 'cloud_client_versions'
		ORDER BY column_name`)
	if err != nil {
		t.Fatalf("query information_schema: %v", err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[col] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	for _, want := range []string{"contributor", "last_client_version", "last_seen_at"} {
		if !cols[want] {
			t.Errorf("cloud_client_versions missing expected column %q; found: %v", want, cols)
		}
	}
}

// TestRecordClientVersionUpsert verifies insert + upsert semantics.
// Satisfies: REQ-CVR-05, REQ-CVR-06, REQ-CVR-09 (T-03 RED → T-04 GREEN)
func TestRecordClientVersionUpsert(t *testing.T) {
	dsn := testDSN(t)

	cs, err := New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cs.Close()

	ctx := context.Background()

	// Insert first version.
	if err := cs.RecordClientVersion(ctx, "alice@example.com", "1.17.0-viva.8"); err != nil {
		t.Fatalf("RecordClientVersion (insert): %v", err)
	}

	// Upsert: second call with different version should overwrite.
	// Capture last_seen_at before and after to confirm it changes.
	var seenBefore time.Time
	if err := cs.db.QueryRowContext(ctx, `SELECT last_seen_at FROM cloud_client_versions WHERE contributor = $1`, "alice@example.com").
		Scan(&seenBefore); err != nil {
		t.Fatalf("scan before: %v", err)
	}

	// Sleep a short duration so last_seen_at changes.
	time.Sleep(5 * time.Millisecond)
	if err := cs.RecordClientVersion(ctx, "alice@example.com", "1.17.0-viva.9"); err != nil {
		t.Fatalf("RecordClientVersion (upsert): %v", err)
	}

	var gotVersion string
	var seenAfter time.Time
	if err := cs.db.QueryRowContext(ctx, `SELECT last_client_version, last_seen_at FROM cloud_client_versions WHERE contributor = $1`, "alice@example.com").
		Scan(&gotVersion, &seenAfter); err != nil {
		t.Fatalf("scan after: %v", err)
	}
	if gotVersion != "1.17.0-viva.9" {
		t.Errorf("expected version 1.17.0-viva.9, got %q", gotVersion)
	}
	if !seenAfter.After(seenBefore) {
		t.Errorf("expected last_seen_at to advance after upsert; before=%v after=%v", seenBefore, seenAfter)
	}
}

// TestRecordClientVersionEmptyContributorNoOps verifies that an empty contributor is a no-op.
// Satisfies: REQ-CVR-09
func TestRecordClientVersionEmptyContributorNoOps(t *testing.T) {
	dsn := testDSN(t)

	cs, err := New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cs.Close()

	ctx := context.Background()
	if err := cs.RecordClientVersion(ctx, "", "1.17.0-viva.9"); err != nil {
		t.Fatalf("RecordClientVersion(empty contributor) should not error, got: %v", err)
	}

	// Table should remain empty.
	var count int
	if err := cs.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_client_versions`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows after empty-contributor no-op, got %d", count)
	}
}

// TestRecordClientVersionEmptyVersionNoOps verifies that an empty version string is a no-op.
// Satisfies: REQ-CVR-09
func TestRecordClientVersionEmptyVersionNoOps(t *testing.T) {
	dsn := testDSN(t)

	cs, err := New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cs.Close()

	ctx := context.Background()
	if err := cs.RecordClientVersion(ctx, "bob@example.com", ""); err != nil {
		t.Fatalf("RecordClientVersion(empty version) should not error, got: %v", err)
	}

	var count int
	if err := cs.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_client_versions`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows after empty-version no-op, got %d", count)
	}
}

// TestBuildDashboardReadModelStampsLastClientVersion asserts that buildDashboardReadModelFromRows
// stamps LastClientVersion on contributor rows when a version map has a matching entry,
// and leaves LastClientVersion == "" when there is no match.
// Satisfies: REQ-CVR-07, REQ-CVR-08 (T-05 RED → T-06 GREEN)
func TestBuildDashboardReadModelStampsLastClientVersion(t *testing.T) {
	chunks := []dashboardChunkRow{
		{
			chunkID: "chunk-1", project: "proj-a", createdBy: "alice@example.com",
			createdAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			parsed:    parseMustBenchmarkChunk(t),
		},
		{
			chunkID: "chunk-2", project: "proj-a", createdBy: "bob@example.com",
			createdAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			parsed:    parseMustBenchmarkChunk(t),
		},
	}

	versionRows := []clientVersionRow{
		{contributor: "alice@example.com", lastClientVersion: "1.17.0-viva.9"},
		// bob has no version row
	}

	model, err := buildDashboardReadModelFromRowsWithVersions(chunks, nil, versionRows)
	if err != nil {
		t.Fatalf("buildDashboardReadModelFromRowsWithVersions: %v", err)
	}

	contributors := model.contributors
	found := map[string]DashboardContributorRow{}
	for _, c := range contributors {
		found[c.CreatedBy] = c
	}

	alice, ok := found["alice@example.com"]
	if !ok {
		t.Fatal("alice@example.com not in contributors")
	}
	if alice.LastClientVersion != "1.17.0-viva.9" {
		t.Errorf("alice LastClientVersion: want %q, got %q", "1.17.0-viva.9", alice.LastClientVersion)
	}

	bob, ok := found["bob@example.com"]
	if !ok {
		t.Fatal("bob@example.com not in contributors")
	}
	if bob.LastClientVersion != "" {
		t.Errorf("bob LastClientVersion: want %q (no row), got %q", "", bob.LastClientVersion)
	}
}

// parseMustBenchmarkChunk builds a minimal parsed chunk for testing.
func parseMustBenchmarkChunk(t *testing.T) engramsync.ChunkData {
	t.Helper()
	return parseMustChunk(t, []byte(`{
		"sessions":[{"id":"sess-1","project":"proj-a","started_at":"2026-01-01T00:00:00Z"}],
		"observations":[],
		"prompts":[]
	}`))
}

// testDSN skips the test when CLOUDSTORE_TEST_DSN is not set, and isolates each
// test in its own Postgres schema so they do not interfere.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := testDSNRaw(t)

	schema := fmt.Sprintf("cvtest_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	defer adminDB.Close()

	if _, err := adminDB.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	if strings.Contains(dsn, "?") {
		return dsn + "&search_path=" + schema
	}
	return dsn + "?search_path=" + schema
}

func testDSNRaw(t *testing.T) string {
	t.Helper()
	dsn := testEnv(t, "CLOUDSTORE_TEST_DSN")
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("CLOUDSTORE_TEST_DSN must be a URL-style DSN for per-test schema isolation")
	}
	return dsn
}

func testEnv(t *testing.T, key string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		t.Skipf("%s not set — skipping integration test (requires Postgres)", key)
	}
	return v
}
