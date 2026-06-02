package scope_test

import (
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloud/scope"
)

// TestNormalizeScope verifies the 4-tier pass-through and default-narrower coercion.
func TestNormalizeScope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  string
	}{
		{"personal", "personal"},
		{"department", "department"},
		{"project", "project"},
		{"team", "team"},
		{"", "project"},
		{"global", "project"},
		{"unknown", "project"},
		{"TEAM", "team"},
		{"PERSONAL", "personal"},
		{"Department", "department"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := scope.NormalizeScope(tc.input)
			if got != tc.want {
				t.Errorf("NormalizeScope(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestVisible covers the full cross-product of the 4-tier filter.
// Axes: scope × (same/other email) × (same/other dept) × enrolled/not × user_deleted.
func TestVisible(t *testing.T) {
	t.Parallel()

	alice := scope.Principal{
		Email:      "alice@example.com",
		Department: "engineering",
		Enrolled:   []string{"general", "eng-notes"},
	}

	cases := []struct {
		name    string
		p       scope.Principal
		a       scope.Attrs
		visible bool
	}{
		// ── personal scope: only same user ────────────────────────────────────
		{
			name:    "personal/same user sees own obs",
			p:       alice,
			a:       scope.Attrs{Scope: "personal", UserEmail: "alice@example.com", Department: "engineering", Project: "eng-notes"},
			visible: true,
		},
		{
			name:    "personal/other user cannot see",
			p:       alice,
			a:       scope.Attrs{Scope: "personal", UserEmail: "bob@example.com", Department: "engineering", Project: "eng-notes"},
			visible: false,
		},
		{
			name:    "personal/same user deleted still sees",
			p:       alice,
			a:       scope.Attrs{Scope: "personal", UserEmail: "alice@example.com", Department: "engineering", Project: "eng-notes", UserDeleted: true},
			visible: true,
		},
		{
			name:    "personal/other user deleted cannot see",
			p:       alice,
			a:       scope.Attrs{Scope: "personal", UserEmail: "bob@example.com", Department: "engineering", Project: "eng-notes", UserDeleted: true},
			visible: false,
		},

		// ── department scope: same dept AND enrolled in project ────────────────
		{
			name:    "department/same dept enrolled sees obs",
			p:       alice,
			a:       scope.Attrs{Scope: "department", UserEmail: "carol@example.com", Department: "engineering", Project: "eng-notes"},
			visible: true,
		},
		{
			name:    "department/other dept cannot see",
			p:       alice,
			a:       scope.Attrs{Scope: "department", UserEmail: "dave@example.com", Department: "product", Project: "eng-notes"},
			visible: false,
		},
		{
			name:    "department/same dept NOT enrolled cannot see",
			p:       alice,
			a:       scope.Attrs{Scope: "department", UserEmail: "eve@example.com", Department: "engineering", Project: "secret-project"},
			visible: false,
		},
		{
			name:    "department/deleted user same dept enrolled still visible",
			p:       alice,
			a:       scope.Attrs{Scope: "department", UserEmail: "deleted@example.com", Department: "engineering", Project: "eng-notes", UserDeleted: true},
			visible: true,
		},

		// ── project scope: enrolled in project ────────────────────────────────
		{
			name:    "project/enrolled sees obs",
			p:       alice,
			a:       scope.Attrs{Scope: "project", UserEmail: "anyone@example.com", Department: "product", Project: "eng-notes"},
			visible: true,
		},
		{
			name:    "project/not enrolled cannot see",
			p:       alice,
			a:       scope.Attrs{Scope: "project", UserEmail: "anyone@example.com", Department: "product", Project: "client-secret"},
			visible: false,
		},
		{
			name:    "project/deleted user enrolled still visible",
			p:       alice,
			a:       scope.Attrs{Scope: "project", UserEmail: "deleted@example.com", Department: "product", Project: "eng-notes", UserDeleted: true},
			visible: true,
		},

		// ── team scope: enrolled in "general" (the team-wide convention) ───────
		{
			name:    "team/enrolled in general sees team obs",
			p:       alice,
			a:       scope.Attrs{Scope: "team", UserEmail: "anyone@example.com", Department: "product", Project: "general"},
			visible: true,
		},
		{
			name:    "team/obs project general, principal enrolled sees it",
			p:       alice,
			a:       scope.Attrs{Scope: "team", UserEmail: "x@example.com", Department: "other", Project: "general"},
			visible: true,
		},
		{
			name:    "team/not enrolled in general cannot see",
			p:       scope.Principal{Email: "outsider@example.com", Department: "ops", Enrolled: []string{"ops-project"}},
			a:       scope.Attrs{Scope: "team", UserEmail: "x@example.com", Department: "eng", Project: "general"},
			visible: false,
		},
		{
			name:    "team/deleted user enrolled still visible",
			p:       alice,
			a:       scope.Attrs{Scope: "team", UserEmail: "deleted@example.com", Department: "product", Project: "general", UserDeleted: true},
			visible: true,
		},

		// ── unknown scope: never visible ──────────────────────────────────────
		{
			name:    "unknown scope not visible",
			p:       alice,
			a:       scope.Attrs{Scope: "org", UserEmail: "alice@example.com", Department: "engineering", Project: "eng-notes"},
			visible: false,
		},
		{
			name:    "empty scope not visible",
			p:       alice,
			a:       scope.Attrs{Scope: "", UserEmail: "alice@example.com", Department: "engineering", Project: "eng-notes"},
			visible: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scope.Visible(tc.p, tc.a)
			if got != tc.visible {
				t.Errorf("Visible() = %v, want %v (scope=%q, principal=%+v, attrs=%+v)",
					got, tc.visible, tc.a.Scope, tc.p, tc.a)
			}
		})
	}
}
