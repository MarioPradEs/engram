package triage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteProjectDefaultScope writes the UI-vocabulary default_scope value
// ("shared" or "personal") to the project's .engram/config.json.
//
// The write is atomic: data is written to a temp file in the same directory
// as config.json, then renamed over the target. This prevents partial reads
// by the binary on concurrent access.
//
// Existing fields in config.json are preserved (round-trip via map).
// If config.json does not exist, a new file is created with only default_scope.
//
// projectDir MUST be the project git-root (the directory containing .engram/).
// Callers should resolve it via detect.Detect before calling this function.
//
// Only "shared" and "personal" are valid scopes (binary UI vocabulary, #937.4).
// Any other value is rejected with an error.
func WriteProjectDefaultScope(projectDir string, scope string) error {
	if scope != "shared" && scope != "personal" {
		return fmt.Errorf("triage: invalid scope %q: must be 'shared' or 'personal'", scope)
	}

	configPath := filepath.Join(projectDir, ".engram", "config.json")

	// Read existing config, or start with an empty map.
	raw := map[string]json.RawMessage{}
	existing, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("triage: read config.json: %w", err)
	}
	if err == nil {
		// Parse into a raw map so all existing keys are preserved verbatim.
		if jsonErr := json.Unmarshal(existing, &raw); jsonErr != nil {
			// Malformed config.json — return an error and leave the file untouched.
			// Silently resetting it would discard fields like project_name (S-3).
			return fmt.Errorf("triage: config.json is malformed and cannot be updated safely: %w", jsonErr)
		}
	}

	// Update or insert default_scope.
	scopeJSON, _ := json.Marshal(scope)
	raw["default_scope"] = json.RawMessage(scopeJSON)

	// Marshal to pretty JSON (matches the existing config.json style).
	newData, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("triage: marshal config.json: %w", err)
	}
	newData = append(newData, '\n') // trailing newline

	// Write to a temp file in the same directory as config.json so that
	// os.Rename is atomic (same filesystem / same volume).
	dir := filepath.Dir(configPath)
	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("triage: create temp config: %w", err)
	}
	tmpName := tmp.Name()
	// Ensure the temp file is removed on any failure path.
	defer func() {
		// If Rename succeeded, the file is gone; ignore the "not found" error.
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(newData); err != nil {
		tmp.Close()
		return fmt.Errorf("triage: write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("triage: close temp config: %w", err)
	}

	// Atomic rename: replaces config.json in a single syscall.
	if err := os.Rename(tmpName, configPath); err != nil {
		return fmt.Errorf("triage: rename config.json: %w", err)
	}

	return nil
}
