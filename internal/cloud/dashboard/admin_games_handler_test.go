package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── B6/B7: GET admin games page renders color section ────────────────────────

// adminGamesMux builds a mux with the games + color section wired.
// gameColors and deptColors populate the ListColors closure so the GET page
// can display current values. writeColor captures save calls for assertion.
func adminGamesMux(games []string, gameColors, deptColors map[string]string, writeColor func(name, color string) error) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:   func(_ *http.Request) bool { return true },
		ListGames: func() []string { return games },
		ListColors: func() (map[string]string, map[string]string) {
			return gameColors, deptColors
		},
		WriteGameColor: func(name, color string) error {
			if writeColor != nil {
				return writeColor(name, color)
			}
			return nil
		},
	})
	return mux
}

// adminGamesSaveMux builds a mux with SaveGame + DeleteGame wired for the new editable-table handlers.
func adminGamesSaveMux(
	games []string,
	gameColors map[string]string,
	saveGame func(newGames []string, newGameColors map[string]string) error,
	deleteGame func(newGames []string, newGameColors map[string]string) error,
) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:   func(_ *http.Request) bool { return true },
		ListGames: func() []string { return games },
		ListColors: func() (map[string]string, map[string]string) {
			return gameColors, nil
		},
		SaveGame:   saveGame,
		DeleteGame: deleteGame,
	})
	return mux
}

// TestAdminGamesGET_RendersColorSection asserts that GET /dashboard/admin/games
// includes the editable games table with name inputs and color pickers.
// (Replaces the old "Graph Color Map" giant section check — Block A refactor.)
func TestAdminGamesGET_RendersColorSection(t *testing.T) {
	mux := adminGamesMux(
		[]string{"spark", "viva-clash"},
		map[string]string{"spark": "#E5C07B", "viva-clash": "#61AFEF"},
		nil, // dept colors are now on /dashboard/admin/departments
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/games?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Table must include game names and color values.
	if !strings.Contains(body, "spark") {
		t.Error("expected response body to contain game 'spark'")
	}
	if !strings.Contains(body, "#E5C07B") {
		t.Error("expected response body to contain current game color #E5C07B")
	}
	// Must contain a color input for games.
	if !strings.Contains(body, `type="color"`) {
		t.Error("expected response body to contain type=color input")
	}
	// Must render editable name inputs (not read-only text cells).
	if !strings.Contains(body, `type="text"`) {
		t.Error("expected response body to contain text inputs for game names")
	}
	// Must render Save buttons.
	if !strings.Contains(body, "Save") {
		t.Error("expected response body to contain Save button")
	}
	// Must render delete button (X).
	if !strings.Contains(body, ">X<") {
		t.Error("expected response body to contain delete X button")
	}
	// Must render the add-row (empty input with placeholder).
	if !strings.Contains(body, "new game") {
		t.Error("expected response body to contain add-row placeholder")
	}
}

// ─── Save handler tests ────────────────────────────────────────────────────────

// TestAdminGameSavePost_AddGame asserts that POST /dashboard/admin/games/save
// with original="" and a new name calls SaveGame with the extended list + color,
// then redirects 303 to /dashboard/admin/games.
func TestAdminGameSavePost_AddGame(t *testing.T) {
	var savedGames []string
	var savedColors map[string]string
	mux := adminGamesSaveMux(
		[]string{"spark"},
		map[string]string{"spark": "#E5C07B"},
		func(ng []string, nc map[string]string) error {
			savedGames = ng
			savedColors = nc
			return nil
		},
		nil,
	)

	form := strings.NewReader("original=&name=nova&color=%2361AFEF") // #61AFEF
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/dashboard/admin/games") {
		t.Errorf("expected redirect to /dashboard/admin/games, got %q", loc)
	}
	if len(savedGames) != 2 {
		t.Fatalf("expected 2 games after add, got %v", savedGames)
	}
	found := false
	for _, g := range savedGames {
		if g == "nova" {
			found = true
		}
	}
	if !found {
		t.Error("nova not found in saved games list")
	}
	if savedColors["nova"] != "#61AFEF" {
		t.Errorf("nova color = %q, want #61AFEF", savedColors["nova"])
	}
}

// TestAdminGameSavePost_RenameGame asserts that POST /dashboard/admin/games/save
// with original!=name renames the game, migrates the color, and removes the old color key.
func TestAdminGameSavePost_RenameGame(t *testing.T) {
	var savedGames []string
	var savedColors map[string]string
	mux := adminGamesSaveMux(
		[]string{"old-name", "other"},
		map[string]string{"old-name": "#AABBCC", "other": "#112233"},
		func(ng []string, nc map[string]string) error {
			savedGames = ng
			savedColors = nc
			return nil
		},
		nil,
	)

	form := strings.NewReader("original=old-name&name=new-name&color=%23AABBCC")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(savedGames) != 2 {
		t.Fatalf("expected 2 games after rename, got %v", savedGames)
	}
	for _, g := range savedGames {
		if g == "old-name" {
			t.Error("old-name should not appear in saved games list after rename")
		}
	}
	if _, exists := savedColors["old-name"]; exists {
		t.Error("old-name color key should not exist after rename")
	}
	if savedColors["new-name"] != "#AABBCC" {
		t.Errorf("new-name color = %q, want #AABBCC", savedColors["new-name"])
	}
}

// TestAdminGameSavePost_ColorOnlyUpdate asserts that POST /dashboard/admin/games/save
// with original==name only updates the color, leaving the games list unchanged.
func TestAdminGameSavePost_ColorOnlyUpdate(t *testing.T) {
	var savedGames []string
	var savedColors map[string]string
	mux := adminGamesSaveMux(
		[]string{"spark"},
		map[string]string{"spark": "#E5C07B"},
		func(ng []string, nc map[string]string) error {
			savedGames = ng
			savedColors = nc
			return nil
		},
		nil,
	)

	form := strings.NewReader("original=spark&name=spark&color=%23FFFFFF")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(savedGames) != 1 || savedGames[0] != "spark" {
		t.Errorf("expected games=[spark], got %v", savedGames)
	}
	if savedColors["spark"] != "#FFFFFF" {
		t.Errorf("spark color = %q after color-only update, want #FFFFFF", savedColors["spark"])
	}
}

// TestAdminGameSavePost_DuplicateNameRejected asserts that adding a game with an
// existing name redirects with an error flash and does NOT call SaveGame.
func TestAdminGameSavePost_DuplicateNameRejected(t *testing.T) {
	saveCalled := false
	mux := adminGamesSaveMux(
		[]string{"spark", "nova"},
		nil,
		func(ng []string, nc map[string]string) error {
			saveCalled = true
			return nil
		},
		nil,
	)

	// Try to add "spark" which already exists.
	form := strings.NewReader("original=&name=spark&color=%23000000")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on error, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flashErr=1") {
		t.Errorf("expected error flash in redirect, got location %q", loc)
	}
	if saveCalled {
		t.Error("SaveGame must NOT be called when name is duplicate")
	}
}

// TestAdminGameSavePost_EmptyNameRejected asserts that an empty name redirects
// with an error flash and does NOT call SaveGame.
func TestAdminGameSavePost_EmptyNameRejected(t *testing.T) {
	saveCalled := false
	mux := adminGamesSaveMux(
		[]string{"spark"},
		nil,
		func(ng []string, nc map[string]string) error {
			saveCalled = true
			return nil
		},
		nil,
	)

	form := strings.NewReader("original=&name=&color=%23000000")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on error, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flashErr=1") {
		t.Errorf("expected error flash, got %q", loc)
	}
	if saveCalled {
		t.Error("SaveGame must NOT be called when name is empty")
	}
}

// TestAdminGameSavePost_NonAdminReturns403 asserts that a non-admin request
// to POST /dashboard/admin/games/save returns 403.
func TestAdminGameSavePost_NonAdminReturns403(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
	})

	form := strings.NewReader("original=&name=nova&color=%23000000")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// ─── Delete handler tests ──────────────────────────────────────────────────────

// TestAdminGameDeletePost_RemovesGameAndColor asserts that POST /dashboard/admin/games/delete
// calls DeleteGame with the game removed from the list and its color deleted.
func TestAdminGameDeletePost_RemovesGameAndColor(t *testing.T) {
	var savedGames []string
	var savedColors map[string]string
	mux := adminGamesSaveMux(
		[]string{"spark", "nova"},
		map[string]string{"spark": "#E5C07B", "nova": "#61AFEF"},
		nil,
		func(ng []string, nc map[string]string) error {
			savedGames = ng
			savedColors = nc
			return nil
		},
	)

	form := strings.NewReader("name=nova")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/delete?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	if len(savedGames) != 1 || savedGames[0] != "spark" {
		t.Errorf("expected [spark] after delete, got %v", savedGames)
	}
	if _, exists := savedColors["nova"]; exists {
		t.Error("nova color should be removed after delete")
	}
	if savedColors["spark"] != "#E5C07B" {
		t.Errorf("spark color should be preserved, got %q", savedColors["spark"])
	}
}

// TestAdminGameDeletePost_NonAdminReturns403 asserts that a non-admin request
// to POST /dashboard/admin/games/delete returns 403.
func TestAdminGameDeletePost_NonAdminReturns403(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
	})

	form := strings.NewReader("name=nova")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/delete?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// TestAdminGameDeletePost_LastGameEmptiesList asserts that deleting the last game
// calls DeleteGame with an empty list (not a stale non-empty list) so the
// "games:" key on disk becomes empty after the delete.
func TestAdminGameDeletePost_LastGameEmptiesList(t *testing.T) {
	var savedGames []string
	var savedColors map[string]string
	saveCalled := false
	mux := adminGamesSaveMux(
		[]string{"only-game"},
		map[string]string{"only-game": "#AABBCC"},
		nil,
		func(ng []string, nc map[string]string) error {
			saveCalled = true
			savedGames = ng
			savedColors = nc
			return nil
		},
	)

	form := strings.NewReader("name=only-game")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/delete?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !saveCalled {
		t.Fatal("DeleteGame must be called even when the list becomes empty")
	}
	if len(savedGames) != 0 {
		t.Errorf("expected empty games list after deleting last game, got %v", savedGames)
	}
	if _, exists := savedColors["only-game"]; exists {
		t.Error("only-game color must be removed after delete")
	}
}

// TestAdminGameColorPost_UnknownNameRejected asserts that POSTing a color for a
// game name that does not exist in the current list redirects with an error flash
// and does NOT call WriteGameColor.
func TestAdminGameColorPost_UnknownNameRejected(t *testing.T) {
	writeCalled := false
	mux := adminColorMuxWithGames([]string{"spark"}, func(_, _ string) error {
		writeCalled = true
		return nil
	}, true)

	form := strings.NewReader("color=%23E5C07B")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/ghost/color?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on unknown name, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flashErr=1") {
		t.Errorf("expected error flash for unknown game, got location %q", loc)
	}
	if writeCalled {
		t.Error("WriteGameColor must NOT be called for an unknown game name")
	}
}

// TestAdminGameSavePost_InvalidNameRejected asserts that a game name with
// disallowed characters redirects with an error flash and does NOT call SaveGame.
func TestAdminGameSavePost_InvalidNameRejected(t *testing.T) {
	saveCalled := false
	mux := adminGamesSaveMux(
		[]string{"spark"},
		nil,
		func(ng []string, nc map[string]string) error {
			saveCalled = true
			return nil
		},
		nil,
	)

	// "<script>" is not in the allowlist.
	form := strings.NewReader("original=&name=%3Cscript%3E&color=")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on invalid name, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flashErr=1") {
		t.Errorf("expected error flash for invalid name, got location %q", loc)
	}
	if saveCalled {
		t.Error("SaveGame must NOT be called when name fails allowlist check")
	}
}

// TestAdminGameSavePost_CaseSensitiveRename asserts that renaming "Spark" → "spark"
// (case-only change) is treated as a color-only update (not a ghost-add) and
// calls SaveGame with the original list unchanged.
func TestAdminGameSavePost_CaseSensitiveRename(t *testing.T) {
	var savedGames []string
	mux := adminGamesSaveMux(
		[]string{"Spark"},
		map[string]string{"Spark": "#E5C07B"},
		func(ng []string, nc map[string]string) error {
			savedGames = ng
			return nil
		},
		nil,
	)

	// "original=Spark&name=spark" — same name, different case.
	form := strings.NewReader("original=Spark&name=spark&color=%23E5C07B")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%q", rec.Code, rec.Body.String())
	}
	// Must not ghost-add "spark" — list length must stay 1.
	if len(savedGames) != 1 {
		t.Errorf("case-only rename must not add a second entry; got %v", savedGames)
	}
}

// TestAdminGamesGET_ColorSectionAdminOnly asserts that a non-admin GET does
// not render the color section (returns 403 before reaching the color section).
func TestAdminGamesGET_ColorSectionAdminOnly(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin: func(_ *http.Request) bool { return false },
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard/admin/games?auth=ok", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", rec.Code)
	}
}

// ─── B5: admin games color handler ────────────────────────────────────────────
// Tests for POST /dashboard/admin/games/{name}/color.
// Admin-gated: 403 for non-admin; 400 for invalid hex; 200 for valid hex.

// adminColorMux creates a mux with WriteColors wired (for testing the color-save handler).
// If writeColors is nil, a no-op writer is used.
// If isAdmin is true, the request will be treated as admin.
// games is the known-games list used by the name-existence check; pass nil to use
// the default ["spark"] list so legacy tests targeting "spark" keep working.
func adminColorMux(writeColors func(game string, color string) error, isAdmin bool) *http.ServeMux {
	return adminColorMuxWithGames([]string{"spark"}, writeColors, isAdmin)
}

// adminColorMuxWithGames is like adminColorMux but accepts an explicit games list.
func adminColorMuxWithGames(games []string, writeColors func(game string, color string) error, isAdmin bool) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:  func(_ *http.Request) bool { return isAdmin },
		ListGames: func() []string { return games },
		WriteGameColor: func(game, color string) error {
			if writeColors != nil {
				return writeColors(game, color)
			}
			return nil
		},
	})
	return mux
}

// TestAdminGameColorPost_ValidHexReturns303 (legacy name kept for consistency with older
// slice B tests; see also admin_departments_handler_test.go for the canonical Block A test).
// Asserts that POST /dashboard/admin/games/{name}/color with a valid hex color
// calls WriteGameColor and redirects 303 to /dashboard/admin/games.
func TestAdminGameColorPost_ValidHexReturns303Legacy(t *testing.T) {
	written := map[string]string{}
	mux := adminColorMux(func(game, color string) error {
		written[game] = color
		return nil
	}, true)

	form := strings.NewReader("color=%23E5C07B") // #E5C07B URL-encoded
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/spark/color?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 SeeOther after save, got %d body=%q", rec.Code, rec.Body.String())
	}
	if written["spark"] != "#E5C07B" {
		t.Errorf("WriteGameColor called with game=%q color=%q, want game=spark color=#E5C07B", "spark", written["spark"])
	}
}

// TestAdminGameColorPost_InvalidHexReturns400 asserts that POST with an
// invalid color value returns 400 without calling the write function.
func TestAdminGameColorPost_InvalidHexReturns400(t *testing.T) {
	writeCalled := false
	mux := adminColorMux(func(_, _ string) error {
		writeCalled = true
		return nil
	}, true)

	form := strings.NewReader("color=red")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/spark/color?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
	if writeCalled {
		t.Error("WriteGameColor must NOT be called when color is invalid")
	}
}

// TestAdminGameColorPost_NonAdminReturns403 asserts that a non-admin request
// returns 403 regardless of color value.
func TestAdminGameColorPost_NonAdminReturns403(t *testing.T) {
	mux := adminColorMux(nil, false /* isAdmin=false */)

	form := strings.NewReader("color=%23E5C07B")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/spark/color?auth=ok", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%q", rec.Code, rec.Body.String())
	}
}
