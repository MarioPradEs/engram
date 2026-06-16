package triage_test

import "github.com/Gentleman-Programming/engram/internal/triage"

// WU-C1: Compile-time assertion that fakeMutableStore satisfies MutableTriageStore
// after the interface gains ObservationsByTag + DistinctTagValues.
// This will FAIL (RED) until the interface and fake are extended (WU-C2).
var _ triage.MutableTriageStore = (*fakeMutableStore)(nil)
