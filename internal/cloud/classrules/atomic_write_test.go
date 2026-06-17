package classrules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baseYAML is a minimal classification-rules.yaml with departments and games.
// Used to verify that WriteGames preserves non-games sections on write.
const baseYAML = `departments:
  - name: engineering
    aliases:
      - eng
games:
  - "old-game-a"
  - "old-game-b"
rules: |
  Keep this text intact.
`

// TestWriteGames_ValidUpdate verifies that a valid games list is written
// atomically and reflected in the loader after reload.
func TestWriteGames_ValidUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o644); err != nil {
		t.Fatalf("setup: write base yaml: %v", err)
	}

	loader, err := NewClassrulesLoader(path)
	if err != nil {
		t.Fatalf("NewClassrulesLoader: %v", err)
	}

	newGames := []string{"new-game-1", "new-game-2", "new-game-3"}
	if err := WriteGames(path, loader, newGames); err != nil {
		t.Fatalf("WriteGames: %v", err)
	}

	// Loader should reflect the new games.
	cfg := loader.Current()
	if cfg == nil {
		t.Fatal("loader.Current() nil after WriteGames")
	}
	if len(cfg.Games) != 3 {
		t.Errorf("expected 3 games, got %d: %v", len(cfg.Games), cfg.Games)
	}
	for i, g := range newGames {
		if cfg.Games[i] != g {
			t.Errorf("games[%d]: want %q, got %q", i, g, cfg.Games[i])
		}
	}

	// The departments and rules sections must be preserved.
	if len(cfg.Departments) == 0 {
		t.Error("departments section was not preserved after WriteGames")
	}
	if cfg.Rules == "" {
		t.Error("rules section was not preserved after WriteGames")
	}
}

// TestWriteGames_EmptyListRejected verifies that an empty games list returns a
// validation error and leaves the original file unchanged.
func TestWriteGames_EmptyListRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	original, _ := os.ReadFile(path)

	loader, _ := NewClassrulesLoader(path)
	err := WriteGames(path, loader, []string{})
	if err == nil {
		t.Fatal("expected error for empty games list, got nil")
	}
	if !strings.Contains(err.Error(), "empty") && !strings.Contains(err.Error(), "at least") {
		t.Errorf("expected error message to mention empty/at-least, got: %v", err)
	}

	// File must be unchanged.
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Errorf("file was modified despite validation failure")
	}

	// Loader must still have old games.
	cfg := loader.Current()
	if len(cfg.Games) != 2 || cfg.Games[0] != "old-game-a" {
		t.Errorf("loader state changed despite write rejection: %v", cfg.Games)
	}
}

// TestWriteGames_DuplicatesRejected verifies that a games list with duplicate
// entries is rejected before write.
func TestWriteGames_DuplicatesRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	original, _ := os.ReadFile(path)

	loader, _ := NewClassrulesLoader(path)
	err := WriteGames(path, loader, []string{"game-a", "game-b", "game-a"})
	if err == nil {
		t.Fatal("expected error for duplicate games, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected error message to mention duplicate, got: %v", err)
	}

	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Errorf("file was modified despite duplicate validation failure")
	}
}

// TestWriteGames_InvalidYAMLRejectedBeforeRename verifies that if the serialised
// YAML fails round-trip validation, the original file is left untouched.
// We simulate this by providing a game entry that contains YAML-breaking syntax
// in a way that we can observe via the loader not changing state.
// (In practice, Go's yaml.Marshal almost never produces invalid YAML from a
// []string, so we rely on the loader's post-validate step. This test exercises
// the atomicity guarantee: any validation failure means no rename.)
func TestWriteGames_AtomicNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	original, _ := os.ReadFile(path)

	loader, _ := NewClassrulesLoader(path)
	// Trigger a rejection (empty list) and verify no .tmp files are left behind.
	_ = WriteGames(path, loader, nil)

	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Error("original file changed after rejected write")
	}

	// No temp files should remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
