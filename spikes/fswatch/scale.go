package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// scaleMain builds a tree of about -files files (10 files per directory,
// 10 directories per top-level directory), arms one backend on it, and
// measures setup, descriptors, memory, idle CPU, a burst of writes, and the
// CPU cost of churn in node_modules (an ignored directory).
func scaleMain(args []string) {
	fl := flag.NewFlagSet("scale", flag.ExitOnError)
	backend := fl.String("backend", "fsevents", "fsevents or fsnotify")
	files := fl.Int("files", 20000, "files in the tree")
	base := fl.String("base", "/private/tmp", "parent of the tree")
	idle := fl.Duration("idle", 20*time.Second, "idle window")
	burst := fl.Int("burst", 200, "files in the burst (one per directory)")
	churn := fl.Int("churn", 20000, "files written into -churn-into by a separate process")
	churnInto := fl.String("churn-into", "node_modules", "directory under the root for the churn")
	latency := fl.Duration("latency", 50*time.Millisecond, "FSEvents latency")
	noDefer := fl.Bool("nodefer", false, "FSEvents NoDefer flag")
	_ = fl.Parse(args)

	root := filepath.Join(*base, fmt.Sprintf("fsw-scale-%d-%s", os.Getpid(), *backend))
	defer os.RemoveAll(root)
	t := time.Now()
	dirs := buildTree(root, *files)
	must(os.MkdirAll(filepath.Join(root, *churnInto), 0o755))
	fmt.Printf("# %s/%s backend=%s tree: %d files, %d dirs, built in %s ms\n",
		runtime.GOOS, runtime.GOARCH, *backend, *files, len(dirs), ms(time.Since(t)))

	realRoot, err := filepath.EvalSymlinks(root)
	must(err)
	var mu sync.Mutex
	first := map[string]time.Time{}
	var delivered, ignoredEv int
	prefix := realRoot + "/"
	cb := func(abs string) {
		now := time.Now()
		rel := strings.TrimPrefix(abs, prefix)
		mu.Lock()
		defer mu.Unlock()
		delivered++
		for _, seg := range strings.Split(rel, "/") {
			if ignoredDirs[seg] {
				ignoredEv++
				return
			}
		}
		if _, ok := first[rel]; !ok {
			first[rel] = now
		}
	}

	st := &wstats{}
	runtime.GC()
	fd0 := openFDs()
	cpu0 := cpuTime()
	t = time.Now()
	w, err := newWatcher(*backend, realRoot, cb, wopts{latency: *latency, noDefer: *noDefer, maxDirs: 1 << 30}, st)
	must(err)
	armDur := time.Since(t)
	armCPU := cpuTime() - cpu0
	time.Sleep(500 * time.Millisecond)
	fdArmed := openFDs() - fd0
	fmt.Printf("arm: %s ms wall, %s ms CPU, +%d descriptors, %d dirs added, peak RSS %.1f MB, flags %s\n",
		ms(armDur), ms(armCPU), fdArmed, st.dirs.Load(), maxRSSMB(), flagsOf(st))

	cpu1 := cpuTime()
	time.Sleep(*idle)
	idleCPU := cpuTime() - cpu1
	fmt.Printf("idle: %.3f%% of one core over %s\n", 100*float64(idleCPU)/float64(*idle), *idle)

	// Burst: one new file in each of the first -burst directories, over 1 s.
	wrote := map[string]time.Time{}
	step := max(1, len(dirs) / *burst)
	t0 := time.Now()
	for i := 0; i < *burst && i*step < len(dirs); i++ {
		if d := time.Until(t0.Add(time.Duration(i) * time.Second / time.Duration(*burst))); d > 0 {
			time.Sleep(d)
		}
		rel := filepath.Join(dirs[i*step], "burst.txt")
		wrote[rel] = time.Now()
		must(os.WriteFile(filepath.Join(root, rel), []byte("x"), 0o644))
	}
	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	var lat []time.Duration
	for rel, wt := range wrote {
		if ft, ok := first[rel]; ok {
			lat = append(lat, max(0, ft.Sub(wt)))
		}
	}
	mu.Unlock()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p95 := time.Duration(0)
	if len(lat) > 0 {
		p95 = lat[min(len(lat)-1, len(lat)*95/100)]
	}
	fmt.Printf("burst: %d/%d seen, p95 %s ms, descriptors now +%d\n", len(lat), len(wrote), ms(p95), openFDs()-fd0)

	// Churn: a separate process writes into node_modules; our CPU is the watcher's cost.
	mu.Lock()
	delivered, ignoredEv = 0, 0
	mu.Unlock()
	self, _ := os.Executable()
	cpu2 := cpuTime()
	t = time.Now()
	out, err := exec.Command(self, "churn", filepath.Join(root, *churnInto), strconv.Itoa(*churn)).CombinedOutput()
	if err != nil {
		fatal("churn: %v %s", err, out)
	}
	churnWall := time.Since(t)
	time.Sleep(2 * time.Second)
	churnCPU := cpuTime() - cpu2
	mu.Lock()
	fmt.Printf("churn: %d files in %s in %s ms; watcher delivered %d events (%d in ignored dirs), used %s ms CPU, peak RSS %.1f MB, flags %s\n",
		*churn, *churnInto, ms(churnWall), delivered, ignoredEv, ms(churnCPU), maxRSSMB(), flagsOf(st))
	if !ignoredDirs[*churnInto] {
		seen := 0
		for i := 0; i < *churn; i++ {
			if _, ok := first[filepath.Join(*churnInto, fmt.Sprintf("p%d", i/100), fmt.Sprintf("f%d.js", i))]; ok {
				seen++
			}
		}
		fmt.Printf("churn seen: %d/%d files\n", seen, *churn)
	}
	mu.Unlock()
	st.mu.Lock()
	for _, p := range st.dropPaths {
		fmt.Printf("drop flag path: %q\n", strings.TrimPrefix(p, prefix))
	}
	st.mu.Unlock()

	w.Close()
}

// buildTree creates the tree and returns its leaf directories (relative).
func buildTree(root string, files int) []string {
	var dirs []string
	leaves := max(1, files/10)
	for i := 0; i < leaves; i++ {
		rel := filepath.Join(fmt.Sprintf("a%d", i/10), fmt.Sprintf("b%d", i%10))
		must(os.MkdirAll(filepath.Join(root, rel), 0o755))
		for k := 0; k < 10; k++ {
			must(os.WriteFile(filepath.Join(root, rel, fmt.Sprintf("f%d.txt", k)), []byte("x"), 0o644))
		}
		dirs = append(dirs, rel)
	}
	return dirs
}

func churnMain(args []string) {
	if len(args) != 2 {
		fatal("churn <dir> <n>")
	}
	n, _ := strconv.Atoi(args[1])
	for i := 0; i < n; i++ {
		d := filepath.Join(args[0], fmt.Sprintf("p%d", i/100))
		if i%100 == 0 {
			must(os.MkdirAll(d, 0o755))
		}
		must(os.WriteFile(filepath.Join(d, fmt.Sprintf("f%d.js", i)), []byte("x"), 0o644))
	}
}

// countMain prints dirs and files per root, skipping ignored directories and
// symlinks, as the fsnotify backend would watch them.
func countMain(args []string) {
	for _, root := range args {
		var dirs, files int
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if p != root && ignoredDirs[d.Name()] {
					return fs.SkipDir
				}
				dirs++
				return nil
			}
			files++
			return nil
		})
		fmt.Printf("%d\t%d\t%s\n", dirs, files, root)
	}
}
