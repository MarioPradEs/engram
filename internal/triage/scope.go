package triage

// ToInternalScope converts a UI-vocabulary scope value ("shared" or "personal")
// to the internal 4-tier scope stored in the database.
//
// Mapping (REQ-23):
//
//	"shared"  → "team"     (passes Gate B; visible to enrolled team members)
//	anything  → "personal" (dropped by Gate B; never leaves the machine through sync)
//
// This function is the single source of truth for UI→internal conversion.
// It MUST NOT write "project" or "department" for any new triage-driven mutation.
func ToInternalScope(ui string) string {
	if ui == "shared" {
		return "team"
	}
	return "personal"
}

// UIScopeOf converts an internal stored scope value to the UI vocabulary and
// reports whether the observation needs triage (REQ-24).
//
// Mapping:
//
//	"team"       → ("shared",   false)  — explicitly shared; passes Gate B
//	"personal"   → ("personal", false)  — explicitly personal; dropped by Gate B
//	"project"    → ("personal", true)   — legacy; sync-eligible today but never shown as shared
//	"department" → ("personal", true)   — legacy; same rationale
//	anything else (incl. "")  → ("personal", true)  — defensive needs-triage
//
// The ui return value is safe to display directly in HTML badges.
// needsTriage=true means the observation must show a "needs triage" badge,
// not "shared" or "personal", regardless of the project's default_scope.
func UIScopeOf(scope string) (ui string, needsTriage bool) {
	switch scope {
	case "team":
		return "shared", false
	case "personal":
		return "personal", false
	default:
		// Covers "project", "department", "", and any unrecognised value.
		// These rows are NEVER silently treated as shared (REQ-24).
		return "personal", true
	}
}
