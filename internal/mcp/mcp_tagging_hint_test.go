package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/classrules"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// TestHandleSaveTaggingHint covers the server-side soft nudge for juego tagging.
//
// Three cases as required by the spec:
//  1. Games configured AND juego NOT provided → tagging_hint present in response.
//  2. Games configured AND juego provided     → tagging_hint ABSENT.
//  3. No games configured (ClassRules nil)   → tagging_hint ABSENT.
//
// The nudge is INFORMATIONAL only — the save must succeed in all cases.
func TestHandleSaveTaggingHint(t *testing.T) {
	withGames := &classrules.Config{
		Games: []string{"game-alpha", "game-beta"},
	}

	tests := []struct {
		name          string
		cfg           MCPConfig
		args          map[string]any
		wantHint      bool
		wantSaveOK    bool
	}{
		{
			name: "games configured, juego omitted — hint present",
			cfg:  MCPConfig{ClassRules: withGames},
			args: map[string]any{
				"title":   "Bug in login flow",
				"content": "**What**: login crash\n**Why**: nil pointer",
				"type":    "bugfix",
				"project": "engram",
			},
			wantHint:   true,
			wantSaveOK: true,
		},
		{
			name: "games configured, juego provided — hint absent",
			cfg:  MCPConfig{ClassRules: withGames},
			args: map[string]any{
				"title":   "Bug in login flow",
				"content": "**What**: login crash\n**Why**: nil pointer",
				"type":    "bugfix",
				"project": "engram",
				"juego":   "game-alpha",
			},
			wantHint:   false,
			wantSaveOK: true,
		},
		{
			name: "no games configured (ClassRules nil) — hint absent",
			cfg:  MCPConfig{},
			args: map[string]any{
				"title":   "Bug in login flow",
				"content": "**What**: login crash\n**Why**: nil pointer",
				"type":    "bugfix",
				"project": "engram",
			},
			wantHint:   false,
			wantSaveOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newMCPTestStore(t)
			h := handleSave(s, tt.cfg, NewSessionActivity(10*time.Minute))

			req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: tt.args}}
			res, err := h(context.Background(), req)
			if err != nil {
				t.Fatalf("handler returned unexpected error: %v", err)
			}
			if tt.wantSaveOK && res.IsError {
				t.Fatalf("save must succeed but got error: %s", callResultText(t, res))
			}

			text := callResultText(t, res)

			// Parse the JSON envelope to inspect tagging_hint.
			var envelope map[string]any
			if err := json.Unmarshal([]byte(text), &envelope); err != nil {
				t.Fatalf("response is not valid JSON: %v\nraw: %s", err, text)
			}

			_, hintPresent := envelope["tagging_hint"]

			if tt.wantHint && !hintPresent {
				t.Errorf("expected tagging_hint in response envelope, but it was absent\nenvelope: %s", text)
			}
			if !tt.wantHint && hintPresent {
				t.Errorf("expected NO tagging_hint in response envelope, but found: %v\nenvelope: %s", envelope["tagging_hint"], text)
			}
		})
	}
}
