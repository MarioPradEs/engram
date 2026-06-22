package classrules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/classrules"
)

// mockReloader is a test implementation of classrules.Reloader that records
// whether Reload was called.
type mockReloader struct {
	fn func() error
}

func (m *mockReloader) Reload() error {
	if m.fn != nil {
		return m.fn()
	}
	return nil
}

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

// ─── WriteGameEntry ──────────────────────────────────────────────────────────

// TestWriteGameEntry_AddGame asserts that WriteGameEntry with a new games list
// (including a new entry) + its color writes both atomically, preserving other sections.
func TestWriteGameEntry_AddGame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := `departments:
  - name: engineering
games:
  - "spark"
graph_colors:
  games:
    spark: "#E5C07B"
  departments:
    dev: "#528BFF"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	newGames := []string{"spark", "nova"}
	newColors := map[string]string{"spark": "#E5C07B", "nova": "#61AFEF"}

	reloadCalled := false
	type reloader struct{ called *bool }
	r := &struct{ called *bool }{called: &reloadCalled}
	_ = r

	// Use a nil reloader for this test; verify file contents only.
	if err := classrules.WriteGameEntry(path, nil, newGames, newColors); err != nil {
		t.Fatalf("WriteGameEntry: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after WriteGameEntry: %v", err)
	}
	if len(cfg.Games) != 2 {
		t.Errorf("expected 2 games, got %d: %v", len(cfg.Games), cfg.Games)
	}
	if cfg.GraphColors.Games["nova"] != "#61AFEF" {
		t.Errorf("nova color = %q, want #61AFEF", cfg.GraphColors.Games["nova"])
	}
	if cfg.GraphColors.Games["spark"] != "#E5C07B" {
		t.Errorf("spark color = %q, want #E5C07B", cfg.GraphColors.Games["spark"])
	}
	// Department color must be preserved.
	if cfg.GraphColors.Departments["dev"] != "#528BFF" {
		t.Errorf("dev dept color = %q after WriteGameEntry, want #528BFF (should be preserved)", cfg.GraphColors.Departments["dev"])
	}
	// Departments list must be preserved.
	if len(cfg.Departments) == 0 {
		t.Error("departments section was not preserved after WriteGameEntry")
	}
}

// TestWriteGameEntry_RenameGame asserts that renaming a game (updating games list
// + migrating color) leaves no old key and adds the new key.
func TestWriteGameEntry_RenameGame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := `games:
  - "old-name"
  - "other-game"
graph_colors:
  games:
    old-name: "#AABBCC"
    other-game: "#112233"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// Rename "old-name" → "new-name"; migrate color.
	newGames := []string{"new-name", "other-game"}
	newColors := map[string]string{"new-name": "#AABBCC", "other-game": "#112233"}

	if err := classrules.WriteGameEntry(path, nil, newGames, newColors); err != nil {
		t.Fatalf("WriteGameEntry rename: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after rename: %v", err)
	}
	if _, exists := cfg.GraphColors.Games["old-name"]; exists {
		t.Error("old-name color key should not exist after rename")
	}
	if cfg.GraphColors.Games["new-name"] != "#AABBCC" {
		t.Errorf("new-name color = %q, want #AABBCC", cfg.GraphColors.Games["new-name"])
	}
	found := false
	for _, g := range cfg.Games {
		if g == "new-name" {
			found = true
		}
		if g == "old-name" {
			t.Error("old-name still in games list after rename")
		}
	}
	if !found {
		t.Error("new-name not found in games list after rename")
	}
}

// TestWriteGameEntry_DeleteGame asserts that removing a game from the list and
// its color leaves the file consistent.
func TestWriteGameEntry_DeleteGame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := `games:
  - "alpha"
  - "beta"
graph_colors:
  games:
    alpha: "#111111"
    beta: "#222222"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// Delete "beta" — pass remaining game + its color only.
	if err := classrules.WriteGameEntry(path, nil, []string{"alpha"}, map[string]string{"alpha": "#111111"}); err != nil {
		t.Fatalf("WriteGameEntry delete: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after delete: %v", err)
	}
	if len(cfg.Games) != 1 || cfg.Games[0] != "alpha" {
		t.Errorf("expected [alpha], got %v", cfg.Games)
	}
	if _, exists := cfg.GraphColors.Games["beta"]; exists {
		t.Error("beta color key should not exist after delete")
	}
}

// TestWriteGameEntry_InvalidColorRejected asserts that an invalid color value
// causes an error without modifying the file.
func TestWriteGameEntry_InvalidColorRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := "games:\n  - spark\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	err := classrules.WriteGameEntry(path, nil, []string{"spark"}, map[string]string{"spark": "bad-color"})
	if err == nil {
		t.Error("WriteGameEntry with invalid color must return error")
	}

	got, _ := os.ReadFile(path)
	if string(got) != initial {
		t.Errorf("file must not be modified on validate failure; got:\n%s", string(got))
	}
}

// ─── WriteGameEntryAllowEmpty ────────────────────────────────────────────────

// TestWriteGameEntryAllowEmpty_WritesEmptyGamesList asserts that
// WriteGameEntryAllowEmpty with an empty games list writes an empty games:
// block to disk (not leaving a stale list) and preserves dept colors.
func TestWriteGameEntryAllowEmpty_WritesEmptyGamesList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := `departments:
  - name: dev
games:
  - "last-game"
graph_colors:
  games:
    last-game: "#AABBCC"
  departments:
    dev: "#528BFF"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// Delete the last game — empty games list, empty game colors.
	if err := classrules.WriteGameEntryAllowEmpty(path, nil, []string{}, map[string]string{}); err != nil {
		t.Fatalf("WriteGameEntryAllowEmpty: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after empty write: %v", err)
	}
	if len(cfg.Games) != 0 {
		t.Errorf("expected empty games list on disk, got %v", cfg.Games)
	}
	if _, exists := cfg.GraphColors.Games["last-game"]; exists {
		t.Error("last-game color key must not exist after delete")
	}
	// Dept color must be preserved.
	if cfg.GraphColors.Departments["dev"] != "#528BFF" {
		t.Errorf("dev dept color = %q after empty-game write, want #528BFF (must be preserved)", cfg.GraphColors.Departments["dev"])
	}
}

// TestWriteGameEntryAllowEmpty_InvalidColorRejected asserts that an invalid color
// causes an error without modifying the file, even for an empty games list.
func TestWriteGameEntryAllowEmpty_InvalidColorRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := "games:\n  - spark\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	err := classrules.WriteGameEntryAllowEmpty(path, nil, []string{}, map[string]string{"orphan": "bad-color"})
	if err == nil {
		t.Error("WriteGameEntryAllowEmpty with invalid color must return error")
	}

	got, _ := os.ReadFile(path)
	if string(got) != initial {
		t.Errorf("file must not be modified on validate failure; got:\n%s", string(got))
	}
}

// ─── WriteDeptEntry ──────────────────────────────────────────────────────────

// TestWriteDeptEntry_AddDept asserts that WriteDeptEntry with a new departments
// list (including a new entry) + its color writes both atomically, preserving games.
func TestWriteDeptEntry_AddDept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := `departments:
  - name: dev
    aliases:
      - engineering
games:
  - "spark"
graph_colors:
  games:
    spark: "#E5C07B"
  departments:
    dev: "#528BFF"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	newDepts := []classrules.Department{
		{Name: "dev", Aliases: []string{"engineering"}},
		{Name: "art"},
	}
	newColors := map[string]string{"dev": "#528BFF", "art": "#C678DD"}

	if err := classrules.WriteDeptEntry(path, nil, newDepts, newColors); err != nil {
		t.Fatalf("WriteDeptEntry: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after WriteDeptEntry: %v", err)
	}
	if len(cfg.Departments) != 2 {
		t.Errorf("expected 2 departments, got %d: %v", len(cfg.Departments), cfg.Departments)
	}
	if cfg.GraphColors.Departments["art"] != "#C678DD" {
		t.Errorf("art dept color = %q, want #C678DD", cfg.GraphColors.Departments["art"])
	}
	if cfg.GraphColors.Departments["dev"] != "#528BFF" {
		t.Errorf("dev dept color = %q, want #528BFF", cfg.GraphColors.Departments["dev"])
	}
	// Game color must be preserved.
	if cfg.GraphColors.Games["spark"] != "#E5C07B" {
		t.Errorf("spark game color = %q after WriteDeptEntry, want #E5C07B (should be preserved)", cfg.GraphColors.Games["spark"])
	}
	// Games list must be preserved.
	if len(cfg.Games) == 0 {
		t.Error("games section was not preserved after WriteDeptEntry")
	}
}

// TestWriteDeptEntry_RenamePreservesAliasesAndMigratesColor asserts that renaming a
// department (updating departments list + migrating color) preserves aliases on the
// renamed entry and leaves no old color key.
func TestWriteDeptEntry_RenamePreservesAliasesAndMigratesColor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := `departments:
  - name: dev
    aliases:
      - engineering
      - eng
  - name: art
graph_colors:
  departments:
    dev: "#528BFF"
    art: "#C678DD"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// Rename "dev" → "engineering-team"; preserve its aliases.
	newDepts := []classrules.Department{
		{Name: "engineering-team", Aliases: []string{"engineering", "eng"}},
		{Name: "art"},
	}
	newColors := map[string]string{"engineering-team": "#528BFF", "art": "#C678DD"}

	if err := classrules.WriteDeptEntry(path, nil, newDepts, newColors); err != nil {
		t.Fatalf("WriteDeptEntry rename: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after rename: %v", err)
	}
	if _, exists := cfg.GraphColors.Departments["dev"]; exists {
		t.Error("dev color key should not exist after rename")
	}
	if cfg.GraphColors.Departments["engineering-team"] != "#528BFF" {
		t.Errorf("engineering-team color = %q, want #528BFF", cfg.GraphColors.Departments["engineering-team"])
	}
	// Verify aliases were preserved in the renamed entry.
	found := false
	for _, d := range cfg.Departments {
		if d.Name == "dev" {
			t.Error("dev still in departments list after rename")
		}
		if d.Name == "engineering-team" {
			found = true
			if len(d.Aliases) != 2 {
				t.Errorf("expected 2 aliases on engineering-team, got %v", d.Aliases)
			}
		}
	}
	if !found {
		t.Error("engineering-team not found in departments list after rename")
	}
}

// TestWriteDeptEntry_DeleteDept asserts that removing a dept from the list and
// its color leaves the file consistent.
func TestWriteDeptEntry_DeleteDept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := `departments:
  - name: dev
  - name: art
graph_colors:
  departments:
    dev: "#528BFF"
    art: "#C678DD"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// Delete "art" — pass remaining dept + its color only.
	if err := classrules.WriteDeptEntry(path, nil, []classrules.Department{{Name: "dev"}}, map[string]string{"dev": "#528BFF"}); err != nil {
		t.Fatalf("WriteDeptEntry delete: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after delete: %v", err)
	}
	if len(cfg.Departments) != 1 || cfg.Departments[0].Name != "dev" {
		t.Errorf("expected [dev], got %v", cfg.Departments)
	}
	if _, exists := cfg.GraphColors.Departments["art"]; exists {
		t.Error("art color key should not exist after delete")
	}
}

// TestWriteDeptEntry_InvalidColorRejected asserts that an invalid color value
// causes an error without modifying the file.
func TestWriteDeptEntry_InvalidColorRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := `departments:
  - name: dev
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	err := classrules.WriteDeptEntry(path, nil, []classrules.Department{{Name: "dev"}}, map[string]string{"dev": "bad-color"})
	if err == nil {
		t.Error("WriteDeptEntry with invalid color must return error")
	}

	got, _ := os.ReadFile(path)
	if string(got) != initial {
		t.Errorf("file must not be modified on validate failure; got:\n%s", string(got))
	}
}

// TestWriteDeptEntry_PreservesGamesSection asserts that WriteDeptEntry never
// touches the games list or game colors.
func TestWriteDeptEntry_PreservesGamesSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	initial := `departments:
  - name: dev
games:
  - "spark"
  - "nova"
graph_colors:
  games:
    spark: "#E5C07B"
    nova: "#61AFEF"
  departments:
    dev: "#528BFF"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// Add "art" dept; games must remain untouched.
	newDepts := []classrules.Department{{Name: "dev"}, {Name: "art"}}
	newColors := map[string]string{"dev": "#528BFF", "art": "#C678DD"}

	if err := classrules.WriteDeptEntry(path, nil, newDepts, newColors); err != nil {
		t.Fatalf("WriteDeptEntry: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after WriteDeptEntry: %v", err)
	}
	if len(cfg.Games) != 2 {
		t.Errorf("expected 2 games preserved, got %d: %v", len(cfg.Games), cfg.Games)
	}
	if cfg.GraphColors.Games["spark"] != "#E5C07B" {
		t.Errorf("spark color = %q after WriteDeptEntry, want #E5C07B (should be preserved)", cfg.GraphColors.Games["spark"])
	}
	if cfg.GraphColors.Games["nova"] != "#61AFEF" {
		t.Errorf("nova color = %q after WriteDeptEntry, want #61AFEF (should be preserved)", cfg.GraphColors.Games["nova"])
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

// ─── WriteRules ──────────────────────────────────────────────────────────────

// TestWriteRules_RoundTrip asserts that WriteRules writes the new rules string
// to the YAML file and that LoadFromFile returns it unchanged.
func TestWriteRules_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")

	initial := `games:
  - spark
departments:
  - name: dev
graph_colors:
  games:
    spark: "#E5C07B"
  departments:
    dev: "#528BFF"
rules: old rules text
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	newRules := "## New Classification Rules\n\nUse project scope for all personal work."
	reloadCalled := false
	loader := &mockReloader{fn: func() error { reloadCalled = true; return nil }}

	if err := classrules.WriteRules(path, loader, newRules); err != nil {
		t.Fatalf("WriteRules: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after WriteRules: %v", err)
	}
	if cfg.Rules != newRules {
		t.Errorf("WriteRules round-trip: got Rules=%q, want %q", cfg.Rules, newRules)
	}
	if !reloadCalled {
		t.Error("WriteRules must call loader.Reload() after a successful write")
	}
}

// TestWriteRules_PreservesGamesDeptsAndColors asserts that WriteRules patches
// ONLY cfg.Rules, leaving cfg.Games, cfg.Departments, and cfg.GraphColors intact.
func TestWriteRules_PreservesGamesDeptsAndColors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")

	initial := `games:
  - spark
  - nova
departments:
  - name: dev
    aliases:
      - engineering
graph_colors:
  games:
    spark: "#E5C07B"
    nova: "#61AFEF"
  departments:
    dev: "#528BFF"
rules: old rules
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	if err := classrules.WriteRules(path, nil, "new rules text"); err != nil {
		t.Fatalf("WriteRules: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after WriteRules: %v", err)
	}

	// Games must be preserved.
	if len(cfg.Games) != 2 {
		t.Errorf("games count = %d, want 2 (games must be preserved)", len(cfg.Games))
	}
	// Departments must be preserved (including aliases).
	if len(cfg.Departments) == 0 {
		t.Error("departments must be preserved after WriteRules")
	}
	if len(cfg.Departments[0].Aliases) == 0 {
		t.Error("department aliases must be preserved after WriteRules")
	}
	// GraphColors must be preserved.
	if cfg.GraphColors.Games["spark"] != "#E5C07B" {
		t.Errorf("spark color = %q after WriteRules, want #E5C07B (must be preserved)", cfg.GraphColors.Games["spark"])
	}
	if cfg.GraphColors.Departments["dev"] != "#528BFF" {
		t.Errorf("dev dept color = %q after WriteRules, want #528BFF (must be preserved)", cfg.GraphColors.Departments["dev"])
	}
	// Rules must be the new value.
	if cfg.Rules != "new rules text" {
		t.Errorf("rules = %q, want %q", cfg.Rules, "new rules text")
	}
}

// TestWriteRules_EmptyRulesAllowed asserts that WriteRules accepts an empty
// string (clearing the rules field) without error.
func TestWriteRules_EmptyRulesAllowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte("rules: some text\ngames:\n  - spark\n"), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	if err := classrules.WriteRules(path, nil, ""); err != nil {
		t.Fatalf("WriteRules with empty string must succeed, got: %v", err)
	}

	cfg, err := classrules.LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile after WriteRules(empty): %v", err)
	}
	if cfg.Rules != "" {
		t.Errorf("expected empty rules after WriteRules(\"\"), got %q", cfg.Rules)
	}
}

// TestWriteRules_NilLoaderIsNoOp asserts that WriteRules with a nil loader
// succeeds and does not panic (reload is skipped).
func TestWriteRules_NilLoaderIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(path, []byte("rules: original\n"), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// Must not panic with nil loader.
	if err := classrules.WriteRules(path, nil, "updated"); err != nil {
		t.Fatalf("WriteRules(nil loader): %v", err)
	}
}
