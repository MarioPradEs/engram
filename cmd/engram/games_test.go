package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── SGS1: happy path — server returns games, file is updated, other sections preserved ──

// TestSyncGames_SGS1_HappyPath verifies that syncGames fetches the games list
// from the server, writes it to the classrules file, and preserves non-games
// sections (departments, rules).
func TestSyncGames_SGS1_HappyPath(t *testing.T) {
	t.Parallel()

	// Seed classrules file in a temp directory.
	dir := t.TempDir()
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	initialContent := `departments:
  - name: engineering
    aliases:
      - eng
games:
  - "old-game"
rules: |
  Keep this rules section intact.
`
	if err := os.WriteFile(classrulesPath, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial classrules: %v", err)
	}

	// Start a fake server returning a canned games list.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/classrules/games" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Assert Bearer header is forwarded.
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"games": []string{"game-new-1", "game-new-2", "game-new-3"},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	n, err := syncGames(srv.URL, "test-bearer-token", classrulesPath)
	if err != nil {
		t.Fatalf("syncGames: unexpected error: %v", err)
	}
	if n != 3 {
		t.Errorf("syncGames: expected 3 games synced, got %d", n)
	}

	// Read back and check that games are updated AND departments/rules are preserved.
	content, err := os.ReadFile(classrulesPath)
	if err != nil {
		t.Fatalf("read classrules after sync: %v", err)
	}
	body := string(content)

	if !strings.Contains(body, "game-new-1") {
		t.Errorf("expected 'game-new-1' in updated classrules, got:\n%s", body)
	}
	if !strings.Contains(body, "game-new-2") {
		t.Errorf("expected 'game-new-2' in updated classrules, got:\n%s", body)
	}
	if !strings.Contains(body, "game-new-3") {
		t.Errorf("expected 'game-new-3' in updated classrules, got:\n%s", body)
	}
	if strings.Contains(body, "old-game") {
		t.Errorf("expected 'old-game' to be replaced, but it is still present:\n%s", body)
	}
	if !strings.Contains(body, "engineering") {
		t.Errorf("expected 'engineering' department to be preserved, got:\n%s", body)
	}
	if !strings.Contains(body, "Keep this rules section intact") {
		t.Errorf("expected rules section to be preserved, got:\n%s", body)
	}
}

// ─── SGS2: server returns non-200 → error ────────────────────────────────────

// TestSyncGames_SGS2_ServerNon200ReturnsError verifies that syncGames returns an
// error (and does NOT modify the file) when the server responds with non-200.
func TestSyncGames_SGS2_ServerNon200ReturnsError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	initialContent := `games:
  - "original-game"
`
	if err := os.WriteFile(classrulesPath, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial classrules: %v", err)
	}
	originalContent, _ := os.ReadFile(classrulesPath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := syncGames(srv.URL, "bad-token", classrulesPath)
	if err == nil {
		t.Fatal("syncGames: expected error on server 401, got nil")
	}

	// File must be unchanged.
	after, _ := os.ReadFile(classrulesPath)
	if string(after) != string(originalContent) {
		t.Errorf("classrules was modified despite server error")
	}
}

// ─── SGS3: server returns empty games list → error (WriteGames rejects it) ────

// TestSyncGames_SGS3_EmptyGamesListReturnsError verifies that syncGames returns
// an error when the server returns an empty games list, because WriteGames
// requires at least one game.
func TestSyncGames_SGS3_EmptyGamesListReturnsError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	initialContent := `games:
  - "original-game"
`
	if err := os.WriteFile(classrulesPath, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial classrules: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"games": []string{}})
	}))
	defer srv.Close()

	_, err := syncGames(srv.URL, "token", classrulesPath)
	if err == nil {
		t.Fatal("syncGames: expected error for empty games list, got nil")
	}
}

// ─── S1: server unreachable → error, file unchanged ──────────────────────────

// TestSyncGames_S1_ServerUnreachable verifies that syncGames returns a non-nil
// error when the server is unreachable (connection refused) and that the local
// classrules file is left completely unchanged.
func TestSyncGames_S1_ServerUnreachable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	initialContent := `departments:
  - name: engineering
    aliases:
      - eng
games:
  - "original-game"
rules: |
  Keep this section intact.
`
	if err := os.WriteFile(classrulesPath, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial classrules: %v", err)
	}
	originalContent, _ := os.ReadFile(classrulesPath)

	// Start and immediately close a server to obtain a dead URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	_, err := syncGames(deadURL, "any-token", classrulesPath)
	if err == nil {
		t.Fatal("syncGames: expected error on connection refused, got nil")
	}

	// File must be completely unchanged.
	after, readErr := os.ReadFile(classrulesPath)
	if readErr != nil {
		t.Fatalf("read classrules after failed sync: %v", readErr)
	}
	if string(after) != string(originalContent) {
		t.Errorf("classrules was modified despite connection error:\ngot:\n%s\nwant:\n%s", after, originalContent)
	}
}

// ─── S2: server returns 200 with malformed JSON → error, file unchanged ──────

// TestSyncGames_S2_MalformedJSONOn200 verifies that syncGames returns a non-nil
// error when the server responds HTTP 200 with a non-JSON body and that the
// local classrules file is left completely unchanged.
func TestSyncGames_S2_MalformedJSONOn200(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	classrulesPath := filepath.Join(dir, "classification-rules.yaml")
	initialContent := `games:
  - "original-game"
`
	if err := os.WriteFile(classrulesPath, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial classrules: %v", err)
	}
	originalContent, _ := os.ReadFile(classrulesPath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>internal error</html>"))
	}))
	defer srv.Close()

	_, err := syncGames(srv.URL, "token", classrulesPath)
	if err == nil {
		t.Fatal("syncGames: expected error on malformed JSON response, got nil")
	}

	// File must be completely unchanged.
	after, readErr := os.ReadFile(classrulesPath)
	if readErr != nil {
		t.Fatalf("read classrules after failed sync: %v", readErr)
	}
	if string(after) != string(originalContent) {
		t.Errorf("classrules was modified despite malformed JSON response:\ngot:\n%s\nwant:\n%s", after, originalContent)
	}
}
