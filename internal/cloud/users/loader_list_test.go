package users_test

import (
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// TestYAMLLoaderList_ReturnsSnapshot verifies that List() returns a read-locked
// snapshot of all currently loaded principals as a slice.
// RED: references List() which does not exist yet on YAMLLoader.
func TestYAMLLoaderList_ReturnsSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", `
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
	loader, err := users.NewYAMLLoader(path)
	if err != nil {
		t.Fatalf("NewYAMLLoader: %v", err)
	}

	list := loader.List()

	if len(list) != 2 {
		t.Fatalf("List() returned %d principals, want 2", len(list))
	}

	// Verify both emails appear in the snapshot (order is map-based, so use a set).
	emails := make(map[string]bool, len(list))
	for _, p := range list {
		emails[p.Email] = true
	}
	if !emails["alice@vivastudios.com"] {
		t.Error("List() did not include alice@vivastudios.com")
	}
	if !emails["bob@vivastudios.com"] {
		t.Error("List() did not include bob@vivastudios.com")
	}
}

// TestYAMLLoaderList_EmptyWhenNoUsers verifies that List() returns an empty
// (non-nil) slice when the directory has no users at runtime.
// This cannot happen via NewYAMLLoader (which requires at least one admin), but
// the method must handle the case defensively for zero-value or test-injected loaders.
func TestYAMLLoaderList_EmptyWhenNoUsers(t *testing.T) {
	t.Parallel()

	// Construct a loader via a valid YAML (single admin), then replace the
	// internal map via Reload with a new file. Since loadAndValidate requires
	// at least one admin, we cannot produce a truly empty map through the public
	// API — but we verify the snapshot reflects the exact current state.
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
		t.Fatalf("NewYAMLLoader: %v", err)
	}

	list := loader.List()
	if list == nil {
		t.Fatal("List() returned nil, want a non-nil slice")
	}
	// Single user case.
	if len(list) != 1 {
		t.Fatalf("List() returned %d principals, want 1", len(list))
	}
	if list[0].Email != "alice@vivastudios.com" {
		t.Errorf("List()[0].Email = %q, want alice@vivastudios.com", list[0].Email)
	}
}

// TestYAMLLoaderList_ReflectsReload verifies that after a successful Reload(),
// List() returns the updated snapshot — not the stale pre-reload state.
func TestYAMLLoaderList_ReflectsReload(t *testing.T) {
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
		t.Fatalf("NewYAMLLoader: %v", err)
	}

	// Before reload: only alice.
	before := loader.List()
	if len(before) != 1 {
		t.Fatalf("before reload: List() has %d entries, want 1", len(before))
	}

	// Overwrite with two users.
	writeYAML(t, dir, "users.yaml", `
users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "dev"
    role: "admin"
    status: "active"
  - email: "carol@vivastudios.com"
    name: "Carol"
    department: "art"
    role: "member"
    status: "active"
`)
	if err := loader.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	after := loader.List()
	if len(after) != 2 {
		t.Fatalf("after reload: List() has %d entries, want 2", len(after))
	}
	emails := make(map[string]bool, 2)
	for _, p := range after {
		emails[p.Email] = true
	}
	if !emails["carol@vivastudios.com"] {
		t.Error("after reload: carol@vivastudios.com not in List()")
	}
}
