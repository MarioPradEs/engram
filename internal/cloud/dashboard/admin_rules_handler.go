package dashboard

import (
	"log"
	"net/http"
)

// handleAdminRules serves GET /dashboard/admin/rules.
// Admin-only — returns 403 for non-admin principals.
//
// Renders the Rules editor page with a large textarea pre-filled with the
// current cfg.Rules markdown text sourced from the ListRules closure.
// Flash messages are carried via query params (?flash=...&flashErr=1).
func (h *handlers) handleAdminRules(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var currentRules string
	if h.cfg.ListRules != nil {
		currentRules = h.cfg.ListRules()
	}

	flashMsg := r.URL.Query().Get("flash")
	flashErr := r.URL.Query().Get("flashErr") == "1"

	pendingCount := h.pendingDeletionCount(r.Context())
	component := Layout("Admin — Rules", p.DisplayName(), "admin", true,
		AdminRulesPage(currentRules, flashMsg, flashErr, pendingCount))
	if err := component.Render(r.Context(), w); err != nil {
		log.Printf("[dashboard] handleAdminRules render: %v", err)
	}
}

// handleAdminRulesPost serves POST /dashboard/admin/rules.
// Admin-only — returns 403 for non-admin principals.
//
// Reads the "rules" form field and persists it via the SaveRules closure.
// On success, redirects 303 to /dashboard/admin/rules with a success flash.
// On error, redirects 303 with an error flash.
// When SaveRules is nil, returns 501 Not Implemented.
func (h *handlers) handleAdminRulesPost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if h.cfg.SaveRules == nil {
		http.Error(w, "rules save not configured", http.StatusNotImplemented)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	newRules := r.FormValue("rules")

	if err := h.cfg.SaveRules(newRules); err != nil {
		log.Printf("[dashboard] handleAdminRulesPost SaveRules: %v", err)
		redirectWithFlash(w, r, "/dashboard/admin/rules", "Save failed: "+err.Error(), true)
		return
	}

	redirectWithFlash(w, r, "/dashboard/admin/rules", "Rules saved successfully.", false)
}
