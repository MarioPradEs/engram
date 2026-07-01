package cloudserver

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

// TestValidateChunkSessionReferences_SessionMutationSatisfiesObsRef_EmptyKnown
// verifies that a chunk whose Mutations array contains a session-upsert entry
// passes validateChunkSessionReferences when knownSessionIDs is empty.
//
// This is the critical server-side acceptance gate for the first-push scenario:
// a new project has no entries in cloud_project_sessions yet (knownSessionIDs={}),
// so the server depends entirely on chunk.Mutations session upserts to validate
// that observation.session_id references are resolvable.
func TestValidateChunkSessionReferences_SessionMutationSatisfiesObsRef_EmptyKnown(t *testing.T) {
	const (
		sessionID = "manual-save-new-project"
		obsSync   = "obs-new-project-001"
		project   = "new-project"
		directory = "/tmp/new-project"
	)

	sessPayloadBytes, err := json.Marshal(map[string]any{
		"id":        sessionID,
		"project":   project,
		"directory": directory,
	})
	if err != nil {
		t.Fatalf("marshal session payload: %v", err)
	}

	obsPayloadBytes, err := json.Marshal(map[string]any{
		"sync_id":    obsSync,
		"session_id": sessionID,
		"type":       "decision",
		"title":      "first obs",
		"content":    "first save on new project",
		"scope":      "project",
		"project":    project,
	})
	if err != nil {
		t.Fatalf("marshal obs payload: %v", err)
	}

	chunk := engramsync.ChunkData{
		Sessions: nil, // session OBJECTS are stripped by Export (line 402); only Mutations carry the reference
		Observations: []store.Observation{{
			SyncID:    obsSync,
			SessionID: sessionID,
			Type:      "decision",
			Title:     "first obs",
			Content:   "first save on new project",
			Scope:     "project",
		}},
		Mutations: []store.SyncMutation{
			{
				Entity:    store.SyncEntitySession,
				EntityKey: sessionID,
				Op:        store.SyncOpUpsert,
				Payload:   string(sessPayloadBytes),
				Project:   project,
			},
			{
				Entity:    store.SyncEntityObservation,
				EntityKey: obsSync,
				Op:        store.SyncOpUpsert,
				Payload:   string(obsPayloadBytes),
				Project:   project,
			},
		},
	}

	emptyKnown := map[string]struct{}{}
	if err := validateChunkSessionReferences(chunk, emptyKnown); err != nil {
		t.Errorf("server must accept chunk when session upsert mutation is present in Mutations (first-push scenario); got: %v", err)
	}
}

// TestValidateChunkSessionReferences_NoSessionMutation_EmptyKnown_Returns400
// verifies the baseline failure case: a chunk with an observation referencing a
// session, but no session mutation in Mutations and empty knownSessionIDs, is
// rejected with the exact error text from the production bug.
//
// This test documents the server behavior that the client-side fix must satisfy.
func TestValidateChunkSessionReferences_NoSessionMutation_EmptyKnown_Returns400(t *testing.T) {
	const (
		sessionID = "manual-save-no-session-mutation"
		obsSync   = "obs-no-session-mutation"
		project   = "some-project"
	)

	obsPayloadBytes, err := json.Marshal(map[string]any{
		"sync_id":    obsSync,
		"session_id": sessionID,
		"type":       "manual",
		"title":      "obs",
		"content":    "content",
		"scope":      "project",
		"project":    project,
	})
	if err != nil {
		t.Fatalf("marshal obs payload: %v", err)
	}

	chunk := engramsync.ChunkData{
		Sessions: nil,
		Observations: []store.Observation{{
			SyncID:    obsSync,
			SessionID: sessionID,
			Type:      "manual",
			Title:     "obs",
			Content:   "content",
			Scope:     "project",
		}},
		Mutations: []store.SyncMutation{
			// Only observation mutation — no session mutation (the buggy client state).
			{
				Entity:    store.SyncEntityObservation,
				EntityKey: obsSync,
				Op:        store.SyncOpUpsert,
				Payload:   string(obsPayloadBytes),
				Project:   project,
			},
		},
	}

	emptyKnown := map[string]struct{}{}
	err = validateChunkSessionReferences(chunk, emptyKnown)
	if err == nil {
		t.Fatal("expected server to reject chunk without session mutation (empty knownSessionIDs), but it passed")
	}
}
