package store

import (
	"testing"
)

// TestReclassifyGate_DefaultComplete verifies that IsReclassifyComplete returns
// true for a fresh sync target (default = 1 so existing installs are not blocked).
func TestReclassifyGate_DefaultComplete(t *testing.T) {
	s := newTestStore(t)

	complete, err := s.IsReclassifyComplete(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("IsReclassifyComplete: %v", err)
	}
	if !complete {
		t.Errorf("expected IsReclassifyComplete=true for fresh install (default), got false")
	}
}

// TestReclassifyGate_MarkAndCheck verifies the round-trip: after MarkReclassifyComplete,
// IsReclassifyComplete returns true. After MarkReclassifyIncomplete, it returns false.
func TestReclassifyGate_MarkAndCheck(t *testing.T) {
	s := newTestStore(t)

	// Mark incomplete (Phase 3 login hook will call this on first login).
	if err := s.MarkReclassifyIncomplete(DefaultSyncTargetKey); err != nil {
		t.Fatalf("MarkReclassifyIncomplete: %v", err)
	}

	complete, err := s.IsReclassifyComplete(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("IsReclassifyComplete after incomplete: %v", err)
	}
	if complete {
		t.Errorf("expected IsReclassifyComplete=false after MarkReclassifyIncomplete, got true")
	}

	// Mark complete (Phase 3 reclassify command will call this after classification).
	if err := s.MarkReclassifyComplete(DefaultSyncTargetKey); err != nil {
		t.Fatalf("MarkReclassifyComplete: %v", err)
	}

	complete, err = s.IsReclassifyComplete(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("IsReclassifyComplete after complete: %v", err)
	}
	if !complete {
		t.Errorf("expected IsReclassifyComplete=true after MarkReclassifyComplete, got false")
	}
}
