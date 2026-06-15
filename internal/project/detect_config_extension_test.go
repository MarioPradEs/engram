package project

import (
	"encoding/json"
	"testing"
)

// TestConfigFile_DefaultScopeField verifies REQ-27 / Scenario B-09:
// parsing a config.json with only project_name (no default_scope) must succeed,
// and DefaultScope must be the zero value (empty string).
// This confirms that json.Unmarshal treats the new field as optional and does
// not break existing callers that write config.json without default_scope.
func TestConfigFile_DefaultScopeField_NonBreakingParse(t *testing.T) {
	fixture := `{"project_name": "my-project"}`
	var cfg configFile
	if err := json.Unmarshal([]byte(fixture), &cfg); err != nil {
		t.Fatalf("json.Unmarshal failed on config without default_scope: %v", err)
	}
	if cfg.ProjectName != "my-project" {
		t.Errorf("ProjectName = %q; want %q", cfg.ProjectName, "my-project")
	}
	if cfg.DefaultScope != "" {
		t.Errorf("DefaultScope = %q; want empty string when field absent", cfg.DefaultScope)
	}
}

// TestConfigFile_DefaultScopeField_ParsesKnownValues verifies that both valid
// values ("shared" and "personal") are round-tripped through json.Unmarshal.
func TestConfigFile_DefaultScopeField_ParsesKnownValues(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantVal string
	}{
		{
			name:    "shared value round-trips",
			json:    `{"project_name":"alpha","default_scope":"shared"}`,
			wantVal: "shared",
		},
		{
			name:    "personal value round-trips",
			json:    `{"project_name":"alpha","default_scope":"personal"}`,
			wantVal: "personal",
		},
		{
			name:    "unrecognised value is preserved verbatim by struct",
			json:    `{"project_name":"alpha","default_scope":"unknown"}`,
			wantVal: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg configFile
			if err := json.Unmarshal([]byte(tt.json), &cfg); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			if cfg.DefaultScope != tt.wantVal {
				t.Errorf("DefaultScope = %q; want %q", cfg.DefaultScope, tt.wantVal)
			}
		})
	}
}
