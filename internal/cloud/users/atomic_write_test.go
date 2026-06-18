package users_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// TestWriteAtomic_ValidData_WritesAndCallsValidator verifies that valid data is
// written to the target path and the validator is called with the temp file path.
// RED: references WriteAtomic which does not exist yet.
func TestWriteAtomic_ValidData_WritesAndCallsValidator(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "users.yaml")
	data := []byte("users: []\n")

	var validatorCalledWith string
	validator := func(tmpPath string) error {
		validatorCalledWith = tmpPath
		return nil
	}

	if err := users.WriteAtomic(target, data, validator); err != nil {
		t.Fatalf("WriteAtomic returned unexpected error: %v", err)
	}

	// Target file must exist and contain the written data.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", target, err)
	}
	if string(got) != string(data) {
		t.Errorf("target content = %q, want %q", got, data)
	}

	// Validator must have been called with a temp file path (not the target).
	if validatorCalledWith == "" {
		t.Error("validator was not called")
	}
	if validatorCalledWith == target {
		t.Errorf("validator was called with target path %q; expected a temp path", target)
	}
}

// TestWriteAtomic_ValidatorRejectsData_OriginalUntouched verifies that when the
// validator returns an error, the target file is not created or modified.
func TestWriteAtomic_ValidatorRejectsData_OriginalUntouched(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "users.yaml")
	original := []byte("original content\n")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	validationErr := errors.New("invalid data")
	validator := func(_ string) error {
		return validationErr
	}

	err := users.WriteAtomic(target, []byte("new content\n"), validator)
	if err == nil {
		t.Fatal("WriteAtomic should have returned an error when validator fails")
	}
	if !errors.Is(err, validationErr) {
		t.Errorf("WriteAtomic error = %v, want wrapping %v", err, validationErr)
	}

	// Original file must be unchanged.
	got, err2 := os.ReadFile(target)
	if err2 != nil {
		t.Fatalf("ReadFile: %v", err2)
	}
	if string(got) != string(original) {
		t.Errorf("target was modified: got %q, want %q", got, original)
	}
}

// TestWriteAtomic_TempFileCleanedUpOnValidationFailure verifies that no residual
// temp file is left in the directory when the validator rejects the data.
func TestWriteAtomic_TempFileCleanedUpOnValidationFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "users.yaml")
	// Write original so the target exists before the failed write.
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	validator := func(_ string) error { return errors.New("reject") }
	_ = users.WriteAtomic(target, []byte("bad\n"), validator)

	// Only "users.yaml" should exist in the directory — no .tmp file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "users.yaml" {
			t.Errorf("unexpected file left in directory: %q", e.Name())
		}
	}
}
