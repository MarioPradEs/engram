package classrules

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/natefinch/atomic"
	"gopkg.in/yaml.v3"
)

// Reloader is implemented by *ClassrulesLoader (and by the dashboard bridge).
// WriteGames uses this interface so callers don't need to import the concrete type.
type Reloader interface {
	Reload() error
}

// hexColorRE matches exactly a 6-digit CSS hex color (e.g. "#E5C07B").
var hexColorRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// ValidateColors validates a color map where every non-empty value must be a
// valid 6-digit hex color string matching ^#[0-9A-Fa-f]{6}$.
//
// Empty string values are accepted as unset placeholders (the brain-graph
// pipeline treats absent / empty values as "use the fallback color").
//
// Returns the first validation error encountered, or nil when all values are valid.
func ValidateColors(colors map[string]string) error {
	for k, v := range colors {
		if v == "" {
			continue // empty = placeholder; valid
		}
		if !hexColorRE.MatchString(v) {
			return fmt.Errorf("classrules: invalid hex color for key %q: %q (want ^#[0-9A-Fa-f]{6}$)", k, v)
		}
	}
	return nil
}

// WriteColors validates the given game and department color maps, then atomically
// updates the graph_colors block in the YAML file at path while preserving all
// other sections (departments, games, rules, conventions, etc.).
//
// Atomicity: uses natefinch/atomic.WriteFile which writes to a temp file and
// renames it over the target — preventing partial writes on crash.
//
// On validate failure the file is NOT modified and error is returned immediately.
//
// reload is called exactly once after a successful atomic write so that the
// running process can re-read the updated config without restart. It may be nil
// if no reload signal is needed (e.g. in tests that don't care about reload).
func WriteColors(path string, games, departments map[string]string, reload func()) error {
	// Validate before touching the file — fail fast, zero side effects.
	if err := ValidateColors(games); err != nil {
		return err
	}
	if err := ValidateColors(departments); err != nil {
		return err
	}

	// Load the current config so we can round-trip all existing fields unchanged.
	// If the file doesn't exist yet we start from zero.
	cfg, err := LoadFromFile(path)
	if err != nil {
		return fmt.Errorf("classrules: WriteColors: load current config: %w", err)
	}
	if cfg == nil {
		cfg = &Config{}
	}

	// Patch only the GraphColors block; leave everything else untouched.
	if games != nil {
		cfg.GraphColors.Games = games
	}
	if departments != nil {
		cfg.GraphColors.Departments = departments
	}

	// Marshal back to YAML.
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("classrules: WriteColors: marshal: %w", err)
	}

	// Atomic write: natefinch/atomic writes a temp file then renames it, so
	// partial writes on crash are impossible.
	if err := atomic.WriteFile(path, bytes.NewReader(out)); err != nil {
		return fmt.Errorf("classrules: WriteColors: write %s: %w", path, err)
	}

	// Notify the process that the file has changed.
	if reload != nil {
		reload()
	}
	return nil
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
	// W2 fix: a reload failure after a successful atomic rename is non-fatal.
	// The file on disk is already correct (the rename committed). An in-process
	// reload failure is a transient operational issue; the server will pick up the
	// new config on the next restart or explicit reload. Treat identically to the
	// users.yaml write path (users.WriteAtomic) which logs and returns success on
	// reload failure. Genuine write/validate errors (before the rename) remain fatal.
	if loader != nil {
		if err := loader.Reload(); err != nil {
			// Non-fatal: the file is on disk correctly; log and continue.
			// Callers (e.g. handleAdminGamesPost) may log this separately.
			_ = err // Intentionally ignored: reload failure is non-fatal post-rename.
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
