package triage_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/triage"
)

// TestWriteProjectDefaultScope_AtomicRoundTrip verifies that writing a default
// scope value to config.json round-trips correctly and is atomic (temp+rename).
func TestWriteProjectDefaultScope_AtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	engDir := filepath.Join(dir, ".engram")
	if err := os.MkdirAll(engDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write initial config.json with project_name only.
	initial := map[string]string{"project_name": "myproject"}
	data, _ := json.Marshal(initial)
	if err := os.WriteFile(filepath.Join(engDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Write shared scope.
	if err := triage.WriteProjectDefaultScope(dir, "shared"); err != nil {
		t.Fatalf("WriteProjectDefaultScope shared: %v", err)
	}

	// Read back and verify.
	got, err := os.ReadFile(filepath.Join(engDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var scope string
	if err := json.Unmarshal(cfg["default_scope"], &scope); err != nil {
		t.Fatalf("unmarshal default_scope: %v", err)
	}
	if scope != "shared" {
		t.Errorf("want default_scope=shared, got %q", scope)
	}

	// project_name must be preserved.
	var projName string
	if err := json.Unmarshal(cfg["project_name"], &projName); err != nil {
		t.Fatalf("unmarshal project_name: %v", err)
	}
	if projName != "myproject" {
		t.Errorf("want project_name=myproject, got %q", projName)
	}
}

// TestWriteProjectDefaultScope_CreatesConfig verifies that writing when no
// config.json exists creates the file (does not error).
func TestWriteProjectDefaultScope_CreatesConfig(t *testing.T) {
	dir := t.TempDir()
	engDir := filepath.Join(dir, ".engram")
	if err := os.MkdirAll(engDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No config.json — should create it.
	if err := triage.WriteProjectDefaultScope(dir, "personal"); err != nil {
		t.Fatalf("WriteProjectDefaultScope: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(engDir, "config.json"))
	if err != nil {
		t.Fatalf("config.json not created: %v", err)
	}
	var cfg map[string]string
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg["default_scope"] != "personal" {
		t.Errorf("want default_scope=personal, got %q", cfg["default_scope"])
	}
}

// TestWriteProjectDefaultScope_PreservesExistingFields verifies that all
// existing config.json fields are preserved after a write.
func TestWriteProjectDefaultScope_PreservesExistingFields(t *testing.T) {
	dir := t.TempDir()
	engDir := filepath.Join(dir, ".engram")
	if err := os.MkdirAll(engDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Config with extra fields we must not drop.
	initial := `{"project_name":"proj","some_other_field":"keep-me","default_scope":"personal"}`
	if err := os.WriteFile(filepath.Join(engDir, "config.json"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := triage.WriteProjectDefaultScope(dir, "shared"); err != nil {
		t.Fatalf("WriteProjectDefaultScope: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(engDir, "config.json"))
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := cfg["some_other_field"]; !ok {
		t.Error("want some_other_field preserved, but it was dropped")
	}
	if _, ok := cfg["project_name"]; !ok {
		t.Error("want project_name preserved")
	}
	var scope string
	_ = json.Unmarshal(cfg["default_scope"], &scope)
	if scope != "shared" {
		t.Errorf("want default_scope=shared, got %q", scope)
	}
}

// TestWriteProjectDefaultScope_TempFileInSameDir verifies that the temp file
// is created in the same directory as config.json so os.Rename is atomic
// (same filesystem). We verify by confirming the .engram directory has no
// leftover temp files after a successful write.
func TestWriteProjectDefaultScope_TempFileInSameDir(t *testing.T) {
	dir := t.TempDir()
	engDir := filepath.Join(dir, ".engram")
	if err := os.MkdirAll(engDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := triage.WriteProjectDefaultScope(dir, "shared"); err != nil {
		t.Fatalf("WriteProjectDefaultScope: %v", err)
	}

	// After success, the only file in engDir should be config.json (no temp residue).
	entries, err := os.ReadDir(engDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("unexpected file in .engram dir after write: %q", e.Name())
		}
	}
}

// TestWriteProjectDefaultScope_OverwritesScope verifies that calling Write
// twice updates the scope rather than duplicating the key.
func TestWriteProjectDefaultScope_OverwritesScope(t *testing.T) {
	dir := t.TempDir()
	engDir := filepath.Join(dir, ".engram")
	if err := os.MkdirAll(engDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := triage.WriteProjectDefaultScope(dir, "shared"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := triage.WriteProjectDefaultScope(dir, "personal"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(engDir, "config.json"))
	var cfg map[string]json.RawMessage
	_ = json.Unmarshal(data, &cfg)
	var scope string
	_ = json.Unmarshal(cfg["default_scope"], &scope)
	if scope != "personal" {
		t.Errorf("want default_scope=personal after second write, got %q", scope)
	}
}
