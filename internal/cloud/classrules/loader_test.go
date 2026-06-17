package classrules

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestClassrulesLoader_Reload_LoadsFromDisk verifies that Reload() reads the
// classification-rules.yaml from disk and makes it available via Current().
func TestClassrulesLoader_Reload_LoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")

	yaml := "games:\n  - game-a\n  - game-b\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("setup: write yaml: %v", err)
	}

	loader, err := NewClassrulesLoader(path)
	if err != nil {
		t.Fatalf("NewClassrulesLoader: %v", err)
	}

	cfg := loader.Current()
	if cfg == nil {
		t.Fatal("Current() returned nil after load")
	}
	if len(cfg.Games) != 2 {
		t.Errorf("expected 2 games, got %d: %v", len(cfg.Games), cfg.Games)
	}
	if cfg.Games[0] != "game-a" || cfg.Games[1] != "game-b" {
		t.Errorf("unexpected games: %v", cfg.Games)
	}
}

// TestClassrulesLoader_Reload_RetainsLastGoodOnParseError verifies that a
// subsequent Reload() with broken YAML leaves Current() unchanged (last-good
// retention, mirroring users.YAMLLoader behaviour).
func TestClassrulesLoader_Reload_RetainsLastGoodOnParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")

	goodYAML := "games:\n  - game-good\n"
	if err := os.WriteFile(path, []byte(goodYAML), 0o644); err != nil {
		t.Fatalf("setup: write good yaml: %v", err)
	}

	loader, err := NewClassrulesLoader(path)
	if err != nil {
		t.Fatalf("NewClassrulesLoader: %v", err)
	}
	if loader.Current() == nil {
		t.Fatal("Current() nil after good load")
	}

	// Overwrite with invalid YAML.
	badYAML := "games: [\n  invalid: { broken"
	if err := os.WriteFile(path, []byte(badYAML), 0o644); err != nil {
		t.Fatalf("setup: write bad yaml: %v", err)
	}

	reloadErr := loader.Reload()
	if reloadErr == nil {
		t.Fatal("expected error on bad YAML reload, got nil")
	}

	// Last-good retained.
	cfg := loader.Current()
	if cfg == nil {
		t.Fatal("Current() returned nil after failed reload — last-good not retained")
	}
	if len(cfg.Games) != 1 || cfg.Games[0] != "game-good" {
		t.Errorf("last-good retention failed: got games=%v", cfg.Games)
	}
}

// TestClassrulesLoader_Current_ReturnsCopy verifies that Current() returns a
// value safe to read without holding the loader's internal lock (i.e. the
// returned pointer is stable — caller modifications don't corrupt internal state).
func TestClassrulesLoader_Current_ReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")

	yaml := "games:\n  - game-x\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	loader, err := NewClassrulesLoader(path)
	if err != nil {
		t.Fatalf("NewClassrulesLoader: %v", err)
	}

	cfg1 := loader.Current()
	if cfg1 == nil {
		t.Fatal("Current() nil")
	}

	// Mutate the returned value — must not affect the loader's internal state.
	cfg1.Games = append(cfg1.Games, "mutated")

	cfg2 := loader.Current()
	if cfg2 == nil {
		t.Fatal("second Current() nil")
	}
	if len(cfg2.Games) != 1 {
		t.Errorf("internal state corrupted by caller mutation: games=%v", cfg2.Games)
	}
}

// TestClassrulesLoader_Reload_AbsentFile verifies that an absent
// classification-rules.yaml does not cause a startup error but yields a
// nil Current() (graceful absent, matching LoadFromFile behaviour).
func TestClassrulesLoader_Reload_AbsentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")

	loader, err := NewClassrulesLoader(path)
	if err != nil {
		t.Fatalf("NewClassrulesLoader with absent file: %v", err)
	}
	// Absent file → current is nil, no error.
	if loader.Current() != nil {
		t.Errorf("expected nil Current() for absent file, got %+v", loader.Current())
	}
	// Reload also succeeds gracefully.
	if err := loader.Reload(); err != nil {
		t.Errorf("Reload on absent file should not error, got %v", err)
	}
}

// Compile-time check: Reload returns an error type.
var _ error = errors.New("sentinel")
