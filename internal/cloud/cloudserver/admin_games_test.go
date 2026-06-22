package cloudserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/classrules"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// ─── Fixtures ────────────────────────────────────────────────────────────────

const gamesTestUsersYAML = `users:
  - email: admin@vivastudios.com
    name: Admin User
    department: dev
    role: admin
    status: active
    enrolled:
      - general
  - email: member@vivastudios.com
    name: Member User
    department: qa
    role: member
    status: active
    enrolled:
      - general
`

const gamesTestClassrulesYAML = `departments:
  - name: engineering
    aliases:
      - eng
games:
  - "game-alpha"
  - "game-beta"
rules: |
  Keep this rules section intact.
`

// ─── Helpers ─────────────────────────────────────────────────────────────────

// buildGamesServer builds a CloudServer wired with a YAMLLoader for users and
// a ClassrulesLoader for classrules. Returns the server plus the loader so
// tests can assert on Current() after writes.
func buildGamesServer(t *testing.T, jwtSecret, usersPath, classrulesPath string) (*CloudServer, *classrules.ClassrulesLoader) {
	t.Helper()

	loader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("buildGamesServer: NewYAMLLoader: %v", err)
	}
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("buildGamesServer: NewHeaderAuthenticatorWithJWT: %v", err)
	}

	cl, err := classrules.NewClassrulesLoader(classrulesPath)
	if err != nil {
		t.Fatalf("buildGamesServer: NewClassrulesLoader: %v", err)
	}

	srv := New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, jwtSecret),
		WithUserDirectoryReload(loader.Reload),
		WithUsersFilePath(usersPath),
		WithClassrulesReload(cl.Reload),
		WithClassrulesCurrentGames(func() []string {
			cfg := cl.Current()
			if cfg == nil {
				return nil
			}
			return cfg.Games
		}),
		WithClassrulesFilePath(classrulesPath),
	)
	return srv, cl
}

// initTempGamesFiles creates temp files for users.yaml and
// classification-rules.yaml; returns the dir + both paths.
func initTempGamesFiles(t *testing.T) (dir, usersPath, classrulesPath string) {
	t.Helper()
	dir = t.TempDir()
	usersPath = filepath.Join(dir, "users.yaml")
	classrulesPath = filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(usersPath, []byte(gamesTestUsersYAML), 0o644); err != nil {
		t.Fatalf("initTempGamesFiles: write users: %v", err)
	}
	if err := os.WriteFile(classrulesPath, []byte(gamesTestClassrulesYAML), 0o644); err != nil {
		t.Fatalf("initTempGamesFiles: write classrules: %v", err)
	}
	return dir, usersPath, classrulesPath
}

// ─── AG1: member session → 403 on GET /dashboard/admin/games ─────────────────

func TestAdminGames_AG1_MemberGets403(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)
	srv, _ := buildGamesServer(t, jwtSecret, usersPath, classrulesPath)

	memberCookie := mintSessionCookieForEmail(t, srv, "member@vivastudios.com", jwtSecret)

	req := makeSessionRequest(http.MethodGet, "/dashboard/admin/games", memberCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /dashboard/admin/games for member: status=%d, want 403", rec.Code)
	}
}

// ─── AG1 (POST): member session → 403 on POST, file untouched ────────────────

func TestAdminGames_AG1_MemberGets403_POST(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)
	original, _ := os.ReadFile(classrulesPath)

	srv, _ := buildGamesServer(t, jwtSecret, usersPath, classrulesPath)
	memberCookie := mintSessionCookieForEmail(t, srv, "member@vivastudios.com", jwtSecret)

	// A member attempts to overwrite the games list. The admin guard must fire
	// BEFORE any write, so the request is rejected and the file stays intact.
	form := url.Values{}
	form.Set("games", "game-injected")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(memberCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /dashboard/admin/games for member: status=%d, want 403", rec.Code)
	}

	after, _ := os.ReadFile(classrulesPath)
	if string(after) != string(original) {
		t.Error("classification-rules.yaml was modified despite member 403 rejection")
	}
}

// ─── AG2: admin session → 200 + current games list rendered ──────────────────

func TestAdminGames_AG2_AdminGets200WithGamesList(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)
	srv, _ := buildGamesServer(t, jwtSecret, usersPath, classrulesPath)

	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	req := makeSessionRequest(http.MethodGet, "/dashboard/admin/games", adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /dashboard/admin/games for admin: status=%d body=%q, want 200", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "game-alpha") {
		t.Errorf("response body does not contain 'game-alpha': %q", body)
	}
	if !strings.Contains(body, "game-beta") {
		t.Errorf("response body does not contain 'game-beta': %q", body)
	}
}

// ─── AG3: admin POST valid update → 200, file updated, loader reloaded ────────

func TestAdminGames_AG3_AdminPostValidUpdate(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)
	srv, cl := buildGamesServer(t, jwtSecret, usersPath, classrulesPath)

	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("games", "game-alpha\ngame-beta\ngame-gamma")

	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Errorf("POST /dashboard/admin/games valid: status=%d body=%q, want 200 or 303", rec.Code, rec.Body.String())
	}

	// The in-memory loader must reflect the new game.
	cfg := cl.Current()
	if cfg == nil {
		t.Fatal("ClassrulesLoader.Current() nil after valid update")
	}
	if len(cfg.Games) != 3 {
		t.Errorf("expected 3 games, got %d: %v", len(cfg.Games), cfg.Games)
	}
	found := false
	for _, g := range cfg.Games {
		if g == "game-gamma" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("'game-gamma' not in loader games after update: %v", cfg.Games)
	}

	// The file on disk must also reflect the update.
	diskCfg, err := classrules.LoadFromFile(classrulesPath)
	if err != nil {
		t.Fatalf("LoadFromFile after update: %v", err)
	}
	if diskCfg == nil || len(diskCfg.Games) != 3 {
		t.Errorf("disk games: %v", diskCfg)
	}

	// Non-games sections must be preserved.
	if len(diskCfg.Departments) == 0 {
		t.Error("departments section was not preserved after write")
	}
	if diskCfg.Rules == "" {
		t.Error("rules section was not preserved after write")
	}
}

// ─── AG4: admin POST empty list → 400, file unchanged ────────────────────────

func TestAdminGames_AG4_AdminPostEmptyList_400(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)
	original, _ := os.ReadFile(classrulesPath)

	srv, _ := buildGamesServer(t, jwtSecret, usersPath, classrulesPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("games", "")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /dashboard/admin/games empty: status=%d, want 400", rec.Code)
	}

	after, _ := os.ReadFile(classrulesPath)
	if string(after) != string(original) {
		t.Error("classification-rules.yaml was modified despite empty list rejection")
	}
}

// ─── AG5: admin POST duplicate entries → 400, file unchanged ─────────────────

func TestAdminGames_AG5_AdminPostDuplicates_400(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)
	original, _ := os.ReadFile(classrulesPath)

	srv, _ := buildGamesServer(t, jwtSecret, usersPath, classrulesPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("games", "game-a\ngame-b\ngame-a") // duplicate
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /dashboard/admin/games duplicates: status=%d, want 400", rec.Code)
	}

	after, _ := os.ReadFile(classrulesPath)
	if string(after) != string(original) {
		t.Error("classification-rules.yaml was modified despite duplicate rejection")
	}
}

// ─── AG-COLOR: color-map integration tests (Slice B) ────────────────────────

const gamesTestClassrulesWithColorsYAML = `departments:
  - name: dev
  - name: art
games:
  - "game-alpha"
  - "game-beta"
graph_colors:
  games:
    game-alpha: "#E5C07B"
  departments:
    dev: "#528BFF"
rules: |
  Keep this rules section intact.
`

// buildGamesServerWithColors builds a CloudServer with the full color-map wiring:
// ListColors, WriteGameColor, and WriteDeptColor closures backed by a ClassrulesLoader.
// Mirrors the production wiring in cmd/engram/cloud.go (Block A: separate game/dept routes).
func buildGamesServerWithColors(t *testing.T, jwtSecret, usersPath, classrulesPath string) (*CloudServer, *classrules.ClassrulesLoader) {
	t.Helper()

	loader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("buildGamesServerWithColors: NewYAMLLoader: %v", err)
	}
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("buildGamesServerWithColors: NewHeaderAuthenticatorWithJWT: %v", err)
	}

	cl, err := classrules.NewClassrulesLoader(classrulesPath)
	if err != nil {
		t.Fatalf("buildGamesServerWithColors: NewClassrulesLoader: %v", err)
	}

	// writeColorHelper performs a read-modify-write on the given target map.
	// isGame=true → writes to graph_colors.games; false → graph_colors.departments.
	writeColorHelper := func(name, color string, isGame bool) error {
		cfg := cl.Current()
		gameColors := make(map[string]string)
		deptColors := make(map[string]string)
		if cfg != nil {
			for k, v := range cfg.GraphColors.Games {
				gameColors[k] = v
			}
			for k, v := range cfg.GraphColors.Departments {
				deptColors[k] = v
			}
		}
		if isGame {
			gameColors[name] = color
		} else {
			deptColors[name] = color
		}
		return classrules.WriteColors(classrulesPath, gameColors, deptColors, func() {
			_ = cl.Reload()
		})
	}

	srv := New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, jwtSecret),
		WithUserDirectoryReload(loader.Reload),
		WithUsersFilePath(usersPath),
		WithClassrulesReload(cl.Reload),
		WithClassrulesCurrentGames(func() []string {
			cfg := cl.Current()
			if cfg == nil {
				return nil
			}
			return cfg.Games
		}),
		WithClassrulesCurrentDepts(func() []string {
			cfg := cl.Current()
			if cfg == nil {
				return nil
			}
			names := make([]string, 0, len(cfg.Departments))
			for _, d := range cfg.Departments {
				names = append(names, d.Name)
			}
			return names
		}),
		WithClassrulesFilePath(classrulesPath),
		// Block A: color read path.
		WithClassrulesCurrentColors(func() (map[string]string, map[string]string) {
			cfg := cl.Current()
			if cfg == nil {
				return nil, nil
			}
			return cfg.GraphColors.Games, cfg.GraphColors.Departments
		}),
		// Block A: separate write paths for games and departments.
		WithClassrulesWriteColor(func(name, color string) error {
			return writeColorHelper(name, color, true)
		}),
		WithClassrulesWriteDeptColor(func(name, color string) error {
			return writeColorHelper(name, color, false)
		}),
	)
	return srv, cl
}

// TestAdminGameColor_GET_RendersColorSection asserts that GET /dashboard/admin/games
// for an admin includes the compact game color table with current colors.
// (Block A: "Graph Color Map" heading removed; dept colors moved to /admin/departments.)
func TestAdminGameColor_GET_RendersColorSection(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(usersPath, []byte(gamesTestUsersYAML), 0o644); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := os.WriteFile(classrulesPath, []byte(gamesTestClassrulesWithColorsYAML), 0o644); err != nil {
		t.Fatalf("write classrules: %v", err)
	}

	srv, _ := buildGamesServerWithColors(t, jwtSecret, usersPath, classrulesPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	req := makeSessionRequest(http.MethodGet, "/dashboard/admin/games", adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/admin/games color: status=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Compact color table must show game names and current colors.
	if !strings.Contains(body, "game-alpha") {
		t.Error("expected 'game-alpha' in GET /dashboard/admin/games response")
	}
	if !strings.Contains(body, "#E5C07B") {
		t.Error("expected current game color '#E5C07B' in response body")
	}
	// Dept colors now live on /dashboard/admin/departments — must not appear here.
	// (Not asserting absence; just ensuring games page is functional.)
}

// TestAdminGameColor_POST_GameColorPersists asserts that POST
// /dashboard/admin/games/{name}/color for a game slug persists to the games map
// and redirects 303 (Block A: redirect-after-save replaces the old empty 200).
func TestAdminGameColor_POST_GameColorPersists(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(usersPath, []byte(gamesTestUsersYAML), 0o644); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := os.WriteFile(classrulesPath, []byte(gamesTestClassrulesWithColorsYAML), 0o644); err != nil {
		t.Fatalf("write classrules: %v", err)
	}

	srv, cl := buildGamesServerWithColors(t, jwtSecret, usersPath, classrulesPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("color", "#C678DD")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/game-beta/color",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	// Block A: handler now redirects 303 (PRG pattern) instead of returning empty 200.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST game color: status=%d body=%q, want 303 SeeOther", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/dashboard/admin/games") {
		t.Errorf("expected Location to start with /dashboard/admin/games, got %q", loc)
	}

	// In-memory loader must reflect the new color.
	cfg := cl.Current()
	if cfg == nil {
		t.Fatal("loader is nil after color write")
	}
	if got := cfg.GraphColors.Games["game-beta"]; got != "#C678DD" {
		t.Errorf("game-beta color = %q, want #C678DD", got)
	}
	// Existing game-alpha color must be preserved (read-modify-write).
	if got := cfg.GraphColors.Games["game-alpha"]; got != "#E5C07B" {
		t.Errorf("game-alpha color = %q after game-beta write, want #E5C07B (should be preserved)", got)
	}
}

// TestAdminGameColor_POST_DeptColorPersists asserts that POST to
// /dashboard/admin/departments/{name}/color routes to deptColors and redirects 303.
// (Block A: dept colors now have their own endpoint, separate from game colors.)
func TestAdminGameColor_POST_DeptColorPersists(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(usersPath, []byte(gamesTestUsersYAML), 0o644); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := os.WriteFile(classrulesPath, []byte(gamesTestClassrulesWithColorsYAML), 0o644); err != nil {
		t.Fatalf("write classrules: %v", err)
	}

	srv, cl := buildGamesServerWithColors(t, jwtSecret, usersPath, classrulesPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("color", "#61AFEF")
	// Block A: dept colors use the new /dashboard/admin/departments/{name}/color route.
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/departments/art/color",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	// Block A: handler redirects 303 (PRG pattern).
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST dept color: status=%d body=%q, want 303 SeeOther", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/dashboard/admin/departments") {
		t.Errorf("expected Location to start with /dashboard/admin/departments, got %q", loc)
	}

	cfg := cl.Current()
	if cfg == nil {
		t.Fatal("loader is nil after dept color write")
	}
	// art dept color must be written.
	if got := cfg.GraphColors.Departments["art"]; got != "#61AFEF" {
		t.Errorf("departments[art] = %q, want #61AFEF", got)
	}
	// dev dept color (#528BFF) must be preserved.
	if got := cfg.GraphColors.Departments["dev"]; got != "#528BFF" {
		t.Errorf("departments[dev] = %q after art write, want #528BFF (should be preserved)", got)
	}
	// game-alpha color must NOT be touched.
	if got := cfg.GraphColors.Games["game-alpha"]; got != "#E5C07B" {
		t.Errorf("games[game-alpha] = %q after dept write, want #E5C07B (should be preserved)", got)
	}
}

// ─── AG-SAVE / AG-DELETE: editable table save + delete integration tests ─────

const gamesTestClassrulesForSaveYAML = `departments:
  - name: engineering
    aliases:
      - eng
games:
  - "game-alpha"
  - "game-beta"
graph_colors:
  games:
    game-alpha: "#E5C07B"
    game-beta: "#61AFEF"
rules: |
  Keep this rules section intact.
`

// buildGamesServerFull builds a CloudServer with the full suite: ListColors,
// WriteGameColor, WriteDeptColor, SaveGame, DeleteGame.
func buildGamesServerFull(t *testing.T, jwtSecret, usersPath, classrulesPath string) (*CloudServer, *classrules.ClassrulesLoader) {
	t.Helper()

	loader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("buildGamesServerFull: NewYAMLLoader: %v", err)
	}
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", jwtSecret)
	if err != nil {
		t.Fatalf("buildGamesServerFull: NewHeaderAuthenticatorWithJWT: %v", err)
	}

	cl, err := classrules.NewClassrulesLoader(classrulesPath)
	if err != nil {
		t.Fatalf("buildGamesServerFull: NewClassrulesLoader: %v", err)
	}

	reloader := &classrulesReloadBridge{cl: cl}

	gameEntryWriter := func(newGames []string, newGameColors map[string]string) error {
		return classrules.WriteGameEntry(classrulesPath, reloader, newGames, newGameColors)
	}

	srv := New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, jwtSecret),
		WithUserDirectoryReload(loader.Reload),
		WithUsersFilePath(usersPath),
		WithClassrulesReload(cl.Reload),
		WithClassrulesCurrentGames(func() []string {
			cfg := cl.Current()
			if cfg == nil {
				return nil
			}
			return cfg.Games
		}),
		WithClassrulesFilePath(classrulesPath),
		WithClassrulesCurrentColors(func() (map[string]string, map[string]string) {
			cfg := cl.Current()
			if cfg == nil {
				return nil, nil
			}
			return cfg.GraphColors.Games, cfg.GraphColors.Departments
		}),
		WithSaveGame(gameEntryWriter),
		WithDeleteGame(gameEntryWriter),
	)
	return srv, cl
}

// classrulesReloadBridge adapts *classrules.ClassrulesLoader to the Reloader interface.
type classrulesReloadBridge struct {
	cl *classrules.ClassrulesLoader
}

func (b *classrulesReloadBridge) Reload() error {
	return b.cl.Reload()
}

// TestAdminGameSave_AddGame_Integration asserts that POST /dashboard/admin/games/save
// with an empty original and a new game name adds it to the list and color map on disk.
func TestAdminGameSave_AddGame_Integration(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("s", 32)
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(usersPath, []byte(gamesTestUsersYAML), 0o644); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := os.WriteFile(classrulesPath, []byte(gamesTestClassrulesForSaveYAML), 0o644); err != nil {
		t.Fatalf("write classrules: %v", err)
	}

	srv, cl := buildGamesServerFull(t, jwtSecret, usersPath, classrulesPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("original", "")
	form.Set("name", "game-gamma")
	form.Set("color", "#C678DD")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /games/save add: status=%d body=%q, want 303", rec.Code, rec.Body.String())
	}

	cfg := cl.Current()
	if cfg == nil {
		t.Fatal("loader nil after add")
	}
	found := false
	for _, g := range cfg.Games {
		if g == "game-gamma" {
			found = true
		}
	}
	if !found {
		t.Errorf("game-gamma not in games list after add: %v", cfg.Games)
	}
	if cfg.GraphColors.Games["game-gamma"] != "#C678DD" {
		t.Errorf("game-gamma color = %q, want #C678DD", cfg.GraphColors.Games["game-gamma"])
	}
	// Existing game colors must be preserved.
	if cfg.GraphColors.Games["game-alpha"] != "#E5C07B" {
		t.Errorf("game-alpha color = %q after add, want #E5C07B", cfg.GraphColors.Games["game-alpha"])
	}
}

// TestAdminGameSave_RenameGame_Integration asserts that renaming a game migrates
// the color key and removes the old entry, atomically.
func TestAdminGameSave_RenameGame_Integration(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("r", 32)
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(usersPath, []byte(gamesTestUsersYAML), 0o644); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := os.WriteFile(classrulesPath, []byte(gamesTestClassrulesForSaveYAML), 0o644); err != nil {
		t.Fatalf("write classrules: %v", err)
	}

	srv, cl := buildGamesServerFull(t, jwtSecret, usersPath, classrulesPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("original", "game-alpha")
	form.Set("name", "game-renamed")
	form.Set("color", "#E5C07B") // keep same color
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /games/save rename: status=%d body=%q, want 303", rec.Code, rec.Body.String())
	}

	cfg := cl.Current()
	if cfg == nil {
		t.Fatal("loader nil after rename")
	}
	for _, g := range cfg.Games {
		if g == "game-alpha" {
			t.Error("game-alpha still in games list after rename")
		}
	}
	if _, exists := cfg.GraphColors.Games["game-alpha"]; exists {
		t.Error("game-alpha color key should not exist after rename")
	}
	if cfg.GraphColors.Games["game-renamed"] != "#E5C07B" {
		t.Errorf("game-renamed color = %q, want #E5C07B", cfg.GraphColors.Games["game-renamed"])
	}
}

// TestAdminGameDelete_RemovesGameAndColor_Integration asserts that POST /games/delete
// removes the game from the list and its color entry on disk.
func TestAdminGameDelete_RemovesGameAndColor_Integration(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("d", 32)
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(usersPath, []byte(gamesTestUsersYAML), 0o644); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := os.WriteFile(classrulesPath, []byte(gamesTestClassrulesForSaveYAML), 0o644); err != nil {
		t.Fatalf("write classrules: %v", err)
	}

	srv, cl := buildGamesServerFull(t, jwtSecret, usersPath, classrulesPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("name", "game-beta")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/delete",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /games/delete: status=%d body=%q, want 303", rec.Code, rec.Body.String())
	}

	cfg := cl.Current()
	if cfg == nil {
		t.Fatal("loader nil after delete")
	}
	for _, g := range cfg.Games {
		if g == "game-beta" {
			t.Error("game-beta still in games list after delete")
		}
	}
	if _, exists := cfg.GraphColors.Games["game-beta"]; exists {
		t.Error("game-beta color should not exist after delete")
	}
	if cfg.GraphColors.Games["game-alpha"] != "#E5C07B" {
		t.Errorf("game-alpha color = %q after delete, want #E5C07B (should be preserved)", cfg.GraphColors.Games["game-alpha"])
	}
}

// TestAdminGameSave_DuplicateName_Integration asserts that attempting to add a
// duplicate game name redirects with an error flash and does NOT modify the file.
func TestAdminGameSave_DuplicateName_Integration(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("x", 32)
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(usersPath, []byte(gamesTestUsersYAML), 0o644); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := os.WriteFile(classrulesPath, []byte(gamesTestClassrulesForSaveYAML), 0o644); err != nil {
		t.Fatalf("write classrules: %v", err)
	}
	original, _ := os.ReadFile(classrulesPath)

	srv, _ := buildGamesServerFull(t, jwtSecret, usersPath, classrulesPath)
	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	form := url.Values{}
	form.Set("original", "")
	form.Set("name", "game-alpha") // already exists
	form.Set("color", "#000000")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games/save",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 on error, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "flashErr=1") {
		t.Errorf("expected error flash in redirect, got %q", loc)
	}
	after, _ := os.ReadFile(classrulesPath)
	if string(after) != string(original) {
		t.Error("classification-rules.yaml was modified despite duplicate rejection")
	}
}

// TestAdminGameSave_MemberGets403_Integration asserts that a member user gets
// 403 on both save and delete routes.
func TestAdminGameSave_MemberGets403_Integration(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("m", 32)
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	if err := os.WriteFile(usersPath, []byte(gamesTestUsersYAML), 0o644); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := os.WriteFile(classrulesPath, []byte(gamesTestClassrulesForSaveYAML), 0o644); err != nil {
		t.Fatalf("write classrules: %v", err)
	}

	srv, _ := buildGamesServerFull(t, jwtSecret, usersPath, classrulesPath)
	memberCookie := mintSessionCookieForEmail(t, srv, "member@vivastudios.com", jwtSecret)

	for _, path := range []string{"/dashboard/admin/games/save", "/dashboard/admin/games/delete"} {
		form := url.Values{}
		form.Set("name", "game-alpha")
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(memberCookie)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s for member: status=%d, want 403", path, rec.Code)
		}
	}
}

// ─── AG6: git auto-commit failure is non-fatal ────────────────────────────────
// The temp directory is NOT a git repo, so git add will fail. The write +
// reload must still succeed (git failure is non-fatal per spec).

func TestAdminGames_AG6_GitFailureIsNonFatal(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)

	// Build a server with an explicit reload counter to verify reload fires.
	userLoader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("NewYAMLLoader: %v", err)
	}
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(userLoader, "", jwtSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	cl, err := classrules.NewClassrulesLoader(classrulesPath)
	if err != nil {
		t.Fatalf("NewClassrulesLoader: %v", err)
	}

	reloadCalled := false
	srv := New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(userLoader, jwtSecret),
		WithUserDirectoryReload(userLoader.Reload),
		WithUsersFilePath(usersPath),
		WithClassrulesReload(func() error {
			reloadCalled = true
			return cl.Reload()
		}),
		WithClassrulesCurrentGames(func() []string {
			cfg := cl.Current()
			if cfg == nil {
				return nil
			}
			return cfg.Games
		}),
		WithClassrulesFilePath(classrulesPath),
	)

	adminCookie := mintSessionCookieForEmail(t, srv, "admin@vivastudios.com", jwtSecret)

	// classrulesPath is in a temp dir that is NOT a git repo → git add will fail.
	form := url.Values{}
	form.Set("games", "game-new-1\ngame-new-2")
	req := httptest.NewRequest(http.MethodPost, "/dashboard/admin/games", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	// Must succeed (200 or 303) despite git failure.
	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Errorf("POST /dashboard/admin/games with git-fail dir: status=%d body=%q, want 200 or 303", rec.Code, rec.Body.String())
	}

	// Reload must have been called.
	if !reloadCalled {
		t.Error("classrulesReloadFn was NOT called after write — should fire even when git fails")
	}

	// Loader must reflect the new games.
	cfg := cl.Current()
	if cfg == nil || len(cfg.Games) != 2 {
		t.Errorf("expected 2 games after git-fail write, got: %v", cfg)
	}
}
