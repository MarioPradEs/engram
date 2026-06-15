package triage_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/triage"
)

// ─── CSRF Origin-check middleware tests ─────────────────────────────────────
//
// The loopback triage server has no authentication. To prevent drive-by
// cross-origin requests from an attacker-controlled page, state-changing POST
// endpoints check the Origin header. Requests with a non-loopback Origin are
// rejected with 403. Requests without an Origin header (curl, direct nav,
// same-origin forms without CORS) pass through.

// TestCSRF_CrossOriginRejected verifies that a POST with a foreign Origin
// header is rejected with 403 on each mutation endpoint.
func TestCSRF_CrossOriginRejected(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	srv.SetCwdProject("proj")
	h := srv.Handler()

	endpoints := []struct {
		method string
		path   string
		body   url.Values
	}{
		{http.MethodPost, "/observations/1/scope", url.Values{"scope": {"shared"}}},
		{http.MethodPost, "/project/proj/share-all", url.Values{}},
		{http.MethodPost, "/project/proj/classify", url.Values{"scope": {"shared"}}},
	}

	for _, ep := range endpoints {
		t.Run(ep.path, func(t *testing.T) {
			req := httptest.NewRequest(ep.method, ep.path,
				strings.NewReader(ep.body.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "https://evil.example.com")

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with foreign Origin: want 403, got %d",
					ep.method, ep.path, rec.Code)
			}
		})
	}
}

// TestCSRF_SameOriginAllowed verifies that a POST with a loopback Origin
// passes through (is not rejected by the CSRF middleware).
func TestCSRF_SameOriginAllowed(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	for _, origin := range []string{
		"http://127.0.0.1:7438",
		"http://localhost:7438",
	} {
		t.Run(origin, func(t *testing.T) {
			form := url.Values{"scope": {"shared"}}
			req := httptest.NewRequest(http.MethodPost, "/observations/1/scope",
				strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", origin)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			// Must NOT be 403 — the CSRF middleware must not block loopback origins.
			if rec.Code == http.StatusForbidden {
				t.Errorf("origin %q: want allowed (not 403), got 403", origin)
			}
		})
	}
}

// TestCSRF_NoOriginAllowed verifies that requests without an Origin header
// (curl, direct browser navigation) are allowed through.
func TestCSRF_NoOriginAllowed(t *testing.T) {
	fs := &fakeMutableStore{}
	srv := triage.NewWithMutableStore(nil, fs, 0, "")
	h := srv.Handler()

	form := url.Values{"scope": {"shared"}}
	req := httptest.NewRequest(http.MethodPost, "/observations/1/scope",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// No Origin header.

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Errorf("no-Origin request: want allowed (not 403), got 403")
	}
}
