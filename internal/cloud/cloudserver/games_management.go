package cloudserver

import "os"

// resolveClassrulesFilePath returns the classification-rules.yaml path for the
// admin games handlers. Prefers the value set via WithClassrulesFilePath; falls
// back to the ENGRAM_CLASSIFICATION_RULES env var (same var the MCP uses).
func (s *CloudServer) resolveClassrulesFilePath() string {
	if s.classrulesFilePath != "" {
		return s.classrulesFilePath
	}
	return os.Getenv("ENGRAM_CLASSIFICATION_RULES")
}

// listGamesFunc returns the classrulesCurrentGamesFn closure registered via
// WithClassrulesCurrentGames, or nil when classrules is not configured.
// The admin games handler uses this to display the current in-memory games list.
func (s *CloudServer) listGamesFunc() func() []string {
	return s.classrulesCurrentGamesFn
}
