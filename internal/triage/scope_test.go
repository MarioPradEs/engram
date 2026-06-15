package triage

import "testing"

// TestToInternalScope verifies UI-vocabulary → internal-scope mapping (REQ-23).
// "shared" maps to "team"; everything else maps to "personal".
func TestToInternalScope(t *testing.T) {
	tests := []struct {
		name     string
		ui       string
		wantInternal string
	}{
		{
			name:         "shared maps to team",
			ui:           "shared",
			wantInternal: "team",
		},
		{
			name:         "personal maps to personal",
			ui:           "personal",
			wantInternal: "personal",
		},
		{
			name:         "empty string is defensive personal",
			ui:           "",
			wantInternal: "personal",
		},
		{
			name:         "unknown value is defensive personal",
			ui:           "anything-else",
			wantInternal: "personal",
		},
		{
			name:         "internal team is not a valid UI input — maps to personal defensively",
			ui:           "team",
			wantInternal: "personal",
		},
		{
			name:         "internal project scope is not valid UI input — defensive personal",
			ui:           "project",
			wantInternal: "personal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToInternalScope(tt.ui)
			if got != tt.wantInternal {
				t.Errorf("ToInternalScope(%q) = %q; want %q", tt.ui, got, tt.wantInternal)
			}
		})
	}
}

// TestUIScopeOf verifies internal-scope → UI-vocabulary + needsTriage mapping (REQ-24).
// Legacy "project" and "department" rows surface as needsTriage=true; they are
// NEVER silently shown as "shared".
func TestUIScopeOf(t *testing.T) {
	tests := []struct {
		name         string
		scope        string
		wantUI       string
		wantNeedsTriage bool
	}{
		{
			name:            "team scope shows as shared",
			scope:           "team",
			wantUI:          "shared",
			wantNeedsTriage: false,
		},
		{
			name:            "personal scope shows as personal",
			scope:           "personal",
			wantUI:          "personal",
			wantNeedsTriage: false,
		},
		{
			name:            "legacy project scope surfaces as needs-triage",
			scope:           "project",
			wantUI:          "personal",
			wantNeedsTriage: true,
		},
		{
			name:            "legacy department scope surfaces as needs-triage",
			scope:           "department",
			wantUI:          "personal",
			wantNeedsTriage: true,
		},
		{
			name:            "empty scope is defensive needs-triage",
			scope:           "",
			wantUI:          "personal",
			wantNeedsTriage: true,
		},
		{
			name:            "unrecognised scope is defensive needs-triage",
			scope:           "unknown_tier",
			wantUI:          "personal",
			wantNeedsTriage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUI, gotNeedsTriage := UIScopeOf(tt.scope)
			if gotUI != tt.wantUI {
				t.Errorf("UIScopeOf(%q) ui = %q; want %q", tt.scope, gotUI, tt.wantUI)
			}
			if gotNeedsTriage != tt.wantNeedsTriage {
				t.Errorf("UIScopeOf(%q) needsTriage = %v; want %v", tt.scope, gotNeedsTriage, tt.wantNeedsTriage)
			}
		})
	}
}
