// Package version checks for newer engram releases on GitHub.
package version

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	// Fork (Viva Studios): auto-update checks releases from the fork, not upstream,
	// so the multi-user/cloud features are not "downgraded" to an upstream release.
	repoOwner = "MarioPradEs"
	repoName  = "engram"
)

var (
	checkTimeout           = 2 * time.Second
	githubLatestReleaseURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	httpClient             = http.DefaultClient
)

type CheckStatus string

const (
	StatusUpToDate        CheckStatus = "up_to_date"
	StatusUpdateAvailable CheckStatus = "update_available"
	StatusCheckFailed     CheckStatus = "check_failed"
)

type CheckResult struct {
	Status  CheckStatus
	Message string
}

// githubRelease is the subset of the GitHub releases API we care about.
type githubRelease struct {
	TagName string `json:"tag_name"`
}

// CheckLatest compares the running version against the latest GitHub release.
// It distinguishes between up-to-date, update available, and check failures.
func CheckLatest(current string) CheckResult {
	switch current {
	case "":
		return checkFailed("Could not check for updates: current version is unknown.")
	case "dev":
		return checkFailed("Could not check for updates: development builds do not map to a release version.")
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubLatestReleaseURL, nil)
	if err != nil {
		return checkFailed("Could not check for updates: could not create the GitHub request.")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := githubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return checkFailed("Could not check for updates: GitHub took too long to respond.")
		}
		return checkFailed(fmt.Sprintf("Could not check for updates: %v.", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return checkFailed(nonOKStatusMessage(resp.Status))
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return checkFailed("Could not check for updates: could not read the GitHub response.")
	}

	latest := normalizeVersion(release.TagName)
	running := normalizeVersion(current)

	if latest == "" {
		return checkFailed("Could not check for updates: GitHub did not return a release version.")
	}

	if latest == running {
		return CheckResult{Status: StatusUpToDate}
	}

	if !isNewer(latest, running) {
		return CheckResult{Status: StatusUpToDate}
	}

	return CheckResult{
		Status: StatusUpdateAvailable,
		Message: fmt.Sprintf(
			"Update available: %s -> %s\nTo update:\n%s",
			running, latest, updateInstructions(),
		),
	}
}

// normalizeVersion strips a leading "v" prefix.
func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// isNewer returns true if latest > current using 4-component semver comparison.
// Components: (major, minor, patch, vivaIteration).
func isNewer(latest, current string) bool {
	latestParts := splitVersion(latest)
	currentParts := splitVersion(current)

	for i := 0; i < 4; i++ {
		if latestParts[i] > currentParts[i] {
			return true
		}
		if latestParts[i] < currentParts[i] {
			return false
		}
	}
	return false
}

// splitVersion parses a version string into a 4-component tuple
// [major, minor, patch, vivaIteration].
//
// The optional fork suffix "-viva.N" is recognised and parsed into the 4th
// component. A version without the suffix has vivaIteration = 0, which means
// legacy releases (e.g. "1.16.4") sort before any viva iteration of the same
// base (e.g. "1.16.4-viva.1"), matching the natural migration order.
//
// Any other prerelease suffix (e.g. "-beta") is silently ignored and yields
// vivaIteration = 0. Returns [0,0,0,0] on a completely unparseable input.
func splitVersion(v string) [4]int {
	var parts [4]int

	// Separate base ("X.Y.Z") from any prerelease suffix ("-...").
	base := v
	vivaIteration := 0
	if idx := strings.Index(v, "-"); idx != -1 {
		base = v[:idx]
		suffix := v[idx+1:] // e.g. "viva.1" or "beta"
		if strings.HasPrefix(suffix, "viva.") {
			iterStr := suffix[len("viva."):]
			for _, c := range iterStr {
				if c >= '0' && c <= '9' {
					vivaIteration = vivaIteration*10 + int(c-'0')
				} else {
					break
				}
			}
		}
	}

	segments := strings.SplitN(base, ".", 3)
	for i, s := range segments {
		if i >= 3 {
			break
		}
		for _, c := range s {
			if c >= '0' && c <= '9' {
				parts[i] = parts[i]*10 + int(c-'0')
			} else {
				break
			}
		}
	}
	parts[3] = vivaIteration
	return parts
}

// updateInstructions returns platform-appropriate update commands.
// All install sources point at the Viva fork (MarioPradEs/engram).
func updateInstructions() string {
	const releasesURL = "https://github.com/MarioPradEs/engram/releases/latest"
	switch runtime.GOOS {
	case "darwin":
		return "  Download the latest release from: " + releasesURL
	case "linux":
		return "  go install github.com/MarioPradEs/engram/cmd/engram@latest\n  or: " + releasesURL
	default:
		return "  go install github.com/MarioPradEs/engram/cmd/engram@latest\n  or: " + releasesURL
	}
}

func githubToken() string {
	if token := strings.TrimSpace(os.Getenv("GH_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

func nonOKStatusMessage(status string) string {
	msg := fmt.Sprintf("Could not check for updates: GitHub API returned %s.", status)
	if strings.HasPrefix(status, "401") || strings.HasPrefix(status, "403") {
		msg += " Set GH_TOKEN or GITHUB_TOKEN to reduce rate limits."
	}
	return msg
}

func checkFailed(message string) CheckResult {
	return CheckResult{Status: StatusCheckFailed, Message: message}
}
