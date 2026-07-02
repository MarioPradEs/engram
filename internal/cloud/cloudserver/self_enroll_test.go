package cloudserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cloudauth "github.com/Gentleman-Programming/engram/internal/cloud/auth"
	"github.com/Gentleman-Programming/engram/internal/cloud/users"
)

// selfEnrollJWTSecret is a test-only signing secret for self-enroll handler tests.
const selfEnrollJWTSecret = "self-enroll-test-secret-that-is-32-bytes-long"

// selfEnrollBaseYAML is the standard two-user YAML used by most self-enroll tests.
// Alice is admin with no initial enrolled projects; Bob has [general].
const selfEnrollBaseYAML = `users:
  - email: alice@vivastudios.com
    name: Alice
    department: dev
    role: admin
    status: active
  - email: bob@vivastudios.com
    name: Bob
    department: dev
    role: member
    status: active
    enrolled:
      - general
`

// selfEnrollWithProjectYAML has alice already enrolled in [general, remove-me].
const selfEnrollWithProjectYAML = `users:
  - email: alice@vivastudios.com
    name: Alice
    department: dev
    role: admin
    status: active
    enrolled:
      - general
      - remove-me
  - email: bob@vivastudios.com
    name: Bob
    department: dev
    role: member
    status: active
    enrolled:
      - general
`

// selfEnrollConcurrentYAML has alice with no enrolled projects for the concurrency test.
const selfEnrollConcurrentYAML = `users:
  - email: alice@vivastudios.com
    name: Alice
    department: dev
    role: admin
    status: active
`

// ─── test helpers ─────────────────────────────────────────────────────────────

// buildSelfEnrollServer builds a CloudServer with real auth + users.yaml wiring
// for self-enroll handler tests. Returns (server, loader, usersPath).
func buildSelfEnrollServer(t *testing.T, yamlContent string) (*CloudServer, *users.YAMLLoader, string) {
	t.Helper()
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	if err := os.WriteFile(usersPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("buildSelfEnrollServer: write yaml: %v", err)
	}
	loader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("buildSelfEnrollServer: NewYAMLLoader: %v", err)
	}
	ha, err := cloudauth.NewHeaderAuthenticatorWithJWT(loader, "", selfEnrollJWTSecret)
	if err != nil {
		t.Fatalf("buildSelfEnrollServer: NewHeaderAuthenticatorWithJWT: %v", err)
	}
	srv := New(&fakeStore{}, ha, 0,
		WithAuthEndpoint(loader, selfEnrollJWTSecret),
		WithUserDirectoryReload(loader.Reload),
		WithUsersFilePath(usersPath),
	)
	return srv, loader, usersPath
}

// mintEnrollJWT mints a Bearer JWT for email using the test secret.
func mintEnrollJWT(t *testing.T, email string) string {
	t.Helper()
	token, err := cloudauth.MintJWT(selfEnrollJWTSecret, cloudauth.JWTClaims{
		Sub:        email,
		Email:      email,
		Name:       "Test User",
		Department: "dev",
		Role:       "admin",
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("mintEnrollJWT: %v", err)
	}
	return token
}

// readEnrolledFromFile reads the YAML file fresh and returns the Enrolled slice for email.
func readEnrolledFromFile(t *testing.T, usersPath, email string) []string {
	t.Helper()
	loader, err := users.NewYAMLLoader(usersPath)
	if err != nil {
		t.Fatalf("readEnrolledFromFile: reload: %v", err)
	}
	p, ok := loader.Lookup(email)
	if !ok {
		t.Fatalf("readEnrolledFromFile: %q not found in %q", email, usersPath)
	}
	return p.Enrolled
}

// selfEnrollJSON marshals v to a JSON request body buffer.
func selfEnrollJSON(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("selfEnrollJSON: marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

// ─── Phase 11.1 — HappyPath ──────────────────────────────────────────────────

func TestSelfEnrollProject_HappyPath(t *testing.T) {
	srv, _, usersPath := buildSelfEnrollServer(t, selfEnrollBaseYAML)
	token := mintEnrollJWT(t, "alice@vivastudios.com")

	req := httptest.NewRequest(http.MethodPost, "/user/enrolled-projects",
		selfEnrollJSON(t, map[string]string{"project": "foo"}))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	enrolled := readEnrolledFromFile(t, usersPath, "alice@vivastudios.com")
	for _, p := range enrolled {
		if p == "foo" {
			return
		}
	}
	t.Fatalf("expected 'foo' in alice's enrolled list, got %v", enrolled)
}

// ─── Phase 11.2 — Idempotent ─────────────────────────────────────────────────

func TestSelfEnrollProject_Idempotent(t *testing.T) {
	srv, _, usersPath := buildSelfEnrollServer(t, selfEnrollBaseYAML)
	token := mintEnrollJWT(t, "alice@vivastudios.com")

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/user/enrolled-projects",
			selfEnrollJSON(t, map[string]string{"project": "dup"}))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: expected 200, got %d body=%q", i+1, rec.Code, rec.Body.String())
		}
	}

	enrolled := readEnrolledFromFile(t, usersPath, "alice@vivastudios.com")
	count := 0
	for _, p := range enrolled {
		if p == "dup" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one 'dup' in enrolled, got %d (full list: %v)", count, enrolled)
	}
}

// ─── Phase 11.3 — Unauthenticated → 401 ──────────────────────────────────────

func TestSelfEnrollProject_Unauthenticated(t *testing.T) {
	srv, _, usersPath := buildSelfEnrollServer(t, selfEnrollBaseYAML)

	req := httptest.NewRequest(http.MethodPost, "/user/enrolled-projects",
		selfEnrollJSON(t, map[string]string{"project": "secret"}))
	// No Authorization header.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", rec.Code, rec.Body.String())
	}

	// users.yaml must be unmodified — alice must not have 'secret' enrolled.
	enrolled := readEnrolledFromFile(t, usersPath, "alice@vivastudios.com")
	for _, p := range enrolled {
		if p == "secret" {
			t.Fatalf("users.yaml must be unmodified after failed auth, but found 'secret': %v", enrolled)
		}
	}
}

// ─── Phase 11.4 — EmptyProject → 400 ─────────────────────────────────────────

func TestSelfEnrollProject_EmptyProject(t *testing.T) {
	srv, _, _ := buildSelfEnrollServer(t, selfEnrollBaseYAML)
	token := mintEnrollJWT(t, "alice@vivastudios.com")

	req := httptest.NewRequest(http.MethodPost, "/user/enrolled-projects",
		selfEnrollJSON(t, map[string]string{"project": ""}))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── Phase 12.1 — Unenroll HappyPath ─────────────────────────────────────────

func TestSelfUnenrollProject_HappyPath(t *testing.T) {
	srv, _, usersPath := buildSelfEnrollServer(t, selfEnrollWithProjectYAML)
	token := mintEnrollJWT(t, "alice@vivastudios.com")

	req := httptest.NewRequest(http.MethodDelete, "/user/enrolled-projects",
		selfEnrollJSON(t, map[string]string{"project": "remove-me"}))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	enrolled := readEnrolledFromFile(t, usersPath, "alice@vivastudios.com")
	for _, p := range enrolled {
		if p == "remove-me" {
			t.Fatalf("expected 'remove-me' removed from enrolled, but still present: %v", enrolled)
		}
	}
}

// ─── Phase 12.2 — Unenroll Idempotent ────────────────────────────────────────

func TestSelfUnenrollProject_Idempotent(t *testing.T) {
	srv, _, _ := buildSelfEnrollServer(t, selfEnrollBaseYAML) // alice has no enrolled projects
	token := mintEnrollJWT(t, "alice@vivastudios.com")

	// DELETE a project that is not enrolled → still 200 (no-op).
	req := httptest.NewRequest(http.MethodDelete, "/user/enrolled-projects",
		selfEnrollJSON(t, map[string]string{"project": "not-enrolled"}))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for idempotent unenroll, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// ─── Phase 12.3 — No-op unenroll must not modify users.yaml ──────────────────

// TestSelfUnenrollProject_NoOpDoesNotWrite asserts that unenrolling a project
// the caller is NOT enrolled in (idempotent / no-op case) does NOT write or
// modify users.yaml.  Spec requirement: "users.yaml is not modified unnecessarily."
func TestSelfUnenrollProject_NoOpDoesNotWrite(t *testing.T) {
	// alice starts with no enrolled projects (selfEnrollBaseYAML).
	srv, _, usersPath := buildSelfEnrollServer(t, selfEnrollBaseYAML)
	token := mintEnrollJWT(t, "alice@vivastudios.com")

	// Capture file state before the request.
	statBefore, err := os.Stat(usersPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	bytesBefore, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	// DELETE a project that alice is NOT enrolled in → must be a pure no-op.
	req := httptest.NewRequest(http.MethodDelete, "/user/enrolled-projects",
		selfEnrollJSON(t, map[string]string{"project": "not-enrolled-project"}))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for no-op unenroll, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Assert users.yaml was NOT modified (neither content nor mtime).
	statAfter, err := os.Stat(usersPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	bytesAfter, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("users.yaml mtime changed: before=%v after=%v — spurious write on no-op unenroll",
			statBefore.ModTime(), statAfter.ModTime())
	}
	if !bytes.Equal(bytesBefore, bytesAfter) {
		t.Errorf("users.yaml content changed on no-op unenroll:\nbefore: %s\nafter:  %s",
			bytesBefore, bytesAfter)
	}
}

// ─── Phase 13.1 — Concurrent safety ──────────────────────────────────────────

func TestSelfEnroll_Concurrent(t *testing.T) {
	srv, _, usersPath := buildSelfEnrollServer(t, selfEnrollConcurrentYAML)
	token := mintEnrollJWT(t, "alice@vivastudios.com")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			project := fmt.Sprintf("proj-%d", idx)
			body, _ := json.Marshal(map[string]string{"project": project})
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/user/enrolled-projects", bytes.NewReader(body))
			if err != nil {
				t.Errorf("goroutine %d: create request: %v", idx, err)
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("goroutine %d: request failed: %v", idx, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("goroutine %d: expected 200, got %d", idx, resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()

	enrolled := readEnrolledFromFile(t, usersPath, "alice@vivastudios.com")
	if len(enrolled) != n {
		t.Fatalf("expected %d enrolled projects, got %d: %v", n, len(enrolled), enrolled)
	}
	seen := make(map[string]int, n)
	for _, p := range enrolled {
		seen[p]++
	}
	for proj, count := range seen {
		if count > 1 {
			t.Fatalf("duplicate project %q in enrolled (count=%d): %v", proj, count, enrolled)
		}
	}
}

// ─── Phase 15.1 — Scope boundary ─────────────────────────────────────────────

func TestSelfEnroll_ScopeBoundary(t *testing.T) {
	srv, _, usersPath := buildSelfEnrollServer(t, selfEnrollBaseYAML)
	token := mintEnrollJWT(t, "alice@vivastudios.com") // alice's JWT

	// Body includes "as_user" pointing at bob — handler MUST ignore it.
	req := httptest.NewRequest(http.MethodPost, "/user/enrolled-projects",
		selfEnrollJSON(t, map[string]any{
			"project": "bar",
			"as_user": "bob@vivastudios.com",
		}))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	// Alice's Enrolled must contain "bar".
	aliceEnrolled := readEnrolledFromFile(t, usersPath, "alice@vivastudios.com")
	foundBar := false
	for _, p := range aliceEnrolled {
		if p == "bar" {
			foundBar = true
			break
		}
	}
	if !foundBar {
		t.Fatalf("expected 'bar' in alice enrolled, got %v", aliceEnrolled)
	}

	// Bob's Enrolled must be unchanged — "bar" must NOT appear.
	bobEnrolled := readEnrolledFromFile(t, usersPath, "bob@vivastudios.com")
	for _, p := range bobEnrolled {
		if strings.EqualFold(p, "bar") {
			t.Fatalf("bob's enrolled should not contain 'bar', got %v", bobEnrolled)
		}
	}
}
