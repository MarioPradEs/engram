package cloudstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestInsertMutationBatchAttributionStamp verifies that InsertMutationBatch
// stamps the server-provided Attribution into the denorm columns and the
// JSONB payload for observation entries.
//
// Gate B: scope==personal entries must be silently dropped (sentinel-seq
// returned so client ack count still matches) and logged in
// cloud_sync_audit_log with outcome "rejected_personal_scope".
func TestInsertMutationBatchAttributionStamp(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	attr := Attribution{
		UserEmail:   "tester@example.com",
		UserName:    "Tester",
		Department:  "engineering",
		UserDeleted: false,
	}

	obs1 := makeObsMutationEntry("test-proj", "obs-attr-1", "project")
	obs2 := makeObsMutationEntry("test-proj", "obs-attr-2", "department")
	obsPersonal := makeObsMutationEntry("test-proj", "obs-attr-personal", "personal")

	seqs, err := cs.InsertMutationBatch(ctx, []MutationEntry{obs1, obs2, obsPersonal}, attr)
	if err != nil {
		t.Fatalf("InsertMutationBatch: %v", err)
	}

	// Must return 3 seqs — one per entry (personal gets a sentinel-seq).
	if len(seqs) != 3 {
		t.Fatalf("expected 3 accepted_seqs, got %d", len(seqs))
	}

	// personal entry seq must not appear in the stored mutations.
	personalSeq := seqs[2]
	var found int
	err = cs.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cloud_mutations WHERE seq = $1`, personalSeq,
	).Scan(&found)
	if err != nil {
		t.Fatalf("count personal seq: %v", err)
	}
	if found != 0 {
		t.Errorf("personal observation must NOT be stored in cloud_mutations (seq=%d)", personalSeq)
	}

	// Verify attribution stamped on non-personal entries.
	for i, seq := range seqs[:2] {
		var userEmail, payloadStr string
		err = cs.db.QueryRowContext(ctx,
			`SELECT user_email, payload::text FROM cloud_mutations WHERE seq = $1`, seq,
		).Scan(&userEmail, &payloadStr)
		if err != nil {
			t.Fatalf("read mutation seq=%d: %v", seq, err)
		}
		if userEmail != attr.UserEmail {
			t.Errorf("entry[%d] user_email: got %q, want %q", i, userEmail, attr.UserEmail)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload["user_email"] != attr.UserEmail {
			t.Errorf("entry[%d] payload.user_email: got %v, want %q", i, payload["user_email"], attr.UserEmail)
		}
	}

	// Verify audit log entry for the dropped personal entry.
	var auditCount int
	err = cs.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cloud_sync_audit_log WHERE contributor = $1 AND outcome = 'rejected_personal_scope'`,
		attr.UserEmail,
	).Scan(&auditCount)
	if err != nil {
		t.Fatalf("count audit entries: %v", err)
	}
	if auditCount < 1 {
		t.Errorf("expected at least 1 audit log entry for rejected_personal_scope, got %d", auditCount)
	}
}

// TestInsertMutationBatchChunkCreatedByAuthenticatedUser verifies that when an
// authenticated user pushes a mutation batch, the resulting cloud_chunks row has
// created_by set to the user's email — NOT the generic label "mutation-push".
func TestInsertMutationBatchChunkCreatedByAuthenticatedUser(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	attr := Attribution{
		UserEmail:  "mpradas@vivastudios.com",
		UserName:   "Mario",
		Department: "qa",
	}

	project := "chunk-created-by-test-" + uniqueTestSuffix(t)

	entry := makeObsMutationEntry(project, "obs-created-by-1", "project")
	if _, err := cs.InsertMutationBatch(ctx, []MutationEntry{entry}, attr); err != nil {
		t.Fatalf("InsertMutationBatch: %v", err)
	}

	var createdBy string
	err := cs.db.QueryRowContext(ctx,
		`SELECT created_by FROM cloud_chunks WHERE project_name = $1 LIMIT 1`, project,
	).Scan(&createdBy)
	if err != nil {
		t.Fatalf("query chunk created_by: %v", err)
	}
	if createdBy == "mutation-push" {
		t.Errorf("chunk created_by must NOT be the generic label %q when an authenticated user pushes; got %q", "mutation-push", createdBy)
	}
	if createdBy != attr.UserEmail {
		t.Errorf("chunk created_by: got %q, want %q", createdBy, attr.UserEmail)
	}
}

// TestInsertMutationBatchChunkCreatedByFallsBackWhenNoIdentity verifies that
// when no authenticated identity is available (zero Attribution), the chunk
// created_by falls back to "mutation-push" — preserving the original behaviour
// for insecure/no-auth deployments.
func TestInsertMutationBatchChunkCreatedByFallsBackWhenNoIdentity(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := "chunk-created-by-noauth-" + uniqueTestSuffix(t)

	entry := makeObsMutationEntry(project, "obs-noauth-1", "project")
	if _, err := cs.InsertMutationBatch(ctx, []MutationEntry{entry}, Attribution{}); err != nil {
		t.Fatalf("InsertMutationBatch (no auth): %v", err)
	}

	var createdBy string
	err := cs.db.QueryRowContext(ctx,
		`SELECT created_by FROM cloud_chunks WHERE project_name = $1 LIMIT 1`, project,
	).Scan(&createdBy)
	if err != nil {
		t.Fatalf("query chunk created_by (no auth): %v", err)
	}
	if strings.TrimSpace(createdBy) == "" {
		t.Errorf("chunk created_by must have a non-empty fallback when no identity is present")
	}
	// Acceptable values for no-auth: "mutation-push" or any non-empty sentinel.
	// Must NOT be an email that was never supplied.
	if strings.Contains(createdBy, "@") {
		t.Errorf("chunk created_by must not look like an email when no identity was provided, got %q", createdBy)
	}
}

// makeObsMutationEntry creates a minimal observation MutationEntry with the given scope.
func makeObsMutationEntry(project, syncID, scope string) MutationEntry {
	payload, _ := json.Marshal(map[string]interface{}{
		"sync_id":    syncID,
		"session_id": "test-session",
		"type":       "manual",
		"title":      "Test Obs",
		"content":    "test",
		"scope":      scope,
	})
	return MutationEntry{
		Project:   project,
		Entity:    "observation",
		EntityKey: syncID,
		Op:        "upsert",
		Payload:   json.RawMessage(payload),
	}
}
