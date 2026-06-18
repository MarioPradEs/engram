package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPrincipalFromRequest_NilGetUserEmail verifies that principalFromRequest
// returns an empty email when GetUserEmail is nil on MountConfig.
func TestPrincipalFromRequest_NilGetUserEmail(t *testing.T) {
	h := &handlers{cfg: MountConfig{}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	p := h.principalFromRequest(req)
	if p.Email() != "" {
		t.Errorf("nil GetUserEmail: expected empty email, got %q", p.Email())
	}
}

// TestPrincipalFromRequest_EmailFromClosure verifies that email comes from GetUserEmail closure.
func TestPrincipalFromRequest_EmailFromClosure(t *testing.T) {
	want := "bob@example.com"
	h := &handlers{cfg: MountConfig{
		GetUserEmail: func(_ *http.Request) string { return want },
	}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	p := h.principalFromRequest(req)
	if p.Email() != want {
		t.Errorf("GetUserEmail set: expected email=%q, got %q", want, p.Email())
	}
}

// TestPrincipalFromRequest_EmptyWhenClosureReturnsEmpty verifies the empty-email case.
func TestPrincipalFromRequest_EmptyWhenClosureReturnsEmpty(t *testing.T) {
	h := &handlers{cfg: MountConfig{
		GetUserEmail: func(_ *http.Request) string { return "" },
	}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	p := h.principalFromRequest(req)
	if p.Email() != "" {
		t.Errorf("empty closure: expected empty email, got %q", p.Email())
	}
}
