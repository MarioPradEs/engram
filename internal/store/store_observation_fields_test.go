package store

import (
	"encoding/json"
	"testing"
)

// TestObservationNewFieldsSerialize verifies that the 5 new attribution/classification
// fields round-trip through JSON correctly, including zero-value omitempty behaviour.
func TestObservationNewFieldsSerialize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		obs         Observation
		wantKeys    []string
		absentKeys  []string
	}{
		{
			name: "all new fields populated",
			obs: Observation{
				ID:              1,
				SyncID:          "abc",
				UserEmail:       "alice@example.com",
				UserName:        "Alice",
				Department:      "engineering",
				UserDeleted:     false,
				ClassifiedByV2:  true,
			},
			wantKeys:   []string{"user_email", "user_name", "department", "classified_by_v2"},
			absentKeys: []string{},
		},
		{
			name: "empty optional fields omitted",
			obs: Observation{
				ID:     2,
				SyncID: "def",
				// UserEmail, UserName, Department zero — should be omitted (omitempty)
				UserDeleted:    false,
				ClassifiedByV2: false,
			},
			wantKeys:   []string{},
			absentKeys: []string{"user_email", "user_name", "department"},
		},
		{
			name: "user_deleted true present",
			obs: Observation{
				ID:          3,
				SyncID:      "ghi",
				UserDeleted: true,
			},
			wantKeys:   []string{"user_deleted"},
			absentKeys: []string{},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tc.obs)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			var m map[string]interface{}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			for _, k := range tc.wantKeys {
				if _, ok := m[k]; !ok {
					t.Errorf("expected key %q in JSON but not found; JSON=%s", k, data)
				}
			}
			for _, k := range tc.absentKeys {
				if _, ok := m[k]; ok {
					t.Errorf("key %q should be absent in JSON but was present; JSON=%s", k, data)
				}
			}
		})
	}
}

// TestObservationNewFieldsRoundTrip verifies unmarshal restores new field values.
func TestObservationNewFieldsRoundTrip(t *testing.T) {
	t.Parallel()

	original := Observation{
		ID:             10,
		SyncID:         "round-trip",
		UserEmail:      "bob@example.com",
		UserName:       "Bob",
		Department:     "product",
		UserDeleted:    true,
		ClassifiedByV2: true,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Observation
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.UserEmail != original.UserEmail {
		t.Errorf("UserEmail: got %q, want %q", got.UserEmail, original.UserEmail)
	}
	if got.UserName != original.UserName {
		t.Errorf("UserName: got %q, want %q", got.UserName, original.UserName)
	}
	if got.Department != original.Department {
		t.Errorf("Department: got %q, want %q", got.Department, original.Department)
	}
	if got.UserDeleted != original.UserDeleted {
		t.Errorf("UserDeleted: got %v, want %v", got.UserDeleted, original.UserDeleted)
	}
	if got.ClassifiedByV2 != original.ClassifiedByV2 {
		t.Errorf("ClassifiedByV2: got %v, want %v", got.ClassifiedByV2, original.ClassifiedByV2)
	}
}
