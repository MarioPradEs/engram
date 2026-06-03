package cloudstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Unit tests for applyChunkAttributionAndGateB ───────────────────────────

// TestApplyChunkAttributionAndGateBDropsPersonal verifies that personal-scope
// observation entries are silently removed from the materialized set and a Gate B
// drop record is returned.
func TestApplyChunkAttributionAndGateBDropsPersonal(t *testing.T) {
	attr := Attribution{
		UserEmail:  "alice@example.com",
		UserName:   "Alice",
		Department: "eng",
	}
	entries := []MutationEntry{
		makeObsMutationEntry("proj", "obs-project", "project"),
		makeObsMutationEntry("proj", "obs-personal", "personal"),
		makeObsMutationEntry("proj", "obs-dept", "department"),
		{Project: "proj", Entity: store.SyncEntitySession, EntityKey: "sess-1", Op: store.SyncOpUpsert,
			Payload: json.RawMessage(`{"id":"sess-1"}`)},
	}

	kept, drops := applyChunkAttributionAndGateB(entries, attr)

	// 3 entries kept (project obs + dept obs + session); personal obs dropped.
	if len(kept) != 3 {
		t.Fatalf("expected 3 kept entries, got %d", len(kept))
	}
	if len(drops) != 1 {
		t.Fatalf("expected 1 drop, got %d", len(drops))
	}
	if drops[0].entityKey != "obs-personal" {
		t.Errorf("expected dropped entity_key=obs-personal, got %q", drops[0].entityKey)
	}
}

// TestApplyChunkAttributionAndGateBStampsAttr verifies that attribution fields
// are merged into observation payloads (non-personal) by applyChunkAttributionAndGateB.
func TestApplyChunkAttributionAndGateBStampsAttr(t *testing.T) {
	attr := Attribution{
		UserEmail:   "bob@example.com",
		UserName:    "Bob",
		Department:  "qa",
		UserDeleted: false,
	}
	entries := []MutationEntry{
		makeObsMutationEntry("proj", "obs-1", "project"),
	}

	kept, drops := applyChunkAttributionAndGateB(entries, attr)
	if len(drops) != 0 {
		t.Fatalf("expected no drops, got %d", len(drops))
	}
	if len(kept) != 1 {
		t.Fatalf("expected 1 kept entry, got %d", len(kept))
	}

	var payload map[string]any
	if err := json.Unmarshal(kept[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal stamped payload: %v", err)
	}
	if payload["user_email"] != attr.UserEmail {
		t.Errorf("payload.user_email: got %v, want %q", payload["user_email"], attr.UserEmail)
	}
	if payload["user_name"] != attr.UserName {
		t.Errorf("payload.user_name: got %v, want %q", payload["user_name"], attr.UserName)
	}
	if payload["department"] != attr.Department {
		t.Errorf("payload.department: got %v, want %q", payload["department"], attr.Department)
	}
}

// TestApplyChunkAttributionAndGateBNoAttrIsNoop verifies that when Attribution
// is zero (no auth), entries pass through unchanged.
func TestApplyChunkAttributionAndGateBNoAttrIsNoop(t *testing.T) {
	entries := []MutationEntry{
		makeObsMutationEntry("proj", "obs-1", "project"),
		makeObsMutationEntry("proj", "obs-personal", "personal"),
	}

	// Zero Attribution → no stamping, no Gate B drop.
	kept, drops := applyChunkAttributionAndGateB(entries, Attribution{})
	if len(kept) != 2 {
		t.Fatalf("expected both entries kept when attr is zero, got %d", len(kept))
	}
	if len(drops) != 0 {
		t.Fatalf("expected no drops when attr is zero, got %d", len(drops))
	}
}

// TestApplyChunkAttributionAndGateBSessionPassesThrough verifies that session
// entries are not modified by the Gate B or attribution logic.
func TestApplyChunkAttributionAndGateBSessionPassesThrough(t *testing.T) {
	attr := Attribution{
		UserEmail:  "alice@example.com",
		UserName:   "Alice",
		Department: "eng",
	}
	sessEntry := MutationEntry{
		Project:   "proj",
		Entity:    store.SyncEntitySession,
		EntityKey: "sess-1",
		Op:        store.SyncOpUpsert,
		Payload:   json.RawMessage(`{"id":"sess-1","directory":"/tmp"}`),
	}

	kept, drops := applyChunkAttributionAndGateB([]MutationEntry{sessEntry}, attr)
	if len(kept) != 1 {
		t.Fatalf("expected session to pass through, got %d kept", len(kept))
	}
	if len(drops) != 0 {
		t.Fatalf("expected no drops for session, got %d", len(drops))
	}
	// Session payload must not be modified.
	if string(kept[0].Payload) != string(sessEntry.Payload) {
		t.Errorf("session payload should not be modified by attribution: got %q", kept[0].Payload)
	}
}

// ─── Integration tests (Postgres): WriteChunkWithAttribution ─────────────────

// TestWriteChunkWithAttributionGateBAndStamp is the primary integration test
// for the chunk-path attribution security layer. It mirrors
// TestInsertMutationBatchAttributionStamp but exercises WriteChunkWithAttribution.
//
// Asserts:
//   - project-scoped obs stored WITH user_email/department in denorm columns + JSONB
//   - personal-scoped obs NOT stored; audit log row with rejected_personal_scope written
//   - session handled without error (no attribution stamp on session entity)
func TestWriteChunkWithAttributionGateBAndStamp(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	attr := Attribution{
		UserEmail:   "chunk-tester@example.com",
		UserName:    "Chunk Tester",
		Department:  "engineering",
		UserDeleted: false,
	}

	project := "chunk-attr-test-" + uniqueTestSuffix(t)

	// Build a chunk with: project-obs, personal-obs, session.
	chunkPayload, err := chunkcodec.CanonicalizeForProject([]byte(`{
		"sessions":[{"id":"sess-chunk-attr","directory":"/tmp/chunk-attr","started_at":"2026-06-01T10:00:00Z"}],
		"observations":[
			{"sync_id":"obs-project-scope","session_id":"sess-chunk-attr","type":"decision",
			 "title":"Project obs","content":"should be stored","scope":"project",
			 "created_at":"2026-06-01T10:01:00Z"},
			{"sync_id":"obs-personal-scope","session_id":"sess-chunk-attr","type":"manual",
			 "title":"Personal obs","content":"should be dropped","scope":"personal",
			 "created_at":"2026-06-01T10:02:00Z"}
		],
		"prompts":[]
	}`), project)
	if err != nil {
		t.Fatalf("canonicalize chunk: %v", err)
	}
	chunkID := chunkIDFromPayload(chunkPayload)

	if err := cs.WriteChunkWithAttribution(ctx, project, chunkID, "chunk-tester", "", chunkPayload, attr); err != nil {
		t.Fatalf("WriteChunkWithAttribution: %v", err)
	}

	// ── Assert: project obs stored WITH attribution ──
	var storedEmail, storedDept, storedPayloadStr string
	err = cs.db.QueryRowContext(ctx, `
		SELECT COALESCE(user_email,''), COALESCE(department,''), payload::text
		FROM cloud_mutations
		WHERE project = $1 AND entity = 'observation' AND entity_key = 'obs-project-scope'
	`, project).Scan(&storedEmail, &storedDept, &storedPayloadStr)
	if err != nil {
		t.Fatalf("query project obs mutation: %v", err)
	}
	if storedEmail != attr.UserEmail {
		t.Errorf("denorm user_email: got %q, want %q", storedEmail, attr.UserEmail)
	}
	if storedDept != attr.Department {
		t.Errorf("denorm department: got %q, want %q", storedDept, attr.Department)
	}
	var obsPayload map[string]any
	if err := json.Unmarshal([]byte(storedPayloadStr), &obsPayload); err != nil {
		t.Fatalf("unmarshal obs payload: %v", err)
	}
	if obsPayload["user_email"] != attr.UserEmail {
		t.Errorf("JSONB payload.user_email: got %v, want %q", obsPayload["user_email"], attr.UserEmail)
	}
	if obsPayload["department"] != attr.Department {
		t.Errorf("JSONB payload.department: got %v, want %q", obsPayload["department"], attr.Department)
	}

	// ── Assert: personal obs NOT stored ──
	var personalCount int
	if err := cs.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cloud_mutations
		WHERE project = $1 AND entity = 'observation' AND entity_key = 'obs-personal-scope'
	`, project).Scan(&personalCount); err != nil {
		t.Fatalf("count personal obs: %v", err)
	}
	if personalCount != 0 {
		t.Errorf("personal obs must NOT be stored in cloud_mutations, found %d row(s)", personalCount)
	}

	// ── Assert: audit log row written for rejected personal obs ──
	var auditCount int
	if err := cs.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cloud_sync_audit_log
		WHERE contributor = $1
		  AND outcome = 'rejected_personal_scope'
		  AND project = $2
	`, attr.UserEmail, project).Scan(&auditCount); err != nil {
		t.Fatalf("count audit entries: %v", err)
	}
	if auditCount < 1 {
		t.Errorf("expected audit log entry for rejected_personal_scope, got %d", auditCount)
	}

	// ── Assert: session stored (no attribution columns expected on sessions) ──
	var sessCount int
	if err := cs.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cloud_mutations
		WHERE project = $1 AND entity = 'session' AND entity_key = 'sess-chunk-attr'
	`, project).Scan(&sessCount); err != nil {
		t.Fatalf("count session mutation: %v", err)
	}
	if sessCount != 1 {
		t.Errorf("expected session mutation stored, got %d", sessCount)
	}
}

// TestWriteChunkWithAttributionFallsBackToNonPersonalWhenAttrZero checks that
// when Attribution is zero (no email), Gate B does not drop personal entries
// and no stamping occurs — matching legacy WriteChunk behavior.
func TestWriteChunkWithAttributionFallsBackToNonPersonalWhenAttrZero(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := "chunk-noattr-test-" + uniqueTestSuffix(t)

	chunkPayload, err := chunkcodec.CanonicalizeForProject([]byte(`{
		"sessions":[],
		"observations":[
			{"sync_id":"obs-personal-noattr","session_id":"sess-x","type":"manual",
			 "title":"Personal","content":"no auth","scope":"personal",
			 "created_at":"2026-06-01T10:00:00Z"}
		],
		"prompts":[]
	}`), project)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	chunkID := chunkIDFromPayload(chunkPayload)

	// Zero Attribution = no auth.
	if err := cs.WriteChunkWithAttribution(ctx, project, chunkID, "anon", "", chunkPayload, Attribution{}); err != nil {
		t.Fatalf("WriteChunkWithAttribution (zero attr): %v", err)
	}

	var count int
	if err := cs.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cloud_mutations
		WHERE project = $1 AND entity = 'observation' AND entity_key = 'obs-personal-noattr'
	`, project).Scan(&count); err != nil {
		t.Fatalf("count personal obs (no attr): %v", err)
	}
	// With zero auth, no Gate B — entry is stored.
	if count != 1 {
		t.Errorf("expected personal obs stored when no auth, got %d", count)
	}
}

// uniqueTestSuffix returns a short unique suffix for test project names.
func uniqueTestSuffix(t *testing.T) string {
	t.Helper()
	return "tt" + t.Name()[len(t.Name())-3:]
}
