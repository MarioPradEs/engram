package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── B5: admin games color handler ────────────────────────────────────────────
// Tests for POST /dashboard/admin/games/{name}/color.
// Admin-gated: 403 for non-admin; 400 for invalid hex; 200 for valid hex.

// adminColorMux creates a mux with WriteColors wired (for testing the color-save handler).
// If writeColors is nil, a no-op writer is used.
// If isAdmin is true, the request will be treated as admin.
func adminColorMux(writeColors func(game string, color string) error, isAdmin bool) *http.ServeMux {
	mux := http.NewServeMux()
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:     func(_ *http.Request) bool { return isAdmin },
		WriteGameColor: func(game, color string) error {
			if writeColors != nil {
				return writeColors(game, color)
			}
			return nil
		},
	})
	return mux
}

// TestAdminGameColorPost_ValidHexReturns200 asserts that POST
// /dashboard/admin/games/{name}/color with a valid hex color
// calls WriteGameColor and returns 200.
func TestAdminGameColorPost_ValidHexReturns200(t *testing.T) {
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

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
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
