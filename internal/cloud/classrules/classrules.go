// Package classrules provides the classification-rules YAML loader and MCP
// instruction builder for the Engram fork.
//
// Decision #805 mandates that Viva-specific classification data lives in
// config/data, NOT in Go source. This package ships the MECHANISM:
// - a typed schema for classification-rules.yaml
// - LoadFromFile: reads the operator-supplied config with graceful absent behavior
// - BuildInstructions: appends the rules text to the MCP server instructions so
//   every connected agent receives the operator's scope-classification rules
//
// The real Viva-filled classification-rules.yaml lives in the EngramCloud infra
// repo (out of scope for this fork). Operators provide their own copy.
package classrules

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Department describes one organisational unit and its known aliases.
// Aliases are case-insensitive shortcuts the AI may see in conversation.
type Department struct {
	// Name is the canonical department name (e.g. "engineering").
	Name string `yaml:"name"`
	// Aliases are optional synonyms (e.g. ["eng", "backend"]).
	Aliases []string `yaml:"aliases,omitempty"`
}

// Conventions holds the naming conventions that govern scope routing.
type Conventions struct {
	// GeneralProject is the special project that receives team-scoped
	// observations (e.g. "general").
	GeneralProject string `yaml:"general_project,omitempty"`
	// TeamProjectPrefix is the prefix for team-owned projects
	// (e.g. "team-").
	TeamProjectPrefix string `yaml:"team_project_prefix,omitempty"`
}

// ProjectPattern associates a glob-style pattern with a description used
// in the injected instructions to guide the AI's scope decision.
type ProjectPattern struct {
	// Pattern is a glob-style project name pattern (e.g. "juego-*").
	Pattern string `yaml:"pattern"`
	// Description explains the recommended scope for matching projects.
	Description string `yaml:"description,omitempty"`
}

// Config is the top-level structure for classification-rules.yaml.
type Config struct {
	// Departments is the list of organisational units.
	Departments []Department `yaml:"departments,omitempty"`
	// Conventions holds naming conventions for scope routing.
	Conventions Conventions `yaml:"conventions,omitempty"`
	// ProjectPatterns maps project name patterns to scope guidance.
	ProjectPatterns []ProjectPattern `yaml:"project_patterns,omitempty"`
	// Games is the controlled vocabulary for the juego facet. Each entry is a
	// canonical game slug that the connected AI may tag observations with.
	// When empty or absent, juego tagging is disabled entirely.
	Games []string `yaml:"games,omitempty"`
	// Rules is the free-form instruction text injected verbatim into
	// the MCP server instructions. This is the primary injection payload;
	// the structured fields above are available for programmatic use.
	Rules string `yaml:"rules,omitempty"`
}

// LoadFromFile reads classification-rules.yaml from path and returns the parsed
// Config.
//
// Absent file: returns (nil, nil) — graceful; operators may not have provided a
// config yet and the MCP server should start normally.
//
// Empty file: returns a zero-value Config and no error — operators may stage
// the file before filling it.
//
// Invalid YAML: returns (nil, error).
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("classrules: read %s: %w", path, err)
	}

	if len(data) == 0 {
		return &Config{}, nil
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("classrules: parse %s: %w", path, err)
	}
	return &cfg, nil
}

// BuildInstructions appends the operator's scope-classification rules and the
// memory tagging guidance to the base MCP server instructions.
//
// When cfg is nil or both cfg.Rules and cfg.Games are empty, the base
// instructions are returned unchanged (graceful absent behavior).
//
// When cfg.Games is non-empty, a "Memory Tagging Rules" block is appended that
// instructs the AI to pick juego from the controlled list and infer tipo freely,
// while telling it NOT to supply departamento or proyecto (server-authoritative).
//
// When cfg.Games is empty or nil, the tagging block instructs the AI NOT to set
// juego (empty vocabulary disables juego tagging entirely).
//
// The injected sections are clearly delimited so operators can review what
// agents receive.
func BuildInstructions(base string, cfg *Config) string {
	if cfg == nil {
		return base
	}

	result := base

	// Append free-form scope-classification rules when present.
	if cfg.Rules != "" {
		result += "\n\n## Scope Classification Rules (operator-configured)\n\n" + cfg.Rules
	}

	// Append four-facet tagging guidance only when the Games field has been
	// explicitly set (non-nil slice). A nil Games field means the operator has
	// not configured tagging at all — no guidance block is injected. An
	// explicitly empty slice (games: []) means tagging is configured but the
	// vocabulary is empty — inject the "do not set juego" instruction.
	if cfg.Games != nil {
		result += "\n\n" + buildTaggingBlock(cfg.Games)
	}

	return result
}

// buildTaggingBlock constructs the memory tagging instruction text for the given
// games vocabulary. When vocab is empty or nil, juego tagging is disabled.
func buildTaggingBlock(vocab []string) string {
	var sb strings.Builder
	sb.WriteString("## Memory Tagging Rules (operator-configured)\n")
	sb.WriteString("On every mem_save, classify the memory along these facets and pass juego/tipo as arguments:\n")

	if len(vocab) > 0 {
		sb.WriteString("- juego: pick EXACTLY ONE game from the controlled list below, or omit if no clear match. NEVER invent a name not in the list.\n")
		sb.WriteString("  Allowed games: [")
		for i, g := range vocab {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(g)
		}
		sb.WriteString("]\n")
	} else {
		sb.WriteString("- juego: do not set juego (no vocabulary configured).\n")
	}

	sb.WriteString("- tipo: infer the content type (e.g. bug, decision, hallazgo, solucion). Free inference; omit if unclear.\n")
	sb.WriteString("- departamento / proyecto: do NOT supply these arguments. The server derives them from your authenticated identity and workspace. Any value you supply for these will be overwritten by the server.\n")

	return sb.String()
}
