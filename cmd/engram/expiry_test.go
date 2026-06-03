package main

import (
	"strings"
	"testing"
)

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
