package main

// ─── PR3: Prune Ticker Wiring Tests ──────────────────────────────────────────
//
// TDD cycle: RED → GREEN → REFACTOR
// Tests written FIRST (RED). They verify the prune ticker is wired into
// defaultCloudRuntime.Start() — the goroutine calls PruneAuthAuditBefore
// at startup and at each 24-hour tick.
//
// Why the goroutine itself is not directly unit-testable at 24h resolution:
//   - The real ticker fires every 24h; we cannot wait that long in a test.
//   - defaultCloudRuntime.Start() blocks on server.Start() — we cannot call it
//     in a test without a real server port.
//   - The ticker goroutine uses the runtime context (r.ctx) for cancellation;
//     there is no export seam in defaultCloudRuntime for clock injection.
//
// The pragmatic approach: test the observable wiring behavior by using a
// recordingPruner that the production code will call, and invoke the prune
// logic directly via a testable helper. The core correctness of PruneAuthAuditBefore
// (retention boundary, no cross-table deletion) is fully covered in PR1 cloudstore tests.
//
// Here we test:
//   1. pruneAuthAuditLog90Days calls PruneAuthAuditBefore with a cutoff exactly 90 days ago.
//   2. The cutoff is strictly before "now minus 90 days" (within a 1-second tolerance).
//   3. A failed prune logs WARN and does NOT return an error (failure-safe contract).

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"testing"
	"time"
)

// recordingPruner records calls to PruneAuthAuditBefore for test assertions.
type recordingPruner struct {
	cutoffs []time.Time
	errOnce error // if set, returned on the first call then cleared
}

func (r *recordingPruner) PruneAuthAuditBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	r.cutoffs = append(r.cutoffs, cutoff)
	if r.errOnce != nil {
		err := r.errOnce
		r.errOnce = nil
		return 0, err
	}
	return 0, nil
}

// TestPruneAuthAuditLog90Days_callsPruneWithCorrectCutoff verifies that
// pruneAuthAuditLog90Days calls the store with a cutoff that is exactly 90 days
// before the current time (within a 2-second tolerance to account for test timing).
// This is RED until pruneAuthAuditLog90Days is defined in cloud.go.
func TestPruneAuthAuditLog90Days_callsPruneWithCorrectCutoff(t *testing.T) {
	rec := &recordingPruner{}
	before := time.Now().UTC()
	pruneAuthAuditLog90Days(context.Background(), rec)
	after := time.Now().UTC()

	if len(rec.cutoffs) != 1 {
		t.Fatalf("expected exactly 1 prune call, got %d", len(rec.cutoffs))
	}
	cutoff := rec.cutoffs[0]

	// The cutoff should be approximately (now - 90 days). Allow ±2s for test execution time.
	expectedLow := before.Add(-90 * 24 * time.Hour).Add(-2 * time.Second)
	expectedHigh := after.Add(-90 * 24 * time.Hour).Add(2 * time.Second)

	if cutoff.Before(expectedLow) || cutoff.After(expectedHigh) {
		t.Errorf("prune cutoff %v is outside the expected 90-day window [%v, %v]", cutoff, expectedLow, expectedHigh)
	}
}

// TestPruneAuthAuditLog90Days_failureSafe verifies that when PruneAuthAuditBefore
// returns an error, pruneAuthAuditLog90Days does NOT propagate it and does NOT panic.
// This tests the failure-safe / WARN-only contract.
func TestPruneAuthAuditLog90Days_failureSafe(t *testing.T) {
	rec := &recordingPruner{errOnce: errors.New("db connection lost")}
	// Must not panic. No error returned (void signature).
	pruneAuthAuditLog90Days(context.Background(), rec)
	// The call was still attempted.
	if len(rec.cutoffs) != 1 {
		t.Fatalf("expected exactly 1 prune attempt even on error, got %d", len(rec.cutoffs))
	}
}

// TestPruneAuthAuditLog90Days_logWarnOnError verifies that a prune error is
// logged at WARN level and that the log output contains the method name.
// This test must NOT run in parallel because it redirects the global log output.
func TestPruneAuthAuditLog90Days_logWarnOnError(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	rec := &recordingPruner{errOnce: errors.New("timeout")}
	pruneAuthAuditLog90Days(context.Background(), rec)

	got := buf.String()
	if !bytes.Contains([]byte(got), []byte("WARN")) {
		t.Errorf("expected log output to contain %q, got: %s", "WARN", got)
	}
	if !bytes.Contains([]byte(got), []byte("pruneAuthAuditLog90Days")) {
		t.Errorf("expected log output to contain method name %q, got: %s", "pruneAuthAuditLog90Days", got)
	}
}

