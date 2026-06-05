package main

import (
	"os"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud"
)

// TestNewCloudRuntimeUsersFilePassedToConfig verifies that when ENGRAM_USERS_FILE
// is set, cloud.ConfigFromEnv returns a config with UsersFile populated.
// This is the wiring test for task 2.5 (HeaderAuthenticator construction).
func TestNewCloudRuntimeUsersFilePassedToConfig(t *testing.T) {
	dir := t.TempDir()
	usersYAML := dir + "/users.yaml"
	yaml := `users:
  - email: alice@vivastudios.com
    name: Alice
    department: dev
    role: admin
    status: active
    enrolled:
      - general
`
	if err := os.WriteFile(usersYAML, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write users yaml: %v", err)
	}

	t.Setenv("ENGRAM_USERS_FILE", usersYAML)
	cfg := cloud.ConfigFromEnv()
	if cfg.UsersFile != usersYAML {
		t.Errorf("expected UsersFile=%q, got %q", usersYAML, cfg.UsersFile)
	}
}

// TestNewCloudRuntimeHeaderAuthWiresWhenUsersFileSet verifies that newCloudRuntime
// loads the YAMLLoader + HeaderAuthenticator when UsersFile is set. We use a
// stub runtime to capture the config path rather than starting a real Postgres server.
func TestNewCloudRuntimeHeaderAuthWiresWhenUsersFileSet(t *testing.T) {
	if os.Getenv("CLOUDSTORE_TEST_DSN") == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}

	dir := t.TempDir()
	usersYAML := dir + "/users.yaml"
	yaml := `users:
  - email: admin@vivastudios.com
    name: Admin User
    department: dev
    role: admin
    status: active
    enrolled:
      - general
`
	if err := os.WriteFile(usersYAML, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write users yaml: %v", err)
	}

	t.Setenv("ENGRAM_USERS_FILE", usersYAML)
	t.Setenv("ENGRAM_DATABASE_URL", os.Getenv("CLOUDSTORE_TEST_DSN"))
	cfg := cloud.ConfigFromEnv()

	runtime, err := newCloudRuntime(cfg)
	if err != nil {
		t.Fatalf("newCloudRuntime with UsersFile: %v", err)
	}
	if runtime == nil {
		t.Fatal("expected non-nil runtime")
	}

	// Verify the runtime has SIGHUP handler wired (onSIGHUP != nil).
	dr, ok := runtime.(*defaultCloudRuntime)
	if !ok {
		t.Fatalf("expected *defaultCloudRuntime, got %T", runtime)
	}
	if dr.onSIGHUP == nil {
		t.Error("expected onSIGHUP to be set when UsersFile is configured")
	}

	// Clean up.
	if dr.store != nil {
		dr.store.Close()
	}
}
