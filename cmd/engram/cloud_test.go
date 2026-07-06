package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/auth"
)

// TestResolveJWTTTL verifies resolveJWTTTL() parse-and-clamp behavior:
//   - Empty env → DefaultJWTTTL, no warning.
//   - Sub-minimum duration → MinJWTTTL (24h) with warning on stderr.
//   - Unparseable value → DefaultJWTTTL with warning on stderr.
//   - Valid duration at or above minimum → honored as-is, no warning.
//
// Spec: configurable-jwt-ttl §Minimum-TTL Guard.
func TestResolveJWTTTL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		envVal   string
		wantTTL  time.Duration
		wantWarn bool
	}{
		{
			name:     "empty env uses default no warn",
			envVal:   "",
			wantTTL:  auth.DefaultJWTTTL,
			wantWarn: false,
		},
		{
			name:     "sub-minimum clamped to 24h with warn",
			envVal:   "1h",
			wantTTL:  auth.MinJWTTTL,
			wantWarn: true,
		},
		{
			name:     "unparseable falls back to default with warn",
			envVal:   "garbage",
			wantTTL:  auth.DefaultJWTTTL,
			wantWarn: true,
		},
		{
			name:     "2160h honored no warn",
			envVal:   "2160h",
			wantTTL:  2160 * time.Hour,
			wantWarn: false,
		},
		{
			name:     "exactly 24h honored no warn",
			envVal:   "24h",
			wantTTL:  24 * time.Hour,
			wantWarn: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var warnBuf strings.Builder
			got := resolveJWTTTLWithWriter(&warnBuf, tc.envVal)

			if got != tc.wantTTL {
				t.Errorf("resolveJWTTTL(%q): got %v, want %v", tc.envVal, got, tc.wantTTL)
			}

			hasWarn := warnBuf.Len() > 0
			if hasWarn != tc.wantWarn {
				t.Errorf("resolveJWTTTL(%q): warnWritten=%v, wantWarn=%v (output: %q)",
					tc.envVal, hasWarn, tc.wantWarn, warnBuf.String())
			}
		})
	}
}
