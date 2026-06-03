package main

// post_login_push_batching_test.go — STRICT TDD (RED → GREEN → REFACTOR)
// Tests for fix: post-login push must send ≤100 entries per PushMutations request.
//
// The cloud server caps batches at 100 entries and returns 400 "batch too large"
// when the limit is exceeded. Before this fix, doPostLoginPushFromStore read up to
// 500 pending mutations and pushed them all in one call — triggering the 400.
//
// Coverage:
//  1. >100 pending mutations → multiple calls, each ≤100 entries, real total returned.
//  2. Mid-chunk failure → returns count-so-far + error; login still succeeds (warning).
//  3. ≤100 pending mutations → exactly 1 call (existing behavior preserved).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Batch-asserting fake server ─────────────────────────────────────────────

// batchCapFakeServer is an httptest.Server that:
//   - Asserts every push request has ≤maxBatchSize entries.
//   - Tracks total entries received across all calls.
//   - Returns 400 with "batch too large" if the limit is violated (mirrors the real server).
//   - Can be configured to fail on the Nth call (for mid-chunk failure testing).
type batchCapFakeServer struct {
	srv          *httptest.Server
	mu           sync.Mutex
	maxBatchSize int    // server-enforced cap; fail with 400 if exceeded
	callCount    int    // incremented on each push request
	totalEntries int    // sum of entry counts across all successful calls
	failOnCall   int    // if >0, fail with 500 on this call number (1-indexed)
	authHeaders  []string
}

func newBatchCapFakeServer(t *testing.T, maxBatchSize int) *batchCapFakeServer {
	t.Helper()
	f := &batchCapFakeServer{maxBatchSize: maxBatchSize}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sync/mutations/push" {
			http.NotFound(w, r)
			return
		}

		f.mu.Lock()
		f.callCount++
		callNum := f.callCount
		failOn := f.failOnCall
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		f.mu.Unlock()

		// Decode the request body to count entries.
		var req struct {
			Entries []json.RawMessage `json:"entries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		n := len(req.Entries)

		// Enforce the server-side batch cap (mirrors cloudserver/mutations.go).
		if n > f.maxBatchSize {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"batch too large: max %d entries per request"}`, f.maxBatchSize)
			return
		}

		// Simulate mid-chunk failure on the specified call number.
		if failOn > 0 && callNum == failOn {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"simulated server error"}`))
			return
		}

		// Accept all entries in this batch.
		f.mu.Lock()
		f.totalEntries += n
		f.mu.Unlock()

		seqs := make([]int64, n)
		for i := range seqs {
			seqs[i] = int64(i + 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted_seqs": seqs})
	}))
	t.Cleanup(func() { f.srv.Close() })
	return f
}

func (f *batchCapFakeServer) URL() string { return f.srv.URL }

func (f *batchCapFakeServer) TotalEntries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.totalEntries
}

func (f *batchCapFakeServer) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

// ─── Helper: create N pending sync mutations in the store ────────────────────

// seedPendingMutations creates n observations in a fresh session so that
// ListPendingSyncMutations returns at least n pending entries.
func seedPendingMutations(t *testing.T, s *store.Store, project string, n int) {
	t.Helper()
	sessionID := fmt.Sprintf("batch-test-session-%d", n)
	if err := s.CreateSession(sessionID, project, "/tmp/batch-test"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: sessionID,
			Project:   project,
			Content:   fmt.Sprintf("batch test observation %d", i),
		}); err != nil {
			t.Fatalf("AddObservation[%d]: %v", i, err)
		}
	}
}

// ─── Test: >100 pending → multiple calls each ≤100, real total returned ──────

// TestPostLoginPushBatching_Over100_MultipleCalls verifies that when there are
// more than 100 pending mutations, doPostLoginPushFromStore sends them in chunks
// of ≤100 and accumulates the total accepted count across calls.
//
// Failure before the fix: a single call with 250 entries would cause the fake
// server to return 400 "batch too large", doPostLoginPushFromStore would return
// (0, error), and the login summary would print ↑0.
func TestPostLoginPushBatching_Over100_MultipleCalls(t *testing.T) {
	const totalMutations = 250
	const serverCap = 100

	srv := newBatchCapFakeServer(t, serverCap)
	// No failOnCall — all calls should succeed.

	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.EnrollProject("general"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	seedPendingMutations(t, s, "general", totalMutations)

	// Verify we actually have ≥totalMutations pending (ListPendingSyncMutations
	// reads up to 500, so we get the full set).
	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 500)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	if len(pending) < totalMutations {
		t.Fatalf("expected at least %d pending mutations, got %d", totalMutations, len(pending))
	}

	got, err := doPostLoginPushFromStore(srv.URL(), "test-token", s)
	if err != nil {
		t.Fatalf("doPostLoginPushFromStore: unexpected error: %v", err)
	}

	// Total accepted must equal what we pushed.
	if got != len(pending) {
		t.Errorf("expected %d total accepted, got %d", len(pending), got)
	}

	// Server must have been called multiple times.
	calls := srv.CallCount()
	if calls < 2 {
		t.Errorf("expected multiple push calls for %d mutations (cap=%d), got %d call(s)", len(pending), serverCap, calls)
	}

	// Each individual call must have been within the server cap.
	// The fake server already enforces this with 400 — if any call had >100 entries,
	// the server would have returned an error and this test would have failed above.
	// Double-check: total entries received by server == what we sent.
	if srv.TotalEntries() != len(pending) {
		t.Errorf("server received %d total entries, expected %d", srv.TotalEntries(), len(pending))
	}
}

// ─── Test: mid-chunk failure → count-so-far + error, login still succeeds ───

// TestPostLoginPushBatching_MidChunkFailure verifies that if a push call fails
// mid-way through the chunks, doPostLoginPushFromStore returns the count pushed
// so far (from the chunks that succeeded) plus the error. The caller (loginCommand)
// treats push failure as a warning — login still succeeds.
func TestPostLoginPushBatching_MidChunkFailure(t *testing.T) {
	// Seed enough observations to create 2+ chunks at cap=100.
	// Note: CreateSession enqueues 1 extra mutation, so 149 observations → ~150 total pending.
	const seedObservations = 149
	const serverCap = 100

	srv := newBatchCapFakeServer(t, serverCap)
	srv.failOnCall = 2 // chunk 1 succeeds (~100 entries), chunk 2 fails

	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.EnrollProject("general"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	seedPendingMutations(t, s, "general", seedObservations)

	// Count actual pending before push so we know total.
	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 500)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	totalPending := len(pending)
	if totalPending <= serverCap {
		t.Fatalf("test setup error: expected >%d pending mutations for multi-chunk test, got %d", serverCap, totalPending)
	}

	got, pushErr := doPostLoginPushFromStore(srv.URL(), "test-token", s)

	// Must return an error (chunk 2 failed).
	if pushErr == nil {
		t.Fatal("expected error from mid-chunk failure, got nil")
	}

	// Count returned must be the entries from the successful chunk(s).
	// Chunk 1 sent 100 entries and succeeded → count-so-far must be 100.
	if got <= 0 {
		t.Errorf("expected count-so-far > 0 from successful chunk(s), got %d", got)
	}
	if got >= totalPending {
		t.Errorf("expected partial count < %d (only chunk 1 succeeded), got %d", totalPending, got)
	}

	// loginCommand.Run treats push error as a warning — verify the function signature
	// returns (count, error) allowing login to continue.
	// (The integration is tested in TestPostLoginSync_PushFailureIsWarningNotError.)
}

// ─── Test: ≤100 pending → single call (existing behavior preserved) ──────────

// TestPostLoginPushBatching_Under100_SingleCall verifies that when there are ≤100
// pending mutations, doPostLoginPushFromStore makes exactly one push call.
// This preserves the existing behavior and ensures no regression.
func TestPostLoginPushBatching_Under100_SingleCall(t *testing.T) {
	const totalMutations = 42
	const serverCap = 100

	srv := newBatchCapFakeServer(t, serverCap)

	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.EnrollProject("general"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	seedPendingMutations(t, s, "general", totalMutations)

	// Count actual pending mutations before pushing (CreateSession also enqueues one).
	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 500)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	pendingCount := len(pending)
	if pendingCount > serverCap {
		t.Fatalf("test setup error: got %d pending mutations, expected ≤%d for single-call test", pendingCount, serverCap)
	}

	got, err := doPostLoginPushFromStore(srv.URL(), "test-token", s)
	if err != nil {
		t.Fatalf("doPostLoginPushFromStore: unexpected error: %v", err)
	}

	// All mutations pushed successfully.
	if got != pendingCount {
		t.Errorf("expected %d accepted (all pending), got %d", pendingCount, got)
	}

	// Exactly one call to the server.
	if srv.CallCount() != 1 {
		t.Errorf("expected exactly 1 push call for %d mutations, got %d", pendingCount, srv.CallCount())
	}
}
