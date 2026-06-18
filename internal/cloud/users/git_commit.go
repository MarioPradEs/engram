package users

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
)

// RunLocalGitCommit stages the users.yaml file (by basename) and creates a
// local git commit inside the repository at repoPath.
//
// This is a non-fatal operation per the spec (§atomic-write step 4): if the
// commit fails for any reason (no identity configured, not a git repo, etc.),
// the error is logged and returned so the caller can log it — but the reload
// step still fires regardless of the return value.
//
// repoPath must be the directory that contains the .git directory (typically
// the directory that contains users.yaml — i.e. the same directory WriteAtomic
// wrote to).
func RunLocalGitCommit(repoPath, usersFile, message string) error {
	// Stage only the users.yaml file (by basename relative to repoPath).
	base := filepath.Base(usersFile)
	addCmd := exec.Command("git", "-C", repoPath, "add", base) // #nosec G204
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("users: git add %q in %q: %w — output: %s", base, repoPath, err, strings.TrimSpace(string(out)))
	}

	commitCmd := exec.Command("git", "-C", repoPath, "commit", "-m", message) // #nosec G204
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("users: git commit in %q: %w — output: %s", repoPath, err, strings.TrimSpace(string(out)))
	}
	log.Printf("[engram-cloud] users.yaml committed in %s: %s", repoPath, message)
	return nil
}
