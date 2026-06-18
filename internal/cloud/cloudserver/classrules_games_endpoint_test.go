package cloudserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

// testJWTNow returns a fixed issuance time that is recent enough so that the
// resulting JWT is not expired during the test run (iat = now, exp = now + 7d).
func testJWTNow() time.Time {
	return time.Now().UTC()
}

// mintMemberBearer mints a raw Bearer JWT for the given email with role=member.
// The token uses the server's jwtSecret so withAuth can verify it.
func mintMemberBearer(t *testing.T, email, jwtSecret string) string {
	t.Helper()
	token, err := cloudauth.MintJWT(jwtSecret, cloudauth.JWTClaims{
		Sub:   email,
		Email: email,
		Role:  "member",
	}, testJWTNow())
	if err != nil {
		t.Fatalf("mintMemberBearer: MintJWT: %v", err)
	}
	return token
}

// mintAdminBearer mints a raw Bearer JWT for the given email with role=admin.
func mintAdminBearer(t *testing.T, email, jwtSecret string) string {
	t.Helper()
	token, err := cloudauth.MintJWT(jwtSecret, cloudauth.JWTClaims{
		Sub:   email,
		Email: email,
		Role:  "admin",
	}, testJWTNow())
	if err != nil {
		t.Fatalf("mintAdminBearer: MintJWT: %v", err)
	}
	return token
}

// ─── GCE1: unauthenticated → 401 ─────────────────────────────────────────────

// TestClassrulesGamesEndpoint_GCE1_UnauthenticatedGets401 verifies that
// GET /classrules/games without an Authorization header returns 401.
func TestClassrulesGamesEndpoint_GCE1_UnauthenticatedGets401(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)
	srv, _ := buildGamesServer(t, jwtSecret, usersPath, classrulesPath)

	req := httptest.NewRequest(http.MethodGet, "/classrules/games", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /classrules/games unauthenticated: status=%d, want 401", rec.Code)
	}
}

// ─── GCE2: member Bearer → 200 + games list ──────────────────────────────────

// TestClassrulesGamesEndpoint_GCE2_MemberBearerGets200WithGames verifies that
// a member with a valid Bearer JWT gets 200 and the configured games in the body.
func TestClassrulesGamesEndpoint_GCE2_MemberBearerGets200WithGames(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)
	srv, _ := buildGamesServer(t, jwtSecret, usersPath, classrulesPath)

	token := mintMemberBearer(t, "member@vivastudios.com", jwtSecret)

	req := httptest.NewRequest(http.MethodGet, "/classrules/games", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /classrules/games member bearer: status=%d body=%q, want 200", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var resp struct {
		Games []string `json:"games"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// gamesTestClassrulesYAML contains "game-alpha" and "game-beta".
	if len(resp.Games) != 2 {
		t.Errorf("expected 2 games, got %d: %v", len(resp.Games), resp.Games)
	}
	foundAlpha, foundBeta := false, false
	for _, g := range resp.Games {
		if g == "game-alpha" {
			foundAlpha = true
		}
		if g == "game-beta" {
			foundBeta = true
		}
	}
	if !foundAlpha {
		t.Errorf("'game-alpha' not found in response games: %v", resp.Games)
	}
	if !foundBeta {
		t.Errorf("'game-beta' not found in response games: %v", resp.Games)
	}
}

// ─── GCE3: admin Bearer → 200 + games list ───────────────────────────────────

// TestClassrulesGamesEndpoint_GCE3_AdminBearerGets200WithGames verifies that
// an admin with a valid Bearer JWT also gets 200 and the games list.
func TestClassrulesGamesEndpoint_GCE3_AdminBearerGets200WithGames(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	_, usersPath, classrulesPath := initTempGamesFiles(t)
	srv, _ := buildGamesServer(t, jwtSecret, usersPath, classrulesPath)

	token := mintAdminBearer(t, "admin@vivastudios.com", jwtSecret)

	req := httptest.NewRequest(http.MethodGet, "/classrules/games", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /classrules/games admin bearer: status=%d body=%q, want 200", rec.Code, rec.Body.String())
	}

	var resp struct {
		Games []string `json:"games"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Games) != 2 {
		t.Errorf("expected 2 games, got %d", len(resp.Games))
	}
}

// ─── GCE4: nil games getter → 200 + empty list ───────────────────────────────

// TestClassrulesGamesEndpoint_GCE4_NilGetterReturnsEmptyList verifies that
// when classrulesCurrentGamesFn is nil (not configured), the endpoint returns
// 200 with {"games": []}.
func TestClassrulesGamesEndpoint_GCE4_NilGetterReturnsEmptyList(t *testing.T) {
	t.Parallel()

	jwtSecret := strings.Repeat("g", 32)
	// Build a users loader without wiring classrules.
	userLoader := buildAuthTestLoader(t, gamesTestUsersYAML)
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(userLoader, "", jwtSecret)
	if err != nil {
		t.Fatalf("NewHeaderAuthenticatorWithJWT: %v", err)
	}
	// Build a server without WithClassrulesCurrentGames — getter remains nil.
	srv := New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(userLoader, jwtSecret),
	)

	token := mintMemberBearer(t, "member@vivastudios.com", jwtSecret)

	req := httptest.NewRequest(http.MethodGet, "/classrules/games", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /classrules/games nil getter: status=%d body=%q, want 200", rec.Code, rec.Body.String())
	}

	var resp struct {
		Games []string `json:"games"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Nil getter means no vocabulary — return empty slice, not null.
	if resp.Games == nil {
		t.Error("expected empty slice (not nil) for games when getter is nil")
	}
	if len(resp.Games) != 0 {
		t.Errorf("expected 0 games when getter is nil, got %d: %v", len(resp.Games), resp.Games)
	}
}

