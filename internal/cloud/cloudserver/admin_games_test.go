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
