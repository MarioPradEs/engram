package store

import (
	"encoding/json"
	"testing"
)

// TestApplyPulledChunk_PreservesSessionDirectoryWhenIncomingIsEmpty verifies that
// importing a cloud session mutation with an empty directory field does NOT overwrite
// the local session's existing non-empty directory.
//
// Regression guard for: fix(store): preserve local session directory when importing
// cloud session mutation with empty directory.
//
// Context: cloud chunk export strips the directory field from session-upsert
// mutation payloads before sending to the cloud (strip-directory guard). On import,
// applySessionPayloadTx must NOT blindly overwrite the local directory with the
// incoming empty string, or the local session loses its working-directory path.
func TestApplyPulledChunk_PreservesSessionDirectoryWhenIncomingIsEmpty(t *testing.T) {
	const (
		project  = "dir-preservation-proj"
		localDir = "/home/user/myproject"
	)

	// Negative case: empty incoming directory MUST NOT wipe local directory.
	t.Run("empty incoming directory preserves local directory", func(t *testing.T) {
		s := newTestStore(t)

		const (
			sessionID = "dir-pres-session-neg"
			chunkID   = "chunk-dir-pres-neg"
		)

		if err := s.EnrollProject(project); err != nil {
			t.Fatalf("EnrollProject: %v", err)
		}
		if err := s.CreateSession(sessionID, project, localDir); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		// Confirm pre-condition: directory is set.
		before, err := s.GetSession(sessionID)
		if err != nil {
			t.Fatalf("GetSession before: %v", err)
		}
		if before.Directory != localDir {
			t.Fatalf("pre-condition failed: expected directory %q, got %q", localDir, before.Directory)
		}

		// Simulate cloud import: session-upsert mutation with empty directory
		// (as stripped by the cloud-export strip-directory guard).
		payload := syncSessionPayload{
			ID:        sessionID,
			Project:   project,
			Directory: "", // omitempty — absent from JSON, decoded as "" on the other end
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}

		mutations := []SyncMutation{
			{
				Entity:    SyncEntitySession,
				EntityKey: sessionID,
				Op:        SyncOpUpsert,
				Payload:   string(payloadJSON),
				Project:   project,
			},
		}

		if err := s.ApplyPulledChunk(DefaultSyncTargetKey, chunkID, mutations); err != nil {
			t.Fatalf("ApplyPulledChunk: %v", err)
		}

		after, err := s.GetSession(sessionID)
		if err != nil {
			t.Fatalf("GetSession after: %v", err)
		}
		if after.Directory != localDir {
			t.Errorf("directory was wiped by empty-directory cloud import: expected %q, got %q (wanted preserved)", localDir, after.Directory)
		}
	})

	// Positive case: non-empty incoming directory DOES update local directory.
	t.Run("non-empty incoming directory updates local directory", func(t *testing.T) {
		s := newTestStore(t)

		const (
			sessionID = "dir-pres-session-pos"
			oldDir    = "/home/user/old-project"
			newDir    = "/home/user/new-project"
			chunkID   = "chunk-dir-pres-pos"
		)

		if err := s.EnrollProject(project); err != nil {
			t.Fatalf("EnrollProject: %v", err)
		}
		if err := s.CreateSession(sessionID, project, oldDir); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		payload := syncSessionPayload{
			ID:        sessionID,
			Project:   project,
			Directory: newDir, // non-empty: cloud has a real updated directory
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}

		mutations := []SyncMutation{
			{
				Entity:    SyncEntitySession,
				EntityKey: sessionID,
				Op:        SyncOpUpsert,
				Payload:   string(payloadJSON),
				Project:   project,
			},
		}

		if err := s.ApplyPulledChunk(DefaultSyncTargetKey, chunkID, mutations); err != nil {
			t.Fatalf("ApplyPulledChunk: %v", err)
		}

		after, err := s.GetSession(sessionID)
		if err != nil {
			t.Fatalf("GetSession after: %v", err)
		}
		if after.Directory != newDir {
			t.Errorf("directory was not updated by non-empty cloud import: expected %q, got %q", newDir, after.Directory)
		}
	})
}
