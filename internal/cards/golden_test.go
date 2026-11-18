package cards

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"agent-view/internal/cells"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// checkGolden compares the grid with testdata/cards/<name>.golden at the
// repository root. A golden file holds cells.Plain, a separator line, then
// cells.Mask.
func checkGolden(t *testing.T, name string, g *cells.Grid) {
	t.Helper()
	got := cells.Plain(g) + "\n---\n" + cells.Mask(g) + "\n"
	path := filepath.Join("..", "..", "testdata", "cards", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden.\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}
