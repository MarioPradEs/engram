package triage_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/triage"
)

// updateGolden is set by -update flag to regenerate golden files.
var updateGolden = flag.Bool("update", false, "update golden files")

// goldenCheck compares got against the golden file at testdata/golden/<name>.html.
// When -update is passed, it writes got to the golden file instead of comparing.
func goldenCheck(t *testing.T, name string, got []byte) {
	t.Helper()

	dir := filepath.Join("testdata", "golden")
	path := filepath.Join(dir, name+".html")

	if *updateGolden {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("golden: mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("golden: write %s: %v", path, err)
		}
		t.Logf("golden: updated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden: read %s: %v (run with -update to generate)", path, err)
	}

	if string(got) != string(want) {
		t.Errorf("golden mismatch for %s:\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}

// ─── Phase 6.2: SharePanel golden snapshots ──────────────────────────────────

// TestSharePanel_NotSharedGolden renders SharePanel with isShared=false and
// compares to a golden baseline.
// Generate baseline: go test ./internal/triage/... -run TestSharePanel -update
func TestSharePanel_NotSharedGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := triage.SharePanel("myproject", false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render SharePanel(not-shared): %v", err)
	}
	goldenCheck(t, "share_panel_not_shared", buf.Bytes())
}

// TestSharePanel_SharedGolden renders SharePanel with isShared=true and
// compares to a golden baseline.
// Generate baseline: go test ./internal/triage/... -run TestSharePanel -update
func TestSharePanel_SharedGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := triage.SharePanel("myproject", true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render SharePanel(shared): %v", err)
	}
	goldenCheck(t, "share_panel_shared", buf.Bytes())
}
