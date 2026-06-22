package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// adminRulesMux builds a minimal mux with the Rules admin page wired.
// listRules is called by GET to get the current rules text.
// saveRules is called by POST to persist new rules text.
// isAdmin controls whether the principal is treated as an admin.
func adminRulesMux(listRules func() string, saveRules func(rules string) error, isAdmin bool) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:   func(_ *http.Request) bool { return isAdmin },
		ListRules: listRules,
		SaveRules: saveRules,
	})
	return mux
}

// ─── GET /dashboard/admin/rules ──────────────────────────────────────────────

// TestAdminRulesGET_RendersTextareaWithCurrentRules asserts that GET
// /dashboard/admin/rules renders a textarea pre-filled with the current rules.
func TestAdminRulesGET_RendersTextareaWithCurrentRules(t *testing.T) {
	currentRules := "## Scope Rules\n\nUse project scope for personal work."
	mux := adminRulesMux(func() string { return currentRules }, nil, true)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/rules?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Must render a textarea named "rules".
	if !strings.Contains(body, `name="rules"`) {
		t.Error("expected textarea with name=rules in response body")
	}
	// Textarea must be pre-filled with the current rules text.
	if !strings.Contains(body, "## Scope Rules") {
		t.Error("expected textarea to contain current rules text")
	}
	// Must render a Save button.
	if !strings.Contains(body, "Save") {
		t.Error("expected a Save button in response body")
	}
	// Must render the adminNav with a "Rules" link.
	if !strings.Contains(body, "/dashboard/admin/rules") {
		t.Error("expected adminNav to contain /dashboard/admin/rules link")
	}
}

// TestAdminRulesGET_NonAdminReturns403 asserts that GET /dashboard/admin/rules
// returns 403 when the principal is not an admin.
func TestAdminRulesGET_NonAdminReturns403(t *testing.T) {
	mux := adminRulesMux(func() string { return "some rules" }, nil, false)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/rules?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d", rec.Code)
	}
}

// TestAdminRulesGET_RendersSubNavWithRulesActive asserts that the adminNav
// marks "Rules" as active when visiting /dashboard/admin/rules.
func TestAdminRulesGET_RendersSubNavWithRulesActive(t *testing.T) {
	mux := adminRulesMux(func() string { return "" }, nil, true)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/rules?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	// The active link should have the "active" class.
	// The pattern in adminNav is: <a href="..." class="active">Rules</a>.
	if !strings.Contains(body, `class="active"`) {
		t.Error("expected active class in adminNav for /dashboard/admin/rules")
	}
}

// ─── POST /dashboard/admin/rules ─────────────────────────────────────────────

// TestAdminRulesPOST_SavesRulesAndRedirects asserts that POST
// /dashboard/admin/rules persists the new rules text via SaveRules and
// redirects 303 to /dashboard/admin/rules with a success flash.
func TestAdminRulesPOST_SavesRulesAndRedirects(t *testing.T) {
	var savedRules string
	mux := adminRulesMux(
		func() string { return "old rules" },
		func(rules string) error {
			savedRules = rules
			return nil
		},
		true,
	)

	newRules := "## New Rules\n\nUpdated text."
	form := url.Values{"rules": {newRules}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/rules?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/dashboard/admin/rules") {
		t.Errorf("expected redirect to /dashboard/admin/rules, got %q", loc)
	}
	if !strings.Contains(loc, "flash=") {
		t.Errorf("expected flash param in redirect location, got %q", loc)
	}
	if savedRules != newRules {
		t.Errorf("SaveRules called with %q, want %q", savedRules, newRules)
	}
}

// TestAdminRulesPOST_NonAdminReturns403 asserts that POST /dashboard/admin/rules
// returns 403 when the principal is not an admin.
func TestAdminRulesPOST_NonAdminReturns403(t *testing.T) {
	mux := adminRulesMux(
		func() string { return "" },
		func(_ string) error { return nil },
		false,
	)

	form := url.Values{"rules": {"anything"}}
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/rules?auth=ok",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin POST, got %d", rec.Code)
	}
}

// TestAdminNavContainsRulesLink asserts that the adminNav component renders a
// "Rules" link pointing to /dashboard/admin/rules on all admin pages.
func TestAdminNavContainsRulesLink(t *testing.T) {
	// Use the games page (already has adminNav wired) to verify the new entry.
	mux := adminGamesMux([]string{"spark"}, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/games?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/dashboard/admin/rules") {
		t.Error("expected adminNav to contain /dashboard/admin/rules link after Rules entry added")
	}
}
