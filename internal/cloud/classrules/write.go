package classrules

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Reloader is implemented by *ClassrulesLoader (and by the dashboard bridge).
// WriteGames uses this interface so callers don't need to import the concrete type.
type Reloader interface {
	Reload() error
}

// WriteGames atomically writes an updated games list to the classification-rules.yaml
// at path. It preserves all other sections (departments, conventions, project_patterns,
// rules) unchanged.
//
// The write sequence (D6):
//  1. Validate: non-nil, non-empty list; no duplicate or blank entries.
//  2. Load current Config so we can preserve non-games fields.
//  3. Merge: replace cfg.Games with newGames.
//  4. Marshal to YAML bytes.
//  5. Write to a temp file in the same directory.
//  6. Validate the temp file by calling LoadFromFile(tmp) — a bad marshal must never
//     corrupt the running config.
//  7. os.Rename(tmp → path): atomic on a same-filesystem mount.
//  8. loader.Reload(): update the cloud server's in-memory Config.
//
// On any validation failure (steps 1–6) the original file is left untouched (atomic
// guarantee). Step 8 (reload) is attempted after a successful rename; reload failure
// is returned as an error but the file is already on disk.
// WriteGames atomically writes an updated games list. See package-level comment for sequence.
// loader may be nil (no in-process reload attempted) or any Reloader (e.g. *ClassrulesLoader
// or the dashboard bridge). The Reloader.Reload() is called AFTER a successful rename.
func WriteGames(path string, loader Reloader, newGames []string) error {
	// Step 1: validate the incoming games list.
	if err := ValidateGames(newGames); err != nil {
		return err
	}

	// Step 2: load current config to preserve non-games fields.
	current, err := LoadFromFile(path)
	if err != nil {
		return fmt.Errorf("classrules: WriteGames: load current config: %w", err)
	}
	if current == nil {
		current = &Config{}
	}

	// Step 3: merge — replace only the Games field.
	current.Games = newGames

	// Step 4: marshal to YAML.
	data, err := yaml.Marshal(current)
	if err != nil {
		return fmt.Errorf("classrules: WriteGames: marshal yaml: %w", err)
	}

	// Step 5: write to temp file in same directory (ensures atomic rename).
	tmp, err := os.CreateTemp(sameDir(path), "classrules.*.tmp")
	if err != nil {
		return fmt.Errorf("classrules: WriteGames: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("classrules: WriteGames: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("classrules: WriteGames: close temp: %w", err)
	}

	// Step 6: validate the temp file by parsing it.
	if _, err := LoadFromFile(tmpPath); err != nil {
		return fmt.Errorf("classrules: WriteGames: temp file validation failed: %w", err)
	}

	// Step 7: atomic rename.
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("classrules: WriteGames: rename: %w", err)
	}
	committed = true

	// Step 8: reload the cloud server's in-memory Config.
	if loader != nil {
		if err := loader.Reload(); err != nil {
			return fmt.Errorf("classrules: WriteGames: reload after write: %w", err)
		}
	}
	return nil
}

// ValidateGames returns an error if the games list is invalid.
// Rules:
//   - Must be non-nil and non-empty (at least one game).
//   - No blank entries.
//   - No duplicate entries (case-insensitive).
func ValidateGames(games []string) error {
	if len(games) == 0 {
		return fmt.Errorf("classrules: games list must have at least one entry (got empty)")
	}
	seen := make(map[string]struct{}, len(games))
	for i, g := range games {
		trimmed := strings.TrimSpace(g)
		if trimmed == "" {
			return fmt.Errorf("classrules: game entry at index %d is blank", i)
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("classrules: duplicate game entry %q", g)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// sameDir returns the directory portion of path so temp files land on the same
// filesystem as the target (enabling atomic os.Rename).
func sameDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
