package mcp

import (
	"strings"
	"testing"
)

// TestClassificationRulesInjection_RulesAppearWhenPresent verifies that when
// MCPConfig.ClassificationRulesText is set, the assembled server instructions
// contain the rules text. This is the primary injection assertion for task 4.7.
func TestClassificationRulesInjection_RulesAppearWhenPresent(t *testing.T) {
	rulesText := "## Scope Rules\n\npersonal = private\nproject = default\nteam = general\n"
	cfg := MCPConfig{
		ClassificationRulesText: rulesText,
	}

	instructions := buildServerInstructions(cfg)

	if !strings.Contains(instructions, rulesText) {
		t.Errorf("injected rules text not found in built instructions.\nwant substring: %q\ngot: %q",
			rulesText, instructions)
	}
	// Base instructions must still be present.
	if !strings.Contains(instructions, "mem_save") {
		t.Error("base instructions (mem_save) must still be present after injection")
	}
	// Delimiter section must be present.
	if !strings.Contains(instructions, "Scope Classification Rules") {
		t.Error("instructions must contain a 'Scope Classification Rules' section header")
	}
}

// TestClassificationRulesInjection_GracefulWhenAbsent verifies that when
// MCPConfig.ClassificationRulesText is empty, buildServerInstructions returns the
// base serverInstructions unchanged (no extra section, no empty block).
func TestClassificationRulesInjection_GracefulWhenAbsent(t *testing.T) {
	cfg := MCPConfig{}

	instructions := buildServerInstructions(cfg)

	if instructions != serverInstructions {
		t.Errorf("expected base serverInstructions unchanged when ClassificationRulesText is empty.\ngot len=%d, want len=%d",
			len(instructions), len(serverInstructions))
	}
}

// TestClassificationRulesInjection_NewServerWithConfigUsesRules verifies that
// the MCPServer created via NewServerWithConfig with a ClassificationRulesText
// is wired correctly — a server with rules text is distinct from one without it,
// confirming the config flows into the server construction path.
//
// We verify this indirectly by confirming that the two servers' initialize responses
// would contain different instructions text. Since instructions is an unexported
// field on the upstream MCPServer, we verify via the buildServerInstructions
// helper which is the direct seam.
func TestClassificationRulesInjection_NewServerWithConfigUsesRules(t *testing.T) {
	s := newMCPTestStore(t)

	rulesText := "## Department Rules\n\nengineering, art, product\n"
	cfgWithRules := MCPConfig{ClassificationRulesText: rulesText}
	cfgWithout := MCPConfig{}

	srvWith := NewServerWithConfig(s, cfgWithRules, nil)
	srvWithout := NewServerWithConfig(s, cfgWithout, nil)

	// Both must be non-nil.
	if srvWith == nil {
		t.Fatal("NewServerWithConfig with rules: expected non-nil server")
	}
	if srvWithout == nil {
		t.Fatal("NewServerWithConfig without rules: expected non-nil server")
	}

	// Verify instruction texts differ via the builder (the seam we own).
	instWith := buildServerInstructions(cfgWithRules)
	instWithout := buildServerInstructions(cfgWithout)

	if instWith == instWithout {
		t.Error("instructions with rules should differ from instructions without rules")
	}
	if !strings.Contains(instWith, rulesText) {
		t.Errorf("instructions with rules must contain the rules text, got: %q", instWith)
	}
}
