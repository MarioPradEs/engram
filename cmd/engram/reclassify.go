package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// secretPatterns are baseline patterns used to detect observations that likely
// contain credentials or other secrets. These mirror the patterns in
// publish-project.ps1 and are intentionally conservative.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(API[_-]?KEY|SECRET[_-]?KEY|ACCESS[_-]?TOKEN|AUTH[_-]?TOKEN)\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PWD)\s*[:=]\s*\S{6,}`),
	regexp.MustCompile(`(?i)sk-[a-zA-Z0-9]{20,}`),          // OpenAI-style keys
	regexp.MustCompile(`(?i)ghp_[a-zA-Z0-9]{36}`),           // GitHub personal access tokens
	regexp.MustCompile(`(?i)Bearer\s+[a-zA-Z0-9\-._~+/]{20,}=*`), // bare Bearer tokens (≥20 char body to avoid natural-language false positives)
}

// clientNDAProjects is the set of project-name prefixes that indicate client or NDA
// work that must never be synced. Matched case-insensitively.
var clientNDAProjects = []string{
	"client-",
	"client/",
	"nda-",
	"nda/",
}

// ReclassifyOutcome describes the result of classifying a single observation.
type ReclassifyOutcome struct {
	ObsID  int64
	Result string // classified | skipped_personal | skipped_client_nda | skipped_secret_scan
}

// classifyObservation applies the baseline classification rules and returns the outcome
// for a single observation. The obs Project field may be nil.
func classifyObservation(obs *store.Observation) string {
	// Rule 1: personal scope — never classify (local-only by design).
	if obs.Scope == "personal" {
		return "skipped_personal"
	}

	// Rule 2: client/NDA project — never upload.
	project := ""
	if obs.Project != nil {
		project = strings.ToLower(strings.TrimSpace(*obs.Project))
	}
	for _, prefix := range clientNDAProjects {
		if strings.HasPrefix(project, prefix) {
			return "skipped_client_nda"
		}
	}

	// Rule 3: secret scan — detect credentials in content.
	for _, pat := range secretPatterns {
		if pat.MatchString(obs.Content) {
			return "skipped_secret_scan"
		}
	}

	return "classified"
}

// cmdReclassify implements the `engram reclassify` command. It iterates all local
// observations, applies classification rules, stamps classified_by_v2 on eligible
// observations, calls MarkReclassifyComplete, and prints a per-outcome summary.
func cmdReclassify(cfg store.Config) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
	}
	defer s.Close()

	// Fetch all observations (no scope/project filter — reclassify scans everything).
	obs, err := s.AllObservationsFull()
	if err != nil {
		fatal(fmt.Errorf("reclassify: list observations: %w", err))
	}

	counts := map[string]int{
		"classified":          0,
		"skipped_personal":    0,
		"skipped_client_nda":  0,
		"skipped_secret_scan": 0,
	}

	for i := range obs {
		o := &obs[i]
		outcome := classifyObservation(o)
		counts[outcome]++

		if outcome == "classified" {
			if err := s.StampClassifiedByV2(o.ID); err != nil {
				// Non-fatal per obs — log and continue.
				fmt.Printf("  [warn] obs#%d: stamp classified_by_v2: %v\n", o.ID, err)
			}
		}
	}

	// Mark reclassification complete so the push gate is lifted.
	if err := s.MarkReclassifyComplete(store.DefaultSyncTargetKey); err != nil {
		fatal(fmt.Errorf("reclassify: mark complete: %w", err))
	}

	fmt.Printf("Reclassification complete:\n")
	fmt.Printf("  classified:          %d\n", counts["classified"])
	fmt.Printf("  skipped_personal:    %d\n", counts["skipped_personal"])
	fmt.Printf("  skipped_client_nda:  %d\n", counts["skipped_client_nda"])
	fmt.Printf("  skipped_secret_scan: %d\n", counts["skipped_secret_scan"])
}
