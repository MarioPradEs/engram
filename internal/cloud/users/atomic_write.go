package users

import (
	"fmt"
	"os"
)

// WriteAtomic writes data to a temporary file in the same directory as path,
// runs validator(tmpPath), and — only on success — atomically renames the temp
// file to path. On any failure, the temp file is removed and path is left
// unchanged (atomic write guarantee per spec §atomic-write).
//
// validator receives the path of the temp file (so it can parse/validate the
// content before the rename commits it). The typical caller passes
// users.loadAndValidate as the validator so that a corrupt write never replaces
// a valid users.yaml.
//
// If the rename itself fails after a successful validation, the error is
// returned and the temp file is cleaned up; path retains its pre-call state.
func WriteAtomic(path string, data []byte, validator func(tmpPath string) error) error {
	// Create temp file in the same directory so os.Rename is an atomic
	// same-filesystem operation (no cross-device move risk).
	tmp, err := os.CreateTemp(sameDir(path), "users.*.tmp")
	if err != nil {
		return fmt.Errorf("users: WriteAtomic: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	// Guarantee cleanup on any failure path.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("users: WriteAtomic: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("users: WriteAtomic: close temp: %w", err)
	}

	// Validate before committing — never rename an invalid file.
	if err := validator(tmpPath); err != nil {
		return fmt.Errorf("users: WriteAtomic: validation failed: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("users: WriteAtomic: rename: %w", err)
	}
	committed = true
	return nil
}

// sameDir returns the directory component of p.
func sameDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
