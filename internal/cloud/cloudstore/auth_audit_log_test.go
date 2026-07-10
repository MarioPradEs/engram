package cloudstore

import (
	"context"
	"testing"
	"time"
)

// ─── PR1 Auth Audit Log Tests ─────────────────────────────────────────────────
// All tests use openTestCloudStore (Postgres-gated via CLOUDSTORE_TEST_DSN).
// In RED phase these fail with "no such table" or compilation errors before
// auth_audit_log.go is created.

// TestInsertAuthAuditEntry_roundtrip inserts one row and retrieves it by its
// unique email, verifying every field round-trips correctly. Uses a unique email
// prefix to avoid asserting over global table state from parallel tests.
func TestInsertAuthAuditEntry_roundtrip(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	email := "roundtrip-" + time.Now().Format("150405.000") + "@example.com"

	err := cs.InsertAuthAuditEntry(ctx, email, OutcomeAllowed, SourceOAuth, "")
	if err != nil {
		t.Fatalf("InsertAuthAuditEntry: %v", err)
	}

	// Locate our row directly by its unique email rather than relying on global ordering.
	var (
		id         int64
		occurredAt string
		outcome    string
		source     string
		reasonCode *string
	)
	if err := cs.db.QueryRowContext(ctx,
		`SELECT id, occurred_at, outcome, source, reason_code
		 FROM cloud_auth_audit_log WHERE email = $1 ORDER BY id DESC LIMIT 1`, email,
	).Scan(&id, &occurredAt, &outcome, &source, &reasonCode); err != nil {
		t.Fatalf("locate row by email: %v", err)
	}

	if id == 0 {
		t.Error("expected non-zero ID")
	}
	if occurredAt == "" {
		t.Error("expected non-empty occurred_at")
	}
	if outcome != OutcomeAllowed {
		t.Errorf("outcome: got %q, want %q", outcome, OutcomeAllowed)
	}
	if source != SourceOAuth {
		t.Errorf("source: got %q, want %q", source, SourceOAuth)
	}
	if reasonCode != nil {
		t.Errorf("reason_code: got %q, want NULL for allowed rows", *reasonCode)
	}
}

// TestListAuthAuditEntriesPaginated verifies offset/limit pagination:
// seeds 15 rows with a unique email prefix and asserts scoped counts and order
// using only this test's own rows (immune to other tests' insertions).
func TestListAuthAuditEntriesPaginated(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	// Unique email so we can scope assertions to only this test's rows.
	email := "paginate-" + time.Now().Format("150405.000") + "@test.com"

	for i := 0; i < 15; i++ {
		rc := ""
		if i%2 == 0 {
			rc = ReasonUnknownEmail
		}
		outcome := OutcomeAllowed
		if i%2 == 0 {
			outcome = OutcomeDenied
		}
		if err := cs.InsertAuthAuditEntry(ctx, email, outcome, SourceJWT, rc); err != nil {
			t.Fatalf("InsertAuthAuditEntry %d: %v", i, err)
		}
	}

	// Count only this test's rows directly — immune to parallel test insertions.
	var ownTotal int
	if err := cs.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cloud_auth_audit_log WHERE email = $1`, email,
	).Scan(&ownTotal); err != nil {
		t.Fatalf("count own rows: %v", err)
	}
	if ownTotal != 15 {
		t.Fatalf("expected 15 own rows, got %d", ownTotal)
	}

	// Fetch this test's rows ordered DESC for pagination/order assertions.
	dbRows, err := cs.db.QueryContext(ctx, `
		SELECT id, occurred_at, email, outcome, source, COALESCE(reason_code, '')
		FROM cloud_auth_audit_log
		WHERE email = $1
		ORDER BY occurred_at DESC, id DESC`, email)
	if err != nil {
		t.Fatalf("fetch own rows: %v", err)
	}
	defer dbRows.Close()

	var allOwn []AuthAuditEntry
	for dbRows.Next() {
		var row AuthAuditEntry
		var occurredAt time.Time
		if err := dbRows.Scan(&row.ID, &occurredAt, &row.Email, &row.Outcome, &row.Source, &row.ReasonCode); err != nil {
			t.Fatalf("scan: %v", err)
		}
		row.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		allOwn = append(allOwn, row)
	}
	if err := dbRows.Err(); err != nil {
		t.Fatalf("iterate own rows: %v", err)
	}
	if len(allOwn) != 15 {
		t.Fatalf("expected 15 scanned own rows, got %d", len(allOwn))
	}

	// Verify DESC order across all 15 own rows.
	for i := 1; i < len(allOwn); i++ {
		tPrev, errP := time.Parse(time.RFC3339, allOwn[i-1].OccurredAt)
		tCurr, errC := time.Parse(time.RFC3339, allOwn[i].OccurredAt)
		if errP != nil || errC != nil {
			continue
		}
		if tPrev.Before(tCurr) {
			t.Errorf("rows not sorted DESC: row[%d] %s before row[%d] %s",
				i-1, allOwn[i-1].OccurredAt, i, allOwn[i].OccurredAt)
		}
	}

	// Verify the store's paginated method returns the global total >= 15 and
	// correct page sizes (page 1 = 10 rows, page 2 has at least 1 row).
	rows1, globalTotal, err := cs.ListAuthAuditEntriesPaginated(ctx, 1, 10)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if globalTotal < 15 {
		t.Errorf("expected global total >= 15, got %d", globalTotal)
	}
	if len(rows1) != 10 {
		t.Errorf("page 1: expected 10 rows, got %d", len(rows1))
	}

	rows2, _, err := cs.ListAuthAuditEntriesPaginated(ctx, 2, 10)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(rows2) == 0 {
		t.Error("page 2: expected rows, got 0")
	}
}

// TestPruneAuthAuditBefore verifies that only rows older than the cutoff are
// deleted, and rows within the window are preserved.
func TestPruneAuthAuditBefore(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	// Insert a row "now" (should survive prune with cutoff = 1 hour ago).
	if err := cs.InsertAuthAuditEntry(ctx, "recent@test.com", OutcomeDenied, SourceJWT, ReasonInvalidJWT); err != nil {
		t.Fatalf("insert recent: %v", err)
	}

	// Insert an old row directly to bypass DEFAULT now().
	_, err := cs.db.ExecContext(ctx, `
		INSERT INTO cloud_auth_audit_log (occurred_at, email, outcome, source, reason_code)
		VALUES ($1, $2, $3, $4, $5)`,
		time.Now().UTC().Add(-100*24*time.Hour), // 100 days ago
		"old@test.com",
		OutcomeDenied,
		SourceLegacy,
		ReasonRemovedUser,
	)
	if err != nil {
		t.Fatalf("insert old row: %v", err)
	}

	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)
	deleted, err := cs.PruneAuthAuditBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneAuthAuditBefore: %v", err)
	}
	if deleted < 1 {
		t.Errorf("expected at least 1 deleted row, got %d", deleted)
	}

	// Verify "old@test.com" row is gone.
	var count int
	if err := cs.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cloud_auth_audit_log WHERE email = 'old@test.com'`,
	).Scan(&count); err != nil {
		t.Fatalf("count old: %v", err)
	}
	if count != 0 {
		t.Errorf("expected old row deleted, got count=%d", count)
	}

	// Verify recent row survived.
	if err := cs.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cloud_auth_audit_log WHERE email = 'recent@test.com'`,
	).Scan(&count); err != nil {
		t.Fatalf("count recent: %v", err)
	}
	if count == 0 {
		t.Error("expected recent row to survive prune, but it was deleted")
	}
}

// TestCheckConstraints verifies that the CHECK constraints on outcome and source
// reject invalid values at the database level.
func TestCheckConstraints(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		outcome string
		source  string
	}{
		{"bad outcome", "unknown", SourceOAuth},
		{"bad source", OutcomeDenied, "password"},
		{"both bad", "win", "lose"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cs.db.ExecContext(ctx, `
				INSERT INTO cloud_auth_audit_log (email, outcome, source)
				VALUES ($1, $2, $3)`,
				"check@test.com", tc.outcome, tc.source,
			)
			if err == nil {
				t.Errorf("expected CHECK constraint violation for outcome=%q source=%q, got nil error",
					tc.outcome, tc.source)
			}
		})
	}
}

// TestRecordAuthEvent_fireAndForget verifies the fire-and-forget wrapper does
// not panic or return an error, and that a row eventually appears in the table.
func TestRecordAuthEvent_fireAndForget(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	email := "recorder-" + time.Now().Format("150405.000") + "@test.com"

	// Should not panic, should not return error.
	cs.RecordAuthEvent(ctx, email, OutcomeDenied, SourceOAuth, ReasonMissingCredential)

	// Poll until the goroutine's row appears (up to 2s, 50ms intervals).
	deadline := time.Now().Add(2 * time.Second)
	var count int
	for time.Now().Before(deadline) {
		if err := cs.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM cloud_auth_audit_log WHERE email = $1`, email,
		).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if count == 0 {
		t.Error("expected RecordAuthEvent to write a row within 2s, got 0")
	}
}

// TestInsertAuthAuditEntry_nilReasonCode verifies that empty reasonCode is
// stored as NULL (not empty string) for outcome=allowed rows.
func TestInsertAuthAuditEntry_nilReasonCode(t *testing.T) {
	cs := openTestCloudStore(t)
	ctx := context.Background()

	if err := cs.InsertAuthAuditEntry(ctx, "nullreason@test.com", OutcomeAllowed, SourceLegacy, ""); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var reasonCode *string
	if err := cs.db.QueryRowContext(ctx,
		`SELECT reason_code FROM cloud_auth_audit_log WHERE email = 'nullreason@test.com' ORDER BY id DESC LIMIT 1`,
	).Scan(&reasonCode); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if reasonCode != nil {
		t.Errorf("expected NULL reason_code for allowed row, got %q", *reasonCode)
	}
}
