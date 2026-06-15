// Package triage implements the local triage web UI for reviewing and
// classifying observation scope per project.
package triage

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// triageConfigFile mirrors the fields we read from .engram/config.json.
// It is intentionally separate from internal/project.configFile to keep the
// triage package dependency-free from the project detection logic.
// json.Unmarshal silently ignores unknown fields, so this struct is
// non-breaking against config.json files written by older binaries (REQ-27).
//
// NOTE: keep the default_scope JSON tag in sync with internal/project.configFile — divergence is silent.
type triageConfigFile struct {
	ProjectName  string `json:"project_name"`
	DefaultScope string `json:"default_scope,omitempty"`
}

// ResolveDefaultScope reads the project's .engram/config.json and returns the
// effective UI-vocabulary default scope for the project: "shared" or "personal".
//
// The projectDir argument MUST be the project git-root (nearest-config dir),
// NOT an arbitrary subdirectory — callers should resolve it via the
// project-detection logic first (e.g. internal/project.DetectProject).
//
// Fail-safe rules (REQ-20, REQ-21):
//   - config.json absent → "personal"
//   - config.json present but default_scope absent or empty → "personal"
//   - default_scope is "shared" → "shared"
//   - default_scope is "personal" → "personal"
//   - any other value (including "team", "project", "department") → "personal"
//
// This function does NOT call normalizeScope (store.go) or scope.NormalizeScope
// (filter.go). It operates purely at the UI-vocabulary layer.
func ResolveDefaultScope(projectDir string) string {
	scope, _ := ResolveDefaultScopeWithPresence(projectDir)
	return scope
}

// ResolveDefaultScopeWithPresence is like ResolveDefaultScope but also returns
// whether the field was explicitly set to a recognised value.
//
// The projectDir argument MUST be the project git-root (nearest-config dir),
// NOT an arbitrary subdirectory — callers should resolve it via the
// project-detection logic first (e.g. internal/project.DetectProject).
//
//   - hasExplicit=true  → the field contained a valid, recognised value ("shared" or "personal")
//   - hasExplicit=false → the field was absent, empty, unrecognised, or the file was missing/malformed
//
// The landing-page badge uses hasExplicit to distinguish "personal (explicit)"
// from "needs classification" (field never set).
func ResolveDefaultScopeWithPresence(projectDir string) (uiScope string, hasExplicit bool) {
	configPath := filepath.Join(projectDir, ".engram", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		// File absent or unreadable — fail-safe personal, not classified.
		return "personal", false
	}

	var cfg triageConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		// Malformed JSON — fail-safe personal, not classified.
		return "personal", false
	}

	switch cfg.DefaultScope {
	case "shared":
		return "shared", true
	case "personal":
		return "personal", true
	default:
		// Empty string, unrecognised value, or anything else — fail-safe.
		return "personal", false
	}
}
