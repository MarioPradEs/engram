package cloudstore

// get_observation_by_syncid_test.go — RED tests for C1 and C2 bug fixes.
//
// C1: GetObservationBySyncID — new store method that finds an observation by
//     syncID alone (across all projects) without requiring project or sessionID.
//     Before the fix, handleRequestRemoval called GetObservationDetail with
//     empty project and sessionID, which triggered normalizeDashboardProject("")
//     → ErrDashboardProjectInvalid → every request-removal returned 500.
//
// C2: AcceptDeletionRequest sets the correct project on the hard-delete mutation.
//     Before the fix, project='' was inserted into cloud_mutations, so
//     applyDashboardMutation could never match the (realProject, syncID) key
//     and the observation remained visible after an "accepted" deletion request.
//
// These tests require Postgres; they are skipped when CLOUDSTORE_TEST_DSN is absent.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

// seedObservationInProject writes a minimal cloud_chunk that contains a single
// observation with the given syncID in the given project. This populates the
// read model so GetObservationBySyncID can find it.
func seedObservationInProject(t *testing.T, cs *CloudStore, project, syncID, sessionID, userEmail string) {
	t.Helper()
	ctx := context.Background()

	chunk := engramsync.ChunkData{
		Observations: []store.Observation{
			{
				SyncID:    syncID,
				SessionID: sessionID,
				Type:      "decision",
				Title:     "Seeded observation for " + syncID,
				CreatedAt: "2026-06-18T10:00:00Z",
			},
		},
	}
	payload, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("seedObservationInProject: marshal chunk: %v", err)
	}

	chunkID := "chunk-" + syncID
	_, err = cs.db.ExecContext(ctx,
		`INSERT INTO cloud_chunks (chunk_id, project_name, created_by, payload)
		 VALUES ($1, $2, $3, $4)`,
		chunkID, project, userEmail, payload,
	)
	if err != nil {
		t.Fatalf("seedObservationInProject: insert cloud_chunk: %v", err)
	}

	// Invalidate the read model so the next query rebuilds from scratch.
	cs.invalidateDashboardReadModel()
}

// ─── C1 tests: GetObservationBySyncID ────────────────────────────────────────

// TestC1_GetObservationBySyncID_OwnedByMember_ReturnsObs verifies that an admin
// scope finds an observation by syncID alone across all projects.
// Before the C1 fix, GetObservationBySyncID did not exist; the handler called
// GetObservationDetail("", "", syncID) which always returned ErrDashboardProjectInvalid
// (not ErrDashboardObservationNotFound) → 500 instead of proceeding.
func TestC1_GetObservationBySyncID_AdminScope_FindsAcrossProjects(t *testing.T) {
	cs := openTestDeletionDB(t)

	const (
		project   = "project-alpha"
		syncID    = "obs-syncid-c1-admin"
		sessionID = "sess-c1-admin"
		owner     = "alice@vivastudios.com"
	)
	seedObservationInProject(t, cs, project, syncID, sessionID, owner)

	adminScope := &ReadScope{IsAdmin: true}
	obs, err := cs.GetObservationBySyncID(adminScope, syncID)
	if err != nil {
		t.Fatalf("GetObservationBySyncID (admin): want obs, got error: %v", err)
	}
	if obs.SyncID != syncID {
		t.Errorf("GetObservationBySyncID: SyncID = %q, want %q", obs.SyncID, syncID)
	}
	if obs.UserEmail != owner {
		t.Errorf("GetObservationBySyncID: UserEmail = %q, want %q", obs.UserEmail, owner)
	}
	if obs.Project != project {
		t.Errorf("GetObservationBySyncID: Project = %q, want %q", obs.Project, project)
	}
}

// TestC1_GetObservationBySyncID_MemberScope_OwnObs_Allowed verifies that a member
// scope is allowed to look up their OWN observation by syncID.
func TestC1_GetObservationBySyncID_MemberScope_OwnObs_Allowed(t *testing.T) {
	cs := openTestDeletionDB(t)

	const (
		project   = "project-beta"
		syncID    = "obs-syncid-c1-member-own"
		sessionID = "sess-c1-member"
		owner     = "bob@vivastudios.com"
	)
	seedObservationInProject(t, cs, project, syncID, sessionID, owner)

	memberScope := &ReadScope{IsAdmin: false, Email: owner}
	obs, err := cs.GetObservationBySyncID(memberScope, syncID)
	if err != nil {
		t.Fatalf("GetObservationBySyncID (member/own): want obs, got error: %v", err)
	}
	if obs.SyncID != syncID {
		t.Errorf("GetObservationBySyncID: SyncID = %q, want %q", obs.SyncID, syncID)
	}
}

// TestC1_GetObservationBySyncID_MemberScope_OtherObs_Forbidden verifies that a
// member scope cannot see an observation they do NOT own — returns not-found.
// This is the security gate that must fire to prevent request-removal of others' obs.
func TestC1_GetObservationBySyncID_MemberScope_OtherObs_Forbidden(t *testing.T) {
	cs := openTestDeletionDB(t)

	const (
		project   = "project-gamma"
		syncID    = "obs-syncid-c1-other"
		sessionID = "sess-c1-other"
		owner     = "carol@vivastudios.com"
		attacker  = "eve@vivastudios.com"
	)
	seedObservationInProject(t, cs, project, syncID, sessionID, owner)

	attackerScope := &ReadScope{IsAdmin: false, Email: attacker}
	_, err := cs.GetObservationBySyncID(attackerScope, syncID)
	if err == nil {
		t.Fatal("GetObservationBySyncID (member/other): expected not-found error, got nil")
	}
	if !errors.Is(err, ErrDashboardObservationNotFound) {
		t.Errorf("GetObservationBySyncID (member/other): want ErrDashboardObservationNotFound, got: %v", err)
	}
}

// TestC1_GetObservationBySyncID_NonExistent_Returns404 verifies that a non-existent
// syncID returns ErrDashboardObservationNotFound (not a 500-class store error).
func TestC1_GetObservationBySyncID_NonExistent_Returns404(t *testing.T) {
	cs := openTestDeletionDB(t)

	adminScope := &ReadScope{IsAdmin: true}
	_, err := cs.GetObservationBySyncID(adminScope, "obs-does-not-exist-c1")
	if err == nil {
		t.Fatal("GetObservationBySyncID (nonexistent): expected not-found error, got nil")
	}
	if !errors.Is(err, ErrDashboardObservationNotFound) {
		t.Errorf("GetObservationBySyncID (nonexistent): want ErrDashboardObservationNotFound, got: %v", err)
	}
}

// ─── C2 tests: AcceptDeletionRequest sets the real project ───────────────────

// TestC2_AcceptDeletionRequest_ObsIsGoneAfterRebuild verifies the full lifecycle:
//  1. Seed an observation in a real project.
//  2. Create a deletion request for it.
//  3. Accept the deletion request.
//  4. Rebuild (invalidate) and re-read the dashboard read model.
//  5. The observation must NO LONGER be visible.
//
// Before the C2 fix, AcceptDeletionRequest inserted project='' into cloud_mutations,
// so applyDashboardMutation keyed the delete by ("", syncID) which never matched
// the observation's real (project, syncID) key → observation stayed visible.
func TestC2_AcceptDeletionRequest_ObsIsGoneAfterRebuild(t *testing.T) {
	cs := openTestDeletionDB(t)

	const (
		project   = "project-deletion-c2"
		syncID    = "obs-syncid-c2-delete"
		sessionID = "sess-c2-del"
		owner     = "dave@vivastudios.com"
		admin     = "mpradas@vivastudios.com"
	)
	// Step 1: seed observation.
	seedObservationInProject(t, cs, project, syncID, sessionID, owner)

	// Verify observation is visible before deletion.
	adminScope := &ReadScope{IsAdmin: true}
	obsBefore, err := cs.GetObservationBySyncID(adminScope, syncID)
	if err != nil {
		t.Fatalf("C2 pre-check: observation not found before deletion: %v", err)
	}
	if obsBefore.SyncID != syncID {
		t.Fatalf("C2 pre-check: SyncID mismatch: %q", obsBefore.SyncID)
	}

	// Step 2: create deletion request.
	ctx := context.Background()
	reqID, err := cs.CreateDeletionRequest(ctx, DeletionRequest{
		TargetSyncID:   syncID,
		RequesterEmail: owner,
		Reason:         "c2-bug-test",
	})
	if err != nil {
		t.Fatalf("C2: CreateDeletionRequest: %v", err)
	}

	// Step 3: accept the deletion request.
	if err := cs.AcceptDeletionRequest(ctx, reqID, admin); err != nil {
		t.Fatalf("C2: AcceptDeletionRequest: %v", err)
	}

	// Step 4: invalidation already called by AcceptDeletionRequest.
	// Step 5: the observation must be gone from the read model.
	_, err = cs.GetObservationBySyncID(adminScope, syncID)
	if err == nil {
		t.Fatal("C2 BUG CONFIRMED (fix needed): observation still visible after AcceptDeletionRequest — empty-project delete mutation did not match real project key")
	}
	if !errors.Is(err, ErrDashboardObservationNotFound) {
		t.Errorf("C2: want ErrDashboardObservationNotFound after accept, got: %v", err)
	}
}


