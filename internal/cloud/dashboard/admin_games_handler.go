package dashboard

import (
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloud/classrules"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// hexColorRE matches a valid 6-digit CSS hex color (e.g. "#E5C07B").
// Intentionally mirrors classrules.ValidateColors's regex — the handler
// validates before delegating to WriteGameColor so it can return a 400
// without a classrules import cycle.
var hexColorRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// gameNameRE is the allowlist for game and department names accepted by the
// admin save handlers. Names must be 1-100 characters; allowed characters are
// letters, digits, spaces, underscores, dots, and hyphens.
var gameNameRE = regexp.MustCompile(`^[A-Za-z0-9 _.\-]{1,100}$`)

// handleAdminGames serves GET /dashboard/admin/games.
// Admin-only — returns 403 for non-admin principals.
//
// Renders a compact page with:
//  1. Inline color table (one row per game: color picker + Save button).
//  2. Vocabulary editor (textarea for add/remove games, same as before).
//
// Flash messages are carried via query params (?flash=...&flashErr=1)
// set by the redirect-after-save in handleAdminGameColorPost.
func (h *handlers) handleAdminGames(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	games := h.currentGames()

	var gameColors map[string]string
	if h.cfg.ListColors != nil {
		gameColors, _ = h.cfg.ListColors()
	}

	// Read optional flash from redirect query params.
	flashMsg := r.URL.Query().Get("flash")
	flashErr := r.URL.Query().Get("flashErr") == "1"

	component := Layout("Admin — Games", p.DisplayName(), "admin", true,
		AdminGamesCompactPage(games, gameColors, flashMsg, flashErr))
	if err := component.Render(r.Context(), w); err != nil {
		log.Printf("[dashboard] handleAdminGames render: %v", err)
	}
}

// handleAdminGamesPost serves POST /dashboard/admin/games.
// Admin-only — validates the submitted games list and writes it atomically.
func (h *handlers) handleAdminGamesPost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Parse the textarea: one game per line, trimmed, blanks removed.
	raw := r.FormValue("games")
	newGames := parseGamesList(raw)

	// Validate before anything else so we return 400 without touching the file.
	if err := classrules.ValidateGames(newGames); err != nil {
		var gameColors map[string]string
		if h.cfg.ListColors != nil {
			gameColors, _ = h.cfg.ListColors()
		}
		w.WriteHeader(http.StatusBadRequest)
		component := Layout("Admin — Games", p.DisplayName(), "admin", true,
			AdminGamesCompactPage(h.currentGames(), gameColors, err.Error(), true))
		if renderErr := component.Render(r.Context(), w); renderErr != nil {
			log.Printf("[dashboard] handleAdminGamesPost render error: %v", renderErr)
		}
		return
	}

	classrulesPath := h.cfg.ClassrulesFilePath
	if classrulesPath == "" {
		http.Error(w, "classrules not configured", http.StatusInternalServerError)
		return
	}

	// Build a lightweight loader wrapper for WriteGames.
	loaderBridge := &classrulesLoaderBridge{reloadFn: h.cfg.ClassrulesReload}

	// Perform atomic write: validate → temp+rename → reload.
	if err := classrules.WriteGames(classrulesPath, loaderBridge, newGames); err != nil {
		log.Printf("[dashboard] handleAdminGamesPost WriteGames: %v", err)
		var gameColors map[string]string
		if h.cfg.ListColors != nil {
			gameColors, _ = h.cfg.ListColors()
		}
		w.WriteHeader(http.StatusInternalServerError)
		component := Layout("Admin — Games", p.DisplayName(), "admin", true,
			AdminGamesCompactPage(h.currentGames(), gameColors, "Write failed: "+err.Error(), true))
		if renderErr := component.Render(r.Context(), w); renderErr != nil {
			log.Printf("[dashboard] handleAdminGamesPost render error: %v", renderErr)
		}
		return
	}

	// Non-fatal local git auto-commit.
	repoPath := filepath.Dir(classrulesPath)
	if gitErr := users.RunLocalGitCommit(repoPath, classrulesPath, "chore(classrules): update games via admin UI"); gitErr != nil {
		log.Printf("[dashboard] handleAdminGamesPost git commit (non-fatal): %v", gitErr)
	}

	// Redirect-after-POST (PRG pattern) — avoids double-submit on back-button.
	redirectTo := "/dashboard/admin/games?" + url.Values{"flash": {"Games updated successfully."}}.Encode()
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

// handleAdminGameColorPost handles POST /dashboard/admin/games/{name}/color.
//
// Admin-gated (403 when not admin). Accepts a form field "color" with a
// 6-digit hex value (400 when invalid). On success, redirects 303 back to
// /dashboard/admin/games with a flash query param so the GET page shows the
// updated color and a success message (fixes the blank-page-after-Save bug).
func (h *handlers) handleAdminGameColorPost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		http.Error(w, "game name is required", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	color := strings.TrimSpace(r.FormValue("color"))

	// Allow empty string as a valid placeholder (clears the color).
	// Non-empty values must be valid 6-digit hex.
	if color != "" && !hexColorRE.MatchString(color) {
		http.Error(w, "invalid color: must be a 6-digit hex string (e.g. #E5C07B)", http.StatusBadRequest)
		return
	}

	if h.cfg.WriteGameColor == nil {
		http.Error(w, "color write not configured", http.StatusNotImplemented)
		return
	}

	// Reject orphan color keys: the name must exist in the current games list.
	currentGames := h.currentGames()
	found := false
	for _, g := range currentGames {
		if strings.EqualFold(g, name) {
			found = true
			break
		}
	}
	if !found {
		redirectWithFlash(w, r, "/dashboard/admin/games", "game "+name+" does not exist", true)
		return
	}

	if err := h.cfg.WriteGameColor(name, color); err != nil {
		http.Error(w, "failed to save color: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect-after-POST (PRG): 303 back to games page with success flash.
	flash := url.Values{"flash": {"Color saved for " + name + "."}}
	http.Redirect(w, r, "/dashboard/admin/games?"+flash.Encode(), http.StatusSeeOther)
}

// handleAdminGameSavePost handles POST /dashboard/admin/games/save.
//
// Accepts form fields:
//   - "original"  — hidden field; the row's original game name (empty for the add-row).
//   - "name"      — the edited/new game name.
//   - "color"     — a 6-digit hex color string.
//
// Behaviour:
//   - original empty  + name non-empty → ADD game "name" with "color".
//   - original == name                 → color-only update.
//   - original != name (both non-empty) → RENAME: migrate color key, update list.
//
// Validation: name must be non-empty and trimmed; color must be valid hex; no
// duplicate game names after the operation.
// On success redirects 303 to /dashboard/admin/games with a success flash.
// On error redirects 303 to /dashboard/admin/games with an error flash.
func (h *handlers) handleAdminGameSavePost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if h.cfg.SaveGame == nil {
		http.Error(w, "save game not configured", http.StatusNotImplemented)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	original := strings.TrimSpace(r.FormValue("original"))
	name := strings.TrimSpace(r.FormValue("name"))
	color := strings.TrimSpace(r.FormValue("color"))

	// Validate name.
	if name == "" {
		redirectWithFlash(w, r, "/dashboard/admin/games", "game name must not be empty", true)
		return
	}
	if !gameNameRE.MatchString(name) {
		redirectWithFlash(w, r, "/dashboard/admin/games", "invalid game name: use letters, digits, spaces, underscores, dots, or hyphens (1-100 chars)", true)
		return
	}

	// Validate color.
	if color != "" && !hexColorRE.MatchString(color) {
		redirectWithFlash(w, r, "/dashboard/admin/games", "invalid color: must be a 6-digit hex string (e.g. #E5C07B)", true)
		return
	}

	// Build new games list from the current in-memory list.
	currentGames := h.currentGames()
	var gameColors map[string]string
	if h.cfg.ListColors != nil {
		gameColors, _ = h.cfg.ListColors()
	}
	if gameColors == nil {
		gameColors = make(map[string]string)
	} else {
		// Deep copy to avoid mutating the in-memory map.
		copied := make(map[string]string, len(gameColors))
		for k, v := range gameColors {
			copied[k] = v
		}
		gameColors = copied
	}

	var newGames []string
	switch {
	case original == "":
		// ADD: append new game.
		// Duplicate check.
		for _, g := range currentGames {
			if strings.EqualFold(g, name) {
				redirectWithFlash(w, r, "/dashboard/admin/games", "game "+name+" already exists", true)
				return
			}
		}
		newGames = append(currentGames, name)
		if color != "" {
			gameColors[name] = color
		}
	case strings.EqualFold(original, name):
		// COLOR-ONLY update (includes pure case-change renames — treat as color-only).
		newGames = currentGames
		if color != "" {
			gameColors[name] = color
		}
	default:
		// RENAME: replace original with name in the list, migrate color.
		// Duplicate check: ensure new name doesn't conflict with other games.
		for _, g := range currentGames {
			if strings.EqualFold(g, name) && !strings.EqualFold(g, original) {
				redirectWithFlash(w, r, "/dashboard/admin/games", "game "+name+" already exists", true)
				return
			}
		}
		newGames = make([]string, 0, len(currentGames))
		found := false
		for _, g := range currentGames {
			if strings.EqualFold(g, original) {
				newGames = append(newGames, name)
				found = true
			} else {
				newGames = append(newGames, g)
			}
		}
		if !found {
			// original not in list — treat as ADD.
			newGames = append(newGames, name)
		}
		// Migrate color: copy original's color to new name (caller-supplied color overrides).
		// Use case-insensitive key lookup so color is found even if casing differs.
		var oldColorKey string
		var oldColorVal string
		for k, v := range gameColors {
			if strings.EqualFold(k, original) {
				oldColorKey = k
				oldColorVal = v
				break
			}
		}
		if oldColorKey != "" {
			delete(gameColors, oldColorKey)
			if color != "" {
				gameColors[name] = color
			} else {
				gameColors[name] = oldColorVal
			}
		} else if color != "" {
			gameColors[name] = color
		}
	}

	if err := h.cfg.SaveGame(newGames, gameColors); err != nil {
		log.Printf("[dashboard] handleAdminGameSavePost SaveGame: %v", err)
		redirectWithFlash(w, r, "/dashboard/admin/games", "save failed: "+err.Error(), true)
		return
	}

	redirectWithFlash(w, r, "/dashboard/admin/games", "Game saved successfully.", false)
}

// handleAdminGameDeletePost handles POST /dashboard/admin/games/delete.
//
// Accepts form field "name" (the game to delete). Removes it from the games
// list and from graph_colors.games, then writes atomically.
// On success redirects 303 to /dashboard/admin/games with a success flash.
// On error redirects 303 to /dashboard/admin/games with an error flash.
func (h *handlers) handleAdminGameDeletePost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if h.cfg.DeleteGame == nil {
		http.Error(w, "delete game not configured", http.StatusNotImplemented)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		redirectWithFlash(w, r, "/dashboard/admin/games", "game name must not be empty", true)
		return
	}

	currentGames := h.currentGames()
	var gameColors map[string]string
	if h.cfg.ListColors != nil {
		gameColors, _ = h.cfg.ListColors()
	}
	if gameColors == nil {
		gameColors = make(map[string]string)
	} else {
		copied := make(map[string]string, len(gameColors))
		for k, v := range gameColors {
			copied[k] = v
		}
		gameColors = copied
	}

	// Remove the game from the list.
	newGames := make([]string, 0, len(currentGames))
	for _, g := range currentGames {
		if g != name {
			newGames = append(newGames, g)
		}
	}
	// Remove its color.
	delete(gameColors, name)

	// A delete leaving zero games would fail WriteGames validation; allow it
	// by calling DeleteGame with whatever remains (caller validates).
	if err := h.cfg.DeleteGame(newGames, gameColors); err != nil {
		log.Printf("[dashboard] handleAdminGameDeletePost DeleteGame: %v", err)
		redirectWithFlash(w, r, "/dashboard/admin/games", "delete failed: "+err.Error(), true)
		return
	}

	redirectWithFlash(w, r, "/dashboard/admin/games", "Game "+name+" deleted.", false)
}

// redirectWithFlash performs a 303 redirect to target with flash message encoded
// as query params. isError=true encodes flashErr=1.
func redirectWithFlash(w http.ResponseWriter, r *http.Request, target, msg string, isError bool) {
	v := url.Values{"flash": {msg}}
	if isError {
		v.Set("flashErr", "1")
	}
	http.Redirect(w, r, target+"?"+v.Encode(), http.StatusSeeOther)
}

// handleAdminDepartments serves GET /dashboard/admin/departments.
// Admin-only — returns 403 for non-admin principals.
//
// Renders the editable departments table sourced from the canonical
// classification-rules.yaml departments list (not users.yaml).
func (h *handlers) handleAdminDepartments(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	depts := h.currentDepts()

	var deptColors map[string]string
	if h.cfg.ListColors != nil {
		_, deptColors = h.cfg.ListColors()
	}

	// Read optional flash from redirect query params.
	flashMsg := r.URL.Query().Get("flash")
	flashErr := r.URL.Query().Get("flashErr") == "1"

	component := Layout("Admin — Departments", p.DisplayName(), "admin", true,
		AdminDepartmentsPage(depts, deptColors, flashMsg, flashErr))
	if err := component.Render(r.Context(), w); err != nil {
		log.Printf("[dashboard] handleAdminDepartments render: %v", err)
	}
}

// handleAdminDeptSavePost handles POST /dashboard/admin/departments/save.
//
// Accepts form fields:
//   - "original"  — hidden field; the row's original dept name (empty for the add-row).
//   - "name"      — the edited/new dept name.
//   - "color"     — a 6-digit hex color string.
//
// Behaviour:
//   - original empty  + name non-empty → ADD dept "name" with "color".
//   - original == name                 → color-only update.
//   - original != name (both non-empty) → RENAME: migrate color key, update list (preserves Aliases).
//
// Validation: name must be non-empty and trimmed; color must be valid hex; no duplicate names.
// On success redirects 303 to /dashboard/admin/departments with a success flash.
// On error redirects 303 to /dashboard/admin/departments with an error flash.
func (h *handlers) handleAdminDeptSavePost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if h.cfg.SaveDept == nil {
		http.Error(w, "save dept not configured", http.StatusNotImplemented)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	original := strings.TrimSpace(r.FormValue("original"))
	name := strings.TrimSpace(r.FormValue("name"))
	color := strings.TrimSpace(r.FormValue("color"))

	// Validate name.
	if name == "" {
		redirectWithFlash(w, r, "/dashboard/admin/departments", "department name must not be empty", true)
		return
	}
	if !gameNameRE.MatchString(name) {
		redirectWithFlash(w, r, "/dashboard/admin/departments", "invalid department name: use letters, digits, spaces, underscores, dots, or hyphens (1-100 chars)", true)
		return
	}

	// Validate color.
	if color != "" && !hexColorRE.MatchString(color) {
		redirectWithFlash(w, r, "/dashboard/admin/departments", "invalid color: must be a 6-digit hex string (e.g. #528BFF)", true)
		return
	}

	// Build new depts list from the current in-memory canonical list + colors.
	currentDepts := h.currentDepts()
	var deptColors map[string]string
	if h.cfg.ListColors != nil {
		_, deptColors = h.cfg.ListColors()
	}
	if deptColors == nil {
		deptColors = make(map[string]string)
	} else {
		copied := make(map[string]string, len(deptColors))
		for k, v := range deptColors {
			copied[k] = v
		}
		deptColors = copied
	}

	// We need the full DeptEntry list (with Aliases) — caller must supply it via SaveDept.
	// Here we work with names+aliases sourced from the ListDepartmentsCanonical name list;
	// aliases come from the current in-memory config via ListDeptEntries (if available),
	// otherwise we default to empty aliases. The MountConfig pattern: we rely on the
	// SaveDept closure having access to the current config for aliases.
	// Strategy: reconstruct DeptEntry list from current names; we preserve aliases via
	// the SaveDept closure's loader access.
	//
	// For the dashboard handler, we only operate on names. We pass DeptEntry{Name, Aliases}
	// where Aliases come from ListDeptEntriesCanonical if wired, otherwise nil.
	currentEntries := h.currentDeptEntries()

	var newEntries []DeptEntry
	switch {
	case original == "":
		// ADD: append new dept.
		for _, d := range currentDepts {
			if strings.EqualFold(d, name) {
				redirectWithFlash(w, r, "/dashboard/admin/departments", "department "+name+" already exists", true)
				return
			}
		}
		newEntries = append(currentEntries, DeptEntry{Name: name})
		if color != "" {
			deptColors[name] = color
		}
	case strings.EqualFold(original, name):
		// COLOR-ONLY update (includes pure case-change renames — treat as color-only).
		newEntries = currentEntries
		if color != "" {
			deptColors[name] = color
		}
	default:
		// RENAME: replace original with name in the list, migrate color, preserve Aliases.
		for _, d := range currentDepts {
			if strings.EqualFold(d, name) && !strings.EqualFold(d, original) {
				redirectWithFlash(w, r, "/dashboard/admin/departments", "department "+name+" already exists", true)
				return
			}
		}
		newEntries = make([]DeptEntry, 0, len(currentEntries))
		found := false
		for _, e := range currentEntries {
			if strings.EqualFold(e.Name, original) {
				newEntries = append(newEntries, DeptEntry{Name: name, Aliases: e.Aliases})
				found = true
			} else {
				newEntries = append(newEntries, e)
			}
		}
		if !found {
			// original not in list — treat as ADD.
			newEntries = append(newEntries, DeptEntry{Name: name})
		}
		// Migrate color: copy original's color to new name.
		// Use case-insensitive key lookup so color is found even if casing differs.
		var oldColorKey string
		var oldColorVal string
		for k, v := range deptColors {
			if strings.EqualFold(k, original) {
				oldColorKey = k
				oldColorVal = v
				break
			}
		}
		if oldColorKey != "" {
			delete(deptColors, oldColorKey)
			if color != "" {
				deptColors[name] = color
			} else {
				deptColors[name] = oldColorVal
			}
		} else if color != "" {
			deptColors[name] = color
		}
	}

	if err := h.cfg.SaveDept(newEntries, deptColors); err != nil {
		log.Printf("[dashboard] handleAdminDeptSavePost SaveDept: %v", err)
		redirectWithFlash(w, r, "/dashboard/admin/departments", "save failed: "+err.Error(), true)
		return
	}

	redirectWithFlash(w, r, "/dashboard/admin/departments", "Department saved successfully.", false)
}

// handleAdminDeptDeletePost handles POST /dashboard/admin/departments/delete.
//
// Accepts form field "name" (the dept to delete). Removes it from the departments
// list and from graph_colors.departments, then writes atomically.
// On success redirects 303 to /dashboard/admin/departments with a success flash.
// On error redirects 303 to /dashboard/admin/departments with an error flash.
func (h *handlers) handleAdminDeptDeletePost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if h.cfg.DeleteDept == nil {
		http.Error(w, "delete dept not configured", http.StatusNotImplemented)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		redirectWithFlash(w, r, "/dashboard/admin/departments", "department name must not be empty", true)
		return
	}

	currentEntries := h.currentDeptEntries()
	var deptColors map[string]string
	if h.cfg.ListColors != nil {
		_, deptColors = h.cfg.ListColors()
	}
	if deptColors == nil {
		deptColors = make(map[string]string)
	} else {
		copied := make(map[string]string, len(deptColors))
		for k, v := range deptColors {
			copied[k] = v
		}
		deptColors = copied
	}

	// Remove the dept from the list.
	newEntries := make([]DeptEntry, 0, len(currentEntries))
	for _, e := range currentEntries {
		if e.Name != name {
			newEntries = append(newEntries, e)
		}
	}
	// Remove its color.
	delete(deptColors, name)

	if err := h.cfg.DeleteDept(newEntries, deptColors); err != nil {
		log.Printf("[dashboard] handleAdminDeptDeletePost DeleteDept: %v", err)
		redirectWithFlash(w, r, "/dashboard/admin/departments", "delete failed: "+err.Error(), true)
		return
	}

	redirectWithFlash(w, r, "/dashboard/admin/departments", "Department "+name+" deleted.", false)
}

// handleAdminDeptColorPost handles POST /dashboard/admin/departments/{name}/color.
//
// Admin-gated (403 when not admin). Accepts a form field "color" with a
// 6-digit hex value (400 when invalid). On success, redirects 303 back to
// /dashboard/admin/departments with a flash query param.
func (h *handlers) handleAdminDeptColorPost(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		http.Error(w, "department name is required", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	color := strings.TrimSpace(r.FormValue("color"))

	// Allow empty string as a valid placeholder (clears the color).
	// Non-empty values must be valid 6-digit hex.
	if color != "" && !hexColorRE.MatchString(color) {
		http.Error(w, "invalid color: must be a 6-digit hex string (e.g. #528BFF)", http.StatusBadRequest)
		return
	}

	if h.cfg.WriteDeptColor == nil {
		http.Error(w, "dept color write not configured", http.StatusNotImplemented)
		return
	}

	// Reject orphan color keys: the name must exist in the current canonical departments list.
	currentDepts := h.currentDepts()
	deptFound := false
	for _, d := range currentDepts {
		if strings.EqualFold(d, name) {
			deptFound = true
			break
		}
	}
	if !deptFound {
		redirectWithFlash(w, r, "/dashboard/admin/departments", "department "+name+" does not exist", true)
		return
	}

	if err := h.cfg.WriteDeptColor(name, color); err != nil {
		http.Error(w, "failed to save color: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect-after-POST (PRG): 303 back to departments page with success flash.
	flash := url.Values{"flash": {"Color saved for department " + name + "."}}
	http.Redirect(w, r, "/dashboard/admin/departments?"+flash.Encode(), http.StatusSeeOther)
}

// distinctDepartments returns the sorted, deduplicated set of non-empty department
// values from the provisioned users list (ListProvisionedUsers closure).
// When the closure is nil, returns an empty slice.
func (h *handlers) distinctDepartments() []string {
	if h.cfg.ListProvisionedUsers == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, u := range h.cfg.ListProvisionedUsers() {
		if d := strings.TrimSpace(u.Department); d != "" {
			seen[d] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// currentColors returns the current game-color and department-color maps from
// the ListColors closure. Returns nil, nil when the closure is nil (classrules
// not configured) — callers should treat nil maps as "no colors set yet".
func (h *handlers) currentColors() (gameColors, deptColors map[string]string) {
	if h.cfg.ListColors == nil {
		return nil, nil
	}
	return h.cfg.ListColors()
}

// currentGames returns the current games list from the ListGames closure.
// Returns an empty slice when the closure is nil or returns nil.
func (h *handlers) currentGames() []string {
	if h.cfg.ListGames == nil {
		return nil
	}
	g := h.cfg.ListGames()
	if g == nil {
		return nil
	}
	return g
}

// currentDepts returns the current canonical department names from the
// ListDepartmentsCanonical closure. Returns nil when the closure is nil.
func (h *handlers) currentDepts() []string {
	if h.cfg.ListDepartmentsCanonical == nil {
		return nil
	}
	return h.cfg.ListDepartmentsCanonical()
}

// currentDeptEntries returns the current canonical department entries (name + aliases)
// from the ListDeptEntriesCanonical closure if wired. When the closure is nil it falls
// back to building DeptEntry{Name} slices from ListDepartmentsCanonical (no aliases).
func (h *handlers) currentDeptEntries() []DeptEntry {
	if h.cfg.ListDeptEntriesCanonical != nil {
		return h.cfg.ListDeptEntriesCanonical()
	}
	// Fallback: build from names-only list (aliases will be empty).
	names := h.currentDepts()
	entries := make([]DeptEntry, 0, len(names))
	for _, n := range names {
		entries = append(entries, DeptEntry{Name: n})
	}
	return entries
}

// parseGamesList splits the textarea value into a trimmed, deduplicated-blank slice.
// Blank lines are dropped. Leading/trailing spaces per entry are removed.
func parseGamesList(raw string) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// classrulesLoaderBridge adapts the dashboard MountConfig's ClassrulesReload
// callback into the *classrules.ClassrulesLoader interface expected by WriteGames.
// WriteGames calls loader.Reload() after a successful atomic write; we forward
// that call to the MountConfig closure so the cloud server's in-memory Config
// is updated without the dashboard package importing classrules.ClassrulesLoader.
type classrulesLoaderBridge struct {
	reloadFn func() error
}

// Reload implements the loader interface used by classrules.WriteGames.
func (b *classrulesLoaderBridge) Reload() error {
	if b.reloadFn == nil {
		return nil
	}
	return b.reloadFn()
}
