package version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// latestCacheTTL controls how long a fetched version is considered fresh.
// It is a var so tests can override it via withLatestCacheTTL.
var latestCacheTTL = 1 * time.Hour

// latestVersionCache holds a single cached GitHub latest-release version string.
type latestVersionCache struct {
	mu        sync.Mutex
	value     string    // normalised version string (e.g. "1.20.0"); "" means never fetched successfully
	fetchedAt time.Time // zero value means cache is cold
}

// latestCache is the package-level singleton.
var latestCache latestVersionCache

// LatestCached returns the latest published version from GitHub, using a
// package-level TTL cache to avoid repeated network calls.
//
// On a cache hit (age < latestCacheTTL): returns cached value, nil.
// On a cache miss / expiry: fetches from GitHub.
//   - Success: updates cache, returns normalised version string, nil.
//   - Failure with prior cached value: returns stale value and a non-nil error.
//   - Failure with no prior value (cold start): returns "", non-nil error.
func LatestCached() (string, error) {
	latestCache.mu.Lock()
	defer latestCache.mu.Unlock()

	if latestCache.value != "" && time.Since(latestCache.fetchedAt) < latestCacheTTL {
		return latestCache.value, nil
	}

	fetched, fetchErr := doFetchLatestVersion()
	if fetchErr != nil {
		if latestCache.value != "" {
			return latestCache.value, fmt.Errorf("version: GitHub fetch failed; returning stale cached value %q: %w", latestCache.value, fetchErr)
		}
		return "", fmt.Errorf("version: GitHub fetch failed and no cached value available: %w", fetchErr)
	}

	latestCache.value = fetched
	latestCache.fetchedAt = time.Now()
	return fetched, nil
}

// IsNewer reports whether latest is a newer version than current using 4-component
// semver comparison that understands the "-viva.N" fork suffix. It is an exported
// wrapper around the internal isNewer function so callers outside this package
// (e.g. the dashboard handler) can compare versions without reimplementing the logic.
// Satisfies: REQ-VID-09 (reuse internal/version logic, no duplicate parsing code).
func IsNewer(latest, current string) bool {
	return isNewer(latest, current)
}

// doFetchLatestVersion performs a bounded HTTP GET to the GitHub releases API
// and returns the normalised tag name (e.g. "1.20.0"). Returns an error on any
// network, HTTP, or decode failure. Reuses package-level vars (githubLatestReleaseURL,
// checkTimeout, httpClient) so tests can override them via withCheckServer.
func doFetchLatestVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubLatestReleaseURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := githubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	v := normalizeVersion(release.TagName)
	if v == "" {
		return "", fmt.Errorf("GitHub returned empty tag_name")
	}
	return v, nil
}
