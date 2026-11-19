package fschange

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// gitignoreCase is one row of testdata/gitignore-cases.json. The expected
// values come from the TypeScript GitignoreMatcher (ignore 7.0.16).
type gitignoreCase struct {
	Name string `json:"name"`
	// Files maps a slash path below the root (".gitignore",
	// "pkg/.gitignore", ".git/info/exclude", or ".git" as a file) to its
	// content.
	Files   map[string]string `json:"files"`
	Path    string            `json:"path"`
	Ignored bool              `json:"ignored"`
}

// Each row's files are created in a fresh root, and IsIgnored must give the
// answer the TypeScript matcher gives for the same files.
func TestGitignoreCases(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gitignore-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []gitignoreCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("got %d cases, want at least 30", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			dir := tempDir(t)
			for rel, text := range c.Files {
				file := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			m := NewGitignoreMatcher(dir)
			if got := m.IsIgnored(filepath.Join(dir, filepath.FromSlash(c.Path))); got != c.Ignored {
				t.Errorf("files %q: IsIgnored(%q) = %v, want %v", c.Files, c.Path, got, c.Ignored)
			}
		})
	}
}
