package triage_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
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
