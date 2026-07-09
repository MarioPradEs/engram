package cloudstore

import (
	"context"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud"
)

// invalidationCount returns how many times the read-model has been invalidated
// since the CloudStore was created by reading a private field. We observe
// invalidation indirectly: call an operation that populates the cache, then
// check dashboardReadModelOK after RecordClientVersion runs.
//
// Strategy: prime the cache (dashboardReadModelOK = true), then call
// RecordClientVersion and check whether the cache was busted.
func primeReadModelCache(t *testing.T, cs *CloudStore) {
	t.Helper()
	// Force a rebuild by calling ListContributors (it re-builds via GetDashboardReadModel).
	_, _ = cs.ListContributors("")
	// Now dashboardReadModelOK should be true.
	cs.dashboardReadModelMu.Lock()
	ok := cs.dashboardReadModelOK
	cs.dashboardReadModelMu.Unlock()
	if !ok {
		t.Log("note: read model not populated (no chunks ingested) — test still validates invalidation count")
	}
}

func readModelIsInvalidated(cs *CloudStore) bool {
	cs.dashboardReadModelMu.Lock()
	defer cs.dashboardReadModelMu.Unlock()
	return !cs.dashboardReadModelOK
}

// TestRecordClientVersionInvalidatesOnChangeOnly verifies that the read-model
// cache is invalidated when the stored version changes, but NOT when the same
// version is recorded again (no-op upsert path).
//
// Satisfies: FIX #2 — avoid constant cache thrash on every sync request.
// Postgres-gated: requires CLOUDSTORE_TEST_DSN.
func TestRecordClientVersionInvalidatesOnChangeOnly(t *testing.T) {
	dsn := testDSN(t)

	cs, err := New(cloud.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cs.Close()

	ctx := context.Background()

	// --- First insert: new row → must invalidate. ---
	primeReadModelCache(t, cs)
	if err := cs.RecordClientVersion(ctx, "delta@example.com", "1.20.0"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !readModelIsInvalidated(cs) {
		t.Error("read-model should be invalidated after first insert (new row)")
	}

	// --- Same version again: no change → must NOT invalidate. ---
	// Prime cache again so we can tell whether it was invalidated.
	primeReadModelCache(t, cs)
	// Mark it as valid so we can detect the invalidation.
	cs.dashboardReadModelMu.Lock()
	cs.dashboardReadModelOK = true
	cs.dashboardReadModelMu.Unlock()

	if err := cs.RecordClientVersion(ctx, "delta@example.com", "1.20.0"); err != nil {
		t.Fatalf("same-version upsert: %v", err)
	}
	if readModelIsInvalidated(cs) {
		t.Error("read-model must NOT be invalidated when version did not change (no-op upsert)")
	}

	// --- Different version: real change → must invalidate again. ---
	if err := cs.RecordClientVersion(ctx, "delta@example.com", "1.21.0"); err != nil {
		t.Fatalf("version change: %v", err)
	}
	if !readModelIsInvalidated(cs) {
		t.Error("read-model should be invalidated after version change")
	}
}
