package classrules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/classrules"
)

// TestLoadFromFile_ValidConfig verifies that a well-formed classification-rules.yaml
// is parsed correctly and the Rules text is non-empty.
func TestLoadFromFile_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	content := `# Engram classification rules
departments:
  - name: engineering
    aliases:
      - eng
  - name: art
    aliases:
      - art
      - artistic

conventions:
  general_project: "general"
  team_project_prefix: "team-"

project_patterns:
  - pattern: "juego-*"
    description: "Game projects — use project scope by default"
  - pattern: "sdk-*"
    description: "SDK projects — prefer team scope"

rules: |
  ## Scope Classification Rules

  | Tier       | When to use |
  |------------|-------------|
  | personal   | Credentials, private thoughts, in-progress ideas |
  | department | Department-internal conventions |
  | project    | Game/project-specific knowledge (DEFAULT) |
  | team       | Cross-cutting knowledge — route to project 'general' |

  Departments: engineering (eng), art
  Team-project prefix: team-
  Game projects: juego-* → project scope
  SDK projects: sdk-* → prefer team scope
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Departments) != 2 {
		t.Errorf("departments: got %d, want 2", len(cfg.Departments))
	}
	if cfg.Conventions.GeneralProject != "general" {
		t.Errorf("general_project: got %q, want %q", cfg.Conventions.GeneralProject, "general")
	}
	if cfg.Conventions.TeamProjectPrefix != "team-" {
		t.Errorf("team_project_prefix: got %q, want %q", cfg.Conventions.TeamProjectPrefix, "team-")
	}
	if len(cfg.ProjectPatterns) != 2 {
		t.Errorf("project_patterns: got %d, want 2", len(cfg.ProjectPatterns))
	}
	// Rules text must be non-empty so the MCP injection has content.
	if cfg.Rules == "" {
		t.Error("rules: expected non-empty text")
	}
}

// TestLoadFromFile_InvalidYAML verifies that malformed YAML returns an error
// and does not panic.
func TestLoadFromFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte(":\tinvalid: yaml: [\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := classrules.LoadFromFile(path)
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

// TestLoadFromFile_Absent verifies that when the file does not exist, LoadFromFile
// returns a nil config and no error — graceful absent behavior.
func TestLoadFromFile_Absent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("expected no error for absent file, got: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for absent file")
	}
}

// TestLoadFromFile_Empty verifies that an empty (zero-byte) file returns a default
// zero-value config and no error — operators may stage the file before filling it.
func TestLoadFromFile_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile empty: %v", err)
	}
	// Empty file → non-nil but zero-value config (all fields empty).
	if cfg == nil {
		t.Fatal("expected non-nil config for empty file")
	}
	if len(cfg.Departments) != 0 {
		t.Errorf("departments: got %d, want 0", len(cfg.Departments))
	}
}

// TestBuildInstructions_WithRules verifies that BuildInstructions produces a
// non-empty string containing the rules text when a valid config is provided.
func TestBuildInstructions_WithRules(t *testing.T) {
	cfg := &classrules.Config{
		Rules: "## Scope Rules\n\npersonal = private\nproject = default\n",
	}
	base := "Base MCP instructions here."
	result := classrules.BuildInstructions(base, cfg)

	if result == "" {
		t.Fatal("BuildInstructions: expected non-empty result")
	}
	// Must contain the base instructions.
	if !contains(result, base) {
		t.Errorf("result must contain base instructions")
	}
	// Must contain the rules text.
	if !contains(result, "Scope Rules") {
		t.Errorf("result must contain rules text, got: %q", result)
	}
}

// TestBuildInstructions_NilConfig verifies that nil config returns the base
// instructions unchanged — graceful absent behavior at injection.
func TestBuildInstructions_NilConfig(t *testing.T) {
	base := "Base MCP instructions here."
	result := classrules.BuildInstructions(base, nil)
	if result != base {
		t.Errorf("expected base instructions unchanged for nil config, got: %q", result)
	}
}

// TestBuildInstructions_EmptyRules verifies that a config with an empty Rules field
// returns base instructions unchanged — operators may have a config without a rules
// block yet.
func TestBuildInstructions_EmptyRules(t *testing.T) {
	cfg := &classrules.Config{}
	base := "Base MCP instructions here."
	result := classrules.BuildInstructions(base, cfg)
	if result != base {
		t.Errorf("expected base instructions unchanged for empty rules, got: %q", result)
	}
}

// TestBuildInstructionsTagging verifies that BuildInstructions renders the games
// vocabulary and four-facet tagging guidance when Games is non-empty, and omits
// the juego instructions when Games is empty or nil.
func TestBuildInstructionsTagging(t *testing.T) {
	base := "Base MCP instructions here."

	tests := []struct {
		name             string
		cfg              *classrules.Config
		wantContains     []string
		wantNotContains  []string
	}{
		{
			name: "non-empty Games renders vocab and four-facet guidance",
			cfg: &classrules.Config{
				Games: []string{"game-a", "game-b"},
				Rules: "existing rules text",
			},
			wantContains: []string{
				"game-a",
				"game-b",
				"Allowed games",
				"juego",
				"tipo",
				"departamento",
				"proyecto",
				"do NOT supply",
				"existing rules text",
				base,
			},
			wantNotContains: []string{
				"do not set juego",
			},
		},
		{
			name: "empty Games omits Allowed games and disables juego",
			cfg: &classrules.Config{
				Games: []string{},
			},
			wantContains: []string{
				base,
				"do not set juego",
			},
			wantNotContains: []string{
				"Allowed games",
			},
		},
		{
			name: "nil Games (no tagging configured) — base unchanged, no tagging block",
			cfg: &classrules.Config{
				Games: nil,
			},
			wantContains: []string{
				base,
			},
			wantNotContains: []string{
				"Allowed games",
				"do not set juego",
				"Memory Tagging Rules",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classrules.BuildInstructions(base, tt.cfg)
			for _, want := range tt.wantContains {
				if !contains(result, want) {
					t.Errorf("result must contain %q\nfull result:\n%s", want, result)
				}
			}
			for _, notWant := range tt.wantNotContains {
				if contains(result, notWant) {
					t.Errorf("result must NOT contain %q\nfull result:\n%s", notWant, result)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
