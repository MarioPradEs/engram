package cloudserver

import (
	"os"

	"github.com/Gentleman-Programming/engram/internal/cloud/dashboard"
)

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

// listColorsFunc returns the classrulesCurrentColorsFn closure registered via
// WithClassrulesCurrentColors, or nil when classrules is not configured.
// The admin color-map editor calls this on every GET /dashboard/admin/games.
func (s *CloudServer) listColorsFunc() func() (map[string]string, map[string]string) {
	return s.classrulesCurrentColorsFn
}

// writeGameColorFunc returns the classrulesWriteColorFn closure registered via
// WithClassrulesWriteColor, or nil when classrules is not configured (in which
// case the dashboard handler returns 501 Not Implemented).
func (s *CloudServer) writeGameColorFunc() func(name, color string) error {
	return s.classrulesWriteColorFn
}

// writeDeptColorFunc returns the classrulesWriteDeptColorFn closure registered via
// WithClassrulesWriteDeptColor, or nil when classrules is not configured.
func (s *CloudServer) writeDeptColorFunc() func(name, color string) error {
	return s.classrulesWriteDeptColorFn
}

// saveGameFunc returns the classrulesSaveGameFn closure registered via WithSaveGame,
// or nil when not configured.
func (s *CloudServer) saveGameFunc() func(newGames []string, newGameColors map[string]string) error {
	return s.classrulesSaveGameFn
}

// deleteGameFunc returns the classrulesDeleteGameFn closure registered via WithDeleteGame,
// or nil when not configured.
func (s *CloudServer) deleteGameFunc() func(newGames []string, newGameColors map[string]string) error {
	return s.classrulesDeleteGameFn
}

// listDeptsFunc returns the classrulesCurrentDeptsFn closure registered via
// WithClassrulesCurrentDepts, or nil when classrules is not configured.
func (s *CloudServer) listDeptsFunc() func() []string {
	return s.classrulesCurrentDeptsFn
}

// listDeptEntriesFunc returns the classrulesCurrentDeptEntriesFn closure registered via
// WithClassrulesCurrentDeptEntries, or nil when classrules is not configured.
func (s *CloudServer) listDeptEntriesFunc() func() []dashboard.DeptEntry {
	return s.classrulesCurrentDeptEntriesFn
}

// saveDeptFunc returns the classrulesSaveDeptFn closure registered via WithSaveDept,
// or nil when not configured.
func (s *CloudServer) saveDeptFunc() func(newDepts []dashboard.DeptEntry, newDeptColors map[string]string) error {
	return s.classrulesSaveDeptFn
}

// deleteDeptFunc returns the classrulesDeleteDeptFn closure registered via WithDeleteDept,
// or nil when not configured.
func (s *CloudServer) deleteDeptFunc() func(newDepts []dashboard.DeptEntry, newDeptColors map[string]string) error {
	return s.classrulesDeleteDeptFn
}

// listRulesFunc returns the classrulesListRulesFn closure registered via
// WithClassrulesListRules, or nil when classrules is not configured.
func (s *CloudServer) listRulesFunc() func() string {
	return s.classrulesListRulesFn
}

// saveRulesFunc returns the classrulesSaveRulesFn closure registered via
// WithClassrulesSaveRules, or nil when classrules is not configured.
func (s *CloudServer) saveRulesFunc() func(rules string) error {
	return s.classrulesSaveRulesFn
}
