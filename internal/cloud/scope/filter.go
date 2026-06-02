// Package scope provides pure domain functions for the 4-tier observation
// visibility model: personal < department < project < team.
//
// All functions are side-effect-free and exhaustively table-tested.
package scope

import "strings"

// Principal represents the authenticated caller requesting access.
type Principal struct {
	Email      string   // Authenticated email address.
	Department string   // Department from the user directory.
	Enrolled   []string // Projects the principal is enrolled in (includes "general" for team access).
}

// Attrs represents the attributes of the observation being filtered.
type Attrs struct {
	Scope       string // One of: personal, department, project, team.
	UserEmail   string // Email of the user who authored the observation.
	Department  string // Department of the author at time of storage.
	Project     string // Project the observation belongs to.
	UserDeleted bool   // True if the author's account has been deleted.
	// NOTE: user_deleted does NOT change visibility — the original scope rules apply.
}

// Visible returns true when principal p is allowed to see an observation with
// attributes a under the 4-tier scope ladder:
//
//   - personal:   only the author (p.Email == a.UserEmail)
//   - department: same department AND principal is enrolled in a.Project
//   - project:    principal is enrolled in a.Project
//   - team:       principal is enrolled in "general" (the team-wide convention)
//
// Unknown or empty scopes are NEVER visible — they are not coerced to a default
// tier. Use NormalizeScope separately before storing if defaulting is desired.
// user_deleted on the observation does NOT alter these rules.
func Visible(p Principal, a Attrs) bool {
	// You always see what you authored, regardless of scope tier or department changes.
	if a.UserEmail != "" && strings.EqualFold(a.UserEmail, p.Email) {
		return true
	}

	// Use raw canonical check — do NOT normalize unknown scopes to a default tier,
	// as that would grant visibility to malformed/legacy observations.
	normalized := strings.TrimSpace(strings.ToLower(a.Scope))
	switch normalized {
	case "personal":
		return p.Email == a.UserEmail

	case "department":
		return p.Department == a.Department && isEnrolled(p, a.Project)

	case "project":
		return isEnrolled(p, a.Project)

	case "team":
		return isEnrolled(p, "general")

	default:
		// Unknown scopes (including empty, "global", "org", etc.) are never visible.
		return false
	}
}

// NormalizeScope maps a raw scope string to one of the 4 canonical tier names.
// Unknown or empty values default to "project" (default-narrower rule).
// Valid values pass through case-insensitively.
func NormalizeScope(s string) string {
	v := strings.TrimSpace(strings.ToLower(s))
	switch v {
	case "personal", "department", "project", "team":
		return v
	default:
		return "project"
	}
}

// isEnrolled returns true when project is in p.Enrolled.
func isEnrolled(p Principal, project string) bool {
	for _, ep := range p.Enrolled {
		if ep == project {
			return true
		}
	}
	return false
}
