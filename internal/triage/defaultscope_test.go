package triage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveDefaultScope verifies the fail-safe and valid-value resolution logic.
// All cases use t.TempDir() fixture files — no real home directory is touched.
func TestResolveDefaultScope(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) string // returns projectDir
		wantUI   string
		wantHas  bool // wantHas=true means config.json was present with a recognised value
	}{
		{
			name: "shared value returns shared",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				writeConfig(t, dir, `{"project_name":"alpha","default_scope":"shared"}`)
				return dir
			},
			wantUI:  "shared",
			wantHas: true,
		},
		{
			name: "personal value returns personal",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				writeConfig(t, dir, `{"project_name":"alpha","default_scope":"personal"}`)
				return dir
			},
			wantUI:  "personal",
			wantHas: true,
		},
		{
			name: "missing default_scope field is fail-safe personal",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				writeConfig(t, dir, `{"project_name":"alpha"}`)
				return dir
			},
			wantUI:  "personal",
			wantHas: false,
		},
		{
			name: "absent config.json is fail-safe personal",
			setup: func(t *testing.T) string {
				t.Helper()
				return t.TempDir() // no .engram/config.json
			},
			wantUI:  "personal",
			wantHas: false,
		},
		{
			name: "invalid JSON in config.json is fail-safe personal",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				writeConfig(t, dir, `{not valid json`)
				return dir
			},
			wantUI:  "personal",
			wantHas: false,
		},
		{
			name: "unrecognised value is fail-safe personal",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				writeConfig(t, dir, `{"project_name":"alpha","default_scope":"unknown_value"}`)
				return dir
			},
			wantUI:  "personal",
			wantHas: false,
		},
		{
			name: "empty string value is fail-safe personal",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				writeConfig(t, dir, `{"project_name":"alpha","default_scope":""}`)
				return dir
			},
			wantUI:  "personal",
			wantHas: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir := tt.setup(t)
			got := ResolveDefaultScope(projectDir)
			if got != tt.wantUI {
				t.Errorf("ResolveDefaultScope(%q) = %q; want %q", projectDir, got, tt.wantUI)
			}
		})
	}
}

// TestResolveDefaultScope_HasExplicitValue verifies that HasExplicitDefaultScope
// distinguishes between "explicitly set to personal" and "absent/fail-safe".
func TestResolveDefaultScope_HasExplicitValue(t *testing.T) {
	tests := []struct {
		name        string
		configJSON  string // empty means no config file
		wantHas     bool
		wantUIScope string
	}{
		{
			name:        "shared is explicit",
			configJSON:  `{"project_name":"alpha","default_scope":"shared"}`,
			wantHas:     true,
			wantUIScope: "shared",
		},
		{
			name:        "personal is explicit",
			configJSON:  `{"project_name":"alpha","default_scope":"personal"}`,
			wantHas:     true,
			wantUIScope: "personal",
		},
		{
			name:        "absent field is not explicit",
			configJSON:  `{"project_name":"alpha"}`,
			wantHas:     false,
			wantUIScope: "personal",
		},
		{
			name:        "no config file is not explicit",
			configJSON:  "", // no file
			wantHas:     false,
			wantUIScope: "personal",
		},
		{
			name:        "unrecognised value is not explicit",
			configJSON:  `{"project_name":"alpha","default_scope":"department"}`,
			wantHas:     false,
			wantUIScope: "personal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.configJSON != "" {
				writeConfig(t, dir, tt.configJSON)
			}
			uiScope, hasExplicit := ResolveDefaultScopeWithPresence(dir)
			if uiScope != tt.wantUIScope {
				t.Errorf("ui scope = %q; want %q", uiScope, tt.wantUIScope)
			}
			if hasExplicit != tt.wantHas {
				t.Errorf("hasExplicit = %v; want %v", hasExplicit, tt.wantHas)
			}
		})
	}
}

// writeConfig writes the given JSON content to <projectDir>/.engram/config.json.
func writeConfig(t *testing.T, projectDir, content string) {
	t.Helper()
	engramDir := filepath.Join(projectDir, ".engram")
	if err := os.MkdirAll(engramDir, 0o755); err != nil {
		t.Fatalf("mkdir .engram: %v", err)
	}
	if err := os.WriteFile(filepath.Join(engramDir, "config.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}
