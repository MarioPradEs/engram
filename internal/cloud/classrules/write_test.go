package classrules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/classrules"
)

// ─── B3: ValidateColors ───────────────────────────────────────────────────────

// TestValidateColors_RejectsInvalidHex asserts that ValidateColors returns an
// error for values that do not match ^#[0-9A-Fa-f]{6}$.
func TestValidateColors_RejectsInvalidHex(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"named color", "red"},
		{"invalid hex digits", "#GG0000"},
		{"short hex", "#FFF"},
		{"eight digit hex", "#AABBCCDD"},
		{"no hash", "AABBCC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := classrules.ValidateColors(map[string]string{"game": tc.value})
			if err == nil {
				t.Errorf("ValidateColors(%q) = nil, want error", tc.value)
			}
		})
	}
}

// TestValidateColors_AcceptsValidHex asserts that ValidateColors accepts well-formed
// 6-digit hex strings.
func TestValidateColors_AcceptsValidHex(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"warm amber", "#E5C07B"},
		{"black", "#000000"},
		{"white", "#FFFFFF"},
		{"lowercase", "#aabbcc"},
		{"mixed case", "#aAbBcC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := classrules.ValidateColors(map[string]string{"key": tc.value})
			if err != nil {
				t.Errorf("ValidateColors(%q) = %v, want nil", tc.value, err)
			}
		})
	}
}

// TestValidateColors_AcceptsEmptyString asserts that empty string values are
// treated as unset placeholders and accepted without error.
// (Pipeline treats absent/empty as "use fallback color".)
func TestValidateColors_AcceptsEmptyStringPlaceholder(t *testing.T) {
	err := classrules.ValidateColors(map[string]string{"game": ""})
	if err != nil {
		t.Errorf("ValidateColors(empty string placeholder) = %v, want nil", err)
	}
}

// ─── B3: WriteColors ─────────────────────────────────────────────────────────

// TestWriteColors_WritesValidHexAndPreservesRestOfFile asserts that WriteColors
// with a valid hex map writes the graph_colors block into the YAML file while
// preserving the existing departments/games/rules sections exactly.
func TestWriteColors_WritesValidHexAndPreservesRestOfFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")

	initial := `departments:
  - name: engineering
    aliases:
      - eng
games:
  - "spark"
  - "Tower Battle"
rules: |
  ## Scope Rules
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	games := map[string]string{"spark": "#E5C07B"}
	depts := map[string]string{"dev": "#61AFEF"}

	reloadCalled := false
	reload := func() { reloadCalled = true }

	if err := classrules.WriteColors(path, games, depts, reload); err != nil {
		t.Fatalf("WriteColors: %v", err)
	}

	// File must be readable and contain the new color block.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after WriteColors: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "graph_colors:") {
		t.Errorf("expected graph_colors: in written YAML, got:\n%s", content)
	}
	if !strings.Contains(content, "#E5C07B") {
		t.Errorf("expected #E5C07B in written YAML, got:\n%s", content)
	}
	if !strings.Contains(content, "#61AFEF") {
		t.Errorf("expected #61AFEF in written YAML, got:\n%s", content)
	}
	// Original sections must be preserved.
	if !strings.Contains(content, "engineering") {
		t.Errorf("departments section must be preserved after WriteColors, got:\n%s", content)
	}
	if !strings.Contains(content, "spark") {
		t.Errorf("games section must be preserved after WriteColors, got:\n%s", content)
	}
	// The reload signal must have been called.
	if !reloadCalled {
		t.Error("WriteColors must call the reload function after a successful write")
	}
}

// TestWriteColors_RejectsInvalidHexAndDoesNotWrite asserts that WriteColors
// returns an error and does NOT modify the file when given an invalid color.
func TestWriteColors_RejectsInvalidHexAndDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := "games:\n  - spark\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	err := classrules.WriteColors(path, map[string]string{"spark": "bad-color"}, nil, func() {})
	if err == nil {
		t.Error("WriteColors with invalid hex must return error")
	}

	// File content must be unchanged.
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read after failed WriteColors: %v", readErr)
	}
	if string(got) != initial {
		t.Errorf("file must not be modified on validate failure; got:\n%s", string(got))
	}
}

// TestWriteColors_ReloadsAfterSuccessfulWrite asserts that the reload function
// is called exactly once after a successful write.
func TestWriteColors_ReloadsAfterSuccessfulWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte("games:\n  - spark\n"), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	callCount := 0
	reload := func() { callCount++ }

	if err := classrules.WriteColors(path, map[string]string{"spark": "#000000"}, nil, reload); err != nil {
		t.Fatalf("WriteColors: %v", err)
	}
	if callCount != 1 {
		t.Errorf("reload called %d times, want 1", callCount)
	}
}

// TestWriteColors_ParsesBackCorrectly asserts the written YAML can be loaded
// back via LoadFromFile and returns the same color values.
func TestWriteColors_ParsesBackCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte("games:\n  - spark\n"), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	games := map[string]string{"spark": "#E5C07B", "Tower Battle": "#3D7AE0"}
	depts := map[string]string{"dev": "#61AFEF", "art": "#C678DD"}

	if err := classrules.WriteColors(path, games, depts, func() {}); err != nil {
		t.Fatalf("WriteColors: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after WriteColors: %v", err)
	}
	for k, want := range games {
		if got := cfg.GraphColors.Games[k]; got != want {
			t.Errorf("round-trip: games[%q] = %q, want %q", k, got, want)
		}
	}
	for k, want := range depts {
		if got := cfg.GraphColors.Departments[k]; got != want {
			t.Errorf("round-trip: departments[%q] = %q, want %q", k, got, want)
		}
	}
}
