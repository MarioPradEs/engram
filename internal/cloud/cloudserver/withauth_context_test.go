package cloudserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// contextCapturingStore implements ChunkStore + ClientVersionRecorder and
// records the context received by RecordClientVersion, signalling the caller
// when the write has been attempted.
type contextCapturingStore struct {
	*fakeStore
	capturedCtx context.Context
	done        chan struct{}
}

func newContextCapturingStore() *contextCapturingStore {
	return &contextCapturingStore{
		fakeStore: &fakeStore{},
		done:      make(chan struct{}),
	}
}

func (s *contextCapturingStore) RecordClientVersion(ctx context.Context, _, _ string) error {
	s.capturedCtx = ctx
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

// TestWithAuthUsesWithoutCancelContext is a regression test for the bug where
// the fire-and-forget goroutine called RecordClientVersion with r.Context(),
// which net/http cancels as soon as ServeHTTP returns, causing the async DB
// write to receive context.Canceled.
//
// With context.WithoutCancel (the fix), the captured context's Err() must be
// nil even after the parent request context is cancelled.
//
// Satisfies: ADR-2 (goroutine write must not be cancelled by handler return).
func TestWithAuthUsesWithoutCancelContext(t *testing.T) {
	st := newContextCapturingStore()
	auth := fakeAuthWithAttribution{email: "alice@example.com"}
	srv := New(st, auth, 0)

	// Build a request whose context we can cancel independently.
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	base := httptest.NewRequest(http.MethodGet, "/sync/pull?project=proj-a", nil)
	req := base.WithContext(parentCtx)
	req.Header.Set("X-Engram-Client-Version", "1.17.0-viva.9")

	rec := httptest.NewRecorder()
	// ServeHTTP returns, which causes net/http to cancel r.Context().
	// The fix detaches the context before spawning the goroutine.
	srv.Handler().ServeHTTP(rec, req)

	// Cancel the parent so r.Context() (if mistakenly captured) would be Done.
	cancelParent()

	// Wait for the goroutine to record and signal.
	select {
	case <-st.done:
	case <-time.After(2 * time.Second):
		t.Fatal("RecordClientVersion goroutine did not complete within 2s")
	}

	if st.capturedCtx == nil {
		t.Fatal("capturedCtx is nil — RecordClientVersion was not called")
	}

	// The captured context MUST NOT be cancelled, even though we cancelled
	// the parent. context.WithoutCancel returns a context that is never
	// cancelled by its parent.
	if err := st.capturedCtx.Err(); err != nil {
		t.Errorf("context passed to RecordClientVersion is cancelled (%v); want nil — fix is missing or context.WithoutCancel was not used", err)
	}
}
