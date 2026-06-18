package classrules

// write_reload_nonfatal_test.go — RED test for W2 bug fix.
//
// W2: WriteGames returns an error when the atomic rename succeeded but the
// in-process reload fails. The users.yaml path (users.WriteAtomic) treats reload
// failure as non-fatal: it logs and returns success because the file is already
// correct on disk. WriteGames must be consistent: after a successful rename, a
// reload failure is non-fatal (the file IS correct). Only genuine write/validate
// failures before the rename should be errors.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// failReloader is a Reloader that always returns an error from Reload.
// Used to simulate a reload failure after a successful atomic write.
type failReloader struct {
	err error
}

func (f *failReloader) Reload() error {
	return f.err
}

// TestW2_WriteGames_ReloadFailureAfterRenameIsNonFatal verifies that WriteGames
// returns nil (success) when the file is written and renamed correctly but the
// in-process reload fails.
//
// Before the W2 fix, WriteGames wraps the reload error and returns it:
//   return fmt.Errorf("classrules: WriteGames: reload after write: %w", err)
//
// After the fix, reload failure is logged (or noted) and nil is returned —
// matching the users path's non-fatal reload behavior.
func TestW2_WriteGames_ReloadFailureAfterRenameIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o644); err != nil {
		t.Fatalf("setup: write base yaml: %v", err)
	}

	reloadErr := errors.New("simulated in-process reload failure")
	badReloader := &failReloader{err: reloadErr}

	// Call WriteGames with a valid games list but a failing Reloader.
	err := WriteGames(path, badReloader, []string{"game-a", "game-b"})

	// BEFORE THE FIX: err != nil (WriteGames propagates the reload error).
	// AFTER THE FIX:  err == nil (reload failure is non-fatal after successful rename).
	if err != nil {
		t.Errorf("W2 BUG: WriteGames returned error after reload failure (fix needed): %v\n"+
			"Expected nil — file is on disk correctly; reload failure must be non-fatal.", err)
	}

	// Additionally verify the file WAS actually written correctly despite the reload failure.
	cfg, parseErr := LoadFromFile(path)
	if parseErr != nil {
		t.Fatalf("W2: LoadFromFile after WriteGames: %v", parseErr)
	}
	if cfg == nil {
		t.Fatal("W2: LoadFromFile returned nil config after WriteGames")
	}
	if len(cfg.Games) != 2 {
		t.Errorf("W2: expected 2 games on disk after write, got %d: %v", len(cfg.Games), cfg.Games)
	}
}

// TestW2_WriteGames_GenuineWriteFailureIsStillAnError verifies that failures
// BEFORE the rename (e.g. validation) are still returned as errors.
// This ensures the fix only changes reload-failure behavior, not pre-rename failures.
func TestW2_WriteGames_GenuineWriteFailureIsStillAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte(baseYAML), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	badReloader := &failReloader{err: errors.New("would be reload error if we got this far")}

	// Empty games list → validation fails before any file I/O.
	err := WriteGames(path, badReloader, []string{})
	if err == nil {
		t.Error("W2: expected error for empty games list (pre-rename validation), got nil")
	}
}
