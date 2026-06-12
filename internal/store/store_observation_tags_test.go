package store

import (
	"encoding/json"
	"testing"
)

// TestObservationPayloadRoundTrip verifies the tags field serializes correctly
// through syncObservationPayload and that legacy/untagged observations produce
// no "tags" key (RD4: omitempty on a nil map).
func TestObservationPayloadRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		tags       map[string]string
		wantTags   map[string]string
		absentKeys []string
	}{
		{
			name:     "full tags round-trip",
			tags:     map[string]string{"juego": "game-a", "tipo": "bug"},
			wantTags: map[string]string{"juego": "game-a", "tipo": "bug"},
		},
		{
			name:       "nil tags (legacy) — no tags key in JSON",
			tags:       nil,
			absentKeys: []string{"tags"},
		},
		{
			name:       "empty non-nil map — must also produce no tags key (RD4 normalization)",
			tags:       map[string]string{},
			absentKeys: []string{"tags"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Normalize empty non-nil to nil (RD4: the caller is responsible for this
			// before population; we test the contract here).
			tags := tc.tags
			if len(tags) == 0 {
				tags = nil
			}

			payload := syncObservationPayload{
				SyncID:  "test-sync-id",
				Type:    "manual",
				Title:   "test title",
				Content: "test content",
				Scope:   "project",
				Tags:    tags,
			}

			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}

			var m map[string]interface{}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}

			// Check absent keys
			for _, k := range tc.absentKeys {
				if _, ok := m[k]; ok {
					t.Errorf("key %q should be absent in JSON but was present; JSON=%s", k, data)
				}
			}

			// Check expected tags
			if tc.wantTags != nil {
				raw, ok := m["tags"]
				if !ok {
					t.Fatalf("expected 'tags' key in JSON but not found; JSON=%s", data)
				}
				tagsMap, ok := raw.(map[string]interface{})
				if !ok {
					t.Fatalf("expected 'tags' to be an object, got %T", raw)
				}
				for k, wantV := range tc.wantTags {
					gotV, exists := tagsMap[k]
					if !exists {
						t.Errorf("tags[%q] missing; JSON=%s", k, data)
						continue
					}
					if gotV != wantV {
						t.Errorf("tags[%q] = %q, want %q", k, gotV, wantV)
					}
				}
				if len(tagsMap) != len(tc.wantTags) {
					t.Errorf("tags has %d keys, want %d; JSON=%s", len(tagsMap), len(tc.wantTags), data)
				}
			}

			// Verify round-trip: unmarshal back into syncObservationPayload
			var got syncObservationPayload
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal back: %v", err)
			}
			if tc.wantTags != nil {
				for k, wantV := range tc.wantTags {
					if gotV, exists := got.Tags[k]; !exists || gotV != wantV {
						t.Errorf("round-trip tags[%q] = %q, want %q", k, gotV, wantV)
					}
				}
			} else {
				if got.Tags != nil {
					t.Errorf("expected nil Tags after round-trip for absent-tags case, got %v", got.Tags)
				}
			}
		})
	}
}

// TestObservationTagsCarriedToPayload verifies that Tags set on AddObservationParams
// flow through AddObservation and appear in the sync_mutations payload.
// Tags live exclusively in the payload (no SQL column — design RD6/NG5), so we
// read the sync_mutations table directly to verify the enqueued payload.
func TestObservationTagsCarriedToPayload(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)

	if err := s.CreateSession("sess-tags-1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	wantTags := map[string]string{"tipo": "decision"}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-tags-1",
		Type:      "manual",
		Title:     "tagged observation",
		Content:   "some content about a decision",
		Scope:     "project",
		Tags:      wantTags,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Query the most recent observation sync_mutation payload directly.
	// Tags live in payload only — not in the SQL row (design RD6/NG5).
	row := s.db.QueryRow(
		`SELECT payload FROM sync_mutations
		 WHERE entity = 'observation'
		 ORDER BY seq DESC LIMIT 1`,
	)
	var payloadJSON string
	if err := row.Scan(&payloadJSON); err != nil {
		t.Fatalf("scan payload from sync_mutations: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(payloadJSON), &m); err != nil {
		t.Fatalf("json.Unmarshal payload: %v", err)
	}

	raw, ok := m["tags"]
	if !ok {
		t.Fatalf("'tags' key missing from sync payload; payload=%s", payloadJSON)
	}
	tagsMap, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("'tags' is not an object in payload; payload=%s", payloadJSON)
	}
	if tagsMap["tipo"] != "decision" {
		t.Errorf("payload.tags[tipo] = %v, want %q", tagsMap["tipo"], "decision")
	}
}

// TestObservationTagsPayloadBuilder verifies observationPayloadFromObservation
// directly: an Observation with Tags set produces a payload with the tags object.
func TestObservationTagsPayloadBuilder(t *testing.T) {
	t.Parallel()

	obs := &Observation{
		SyncID:    "test-sync-id-builder",
		SessionID: "sess-builder",
		Type:      "manual",
		Title:     "test",
		Content:   "content",
		Scope:     "project",
		Tags:      map[string]string{"tipo": "decision", "juego": "game-a"},
	}

	payload := observationPayloadFromObservation(obs)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	raw, ok := m["tags"]
	if !ok {
		t.Fatalf("'tags' missing from payload; JSON=%s", data)
	}
	tagsMap, ok := raw.(map[string]interface{})
	if !ok {
		t.Fatalf("'tags' not an object; JSON=%s", data)
	}
	if tagsMap["tipo"] != "decision" {
		t.Errorf("tags[tipo] = %v, want %q", tagsMap["tipo"], "decision")
	}
	if tagsMap["juego"] != "game-a" {
		t.Errorf("tags[juego] = %v, want %q", tagsMap["juego"], "game-a")
	}

	// Nil tags observation: payload must NOT have tags key
	obsNoTags := &Observation{
		SyncID:  "test-sync-id-notags",
		Type:    "manual",
		Title:   "test",
		Content: "content",
		Scope:   "project",
		Tags:    nil,
	}
	payloadNoTags := observationPayloadFromObservation(obsNoTags)
	dataNoTags, _ := json.Marshal(payloadNoTags)
	var mNoTags map[string]interface{}
	_ = json.Unmarshal(dataNoTags, &mNoTags)
	if _, ok := mNoTags["tags"]; ok {
		t.Errorf("nil Tags should produce no 'tags' key; JSON=%s", dataNoTags)
	}
}
