package users_test

import (
	"os"
	"path/filepath"
	"testing"

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
    department: "engineering"
    role: "admin"
    enrolled:
      - "eng-notes"
    status: "active"
  - email: "bob@vivastudios.com"
    name: "Bob"
    department: "product"
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
	if alice.Department != "engineering" {
		t.Errorf("alice.Department = %q, want engineering", alice.Department)
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
    department: "engineering"
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
    department: "engineering"
    role: "admin"
    status: "active"
  - email: "alice@vivastudios.com"
    name: "Alice Duplicate"
    department: "product"
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
    department: "product"
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
    department: "engineering"
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
    department: "engineering"
    role: "admin"
    status: "active"
  - email: "bob@vivastudios.com"
    name: "Bob"
    department: "product"
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

// TestReloadInvalidRetainsLastGood verifies that a failed Reload() keeps the
// last valid state (last-good retention).
func TestReloadInvalidRetainsLastGood(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeYAML(t, dir, "users.yaml", `
users:
  - email: "alice@vivastudios.com"
    name: "Alice"
    department: "engineering"
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
    department: "engineering"
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
