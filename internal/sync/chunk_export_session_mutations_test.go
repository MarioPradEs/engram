package sync

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// TestExport_SessionMutationInChunkMutations_FirstPushNewProject verifies that
// the cloud Export path includes session-upsert mutations in chunk.Mutations.
//
// Root cause (commit ae78fb5 gap): ListPendingSyncMutationsAfterSeq (used by
// filterByPendingMutations) filtered AND sm.entity NOT IN ('prompt', 'session'),
// stripping session mutations from the chunk. Combined with chunk.Sessions = nil
// (line 402), the server's validateChunkSessionReferences found no session entry
// and returned HTTP 400 for the first push to a new project (empty knownSessionIDs).
//
// Fix: a dedicated ListPendingSyncMutationsForExportAfterSeq method that includes
// session mutations in the result so they travel in chunk.Mutations to the server.
func TestExport_SessionMutationInChunkMutations_FirstPushNewProject(t *testing.T) {
	const (
		project   = "first-push-new-project"
		sessionID = "manual-save-first-push-new-project"
		directory = "/tmp/first-push-new-project"
	)

	s := newTestStore(t)

	if err := s.CreateSession(sessionID, project, directory); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: sessionID,
		Type:      "decision",
		Title:     "first observation on new project",
		Content:   "content that triggers the 400 on unpatched code",
		Project:   project,
		Scope:     "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(s, transport, project)

	result, err := sy.Export("test-exporter", project)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.IsEmpty {
		t.Fatal("Export returned IsEmpty=true — nothing to assert; check enrollment and seeding")
	}

	payload, ok := transport.chunks[result.ChunkID]
	if !ok {
		t.Fatalf("chunk %s not found in transport", result.ChunkID)
	}

	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("unmarshal chunk JSON: %v", err)
	}

	// Session OBJECTS must still be stripped (chunk.Sessions = nil at Export line 402).
	if len(chunk.Sessions) != 0 {
		t.Errorf("session objects must be stripped from chunk payload (local-only policy), got %d", len(chunk.Sessions))
	}
	// Prompt OBJECTS must still be stripped.
	if len(chunk.Prompts) != 0 {
		t.Errorf("prompt objects must be stripped from chunk payload (local-only policy), got %d", len(chunk.Prompts))
	}

	sessionMutations, obsMutations, promptMutations := 0, 0, 0
	for _, m := range chunk.Mutations {
		switch m.Entity {
		case store.SyncEntitySession:
			sessionMutations++
		case store.SyncEntityObservation:
			obsMutations++
		case store.SyncEntityPrompt:
			promptMutations++
		}
	}

	// Session MUTATIONS must be present so the server can satisfy observation
	// references via validateChunkSessionReferences (first-push / empty knownSessionIDs).
	if sessionMutations == 0 {
		t.Error("chunk.Mutations must contain session upsert(s) for first-push scenario — " +
			"server's validateChunkSessionReferences will return 400 without them")
	}

	// Observation mutations must be present.
	if obsMutations == 0 {
		t.Error("chunk.Mutations must contain observation mutations, got 0")
	}

	// Prompt mutations must remain excluded (local-only policy).
	if promptMutations > 0 {
		t.Errorf("chunk.Mutations must not contain prompt mutations (local-only), got %d", promptMutations)
	}
}

// TestExport_PromptMutationStillExcludedFromChunk verifies that adding a prompt
// to a project does NOT cause its mutation to appear in chunk.Mutations even after
// the session-mutation fix. Prompts are local-only by policy.
func TestExport_PromptMutationStillExcludedFromChunk(t *testing.T) {
	const (
		project   = "prompt-exclusion-project"
		sessionID = "manual-save-prompt-exclusion"
		directory = "/tmp/prompt-exclusion"
	)

	s := newTestStore(t)

	if err := s.CreateSession(sessionID, project, directory); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: sessionID,
		Type:      "manual",
		Title:     "prompt exclusion obs",
		Content:   "must appear in chunk",
		Project:   project,
		Scope:     "project",
	}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := s.AddPrompt(store.AddPromptParams{
		SessionID: sessionID,
		Content:   "user prompt — must NOT travel to cloud",
		Project:   project,
	}); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(s, transport, project)

	result, err := sy.Export("test-exporter", project)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.IsEmpty {
		t.Fatal("Export returned IsEmpty=true")
	}

	payload, ok := transport.chunks[result.ChunkID]
	if !ok {
		t.Fatalf("chunk %s not found in transport", result.ChunkID)
	}

	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("unmarshal chunk JSON: %v", err)
	}

	for _, m := range chunk.Mutations {
		if m.Entity == store.SyncEntityPrompt {
			t.Errorf("chunk.Mutations must not contain prompt mutations (local-only), found entity_key=%s", m.EntityKey)
		}
	}
}
