package triage_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/triage"
)

// TestServer_HandlerReturnsOK verifies the stub index route returns 200 OK
// and the expected placeholder body. Handler tests use httptest.NewRecorder
// so no real listener or port is needed.
func TestServer_HandlerReturnsOK(t *testing.T) {
	srv := triage.New(nil, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "triage") {
		t.Errorf("want body to mention 'triage', got: %q", body)
	}
}

// TestServer_HealthRoute verifies the /health stub route returns 200 OK.
func TestServer_HealthRoute(t *testing.T) {
	srv := triage.New(nil, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

// TestServer_BindsOnPort verifies the server can bind to a real listener on
// an OS-assigned port and that the index route is reachable over HTTP.
// Skip in -short because it opens a real TCP socket.
func TestServer_BindsOnPort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-listener test in -short mode")
	}

	// Use port 0 to let the OS pick a free port; avoids collisions.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open test listener: %v", err)
	}
	defer ln.Close()

	srv := triage.New(nil, 0)
	// Start serving in background; Close the listener to stop it.
	go func() { _ = http.Serve(ln, srv.Handler()) }()

	addr := ln.Addr().String()
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

// TestServer_BrowserOpenerCalled verifies that Start invokes the injected
// browser-open seam exactly once with a URL containing the configured port.
// Uses a real listener on port :0 and shuts down immediately via the listener.
func TestServer_BrowserOpenerCalled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-listener test in -short mode")
	}

	var called []string

	srv := triage.New(nil, 0)
	srv.SetBrowserOpener(func(url string) error {
		called = append(called, url)
		return nil
	})

	// Provide a real listener on port :0 so Start binds immediately.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open test listener: %v", err)
	}
	defer ln.Close()
	srv.SetListener(ln)

	// Run Start in background; closing the listener stops it.
	done := make(chan error, 1)
	go func() { done <- srv.Start() }()

	// Give the goroutine a moment to call the opener before closing.
	ln.Close() // stops http.Serve
	<-done     // wait for Start to return

	if len(called) != 1 {
		t.Fatalf("expected browser opener called once, got %d times", len(called))
	}
	if !strings.HasPrefix(called[0], "http://127.0.0.1:") {
		t.Errorf("expected loopback URL, got %q", called[0])
	}
}

// TestServer_BrowserOpenerSkippedWhenEnvSet verifies that setting
// ENGRAM_TRIAGE_NO_BROWSER=1 suppresses the auto-open call.
func TestServer_BrowserOpenerSkippedWhenEnvSet(t *testing.T) {
	t.Setenv("ENGRAM_TRIAGE_NO_BROWSER", "1")

	var called int

	srv := triage.New(nil, 0)
	srv.SetBrowserOpener(func(url string) error {
		called++
		return nil
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open test listener: %v", err)
	}
	defer ln.Close()
	srv.SetListener(ln)

	done := make(chan error, 1)
	go func() { done <- srv.Start() }()
	ln.Close()
	<-done

	if called != 0 {
		t.Errorf("expected browser opener NOT called, but was called %d times", called)
	}
}
