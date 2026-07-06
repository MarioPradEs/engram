package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeExpiryCredentials writes a minimal credentials.json with the given ExpiresAt
// into dir and returns the dir path.
func writeExpiryCredentials(t *testing.T, dir string, expiresAt time.Time) {
	t.Helper()
	creds := struct {
		AccessToken string `json:"access_token"`
		IssuedAt    string `json:"issued_at"`
		ExpiresAt   string `json:"expires_at"`
		Email       string `json:"email"`
	}{
		AccessToken: "tok",
		IssuedAt:    time.Now().UTC().Format(time.RFC3339),
		ExpiresAt:   expiresAt.UTC().Format(time.RFC3339),
		Email:       "test@vivastudios.com",
	}
	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		t.Fatalf("writeExpiryCredentials marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), b, 0600); err != nil {
		t.Fatalf("writeExpiryCredentials write: %v", err)
	}
}

// TestWarnIfSessionExpiringSoon verifies the proactive expiry-warning function.
// Spec: session-expiry-warning §Proactive Expiry Warning at Startup.
func TestWarnIfSessionExpiringSoon(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	threshold := 7 * 24 * time.Hour

	cases := []struct {
		name      string
		expiresAt time.Time
		wantWarn  bool
	}{
		{
			name:      "expires in 3d → warn",
			expiresAt: now.Add(3 * 24 * time.Hour),
			wantWarn:  true,
		},
		{
			name:      "expires in 30d → silent",
			expiresAt: now.Add(30 * 24 * time.Hour),
			wantWarn:  false,
		},
		{
			name:      "already expired → silent (reactive path handles it)",
			expiresAt: now.Add(-1 * time.Hour),
			wantWarn:  false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			credDir := t.TempDir()
			writeExpiryCredentials(t, credDir, tc.expiresAt)

			var buf strings.Builder
			warnIfSessionExpiringSoon(&buf, credDir, threshold, now)

			hasWarn := buf.Len() > 0
			if hasWarn != tc.wantWarn {
				t.Errorf("warnIfSessionExpiringSoon: warnWritten=%v, wantWarn=%v (output: %q)",
					hasWarn, tc.wantWarn, buf.String())
			}
		})
	}
}

// writeExpiryCredentialsWithToken writes a credentials.json with a caller-supplied
// access_token value. Use to assert that warnIfSessionExpiringSoon does NOT inspect
// or decode the access_token field.
func writeExpiryCredentialsWithToken(t *testing.T, dir string, accessToken string, expiresAt time.Time) {
	t.Helper()
	creds := struct {
		AccessToken string `json:"access_token"`
		IssuedAt    string `json:"issued_at"`
		ExpiresAt   string `json:"expires_at"`
		Email       string `json:"email"`
	}{
		AccessToken: accessToken,
		IssuedAt:    time.Now().UTC().Format(time.RFC3339),
		ExpiresAt:   expiresAt.UTC().Format(time.RFC3339),
		Email:       "test@vivastudios.com",
	}
	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		t.Fatalf("writeExpiryCredentialsWithToken marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), b, 0600); err != nil {
		t.Fatalf("writeExpiryCredentialsWithToken write: %v", err)
	}
}

// TestWarnIfSessionExpiringSoon_NoDecodeAccessToken verifies S2:
// warnIfSessionExpiringSoon reads ExpiresAt only — it MUST NOT attempt to parse
// or validate the access_token field. A garbage (non-JWT) access_token must not
// affect the warning decision; the function must produce the same result it would
// with a valid token.
func TestWarnIfSessionExpiringSoon_NoDecodeAccessToken(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	threshold := 7 * 24 * time.Hour
	const garbageToken = "not-a-jwt.garbage"

	cases := []struct {
		name      string
		expiresAt time.Time
		wantWarn  bool
	}{
		{
			name:      "garbage access_token, expires in 3d → warn",
			expiresAt: now.Add(3 * 24 * time.Hour),
			wantWarn:  true,
		},
		{
			name:      "garbage access_token, expires in 30d → silent",
			expiresAt: now.Add(30 * 24 * time.Hour),
			wantWarn:  false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			credDir := t.TempDir()
			writeExpiryCredentialsWithToken(t, credDir, garbageToken, tc.expiresAt)

			var buf strings.Builder
			warnIfSessionExpiringSoon(&buf, credDir, threshold, now)

			hasWarn := buf.Len() > 0
			if hasWarn != tc.wantWarn {
				t.Errorf("S2: warnWritten=%v, wantWarn=%v (output: %q)",
					hasWarn, tc.wantWarn, buf.String())
			}
		})
	}
}

// TestCloudSyncFailureMessage401PrintsExpiryMessage verifies that cloudSyncFailureMessage
// returns the canonical expiry prompt when the error is an HTTP 401.
// Spec: cli-auth §Token Lifetime and Expiry Handling — expired token triggers login prompt.
func TestCloudSyncFailureMessage401PrintsExpiryMessage(t *testing.T) {
	err401 := makeHTTPStatusError401()

	msg := cloudSyncFailureMessage("team-x", err401)
	const want = "Session expired. Run 'engram login' to re-authenticate."
	if msg != want {
		t.Errorf("cloudSyncFailureMessage on 401:\ngot:  %q\nwant: %q", msg, want)
	}
}

// TestCloudSyncFailureMessageNon401UsesGuidance verifies that non-401 errors fall
// through to the normal sync guidance message (not the expiry prompt).
func TestCloudSyncFailureMessageNon401UsesGuidance(t *testing.T) {
	networkErr := makeHTTPStatusError403()

	msg := cloudSyncFailureMessage("team-x", networkErr)
	const expirySubstr = "Session expired"
	if strings.Contains(msg, expirySubstr) {
		t.Errorf("non-401 should NOT produce expiry message, got: %q", msg)
	}
}
