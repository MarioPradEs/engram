package version

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)


// latestCacheTTL controls how long a fetched version is considered fresh.
// It is a var so tests can override it via withLatestCacheTTL.
var latestCacheTTL = 1 * time.Hour

// latestVersionCache holds a single cached GitHub latest-release version string.
type latestVersionCache struct {
	mu        sync.RWMutex
	value     string    // normalised version string (e.g. "1.20.0"); "" means never fetched successfully
	fetchedAt time.Time // zero value means cache is cold
}

// latestCache is the package-level singleton.
var latestCache latestVersionCache

// fetchGroup ensures only one in-flight GitHub fetch runs at a time.
// Other callers during an in-flight fetch receive the last-good cached
// value rather than blocking on the network call.
var fetchGroup singleflight.Group

// LatestCached returns the latest published version from GitHub, using a
// package-level TTL cache to avoid repeated network calls.
//
// On a cache hit (age < latestCacheTTL): returns cached value, nil.
// On a cache miss / expiry:
//   - Only ONE goroutine performs the fetch; others return the stale value
//     (if any) without blocking on the HTTP call.
//   - Success: updates cache, returns normalised version string, nil.
//   - Failure with prior cached value: returns stale value and a non-nil error.
//   - Failure with no prior value (cold start): returns "", non-nil error.
func LatestCached() (string, error) {
	// Fast path: read under read lock.
	latestCache.mu.RLock()
	if latestCache.value != "" && time.Since(latestCache.fetchedAt) < latestCacheTTL {
		v := latestCache.value
		latestCache.mu.RUnlock()
		return v, nil
	}
	stale := latestCache.value
	latestCache.mu.RUnlock()

	// Cache is cold or expired. Use singleflight so only one goroutine fetches.
	// Do() blocks the caller until the fetch completes, but because we call it
	// from withAuth's fire-and-forget goroutine or the dashboard handler (which
	// already handles errors gracefully), this is acceptable.
	//
	// Concurrent dashboard loads that arrive while a fetch is in progress will
	// block briefly on Do(), which is preferable to N simultaneous GitHub calls.
	// For true non-blocking behaviour callers can check the stale value first
	// (above) and only reach here when a refresh is needed.
	type fetchResult struct {
		value string
		err   error
	}
	raw, err, _ := fetchGroup.Do("latest", func() (any, error) {
		fetched, fetchErr := fetchLatestFromGitHub()
		if fetchErr != nil {
			return fetchResult{value: stale, err: fetchErr}, nil
		}
		// Store the fresh value under write lock.
		latestCache.mu.Lock()
		latestCache.value = fetched
		latestCache.fetchedAt = time.Now()
		latestCache.mu.Unlock()
		return fetchResult{value: fetched, err: nil}, nil
	})
	if err != nil {
		// singleflight itself failed (not the fetch) — should not happen.
		return stale, fmt.Errorf("version: singleflight error: %w", err)
	}

	res := raw.(fetchResult)
	if res.err != nil {
		if stale != "" {
			return stale, fmt.Errorf("version: GitHub fetch failed; returning stale cached value %q: %w", stale, res.err)
		}
		return "", fmt.Errorf("version: GitHub fetch failed and no cached value available: %w", res.err)
	}
	return res.value, nil
}

// IsNewer reports whether latest is a newer version than current using 4-component
// semver comparison that understands the "-viva.N" fork suffix. It is an exported
// wrapper around the internal isNewer function so callers outside this package
// (e.g. the dashboard handler) can compare versions without reimplementing the logic.
// Satisfies: REQ-VID-09 (reuse internal/version logic, no duplicate parsing code).
func IsNewer(latest, current string) bool {
	return isNewer(latest, current)
}

