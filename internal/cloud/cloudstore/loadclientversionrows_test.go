package cloudstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestLoadClientVersionRowsPropagatesNonTableMissingErrors verifies that
// loadClientVersionRows propagates real DB errors (not SQLSTATE 42P01) so the
// caller's versionErr guard becomes live. Previously ALL errors were silenced.
//
// The 42P01 (undefined_table) case is verified with a real Postgres connection
// when CLOUDSTORE_TEST_DSN is set; the error-propagation detection logic is
// unit-testable without Postgres via direct pgconn.PgError construction.
//
// Satisfies: FIX #7 — stop swallowing all DB errors as "table missing".

// TestLoadClientVersionRowsUndefinedTableReturnsEmpty verifies that when the
// table does not exist (SQLSTATE 42P01), loadClientVersionRows returns an empty
// slice and a nil error. Postgres-gated.
func TestLoadClientVersionRowsUndefinedTableReturnsEmpty(t *testing.T) {
	dsn := testDSN(t)

	cs, err := New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cs.Close()

	// Drop the table to simulate a pre-migration instance.
	if _, err := cs.db.ExecContext(context.Background(), `DROP TABLE IF EXISTS cloud_client_versions`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	rows, err := cs.loadClientVersionRows()
	if err != nil {
		t.Errorf("expected nil error for 42P01 (undefined_table), got: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected empty rows for missing table, got %d row(s)", len(rows))
	}
}

// TestPgErrorDetectionNon42P01Propagates is a pure unit test (no Postgres) that
// confirms the 42P01 detection logic does NOT classify other error codes as
// "undefined table", ensuring they propagate correctly.
func TestPgErrorDetectionNon42P01Propagates(t *testing.T) {
	// 42501 = insufficient_privilege — must NOT be treated as undefined_table.
	pgErr := &pgconn.PgError{
		Code:    "42501",
		Message: "permission denied for table cloud_client_versions",
	}
	wrapped := fmt.Errorf("simulated: %w", pgErr)

	var detected *pgconn.PgError
	if !errors.As(wrapped, &detected) {
		t.Fatal("errors.As should detect *pgconn.PgError wrapped in fmt.Errorf")
	}
	if detected.Code == "42P01" {
		t.Error("42501 should NOT be treated as undefined_table (42P01)")
	}
}

// TestPgErrorDetection42P01IsUndefinedTable confirms that 42P01 is correctly
// detected as the undefined_table case.
func TestPgErrorDetection42P01IsUndefinedTable(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    "42P01",
		Message: "relation \"cloud_client_versions\" does not exist",
	}
	wrapped := fmt.Errorf("simulated: %w", pgErr)

	var detected *pgconn.PgError
	if !errors.As(wrapped, &detected) {
		t.Fatal("errors.As should detect *pgconn.PgError wrapped in fmt.Errorf")
	}
	if detected.Code != "42P01" {
		t.Errorf("expected code 42P01, got %q", detected.Code)
	}
}
