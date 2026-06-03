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

// BuildInstructions appends the operator's scope-classification rules to the
// base MCP server instructions. When cfg is nil or cfg.Rules is empty, the
// base instructions are returned unchanged.
//
// The injected section is clearly delimited so operators can review what agents
// receive.
func BuildInstructions(base string, cfg *Config) string {
	if cfg == nil || cfg.Rules == "" {
		return base
	}
	return base + "\n\n## Scope Classification Rules (operator-configured)\n\n" + cfg.Rules
}
