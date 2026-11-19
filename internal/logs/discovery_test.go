package logs

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const day = 24 * 3600 * 1000

func realDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mkdir(t *testing.T, p ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(p...), 0o755); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, file string, ageMs int64) {
	t.Helper()
	mkdir(t, filepath.Dir(file))
	write(t, file, []byte("{}\n"))
	mt := time.Now().Add(-time.Duration(ageMs) * time.Millisecond)
	if err := os.Chtimes(file, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func bases(logs []RecentLog) []string {
	var out []string
	for _, l := range logs {
		out = append(out, filepath.Base(l.File))
	}
	return out
}

// discoverRoots: CODEX_HOME, ~/.codex-accounts/* (scope = account dir), CLAUDE_CONFIG_DIR; realpath dedupe
func TestDiscoverRoots(t *testing.T) {
	home := realDir(t)
	mkdir(t, home, ".codex", "sessions")
	mkdir(t, home, ".codex-accounts", "work", "sessions")
	mkdir(t, home, ".codex-accounts", "empty") // no sessions dir: skipped
	mkdir(t, home, ".claude", "projects")
	eq(t, DiscoverRoots(envOf(nil), home), []Root{
		{"codex", filepath.Join(home, ".codex", "sessions"), "default"},
		{"codex", filepath.Join(home, ".codex-accounts", "work", "sessions"), "work"},
		{"claude", filepath.Join(home, ".claude", "projects"), "default"},
	})

	// Env overrides replace the default locations.
	mkdir(t, home, "other-codex", "sessions")
	mkdir(t, home, "other-claude", "projects")
	overridden := DiscoverRoots(envOf(map[string]string{
		"CODEX_HOME":        filepath.Join(home, "other-codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, "other-claude"),
	}), home)
	eq(t, overridden, []Root{
		{"codex", filepath.Join(home, "other-codex", "sessions"), "default"},
		{"codex", filepath.Join(home, ".codex-accounts", "work", "sessions"), "work"},
		{"claude", filepath.Join(home, "other-claude", "projects"), "default"},
	})

	// A symlinked CODEX_HOME that resolves to an account root yields one root, scoped to the account.
	if err := os.Symlink(filepath.Join(home, ".codex-accounts", "work"), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	deduped := DiscoverRoots(envOf(map[string]string{"CODEX_HOME": filepath.Join(home, "alias")}), home)
	var codex []Root
	for _, r := range deduped {
		if r.Provider == "codex" {
			codex = append(codex, r)
		}
	}
	eq(t, codex, []Root{{"codex", filepath.Join(home, ".codex-accounts", "work", "sessions"), "work"}})
}

// discoverRoots returns nothing when no root exists
func TestDiscoverRootsNone(t *testing.T) {
	roots := DiscoverRoots(envOf(nil), realDir(t))
	eq(t, len(roots), 0)
}

// listRecentLogs: only .jsonl modified within sinceMs, newest first, quota split equally per provider
func TestListRecentLogsQuota(t *testing.T) {
	dir := realDir(t)
	codex := Root{"codex", filepath.Join(dir, "codex"), "default"}
	claude := Root{"claude", filepath.Join(dir, "claude"), "default"}
	touch(t, filepath.Join(codex.Path, "c1.jsonl"), 1000)
	touch(t, filepath.Join(codex.Path, "2026", "c2.jsonl"), 2000)
	touch(t, filepath.Join(codex.Path, "c3.jsonl"), 3000)
	touch(t, filepath.Join(codex.Path, "c-old.jsonl"), 2*day)
	touch(t, filepath.Join(codex.Path, "notes.txt"), 10)
	for i := 0; i < 4; i++ {
		touch(t, filepath.Join(claude.Path, "proj", "a"+string(rune('0'+i))+".jsonl"), int64(10_000+i*1000))
	}

	all := ListRecentLogs([]Root{codex, claude}, day, 100, nil)
	eq(t, bases(all), []string{"c1.jsonl", "c2.jsonl", "c3.jsonl", "a0.jsonl", "a1.jsonl", "a2.jsonl", "a3.jsonl"})
	eq(t, all[0].Root, codex)
	eq(t, all[0].File, filepath.Join(codex.Path, "c1.jsonl"))
	if all[0].MtimeMs <= 0 {
		t.Errorf("MtimeMs = %v", all[0].MtimeMs)
	}

	// maxLogs 4 across two providers: 2 each, newest first.
	eq(t, bases(ListRecentLogs([]Root{codex, claude}, day, 4, nil)), []string{"c1.jsonl", "c2.jsonl", "a0.jsonl", "a1.jsonl"})

	// A provider with no recent logs does not take a share.
	eq(t, bases(ListRecentLogs([]Root{codex, claude}, 5000, 4, nil)), []string{"c1.jsonl", "c2.jsonl", "c3.jsonl"})
}

// listRecentLogs: depth limit 8 and missing roots are skipped
func TestListRecentLogsDepthAndMissing(t *testing.T) {
	dir := realDir(t)
	root := Root{"claude", filepath.Join(dir, "r"), "default"}
	d8 := filepath.Join(root.Path, "a", "b", "c", "d", "e", "f", "g", "h")
	touch(t, filepath.Join(d8, "ok.jsonl"), 10)        // depth 8
	touch(t, filepath.Join(d8, "i", "deep.jsonl"), 10) // depth 9
	missing := Root{"codex", filepath.Join(dir, "nope"), "default"}
	var called []string
	logs := ListRecentLogs([]Root{missing, root}, day, 10, func(r Root, code string) { called = append(called, r.Path+"|"+code) })
	eq(t, bases(logs), []string{"ok.jsonl"})
	eq(t, called, []string{root.Path + "|"}) // the missing root reports nothing
}

// listRecentLogs: reports a root error code through the callback
func TestListRecentLogsRootErrorCode(t *testing.T) {
	dir := realDir(t)
	root := Root{"claude", filepath.Join(dir, "file"), "default"}
	write(t, root.Path, []byte("not a directory"))
	var results [][2]string
	ListRecentLogs([]Root{root}, day, 10, func(r Root, code string) { results = append(results, [2]string{r.Path, code}) })
	eq(t, results, [][2]string{{root.Path, "ENOTDIR"}})
}

func TestScanLogsSkipsSymlinks(t *testing.T) {
	dir := realDir(t)
	root := Root{"claude", filepath.Join(dir, "r"), "default"}
	touch(t, filepath.Join(root.Path, "real", "a.jsonl"), 10)
	touch(t, filepath.Join(dir, "outside", "b.jsonl"), 10)
	touch(t, filepath.Join(dir, "outside", "c.jsonl"), 10)
	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(root.Path, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside", "c.jsonl"), filepath.Join(root.Path, "linkfile.jsonl")); err != nil {
		t.Fatal(err)
	}
	eq(t, bases(ScanLogs([]Root{root}, day, nil)), []string{"a.jsonl"})
}

func TestScanLogsCutoffAndTies(t *testing.T) {
	dir := realDir(t)
	root := Root{"claude", filepath.Join(dir, "r"), "default"}
	mt := time.Now().Add(-time.Hour)
	for _, n := range []string{"b.jsonl", "a.jsonl", "c.jsonl"} {
		f := filepath.Join(root.Path, n)
		mkdir(t, root.Path)
		write(t, f, nil)
		if err := os.Chtimes(f, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	logs := ScanLogs([]Root{root}, day, nil)
	eq(t, bases(logs), []string{"a.jsonl", "b.jsonl", "c.jsonl"})
	want := float64(mt.UnixNano()) / 1e6
	if logs[0].MtimeMs != want {
		t.Errorf("MtimeMs %v, want %v", logs[0].MtimeMs, want)
	}
	// Older than the window: excluded.
	eq(t, len(ScanLogs([]Root{root}, 30*60*1000, nil)), 0)
}

func TestScanLogsFirstErrorWins(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	dir := realDir(t)
	root := Root{"claude", filepath.Join(dir, "r"), "default"}
	touch(t, filepath.Join(root.Path, "ok", "a.jsonl"), 10)
	mkdir(t, root.Path, "locked")
	if err := os.Chmod(filepath.Join(root.Path, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(root.Path, "locked"), 0o755)
	var codes []string
	logs := ScanLogs([]Root{root}, day, func(_ Root, code string) { codes = append(codes, code) })
	eq(t, bases(logs), []string{"a.jsonl"})
	eq(t, codes, []string{"EACCES"})
}

func TestSelectLogsQuota(t *testing.T) {
	var all []RecentLog
	for _, p := range []string{"claude", "codex", "claude", "claude", "codex", "claude"} {
		all = append(all, RecentLog{Root: Root{Provider: p}, File: p})
	}
	got := SelectLogs(all, 5) // 5 / 2 providers = 2 each
	var names []string
	for _, l := range got {
		names = append(names, l.Provider)
	}
	if !slices.Equal(names, []string{"claude", "codex", "claude", "codex"}) {
		t.Errorf("got %v", names)
	}
	eq(t, len(SelectLogs(nil, 5)), 0)
}
