package dashboard

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/cloudstore"
)

// countingStoreWrapper wraps a DashboardStore and counts calls to ListContributors.
type countingStoreWrapper struct {
	DashboardStore
	listContributorsCalls atomic.Int64
}

func (w *countingStoreWrapper) ListContributors(filter string) ([]cloudstore.DashboardContributorRow, error) {
	w.listContributorsCalls.Add(1)
	return w.DashboardStore.ListContributors(filter)
}

// newCountingVersionMux builds a test mux like newVersionMux but wraps the store
// with a counter so tests can assert how many times ListContributors was called.
func newCountingVersionMux(
	serverVersion string,
	latestVersion func() (string, error),
	contributors []cloudstore.DashboardContributorRow,
	isAdmin bool,
	userEmail string,
) (*http.ServeMux, *countingStoreWrapper) {
	mux := http.NewServeMux()
	inner := parityStoreStub{contributors: contributors}
	counting := &countingStoreWrapper{DashboardStore: inner}
	Mount(mux, MountConfig{
		RequireSession: func(r *http.Request) error {
			if r.URL.Query().Get("auth") == "ok" {
				return nil
			}
			return errUnauthorized
		},
		IsAdmin:       func(_ *http.Request) bool { return isAdmin },
		GetUserEmail:  func(_ *http.Request) string { return userEmail },
		Store:         counting,
		ServerVersion: serverVersion,
		LatestVersion: latestVersion,
	})
	return mux, counting
}

// TestDashboardHomeListContributorsCalledOnce_Admin asserts that for an admin
// user, handleDashboardHome calls ListContributors exactly once per request
// (for the fleet view AND the version indicator lookup combined).
// Satisfies: FIX #4 — avoid double ListContributors + O(N) scan per render.
func TestDashboardHomeListContributorsCalledOnce_Admin(t *testing.T) {
	contributors := []cloudstore.DashboardContributorRow{
		{CreatedBy: "alice@example.com", LastClientVersion: "1.20.0"},
		{CreatedBy: "bob@example.com", LastClientVersion: "1.19.0"},
	}
	mux, counting := newCountingVersionMux(
		"1.20.0",
		func() (string, error) { return "1.20.0", nil },
		contributors,
		true, // isAdmin
		"alice@example.com",
	)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	if got := counting.listContributorsCalls.Load(); got != 1 {
		t.Errorf("admin dashboard home: ListContributors called %d times, want exactly 1", got)
	}
}

// TestDashboardHomeListContributorsCalledOnce_NonAdmin asserts that for a
// non-admin user, handleDashboardHome also calls ListContributors exactly once
// (only for the version indicator lookup; fleet view is skipped).
// Satisfies: FIX #4 — avoid double ListContributors + O(N) scan per render.
func TestDashboardHomeListContributorsCalledOnce_NonAdmin(t *testing.T) {
	contributors := []cloudstore.DashboardContributorRow{
		{CreatedBy: "carol@example.com", LastClientVersion: "1.18.0"},
	}
	mux, counting := newCountingVersionMux(
		"1.20.0",
		func() (string, error) { return "1.20.0", nil },
		contributors,
		false, // isAdmin
		"carol@example.com",
	)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/?auth=ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	if got := counting.listContributorsCalls.Load(); got != 1 {
		t.Errorf("non-admin dashboard home: ListContributors called %d times, want exactly 1", got)
	}
}
