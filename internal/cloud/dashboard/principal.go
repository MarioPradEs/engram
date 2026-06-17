package dashboard

import (
	"net/http"
	"strings"
)

// Principal represents the authenticated dashboard user. It is a read-only
// view over MountConfig closures — no context reads for identity are performed
// inside handlers. Satisfies Design Decision 6.
type Principal struct {
	displayName string
	isAdmin     bool
	email       string // NEW (D2) — resolved from GetUserEmail closure (verified JWT email claim)
}

// DisplayName returns the display name for this principal.
// An empty or whitespace-only name falls back to "OPERATOR".
func (p Principal) DisplayName() string {
	if strings.TrimSpace(p.displayName) == "" {
		return "OPERATOR"
	}
	return p.displayName
}

// IsAdmin returns whether this principal has admin privileges.
func (p Principal) IsAdmin() bool { return p.isAdmin }

// Email returns the caller's verified email address (from the JWT email claim).
// Returns "" when no email is available (e.g. static admin token sessions).
func (p Principal) Email() string { return p.email }

// principalFromRequest derives a Principal from the current request using the
// MountConfig closures. Handlers call p := h.principalFromRequest(r) and never
// read r.Context() for identity.
func (h *handlers) principalFromRequest(r *http.Request) Principal {
	name := ""
	if h.cfg.GetDisplayName != nil {
		name = strings.TrimSpace(h.cfg.GetDisplayName(r))
	}
	admin := false
	if h.cfg.IsAdmin != nil {
		admin = h.cfg.IsAdmin(r)
	}
	email := ""
	if h.cfg.GetUserEmail != nil {
		email = strings.TrimSpace(h.cfg.GetUserEmail(r))
	}
	return Principal{displayName: name, isAdmin: admin, email: email}
}
