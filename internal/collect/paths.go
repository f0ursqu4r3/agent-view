package collect

import (
	"container/list"
	"os"
	"path/filepath"
	"slices"

	"agent-view/internal/fsutil"
)

// pathCacheSize is the number of entries each path cache holds.
const pathCacheSize = 5000

// pathCache is a least-recently-used cache of resolved paths. Misses are
// never stored: the path may exist a moment later.
type pathCache struct {
	max   int
	items map[string]*list.Element
	order list.List // least recently used first
}

type pathEntry struct{ key, val string }

func newPathCache(max int) *pathCache {
	return &pathCache{max: max, items: map[string]*list.Element{}}
}

func (c *pathCache) get(key string) (string, bool) {
	el, ok := c.items[key]
	if !ok {
		return "", false
	}
	c.order.MoveToBack(el)
	return el.Value.(*pathEntry).val, true
}

func (c *pathCache) set(key, val string) {
	if el, ok := c.items[key]; ok {
		el.Value.(*pathEntry).val = val
		c.order.MoveToBack(el)
		return
	}
	c.items[key] = c.order.PushBack(&pathEntry{key, val})
	if c.order.Len() > c.max {
		oldest := c.order.Front()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*pathEntry).key)
	}
}

// paths resolves the paths that events carry to real paths. It is not safe
// for concurrent use: the collector's loop goroutine owns it.
type paths struct {
	dirs         *pathCache
	projectRoots *pathCache
}

func newPaths() *paths {
	return &paths{dirs: newPathCache(pathCacheSize), projectRoots: newPathCache(pathCacheSize)}
}

// projectRootFor returns the real path of the project root of cwd. It
// reports false when that path does not exist.
func (p *paths) projectRootFor(cwd string) (string, bool) {
	if root, ok := p.projectRoots.get(cwd); ok {
		return root, true
	}
	root, err := realpath(fsutil.FindProjectRoot(cwd))
	if err != nil {
		return "", false
	}
	p.projectRoots.set(cwd, root)
	return root, true
}

func (p *paths) mapPaths(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = p.realPath(s)
	}
	return out
}

// realPath is the real path of the parent directory plus the base name. The
// file, and even its directory, may not exist yet: Claude logs a Write
// before the tool creates it. The nearest existing ancestor is resolved and
// the missing segments are appended. A relative path stays as it is.
func (p *paths) realPath(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	dir, ok := p.realDir(dirname(path))
	if !ok {
		return path
	}
	return filepath.Join(dir, filepath.Base(path))
}

func (p *paths) realDir(dir string) (string, bool) {
	var missing []string
	cur := dir
	for {
		real, ok := p.dirs.get(cur)
		if !ok {
			if rp, err := realpath(cur); err == nil {
				real, ok = rp, true
				p.dirs.set(cur, rp)
			}
		}
		if ok {
			slices.Reverse(missing)
			return filepath.Join(append([]string{real}, missing...)...), true
		}
		parent := dirname(cur)
		if parent == cur {
			return "", false
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

// realpath is fs.realpath: the absolute path with every symlink resolved.
// Unlike filepath.Abs it does not clean the path first, so ".." leaves the
// target of a symlink, as realpath(3) does.
func realpath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = wd + string(filepath.Separator) + path
	}
	return filepath.EvalSymlinks(path)
}

// dirname is Node's path.posix.dirname. Unlike filepath.Dir it ignores
// trailing slashes and does not clean the result, so a ".." segment still
// resolves through a symlink.
func dirname(path string) string {
	if path == "" {
		return "."
	}
	hasRoot := path[0] == '/'
	end := -1
	matchedSlash := true
	for i := len(path) - 1; i >= 1; i-- {
		if path[i] == '/' {
			if !matchedSlash {
				end = i
				break
			}
		} else {
			matchedSlash = false
		}
	}
	switch {
	case end == -1 && hasRoot:
		return "/"
	case end == -1:
		return "."
	case hasRoot && end == 1:
		return "//"
	}
	return path[:end]
}
