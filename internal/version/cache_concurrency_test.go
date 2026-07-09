package version

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLatestCachedSingleFlightOnExpiry verifies that when the TTL expires and N
// concurrent callers all call LatestCached simultaneously, only ONE HTTP fetch
// is issued to GitHub. Other callers block briefly on singleflight.Do and then
// receive the fetched value (not N parallel requests).
//
// The test also confirms that a cache HIT never triggers a fetch.
//
// Satisfies: FIX #3 — do not hold cache mutex across the HTTP call.
func TestLatestCachedSingleFlightOnExpiry(t *testing.T) {
	resetLatestCache(t)
	withLatestCacheTTL(t, 10*time.Millisecond)

	var fetchCount atomic.Int64
	// unblock is used to hold all concurrent fetch goroutines at the same point,
	// ensuring they truly race for the singleflight key.
	startFetch := make(chan struct{})

	withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		// Wait until the test signals us; this keeps the fetch "in flight"
		// long enough for all N callers to arrive at singleflight.Do.
		<-startFetch
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
	}))

	// Prime the cache so callers get a stale value while the fetch is in flight.
	withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	got, err := LatestCached()
	if err != nil || got != "1.0.0" {
		t.Fatalf("prime: got=%q err=%v", got, err)
	}

	// Expire the cache.
	time.Sleep(20 * time.Millisecond)

	// Replace server with the blocking one (re-register after withCheckServer setup).
	// withCheckServer already replaced githubLatestReleaseURL; re-set with the blocker.
	fetchCount.Store(0)
	blocker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		<-startFetch
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
	}))
	t.Cleanup(blocker.Close)
	oldURL := githubLatestReleaseURL
	githubLatestReleaseURL = blocker.URL
	t.Cleanup(func() { githubLatestReleaseURL = oldURL })

	// Launch N concurrent callers. They all arrive after cache expiry.
	const N = 10
	var wg sync.WaitGroup
	results := make([]string, N)
	errs := make([]error, N)
	for i := range N {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = LatestCached()
		}(i)
	}

	// Give goroutines time to queue up behind singleflight.
	time.Sleep(50 * time.Millisecond)

	// Release the blocked HTTP handler.
	close(startFetch)

	wg.Wait()

	// Exactly one HTTP call should have been made.
	if got := fetchCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 HTTP fetch, got %d (singleflight not working)", got)
	}

	// All callers should have received the new version (no errors).
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: unexpected error: %v", i, err)
		}
	}
	for i, v := range results {
		if v != "2.0.0" {
			t.Errorf("caller %d: got version %q, want %q", i, v, "2.0.0")
		}
	}
}

// TestLatestCachedHitNeverFetches asserts that a warm cache hit issues zero HTTP calls.
func TestLatestCachedHitNeverFetches(t *testing.T) {
	resetLatestCache(t)
	withLatestCacheTTL(t, time.Hour)

	var fetchCount atomic.Int64
	withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.5.0"}`))
	}))

	// First call populates the cache.
	got, err := LatestCached()
	if err != nil || got != "1.5.0" {
		t.Fatalf("first call: got=%q err=%v", got, err)
	}
	if fetchCount.Load() != 1 {
		t.Fatalf("expected 1 fetch after cold start, got %d", fetchCount.Load())
	}

	// Subsequent calls must be served from cache without any HTTP call.
	for range 5 {
		v, err := LatestCached()
		if err != nil || v != "1.5.0" {
			t.Fatalf("cache hit: got=%q err=%v", v, err)
		}
	}
	if got := fetchCount.Load(); got != 1 {
		t.Errorf("cache hit triggered %d extra fetches, want 0 extra", got-1)
	}
}
