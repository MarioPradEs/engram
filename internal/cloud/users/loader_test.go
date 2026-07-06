package users_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// writeYAML writes content to a temp file and returns its path.
func writeYAML(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatalf("writeYAML: %v", err)
	}
	return p
}

// TestLoadValidYAML verifies a well-formed users.yaml loads correctly.
func TestLoadValidYAML(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", `
users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "dev"
    role: "admin"
    enrolled:
      - "eng-notes"
    status: "active"
  - email: "bob@vivastudios.com"
    name: "Bob"
    department: "qa"
    role: "member"
    enrolled:
      - "eng-notes"
    status: "active"
`)

	loader, err := users.NewYAMLLoader(path)
	if err != nil {
		t.Fatalf("NewYAMLLoader: %v", err)
	}

	alice, ok := loader.Lookup("alice@vivastudios.com")
	if !ok {
		t.Fatal("expected alice to be found")
	}
	if alice.Name != "Alice" {
		t.Errorf("alice.Name = %q, want Alice", alice.Name)
	}
	if alice.Department != "dev" {
		t.Errorf("alice.Department = %q, want dev", alice.Department)
	}
	if alice.Role != "admin" {
		t.Errorf("alice.Role = %q, want admin", alice.Role)
	}
	if alice.Status != "active" {
		t.Errorf("alice.Status = %q, want active", alice.Status)
	}

	_, ok = loader.Lookup("unknown@vivastudios.com")
	if ok {
		t.Error("expected unknown email not to be found")
	}
}

// TestLoadRejectsNonVivastudiosEmail verifies that users with emails not ending
// in @vivastudios.com are rejected at load time (task 1.10, W4).
func TestLoadRejectsNonVivastudiosEmail(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		email string
	}{
		{"gmail", "alice@gmail.com"},
		{"external company", "alice@othercorp.com"},
		{"bare domain", "alice@vivastudios"},
		{"subdomain", "alice@sub.vivastudios.com"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := writeYAML(t, dir, "users.yaml", `
users:
  - email: "`+tc.email+`"
    name: "Alice"
    department: "dev"
    role: "admin"
    status: "active"
`)
			_, err := users.NewYAMLLoader(path)
			if err == nil {
				t.Errorf("expected error for non-vivastudios email %q, got nil", tc.email)
			}
		})
	}
}

// TestLoadInvalidDepartment verifies that an invalid department enum is rejected.
func TestLoadInvalidDepartment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", `
users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "skunkworks"
    role: "admin"
    status: "active"
`)

	_, err := users.NewYAMLLoader(path)
	if err == nil {
		t.Fatal("expected error for invalid department, got nil")
	}
}

// TestLoadDuplicateEmail verifies that duplicate emails are rejected.
func TestLoadDuplicateEmail(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", `
users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "dev"
    role: "admin"
    status: "active"
  - email: "alice@vivastudios.com"
    name: "Alice Duplicate"
    department: "qa"
    role: "member"
    status: "active"
`)

	_, err := users.NewYAMLLoader(path)
	if err == nil {
		t.Fatal("expected error for duplicate email, got nil")
	}
}

// TestLoadNoAdmin verifies that a user list without at least one admin is rejected.
func TestLoadNoAdmin(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", `
users:
  - email: "bob@vivastudios.com"
    name: "Bob"
    department: "qa"
    role: "member"
    status: "active"
`)

	_, err := users.NewYAMLLoader(path)
	if err == nil {
		t.Fatal("expected error for no admin user, got nil")
	}
}

// TestReloadUpdatesDirectory verifies that Reload() picks up a modified YAML.
func TestReloadUpdatesDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", `
users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "dev"
    role: "admin"
    status: "active"
`)

	loader, err := users.NewYAMLLoader(path)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}

	_, ok := loader.Lookup("bob@vivastudios.com")
	if ok {
		t.Fatal("bob should not exist before reload")
	}

	// Overwrite with new content including bob.
	writeYAML(t, dir, "users.yaml", `
users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "dev"
    role: "admin"
    status: "active"
  - email: "bob@vivastudios.com"
    name: "Bob"
    department: "qa"
    role: "member"
    status: "active"
`)

	if err := loader.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	_, ok = loader.Lookup("bob@vivastudios.com")
	if !ok {
		t.Error("bob should exist after reload")
	}
}

// ─── Watch helpers ───────────────────────────────────────────────────────────

// watcherBaseYAML is a minimal valid users.yaml used as the initial state for
// watcher tests (alice admin only; no bob).
const watcherBaseYAML = `users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "dev"
    role: "admin"
    status: "active"
    enrolled:
      - "eng-notes"
`

// watcherYAMLPlusBob extends watcherBaseYAML by adding bob as an active member.
const watcherYAMLPlusBob = `users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "dev"
    role: "admin"
    status: "active"
    enrolled:
      - "eng-notes"
  - email: "bob@vivastudios.com"
    name: "Bob"
    department: "qa"
    role: "member"
    status: "active"
    enrolled:
      - "eng-notes"
`

// pollLookup polls loader.Lookup(email) until the email is found or the
// timeout elapses. Returns true when found before the deadline.
func pollLookup(loader *users.YAMLLoader, email string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := loader.Lookup(email); ok {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// pollLookupAbsent polls loader.Lookup(email) and returns true only when the
// email remains absent for the entire duration (no premature hit).
func pollLookupAbsent(loader *users.YAMLLoader, email string, duration time.Duration) bool {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if _, ok := loader.Lookup(email); ok {
			return false // unexpectedly appeared
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// ─── Watch tests ─────────────────────────────────────────────────────────────

// TestWatcherTriggersReload_DirectWrite verifies that a direct os.WriteFile
// to the watched users.yaml is detected by Watch and causes Reload, making
// newly-added users visible via Lookup.
func TestWatcherTriggersReload_DirectWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", watcherBaseYAML)

	loader, err := users.NewYAMLLoader(path)
	if err != nil {
		t.Fatalf("NewYAMLLoader: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- loader.Watch(ctx) }()

	// Allow the watcher goroutine to initialise and start listening.
	time.Sleep(100 * time.Millisecond)

	// Direct write — no atomic rename.
	writeYAML(t, dir, "users.yaml", watcherYAMLPlusBob)

	// Poll for bob; generous timeout covers debounce (100ms) + CI latency.
	if !pollLookup(loader, "bob@vivastudios.com", 3*time.Second) {
		t.Error("bob not found after direct write — Watch did not trigger Reload")
	}
}

// TestWatcherTriggersReload_AtomicRename verifies that an atomic os.Rename
// (write tmp then rename over users.yaml, mirroring WriteAtomic / admin
// dashboard writes) is detected by Watch and causes Reload.
func TestWatcherTriggersReload_AtomicRename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", watcherBaseYAML)

	loader, err := users.NewYAMLLoader(path)
	if err != nil {
		t.Fatalf("NewYAMLLoader: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- loader.Watch(ctx) }()

	// Allow the watcher goroutine to initialise and start listening.
	time.Sleep(100 * time.Millisecond)

	// Atomic rename: write to a temp file in the same directory, then rename
	// over users.yaml. This mirrors WriteAtomic used by the admin dashboard.
	tmpPath := filepath.Join(dir, "users.tmp.yaml")
	if err := os.WriteFile(tmpPath, []byte(watcherYAMLPlusBob), 0o600); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatalf("rename tmp → users.yaml: %v", err)
	}

	// Poll for bob.
	if !pollLookup(loader, "bob@vivastudios.com", 3*time.Second) {
		t.Error("bob not found after atomic rename — Watch did not trigger Reload")
	}
}

// TestWatcherIgnoresUnrelated verifies that writing a sibling file in the same
// watched directory does not corrupt the loader state, and that the watcher
// remains alive and functional for subsequent users.yaml changes.
func TestWatcherIgnoresUnrelated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", watcherBaseYAML) // alice only; no bob

	loader, err := users.NewYAMLLoader(path)
	if err != nil {
		t.Fatalf("NewYAMLLoader: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchErr := make(chan error, 1)
	go func() { watchErr <- loader.Watch(ctx) }()

	// Allow the watcher goroutine to initialise.
	time.Sleep(100 * time.Millisecond)

	// Write a sibling file — must NOT trigger a reload that introduces bob.
	sibling := filepath.Join(dir, "other.yaml")
	if err := os.WriteFile(sibling, []byte("unrelated: content\n"), 0o600); err != nil {
		t.Fatalf("write sibling file: %v", err)
	}

	// Bob must not appear during the absence window (users.yaml was not updated).
	if !pollLookupAbsent(loader, "bob@vivastudios.com", 500*time.Millisecond) {
		t.Error("bob appeared after sibling write — Watch incorrectly fired on unrelated file")
	}

	// Confirm the watcher is still alive: write users.yaml with bob and verify.
	writeYAML(t, dir, "users.yaml", watcherYAMLPlusBob)
	if !pollLookup(loader, "bob@vivastudios.com", 3*time.Second) {
		t.Error("bob not found after users.yaml update — watcher no longer alive after sibling write")
	}

	_ = watchErr // goroutine exits via ctx cancel (deferred above)
}

// ─── original tests continue ──────────────────────────────────────────────────

// TestReloadInvalidRetainsLastGood verifies that a failed Reload() keeps the
// last valid state (last-good retention).
func TestReloadInvalidRetainsLastGood(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", `
users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "dev"
    role: "admin"
    status: "active"
`)

	loader, err := users.NewYAMLLoader(path)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}

	// Replace file with invalid YAML.
	writeYAML(t, dir, "users.yaml", `
users:
  - email: "bad-no-admin@vivastudios.com"
    name: "BadUser"
    department: "dev"
    role: "member"
    status: "active"
`)

	err = loader.Reload()
	if err == nil {
		t.Fatal("expected error from invalid YAML (no admin), got nil")
	}

	// Alice should still be visible from last-good state.
	_, ok := loader.Lookup("alice@vivastudios.com")
	if !ok {
		t.Error("alice should still be available after failed reload (last-good retention)")
	}
}
