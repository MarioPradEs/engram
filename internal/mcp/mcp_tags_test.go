package mcp

import (
	"testing"
)

// TestBuildTagsFromArgs verifies the juego/tipo tag validation and merge logic.
// juego is validated against the games vocabulary; tipo is accepted freely.
// departamento and proyecto are NEVER set at this layer (identity is server-derived).
func TestBuildTagsFromArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]any
		vocab    []string
		wantTags map[string]string
	}{
		{
			name:     "juego in vocab and tipo set",
			args:     map[string]any{"juego": "game-a", "tipo": "bug"},
			vocab:    []string{"game-a", "game-b"},
			wantTags: map[string]string{"juego": "game-a", "tipo": "bug"},
		},
		{
			name:     "juego out of vocab — dropped, tipo preserved",
			args:     map[string]any{"juego": "game-c", "tipo": "bug"},
			vocab:    []string{"game-a", "game-b"},
			wantTags: map[string]string{"tipo": "bug"},
		},
		{
			name:     "no juego arg, tipo only",
			args:     map[string]any{"tipo": "decision"},
			vocab:    []string{"game-a"},
			wantTags: map[string]string{"tipo": "decision"},
		},
		{
			name:     "both absent — nil tags (RD4: nil not empty map)",
			args:     map[string]any{},
			vocab:    []string{"game-a"},
			wantTags: nil,
		},
		{
			name:     "empty vocab disables juego — tipo still accepted",
			args:     map[string]any{"juego": "game-a", "tipo": "bug"},
			vocab:    []string{},
			wantTags: map[string]string{"tipo": "bug"},
		},
		{
			name:     "tipo only no juego arg",
			args:     map[string]any{"tipo": "hallazgo"},
			vocab:    []string{"game-a"},
			wantTags: map[string]string{"tipo": "hallazgo"},
		},
		{
			name:     "departamento arg ignored — not set by this layer",
			args:     map[string]any{"juego": "game-a", "tipo": "bug", "departamento": "qa"},
			vocab:    []string{"game-a"},
			wantTags: map[string]string{"juego": "game-a", "tipo": "bug"},
		},
		{
			name:     "proyecto arg ignored — not set by this layer",
			args:     map[string]any{"tipo": "bug", "proyecto": "game-x"},
			vocab:    []string{"game-a"},
			wantTags: map[string]string{"tipo": "bug"},
		},
		{
			name:     "nil vocab disables juego",
			args:     map[string]any{"juego": "game-a", "tipo": "decision"},
			vocab:    nil,
			wantTags: map[string]string{"tipo": "decision"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildTagsFromArgs(tt.args, tt.vocab)

			if tt.wantTags == nil {
				if got != nil {
					t.Errorf("expected nil tags, got %v", got)
				}
				return
			}

			if got == nil {
				t.Fatalf("expected tags %v, got nil", tt.wantTags)
			}

			// Check expected keys are present with correct values.
			for k, v := range tt.wantTags {
				if got[k] != v {
					t.Errorf("tags[%q]: got %q, want %q", k, got[k], v)
				}
			}

			// Check no unexpected keys (specifically: departamento and proyecto
			// MUST NOT be present at this layer).
			for k := range got {
				if _, expected := tt.wantTags[k]; !expected {
					t.Errorf("unexpected key in tags: %q = %q (should not be set at MCP layer)", k, got[k])
				}
			}
		})
	}
}

// TestBuildTagsFromArgsPartialTagging verifies that missing facets are absent,
// not placeholder empty strings (memory-tagging §Partial tagging).
func TestBuildTagsFromArgsPartialTagging(t *testing.T) {
	args := map[string]any{"tipo": "hallazgo"}
	vocab := []string{"game-a", "game-b"}

	got := buildTagsFromArgs(args, vocab)

	if got == nil {
		t.Fatal("expected non-nil tags for tipo-only args")
	}
	if _, hasJuego := got["juego"]; hasJuego {
		t.Errorf("juego must not be present when not supplied; got %q", got["juego"])
	}
	if got["tipo"] != "hallazgo" {
		t.Errorf("tipo: got %q, want %q", got["tipo"], "hallazgo")
	}
	if v, hasDept := got["departamento"]; hasDept {
		t.Errorf("departamento must not be set at MCP layer; got %q", v)
	}
}
