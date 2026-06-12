package cloudstore

import (
	"encoding/json"
	"testing"
)

// makeObsEntryWithTags creates a MutationEntry carrying an existing tags map in
// the payload. Used to test that stampAttributionIntoEntry handles pre-existing
// client-supplied tag facets correctly.
func makeObsEntryWithTags(project, syncID, scope string, tags map[string]interface{}) MutationEntry {
	m := map[string]interface{}{
		"sync_id":    syncID,
		"session_id": "test-session",
		"type":       "manual",
		"title":      "Test Obs",
		"content":    "test",
		"scope":      scope,
	}
	if tags != nil {
		m["tags"] = tags
	}
	payload, _ := json.Marshal(m)
	return MutationEntry{
		Project:   project,
		Entity:    "observation",
		EntityKey: syncID,
		Op:        "upsert",
		Payload:   payload,
	}
}

// payloadTags reads the "tags" sub-object from a MutationEntry payload.
// Returns nil if tags key is absent.
func payloadTags(t *testing.T, entry MutationEntry) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(entry.Payload, &m); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if m["tags"] == nil {
		return nil
	}
	tags, ok := m["tags"].(map[string]interface{})
	if !ok {
		t.Fatalf("tags is not a map: %T", m["tags"])
	}
	return tags
}

// TestStampAttributionTagsIdentity verifies that stampAttributionIntoEntry writes
// authoritative departamento and proyecto tag facets.
func TestStampAttributionTagsIdentity(t *testing.T) {
	tests := []struct {
		name           string
		entryTags      map[string]interface{}
		project        string
		attr           Attribution
		wantDept       string   // expected tags.departamento; "" means key must be absent
		wantProyecto   string   // expected tags.proyecto; "" means key must be absent
		wantDeptAbsent bool     // true means departamento must not exist in tags
		preserveKeys   []string // tag keys that must be unchanged
		preserveVals   map[string]string
	}{
		{
			name:         "both stamped on clean payload",
			entryTags:    nil,
			project:      "game-x",
			attr:         Attribution{UserEmail: "u@e.com", Department: "qa"},
			wantDept:     "qa",
			wantProyecto: "game-x",
		},
		{
			name:           "contradiction override — client departamento overwritten by JWT",
			entryTags:      map[string]interface{}{"departamento": "dev", "tipo": "bug"},
			project:        "game-x",
			attr:           Attribution{UserEmail: "u@e.com", Department: "qa"},
			wantDept:       "qa",
			wantProyecto:   "game-x",
			preserveKeys:   []string{"tipo"},
			preserveVals:   map[string]string{"tipo": "bug"},
		},
		{
			name:           "empty department — departamento key must be absent",
			entryTags:      nil,
			project:        "game-x",
			attr:           Attribution{UserEmail: "u@e.com", Department: ""},
			wantDeptAbsent: true,
			wantProyecto:   "game-x",
		},
		{
			name:         "AI facets preserved after stamp",
			entryTags:    map[string]interface{}{"juego": "game-a", "tipo": "decision"},
			project:      "game-x",
			attr:         Attribution{UserEmail: "u@e.com", Department: "qa"},
			wantDept:     "qa",
			wantProyecto: "game-x",
			preserveKeys: []string{"juego", "tipo"},
			preserveVals: map[string]string{"juego": "game-a", "tipo": "decision"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := makeObsEntryWithTags("proj", "obs-1", "project", tt.entryTags)
			entry.Project = tt.project

			stamped := stampAttributionIntoEntry(entry, tt.attr)
			tags := payloadTags(t, stamped)

			if tt.wantDeptAbsent {
				if tags != nil {
					if _, exists := tags["departamento"]; exists {
						t.Errorf("departamento must be absent when Department is empty, got %q", tags["departamento"])
					}
				}
			} else if tt.wantDept != "" {
				if tags == nil {
					t.Fatalf("expected tags map, got nil")
				}
				if tags["departamento"] != tt.wantDept {
					t.Errorf("tags.departamento: got %q, want %q", tags["departamento"], tt.wantDept)
				}
			}

			if tt.wantProyecto != "" {
				if tags == nil {
					t.Fatalf("expected tags map, got nil")
				}
				if tags["proyecto"] != tt.wantProyecto {
					t.Errorf("tags.proyecto: got %q, want %q", tags["proyecto"], tt.wantProyecto)
				}
			}

			// Verify AI-owned facets are preserved untouched.
			for _, k := range tt.preserveKeys {
				want := tt.preserveVals[k]
				if tags == nil {
					t.Fatalf("expected tags map for preserved keys, got nil")
				}
				if tags[k] != want {
					t.Errorf("tags[%q] should be preserved: got %q, want %q", k, tags[k], want)
				}
			}
		})
	}
}

// TestStampAttributionScopeOrthogonality verifies that stampAttributionIntoEntry
// does not touch the scope field — tags and scope are fully independent axes.
func TestStampAttributionScopeOrthogonality(t *testing.T) {
	entry := makeObsEntryWithTags("proj", "obs-scope", "department", map[string]interface{}{"juego": "game-a"})
	entry.Project = "game-x"
	attr := Attribution{UserEmail: "u@e.com", Department: "qa"}

	stamped := stampAttributionIntoEntry(entry, attr)

	// Verify scope is untouched.
	var m map[string]interface{}
	if err := json.Unmarshal(stamped.Payload, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["scope"] != "department" {
		t.Errorf("scope must be unchanged after stamp: got %q, want %q", m["scope"], "department")
	}
}

// TestMergeIdentityTagsIntoPayloadMapRD4 verifies RD4: a payload that arrives
// with an empty "tags":{} object and NO identity facets (empty department +
// empty project) must have the "tags" key DELETED, not left as an empty object.
//
// This tests the cloud trust boundary: the server must not persist an empty
// tags:{} in JSONB when the client sent one and identity provides nothing to
// merge in (legacy-absence contract).
func TestMergeIdentityTagsIntoPayloadMapRD4(t *testing.T) {
	tests := []struct {
		name       string
		inputTags  map[string]interface{} // pre-existing tags in payload (may be empty map)
		department string
		project    string
		wantAbsent bool // if true, the "tags" key must not exist in the output map at all
	}{
		{
			name:       "empty tags object + empty identity → tags key must be deleted (RD4)",
			inputTags:  map[string]interface{}{}, // client sent tags:{}
			department: "",
			project:    "",
			wantAbsent: true,
		},
		{
			name:       "empty tags object + only department → tags key present with departamento",
			inputTags:  map[string]interface{}{},
			department: "qa",
			project:    "",
			wantAbsent: false,
		},
		{
			name:       "no tags key + empty identity → tags key remains absent",
			inputTags:  nil, // no tags key at all
			department: "",
			project:    "",
			wantAbsent: true,
		},
		{
			name:       "tags with AI facets + empty identity → tags key preserved (AI facets kept)",
			inputTags:  map[string]interface{}{"juego": "game-a", "tipo": "decision"},
			department: "",
			project:    "",
			wantAbsent: false, // juego+tipo must survive even with no identity
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build the map directly to test mergeIdentityTagsIntoPayloadMap.
			m := map[string]interface{}{
				"sync_id": "test-1",
				"content": "test",
			}
			if tt.inputTags != nil {
				m["tags"] = tt.inputTags
			}

			mergeIdentityTagsIntoPayloadMap(m, tt.department, tt.project)

			_, hasTags := m["tags"]
			if tt.wantAbsent && hasTags {
				t.Errorf("expected tags key to be absent after merge with empty identity, but it still exists: %v", m["tags"])
			}
			if !tt.wantAbsent && !hasTags {
				t.Errorf("expected tags key to be present, but it was absent")
			}
		})
	}
}

// TestChunkAttributionTagsParity verifies that applyChunkAttributionAndGateB
// applies the same identity tag stamp as stampAttributionIntoEntry (RD3 parity).
func TestChunkAttributionTagsParity(t *testing.T) {
	tests := []struct {
		name         string
		entryTags    map[string]interface{}
		project      string
		attr         Attribution
		wantDept     string
		wantProyecto string
		deptAbsent   bool
		preserveAI   bool
	}{
		{
			name:         "departamento stamped in chunk",
			entryTags:    nil,
			project:      "game-x",
			attr:         Attribution{UserEmail: "u@e.com", Department: "qa"},
			wantDept:     "qa",
			wantProyecto: "game-x",
		},
		{
			name:         "contradiction override in chunk — client dept overwritten",
			entryTags:    map[string]interface{}{"departamento": "dev", "tipo": "bug"},
			project:      "game-x",
			attr:         Attribution{UserEmail: "u@e.com", Department: "qa"},
			wantDept:     "qa",
			wantProyecto: "game-x",
		},
		{
			name:       "AI facets untouched in chunk stamp",
			entryTags:  map[string]interface{}{"juego": "game-b", "tipo": "decision"},
			project:    "game-x",
			attr:       Attribution{UserEmail: "u@e.com", Department: "qa"},
			wantDept:   "qa",
			preserveAI: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := makeObsEntryWithTags(tt.project, "obs-chunk", "project", tt.entryTags)
			entry.Project = tt.project

			kept, _, _ := applyChunkAttributionAndGateB([]MutationEntry{entry}, tt.attr)
			if len(kept) != 1 {
				t.Fatalf("expected 1 kept entry, got %d", len(kept))
			}

			tags := payloadTags(t, kept[0])

			if tt.deptAbsent {
				if tags != nil {
					if _, exists := tags["departamento"]; exists {
						t.Errorf("departamento must be absent, got %q", tags["departamento"])
					}
				}
			} else if tt.wantDept != "" {
				if tags == nil {
					t.Fatalf("expected tags map, got nil")
				}
				if tags["departamento"] != tt.wantDept {
					t.Errorf("tags.departamento: got %q, want %q", tags["departamento"], tt.wantDept)
				}
			}

			if tt.wantProyecto != "" {
				if tags == nil {
					t.Fatalf("expected tags map, got nil")
				}
				if tags["proyecto"] != tt.wantProyecto {
					t.Errorf("tags.proyecto: got %q, want %q", tags["proyecto"], tt.wantProyecto)
				}
			}

			if tt.preserveAI {
				if tt.entryTags != nil {
					for k, v := range tt.entryTags {
						if k == "departamento" || k == "proyecto" {
							continue // these are overwritten by design
						}
						if tags == nil {
							t.Fatalf("expected tags map for AI facets, got nil")
						}
						if tags[k] != v {
							t.Errorf("AI facet tags[%q]: got %q, want %v", k, tags[k], v)
						}
					}
				}
			}
		})
	}
}
