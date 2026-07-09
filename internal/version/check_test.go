package version

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"v1.8.1", "1.8.1"},
		{"1.8.1", "1.8.1"},
		{" v2.0.0 ", "2.0.0"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeVersion(tt.in); got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSplitVersion(t *testing.T) {
	tests := []struct {
		in   string
		want [4]int
	}{
		{"1.8.1", [4]int{1, 8, 1, 0}},
		{"2.0.0", [4]int{2, 0, 0, 0}},
		{"1.0", [4]int{1, 0, 0, 0}},
		{"", [4]int{0, 0, 0, 0}},
		// non-viva prerelease: patch digits stop at '-', iteration stays 0
		{"1.8.1-beta", [4]int{1, 8, 1, 0}},
		// viva suffix: parsed into the 4th component
		{"1.16.3-viva.1", [4]int{1, 16, 3, 1}},
		{"1.16.3-viva.9", [4]int{1, 16, 3, 9}},
		{"1.16.3-viva.12", [4]int{1, 16, 3, 12}},
	}
	for _, tt := range tests {
		if got := splitVersion(tt.in); got != tt.want {
			t.Errorf("splitVersion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name            string
		latest, current string
		want            bool
	}{
		// existing base-version cases
		{"patch bump", "1.8.1", "1.8.0", true},
		{"major bump", "2.0.0", "1.9.9", true},
		{"equal", "1.8.1", "1.8.1", false},
		{"older major", "1.7.0", "1.8.1", false},
		{"patch newer", "1.8.2", "1.8.1", true},
		// viva iteration: same base, iteration advances
		{"viva iter newer", "1.16.4-viva.2", "1.16.4-viva.1", true},
		{"viva iter older", "1.16.4-viva.1", "1.16.4-viva.2", false},
		{"viva iter equal", "1.16.4-viva.1", "1.16.4-viva.1", false},
		// different base with viva suffix
		{"base patch beats viva iter", "1.16.5-viva.1", "1.16.4-viva.9", true},
		// legacy (no viva) vs viva.N — viva.1 is newer than bare release
		{"viva beats legacy same base", "1.16.4-viva.1", "1.16.4", true},
		// legacy is NOT newer than viva
		{"legacy not newer than viva", "1.16.4", "1.16.4-viva.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNewer(tt.latest, tt.current); got != tt.want {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}

func TestCheckLatest(t *testing.T) {
	t.Run("dev and empty versions fail honestly", func(t *testing.T) {
		result := CheckLatest("dev")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "do not map to a release version") {
			t.Fatalf("message = %q", result.Message)
		}

		result = CheckLatest("")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "current version is unknown") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("update available", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.8"}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusUpdateAvailable {
			t.Fatalf("status = %q, want %q", result.Status, StatusUpdateAvailable)
		}
		if !strings.Contains(result.Message, "Update available: 1.10.7 -> 1.10.8") || !strings.Contains(result.Message, "To update:") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("up to date", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusUpToDate {
			t.Fatalf("status = %q, want %q", result.Status, StatusUpToDate)
		}
		if result.Message != "" {
			t.Fatalf("message = %q, want empty", result.Message)
		}
	})

	t.Run("non-200 becomes check failed", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "rate limited", http.StatusForbidden)
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "403 Forbidden") || !strings.Contains(result.Message, "GH_TOKEN") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("decode error becomes check failed", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "could not read the GitHub response") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("missing tag becomes check failed", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":""}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "did not return a release version") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("timeout becomes check failed", func(t *testing.T) {
		withCheckTimeout(t, 20*time.Millisecond)
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.8"}`))
		}))

		result := CheckLatest("1.10.7")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "took too long to respond") {
			t.Fatalf("message = %q", result.Message)
		}
	})
}

func TestCheckLatestUsesGitHubToken(t *testing.T) {
	t.Run("prefers GH_TOKEN", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "gh-token")
		t.Setenv("GITHUB_TOKEN", "github-token")

		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer gh-token" {
				t.Fatalf("authorization = %q", got)
			}
			if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Fatalf("accept = %q", got)
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		_ = CheckLatest("1.10.7")
	})

	t.Run("falls back to GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "github-token")

		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer github-token" {
				t.Fatalf("authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		_ = CheckLatest("1.10.7")
	})

	t.Run("omits authorization header without token", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")

		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("authorization = %q, want empty", got)
			}
			_, _ = w.Write([]byte(`{"tag_name":"v1.10.7"}`))
		}))

		_ = CheckLatest("1.10.7")
	})
}

func TestCheckLatestVivaVersioning(t *testing.T) {
	t.Run("viva iteration bump is detected as update", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.16.4-viva.2"}`))
		}))

		result := CheckLatest("1.16.4-viva.1")
		if result.Status != StatusUpdateAvailable {
			t.Fatalf("status = %q, want %q", result.Status, StatusUpdateAvailable)
		}
		if !strings.Contains(result.Message, "1.16.4-viva.1 -> 1.16.4-viva.2") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("same viva version is up to date", func(t *testing.T) {
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.16.4-viva.1"}`))
		}))

		result := CheckLatest("1.16.4-viva.1")
		if result.Status != StatusUpToDate {
			t.Fatalf("status = %q, want %q", result.Status, StatusUpToDate)
		}
	})

	t.Run("dev version still yields no update check", func(t *testing.T) {
		// Do NOT call withCheckServer here; the dev guard fires before any HTTP
		// request is made, so no server is needed.
		result := CheckLatest("dev")
		if result.Status != StatusCheckFailed {
			t.Fatalf("status = %q, want %q", result.Status, StatusCheckFailed)
		}
		if !strings.Contains(result.Message, "do not map to a release version") {
			t.Fatalf("message = %q", result.Message)
		}
	})
}

func TestUpdateInstructions(t *testing.T) {
	msg := updateInstructions()
	if msg == "" {
		t.Fatal("expected non-empty update instructions")
	}
	if !strings.Contains(msg, "MarioPradEs/engram") {
		t.Fatalf("update instructions should reference the fork (MarioPradEs/engram), got: %q", msg)
	}
}

// TestLatestCached verifies the TTL-cache wrapper around the GitHub latest-release fetch.
// The cache is package-level state; each sub-test must reset it via resetLatestCache().
func TestLatestCached(t *testing.T) {
	t.Run("returns version string on first successful fetch", func(t *testing.T) {
		resetLatestCache(t)
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.20.0"}`))
		}))

		got, err := LatestCached()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "1.20.0" {
			t.Fatalf("version = %q, want %q", got, "1.20.0")
		}
	})

	t.Run("cache hit avoids second HTTP fetch", func(t *testing.T) {
		resetLatestCache(t)
		calls := 0
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.20.0"}`))
		}))

		_, _ = LatestCached()
		_, _ = LatestCached()

		if calls != 1 {
			t.Fatalf("expected 1 HTTP call, got %d", calls)
		}
	})

	t.Run("expired cache triggers refetch", func(t *testing.T) {
		resetLatestCache(t)
		withLatestCacheTTL(t, 10*time.Millisecond)
		calls := 0
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.20.0"}`))
		}))

		_, _ = LatestCached()
		time.Sleep(20 * time.Millisecond) // let TTL expire
		_, _ = LatestCached()

		if calls < 2 {
			t.Fatalf("expected at least 2 HTTP calls after TTL expiry, got %d", calls)
		}
	})

	t.Run("fetch error returns last-good cached value", func(t *testing.T) {
		resetLatestCache(t)
		withLatestCacheTTL(t, 10*time.Millisecond)
		callN := 0
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callN++
			if callN == 1 {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"tag_name":"v1.20.0"}`))
				return
			}
			// second call: return error status
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		}))

		got1, err1 := LatestCached()
		if err1 != nil || got1 != "1.20.0" {
			t.Fatalf("first call: got=%q err=%v", got1, err1)
		}

		time.Sleep(20 * time.Millisecond) // expire
		got2, err2 := LatestCached()
		// On error, should return last-good value (non-empty) and a non-nil error
		if err2 == nil {
			t.Fatalf("expected error on second (failing) fetch, got nil")
		}
		if got2 != "1.20.0" {
			t.Fatalf("expected stale value %q on fetch failure, got %q", "1.20.0", got2)
		}
	})

	t.Run("cold start fetch error returns empty string and error", func(t *testing.T) {
		resetLatestCache(t)
		withCheckServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		}))

		got, err := LatestCached()
		if err == nil {
			t.Fatal("expected error on cold fetch failure, got nil")
		}
		if got != "" {
			t.Fatalf("expected empty string on cold fetch failure, got %q", got)
		}
	})
}

func withCheckServer(t *testing.T, handler http.Handler) {
	t.Helper()

	srv := httptest.NewServer(handler)
	oldURL := githubLatestReleaseURL
	githubLatestReleaseURL = srv.URL
	t.Cleanup(func() {
		githubLatestReleaseURL = oldURL
		srv.Close()
	})
}

// resetLatestCache zeroes the package-level LatestCached cache so each test
// starts with a cold state. Registered as a t.Cleanup so it runs on failure too.
func resetLatestCache(t *testing.T) {
	t.Helper()
	latestCache.mu.Lock()
	latestCache.value = ""
	latestCache.fetchedAt = time.Time{}
	latestCache.mu.Unlock()
	t.Cleanup(func() {
		latestCache.mu.Lock()
		latestCache.value = ""
		latestCache.fetchedAt = time.Time{}
		latestCache.mu.Unlock()
	})
}

// withLatestCacheTTL overrides latestCacheTTL for the duration of the test.
func withLatestCacheTTL(t *testing.T, d time.Duration) {
	t.Helper()
	old := latestCacheTTL
	latestCacheTTL = d
	t.Cleanup(func() { latestCacheTTL = old })
}

func withCheckTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	oldTimeout := checkTimeout
	checkTimeout = timeout
	t.Cleanup(func() { checkTimeout = oldTimeout })
}

func TestNonOKStatusMessage(t *testing.T) {
	if got := nonOKStatusMessage(fmt.Sprintf("%d %s", http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))); !strings.Contains(got, "GH_TOKEN") {
		t.Fatalf("message = %q", got)
	}
}
