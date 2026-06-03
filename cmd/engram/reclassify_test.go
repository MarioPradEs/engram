package main

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// TestReclassifyCommandIntegration is an integration test (skipped with -short) for
// the engram reclassify command. It seeds a local SQLite database with a mixed set of
// observations and asserts the correct outcomes:
//   - project-scoped obs → classified (marker set)
//   - personal-scoped obs → skipped_personal (no marker)
//   - client-nda project obs → skipped_client_nda (no marker)
//   - secret-pattern obs → skipped_secret_scan (no marker)
//   - IsReclassifyComplete returns true after run
//   - Idempotent: re-run does not fail and all markers remain stable
func TestReclassifyCommandIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	cfg := testConfig(t)
	withArgs(t, "engram", "reclassify")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}

	// Seed sessions and observations.
	for _, sess := range []struct{ id, project string }{
		{"sess-proj", "team-x"},
		{"sess-personal", "team-x"},
		{"sess-client", "client-acme"},
		{"sess-secret", "team-x"},
	} {
		if err := s.CreateSession(sess.id, sess.project, "/tmp"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}

	projectObsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-proj",
		Type:      "architecture",
		Title:     "Normal project observation",
		Content:   "This is a normal project observation without secrets.",
		Project:   "team-x",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation project: %v", err)
	}

	personalObsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-personal",
		Type:      "manual",
		Title:     "Personal note",
		Content:   "My private todo list.",
		Project:   "team-x",
		Scope:     "personal",
	})
	if err != nil {
		t.Fatalf("AddObservation personal: %v", err)
	}

	clientObsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-client",
		Type:      "architecture",
		Title:     "Client project observation",
		Content:   "Client NDA project work.",
		Project:   "client-acme",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation client: %v", err)
	}

	secretObsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-secret",
		Type:      "manual",
		Title:     "Obs with secret",
		Content:   "API_KEY=sk-abc1234567890abcdefghijklmnopqrstuvwxyz1234",
		Project:   "team-x",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation secret: %v", err)
	}

	_ = s.Close()

	// Run reclassify and capture output.
	stdout, stderr := captureOutput(t, func() {
		cmdReclassify(cfg)
	})

	combined := stdout + stderr

	// Must report classified for project obs.
	if !strings.Contains(combined, "classified") {
		t.Errorf("expected 'classified' in output, got: %q", combined)
	}
	// Must report skipped outcomes.
	if !strings.Contains(combined, "skipped_personal") {
		t.Errorf("expected 'skipped_personal' in output, got: %q", combined)
	}
	if !strings.Contains(combined, "skipped_client_nda") {
		t.Errorf("expected 'skipped_client_nda' in output, got: %q", combined)
	}
	if !strings.Contains(combined, "skipped_secret_scan") {
		t.Errorf("expected 'skipped_secret_scan' in output, got: %q", combined)
	}

	// Verify markers in database.
	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s2.Close()

	assertClassifiedByV2(t, s2, projectObsID, true, "project obs")
	assertClassifiedByV2(t, s2, personalObsID, false, "personal obs")
	assertClassifiedByV2(t, s2, clientObsID, false, "client obs")
	assertClassifiedByV2(t, s2, secretObsID, false, "secret obs")

	// IsReclassifyComplete must be true.
	complete, err := s2.IsReclassifyComplete(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("IsReclassifyComplete: %v", err)
	}
	if !complete {
		t.Error("expected IsReclassifyComplete=true after reclassify run")
	}

	// Idempotence: re-run must not fail and markers must remain stable.
	withArgs(t, "engram", "reclassify")
	captureOutput(t, func() {
		cmdReclassify(cfg)
	})

	assertClassifiedByV2(t, s2, projectObsID, true, "project obs after re-run")
	assertClassifiedByV2(t, s2, personalObsID, false, "personal obs after re-run")
	assertClassifiedByV2(t, s2, clientObsID, false, "client obs after re-run")
	assertClassifiedByV2(t, s2, secretObsID, false, "secret obs after re-run")
}

// assertClassifiedByV2 checks that the classified_by_v2 field on the given observation
// matches the expected value.
func assertClassifiedByV2(t *testing.T, s *store.Store, id int64, want bool, label string) {
	t.Helper()
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation(%d) [%s]: %v", id, label, err)
	}
	if obs.ClassifiedByV2 != want {
		t.Errorf("[%s] obs#%d: classified_by_v2=%v, want %v", label, id, obs.ClassifiedByV2, want)
	}
}
