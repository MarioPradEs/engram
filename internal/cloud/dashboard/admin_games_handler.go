package dashboard

import (
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloud/classrules"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// handleAdminGames serves GET /dashboard/admin/games.
// Admin-only — returns 403 for non-admin principals.
func (h *handlers) handleAdminGames(w http.ResponseWriter, r *http.Request) {
	p := h.principalFromRequest(r)
	if !p.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	games := h.currentGames()
	component := Layout("Admin — Games", p.DisplayName(), "admin", true, AdminGamesPage(games, "", false))
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
		w.WriteHeader(http.StatusBadRequest)
		component := Layout("Admin — Games", p.DisplayName(), "admin", true,
			AdminGamesPage(h.currentGames(), err.Error(), true))
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
	// WriteGames calls loader.Reload() after the rename; we intercept that via
	// the ClassrulesReload callback on MountConfig so the cloud server's in-memory
	// copy is updated. We pass a minimal loader that only forwards the Reload call.
	loaderBridge := &classrulesLoaderBridge{reloadFn: h.cfg.ClassrulesReload}

	// Perform atomic write: validate → temp+rename → reload.
	if err := classrules.WriteGames(classrulesPath, loaderBridge, newGames); err != nil {
		log.Printf("[dashboard] handleAdminGamesPost WriteGames: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		component := Layout("Admin — Games", p.DisplayName(), "admin", true,
			AdminGamesPage(h.currentGames(), "Write failed: "+err.Error(), true))
		if renderErr := component.Render(r.Context(), w); renderErr != nil {
			log.Printf("[dashboard] handleAdminGamesPost render error: %v", renderErr)
		}
		return
	}

	// Non-fatal local git auto-commit (same pattern as users.yaml D4).
	repoPath := filepath.Dir(classrulesPath)
	if gitErr := users.RunLocalGitCommit(repoPath, classrulesPath, "chore(classrules): update games via admin UI"); gitErr != nil {
		log.Printf("[dashboard] handleAdminGamesPost git commit (non-fatal): %v", gitErr)
	}

	// Render success page with updated games from the reloaded in-memory config.
	w.WriteHeader(http.StatusOK)
	component := Layout("Admin — Games", p.DisplayName(), "admin", true,
		AdminGamesPage(h.currentGames(), "Games updated successfully.", false))
	if err := component.Render(r.Context(), w); err != nil {
		log.Printf("[dashboard] handleAdminGamesPost render success: %v", err)
	}
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
