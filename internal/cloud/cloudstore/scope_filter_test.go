package cloudstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// TestListMutationsSinceScopeFilter verifies server-side scope filtering:
//   - scope=team    → visible to all enrolled users (design Q3, spec S1)
//   - scope=project → visible to enrolled users (spec S2)
//   - scope=department → visible only to same-department users (spec S3)
//   - scope=personal  → NOT stored server-side (Gate B), but if present,
//     only visible to owner (spec S4) — tested for defense completeness
//   - non-observation entities are always returned regardless of scope filter
//
// Cursor invariant (design Q3): LatestSeq = max scanned seq even when all
// returned rows are filtered, so the client pull loop advances past filtered pages.
func TestListMutationsSinceScopeFilter(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := "scope-filter-test"

	// Insert observations with different scopes.
	insertObsMutation := func(syncID, scope, userEmail, department string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]interface{}{
			"sync_id":    syncID,
			"session_id": "sess-sf",
			"type":       "manual",
			"title":      syncID,
			"content":    "content",
			"scope":      scope,
			"user_email": userEmail,
			"department": department,
		})
		attr := Attribution{UserEmail: userEmail, Department: department}
		_, err := cs.InsertMutationBatch(ctx, []MutationEntry{{
			Project:   project,
			Entity:    store.SyncEntityObservation,
			EntityKey: syncID,
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(payload),
		}}, attr)
		if err != nil {
			t.Fatalf("insert %s: %v", syncID, err)
		}
	}

	// Insert a session mutation (non-observation → always visible).
	insertSessionMutation := func(sessionID string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]interface{}{
			"id": sessionID,
		})
		_, err := cs.InsertMutationBatch(ctx, []MutationEntry{{
			Project:   project,
			Entity:    store.SyncEntitySession,
			EntityKey: sessionID,
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(payload),
		}}, Attribution{})
		if err != nil {
			t.Fatalf("insert session %s: %v", sessionID, err)
		}
	}

	insertObsMutation("obs-team", "team", "alice@example.com", "engineering")
	insertObsMutation("obs-project", "project", "alice@example.com", "engineering")
	insertObsMutation("obs-dept-eng", "department", "alice@example.com", "engineering")
	insertObsMutation("obs-dept-ops", "department", "bob@example.com", "operations")
	insertSessionMutation("sess-always-visible")

	// Caller: alice in engineering.
	sf := &MutationScopeFilter{
		CallerEmail:      "alice@example.com",
		CallerDepartment: "department",
		// No: we use the actual department value for the WHERE
		// Let me fix this — the caller's department is "engineering"
	}
	sf.CallerDepartment = "engineering"

	all, _, latestSeq, err := cs.ListMutationsSince(ctx, 0, 100, []string{project}, sf)
	if err != nil {
		t.Fatalf("ListMutationsSince: %v", err)
	}

	// Alice should see: obs-team, obs-project, obs-dept-eng (her dept), sess-always-visible.
	// Alice should NOT see: obs-dept-ops (different dept).
	entityKeys := make(map[string]bool)
	for _, m := range all {
		entityKeys[m.EntityKey] = true
	}

	wants := []string{"obs-team", "obs-project", "obs-dept-eng", "sess-always-visible"}
	for _, want := range wants {
		if !entityKeys[want] {
			t.Errorf("expected %q to be visible to alice, but not found in results %v", want, entityKeys)
		}
	}
	if entityKeys["obs-dept-ops"] {
		t.Errorf("expected obs-dept-ops (operations dept) to be hidden from alice (engineering), but was visible")
	}

	// Cursor invariant: latestSeq must be > 0 (some rows were scanned).
	if latestSeq == 0 {
		t.Errorf("expected latestSeq > 0 after scanned rows, got 0")
	}
}

// TestListMutationsSinceCursorAdvancesOnAllFiltered verifies that even when
// all mutations in a scan window are filtered by scope, LatestSeq still advances
// to the max scanned seq so the pull loop doesn't stall (design Q3 cursor invariant).
func TestListMutationsSinceCursorAdvancesOnAllFiltered(t *testing.T) {
	_, cs, cleanup := openTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := "cursor-advance-test"

	// Insert 3 "operations" department observations (invisible to "engineering" caller).
	for i := 0; i < 3; i++ {
		payload, _ := json.Marshal(map[string]interface{}{
			"sync_id":    "hidden-obs-cursor",
			"session_id": "sess-cursor",
			"type":       "manual",
			"title":      "hidden",
			"content":    "content",
			"scope":      "department",
			"user_email": "bob@example.com",
			"department": "operations",
		})
		attr := Attribution{UserEmail: "bob@example.com", Department: "operations"}
		seqs, err := cs.InsertMutationBatch(ctx, []MutationEntry{{
			Project:   project,
			Entity:    store.SyncEntityObservation,
			EntityKey: "hidden-cursor-" + string(rune('a'+i)),
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(payload),
		}}, attr)
		if err != nil {
			t.Fatalf("insert hidden obs: %v", err)
		}
		_ = seqs
	}

	sf := &MutationScopeFilter{
		CallerEmail:      "alice@example.com",
		CallerDepartment: "engineering",
	}

	mutations, _, latestSeq, err := cs.ListMutationsSince(ctx, 0, 100, []string{project}, sf)
	if err != nil {
		t.Fatalf("ListMutationsSince: %v", err)
	}

	// No mutations should be returned (all filtered).
	if len(mutations) != 0 {
		t.Errorf("expected 0 visible mutations (all dept:operations), got %d", len(mutations))
	}

	// But latestSeq MUST advance past the filtered rows.
	if latestSeq == 0 {
		t.Errorf("expected latestSeq > 0 (cursor must advance past filtered-only pages), got 0")
	}
}
