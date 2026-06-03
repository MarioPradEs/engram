package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/mcp"
	"github.com/Gentleman-Programming/engram/internal/store"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// TestCmdMCP_ClassificationRulesInjected verifies that when
// ENGRAM_CLASSIFICATION_RULES points to a valid classification-rules.yaml,
// the MCPConfig.ClassificationRulesText is populated with the rules text
// from that file. This proves the injection mechanism is wired end-to-end
// through cmdMCP (task 4.7).
func TestCmdMCP_ClassificationRulesInjected(t *testing.T) {
	cfg := testConfig(t)

	// Write a fixture classification-rules.yaml with a known rules block.
	rulesDir := t.TempDir()
	rulesPath := filepath.Join(rulesDir, "classification-rules.yaml")
	rulesContent := `departments:
  - name: engineering
conventions:
  general_project: "general"
  team_project_prefix: "team-"
rules: |
  ## Scope Rules
  personal = private
  project = default (game team)
  team = general project
`
	if err := os.WriteFile(rulesPath, []byte(rulesContent), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	t.Setenv("ENGRAM_CLASSIFICATION_RULES", rulesPath)

	var capturedCfg mcp.MCPConfig
	oldNew := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNew })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		capturedCfg = mcpCfg
		return oldNew(s, mcpCfg, allowlist)
	}

	oldServe := serveMCP
	t.Cleanup(func() { serveMCP = oldServe })
	serveMCP = func(srv *mcpserver.MCPServer, opts ...mcpserver.StdioOption) error {
		return nil
	}

	withArgs(t, "engram", "mcp")
	_, _ = captureOutput(t, func() { cmdMCP(cfg) })

	if capturedCfg.ClassificationRulesText == "" {
		t.Fatal("MCPConfig.ClassificationRulesText should be non-empty when ENGRAM_CLASSIFICATION_RULES is set")
	}
	if !strings.Contains(capturedCfg.ClassificationRulesText, "Scope Rules") {
		t.Errorf("ClassificationRulesText should contain rules content, got: %q", capturedCfg.ClassificationRulesText)
	}
}

// TestCmdMCP_ClassificationRulesGracefulWhenAbsent verifies that cmdMCP starts
// normally when ENGRAM_CLASSIFICATION_RULES is not set. The MCP server must
// start with the base instructions unchanged (graceful absent behavior).
func TestCmdMCP_ClassificationRulesGracefulWhenAbsent(t *testing.T) {
	cfg := testConfig(t)

	// Ensure env var is not set.
	t.Setenv("ENGRAM_CLASSIFICATION_RULES", "")

	var capturedCfg mcp.MCPConfig
	oldNew := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNew })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		capturedCfg = mcpCfg
		return oldNew(s, mcpCfg, allowlist)
	}

	oldServe := serveMCP
	t.Cleanup(func() { serveMCP = oldServe })
	serveMCP = func(srv *mcpserver.MCPServer, opts ...mcpserver.StdioOption) error {
		return nil
	}

	withArgs(t, "engram", "mcp")
	_, _ = captureOutput(t, func() { cmdMCP(cfg) })

	// ClassificationRulesText must be empty — no rules injected.
	if capturedCfg.ClassificationRulesText != "" {
		t.Errorf("expected empty ClassificationRulesText when env var absent, got: %q",
			capturedCfg.ClassificationRulesText)
	}
}

// TestCmdMCP_ClassificationRulesGracefulWhenFileAbsent verifies that cmdMCP
// starts normally when ENGRAM_CLASSIFICATION_RULES points to a non-existent
// file. The MCP server must start without fataling.
func TestCmdMCP_ClassificationRulesGracefulWhenFileAbsent(t *testing.T) {
	cfg := testConfig(t)

	// Point to a non-existent file.
	t.Setenv("ENGRAM_CLASSIFICATION_RULES", filepath.Join(t.TempDir(), "does-not-exist.yaml"))

	var capturedCfg mcp.MCPConfig
	oldNew := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNew })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		capturedCfg = mcpCfg
		return oldNew(s, mcpCfg, allowlist)
	}

	oldServe := serveMCP
	t.Cleanup(func() { serveMCP = oldServe })
	serveMCP = func(srv *mcpserver.MCPServer, opts ...mcpserver.StdioOption) error {
		return nil
	}

	withArgs(t, "engram", "mcp")
	// Must not panic or call fatal.
	_, _ = captureOutput(t, func() { cmdMCP(cfg) })

	// ClassificationRulesText must be empty — graceful absent.
	if capturedCfg.ClassificationRulesText != "" {
		t.Errorf("expected empty ClassificationRulesText for absent file, got: %q",
			capturedCfg.ClassificationRulesText)
	}
}
