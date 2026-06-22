package cloudserver

// Tests for C1-C2: GET /api/brain/identity — echo X-Forwarded-Email header.
// The endpoint is NOT behind withAuth; oauth2-proxy guarantees authentication
// upstream. The handler simply echoes the header as {"email":"<value>"}.
//
// Cases:
//   1. With X-Forwarded-Email present → 200 {"email":"alice@vivastudios.com"}.
//   2. Without X-Forwarded-Email → 200 {"email":""} (sentinel; NOT 401).
//   3. Content-Type must be application/json in both cases.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrainIdentity_WithEmail(t *testing.T) {
	t.Parallel()

	srv := New(&fakeStore{}, nil, 0)
	req := httptest.NewRequest(http.MethodGet, "/api/brain/identity", nil)
	req.Header.Set("X-Forwarded-Email", "alice@vivastudios.com")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
	var got struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if got.Email != "alice@vivastudios.com" {
		t.Errorf("expected email %q, got %q", "alice@vivastudios.com", got.Email)
	}
}

func TestBrainIdentity_WithoutEmail(t *testing.T) {
	t.Parallel()

	srv := New(&fakeStore{}, nil, 0)
	req := httptest.NewRequest(http.MethodGet, "/api/brain/identity", nil)
	// No X-Forwarded-Email header set — absent header sentinel case.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (sentinel empty, not 401), got %d body=%q", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
	var got struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if got.Email != "" {
		t.Errorf("expected empty email sentinel, got %q", got.Email)
	}
}
