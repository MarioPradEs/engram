package main

import (
	"testing"
)

// TestResolveProjectName verifies the D1 precedence: ENGRAM_PROJECT (explicit
// override) > detectProject(cwd) > ENGRAM_DEFAULT_PROJECT > "personal".
//
// The function under test takes three pre-resolved string arguments so it is
// pure and does not touch env vars or the filesystem.  The callers are
// responsible for reading the env/detecting cwd before calling it.
func TestResolveProjectName(t *testing.T) {
	tests := []struct {
		name        string
		envProject  string // value of ENGRAM_PROJECT
		detected    string // value returned by detectProject(cwd)
		envDefault  string // value of ENGRAM_DEFAULT_PROJECT
		wantProject string
	}{
		{
			name:        "explicit env override wins over everything",
			envProject:  "viva",
			detected:    "android-game-perf-tool-desktop",
			envDefault:  "work-notes",
			wantProject: "viva",
		},
		{
			name:        "detectProject wins when env override is empty",
			envProject:  "",
			detected:    "android-game-perf-tool-desktop",
			envDefault:  "work-notes",
			wantProject: "android-game-perf-tool-desktop",
		},
		{
			name:        "ENGRAM_DEFAULT_PROJECT wins when detect returns empty",
			envProject:  "",
			detected:    "",
			envDefault:  "work-notes",
			wantProject: "work-notes",
		},
		{
			name:        "falls back to personal when all inputs are empty",
			envProject:  "",
			detected:    "",
			envDefault:  "",
			wantProject: "personal",
		},
		{
			name:        "explicit env override wins even when default is set",
			envProject:  "explicit",
			detected:    "",
			envDefault:  "default-project",
			wantProject: "explicit",
		},
		{
			name:        "whitespace-only env override is treated as empty",
			envProject:  "   ",
			detected:    "git-project",
			envDefault:  "",
			wantProject: "git-project",
		},
		{
			name:        "whitespace-only detected is treated as empty — falls to default",
			envProject:  "",
			detected:    "  ",
			envDefault:  "mydefault",
			wantProject: "mydefault",
		},
		{
			name:        "all whitespace — final fallback is personal",
			envProject:  " ",
			detected:    " ",
			envDefault:  " ",
			wantProject: "personal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveProjectName(tc.envProject, tc.detected, tc.envDefault)
			if got != tc.wantProject {
				t.Errorf("resolveProjectName(%q, %q, %q) = %q; want %q",
					tc.envProject, tc.detected, tc.envDefault, got, tc.wantProject)
			}
		})
	}
}
