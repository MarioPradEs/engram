package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEmbeddedLoginPagesPresent ensures the Viva-branded callback pages are
// embedded at build time and carry their key markers. A regression here means
// the //go:embed directive lost its file or the asset was emptied.
func TestEmbeddedLoginPagesPresent(t *testing.T) {
	if strings.TrimSpace(loginSuccessHTML) == "" {
		t.Fatal("loginSuccessHTML is empty — //go:embed login_success.html failed")
	}
	if strings.TrimSpace(loginErrorHTML) == "" {
		t.Fatal("loginErrorHTML is empty — //go:embed login_error.html failed")
	}
	for _, marker := range []string{"Conectado", "Bienvenido a Engram Cloud", "data:image/webp"} {
		if !strings.Contains(loginSuccessHTML, marker) {
			t.Errorf("success page missing marker %q", marker)
		}
	}
	if !strings.Contains(loginErrorHTML, "No se pudo iniciar") {
		t.Error("error page missing its status marker")
	}
}

// TestWriteLoginSuccessPage verifies the success helper writes HTML with the
// correct content type and a 200 status.
func TestWriteLoginSuccessPage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeLoginSuccessPage(rec)

	if got := rec.Code; got != http.StatusOK {
		t.Errorf("status = %d, want 200", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "Conectado") {
		t.Error("body does not contain the success page")
	}
}

// TestWriteLoginErrorPage verifies the error helper honors the given status and
// writes the branded error HTML.
func TestWriteLoginErrorPage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeLoginErrorPage(rec, http.StatusBadRequest)

	if got := rec.Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "No se pudo iniciar") {
		t.Error("body does not contain the error page")
	}
}
