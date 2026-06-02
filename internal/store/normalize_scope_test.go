package store

import "testing"

func TestNormalizeScope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		// All 4 valid tier values pass through unchanged.
		{name: "personal pass-through", input: "personal", want: "personal"},
		{name: "department pass-through", input: "department", want: "department"},
		{name: "project pass-through", input: "project", want: "project"},
		{name: "team pass-through", input: "team", want: "team"},

		// Unknown / empty values default to "project".
		{name: "empty string defaults to project", input: "", want: "project"},
		{name: "unknown value defaults to project", input: "global", want: "project"},
		{name: "whitespace-only defaults to project", input: "  ", want: "project"},
		{name: "mixed-case Team normalizes to team", input: "Team", want: "team"},
		{name: "random string defaults to project", input: "org", want: "project"},

		// Whitespace trimming + lowercasing within valid values.
		{name: "personal with leading space", input: " personal", want: "personal"},
		{name: "DEPARTMENT uppercase", input: "DEPARTMENT", want: "department"},
		{name: "PROJECT uppercase", input: "PROJECT", want: "project"},
		{name: "TEAM uppercase", input: "TEAM", want: "team"},

		// Default-narrower path: previously "global" was accepted; now it must default to "project".
		{name: "legacy global coerced to project", input: "global", want: "project"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeScope(tc.input)
			if got != tc.want {
				t.Errorf("normalizeScope(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
