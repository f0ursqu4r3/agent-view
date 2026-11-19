package logs

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"agent-view/internal/fsutil"
)

// Root is a directory of agent logs.
type Root struct{ Provider, Path, Scope string }

// RecentLog is a log file found by a scan. MtimeMs keeps sub-millisecond precision.
type RecentLog struct {
	Root
	File    string
	MtimeMs float64
}

const maxDepth = 8

// DiscoverRoots finds the log roots that exist: Codex `sessions` (CODEX_HOME
// or ~/.codex), one per account under ~/.codex-accounts, and Claude `projects`
// (CLAUDE_CONFIG_DIR or ~/.claude). Roots are deduplicated by real path.
func DiscoverRoots(getenv func(string) string, home string) []Root {
	var roots []Root
	seen := map[string]bool{}
	add := func(provider, folder, scope string) {
		real, err := filepath.EvalSymlinks(folder)
		if err != nil || seen[real] {
			return
		}
		seen[real] = true
		sep := string(filepath.Separator)
		if strings.Contains(real, sep+".codex-accounts"+sep) {
			scope = filepath.Base(filepath.Dir(real))
		}
		roots = append(roots, Root{provider, real, scope})
	}
	codexHome := getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	add("codex", filepath.Join(codexHome, "sessions"), "default")
	accounts := filepath.Join(home, ".codex-accounts")
	entries, _ := os.ReadDir(accounts)
	for _, e := range entries {
		if e.Type().IsDir() {
			add("codex", filepath.Join(accounts, e.Name(), "sessions"), e.Name())
		}
	}
	claudeHome := getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	add("claude", filepath.Join(claudeHome, "projects"), "default")
	return roots
}

// ScanLogs lists every .jsonl file under each root that was modified within
// sinceMs, newest first (ties by File ascending). Like Node's Dirent, a symlink
// is neither a directory nor a file, so symlinks are skipped. onRoot is called
// once per root with "" when the scan worked, or the first non-ENOENT error
// code. It is not called for a root that does not exist.
func ScanLogs(roots []Root, sinceMs int64, onRoot func(r Root, code string)) []RecentLog {
	cutoff := float64(time.Now().UnixMilli() - sinceMs)
	var found []RecentLog
	for _, root := range roots {
		code := ""
		missing := false
		var visit func(dir string, depth int)
		visit = func(dir string, depth int) {
			if depth > maxDepth {
				return
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				c := fsutil.ErrCode(err)
				if c == "ENOENT" {
					if depth == 0 {
						missing = true
					}
					return
				}
				if code == "" {
					code = c
				}
				return
			}
			for _, e := range entries {
				file := filepath.Join(dir, e.Name())
				switch {
				case e.Type().IsDir():
					visit(file, depth+1)
				case e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl"):
					fi, err := os.Stat(file)
					if err != nil {
						continue
					}
					mtime := float64(fi.ModTime().UnixNano()) / 1e6
					if mtime >= cutoff {
						found = append(found, RecentLog{root, file, mtime})
					}
				}
			}
		}
		visit(root.Path, 0)
		if !missing && onRoot != nil {
			onRoot(root, code)
		}
	}
	slices.SortStableFunc(found, func(a, b RecentLog) int {
		switch {
		case a.MtimeMs > b.MtimeMs:
			return -1
		case a.MtimeMs < b.MtimeMs:
			return 1
		}
		return strings.Compare(a.File, b.File)
	})
	return found
}

// SelectLogs keeps the newest logs, floor(maxLogs / providers) per provider
// that has logs. The input is newest first.
func SelectLogs(all []RecentLog, maxLogs int) []RecentLog {
	providers := map[string]bool{}
	for _, l := range all {
		providers[l.Provider] = true
	}
	quota := maxLogs / max(len(providers), 1)
	taken := map[string]int{}
	var out []RecentLog
	for _, l := range all {
		if taken[l.Provider] >= quota {
			continue
		}
		taken[l.Provider]++
		out = append(out, l)
	}
	return out
}

// ListRecentLogs scans the roots and keeps the newest logs per provider.
func ListRecentLogs(roots []Root, sinceMs int64, maxLogs int, onRoot func(Root, string)) []RecentLog {
	return SelectLogs(ScanLogs(roots, sinceMs, onRoot), maxLogs)
}
