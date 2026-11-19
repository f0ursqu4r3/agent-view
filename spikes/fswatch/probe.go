package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// class is one M0 path class.
type class struct {
	name      string
	base      string // parent of the test root (may be a symlinked spelling such as /tmp)
	late      bool   // create the sub-directories after the watcher is armed
	external  bool   // a separate process writes the files
	rootWatch bool   // watch the parent of the test root (a shared, busy directory)
	symlink   bool   // the test root is a symlink to the real directory
}

func classes(homeBase string) []class {
	if runtime.GOOS == "darwin" {
		tmpdir, _ := filepath.EvalSymlinks(os.TempDir())
		return []class{
			{name: "home", base: homeBase},
			{name: "tmpdir", base: tmpdir},
			{name: "private-tmp", base: "/private/tmp"},
			{name: "private-tmp+late-subdirs", base: "/private/tmp", late: true},
			{name: "private-tmp-ROOT-watch", base: "/private/tmp", rootWatch: true},
			{name: "private-tmp+external-writer", base: "/private/tmp", external: true},
			{name: "tmp-symlinked-path", base: "/tmp"},
			{name: "home+external-writer", base: homeBase, external: true},
			{name: "symlink", base: homeBase, symlink: true},
		}
	}
	home, _ := os.UserHomeDir()
	return []class{
		{name: "home", base: home},
		{name: "tmp", base: "/tmp"},
		{name: "tmp+late-subdirs", base: "/tmp", late: true},
		{name: "tmp-ROOT-watch", base: "/tmp", rootWatch: true},
		{name: "tmp+external-writer", base: "/tmp", external: true},
		{name: "symlink", base: "/tmp", symlink: true},
		{name: "dev-shm (tmpfs)", base: "/dev/shm"},
	}
}

type popts struct {
	files, subdirs    int
	writeMs, settleMs int
	armMs             int
	w                 wopts
}

type result struct {
	class, backend  string
	seen, missed    int
	events, unknown int
	foreign         int
	p50, p95, max   time.Duration
	armDur          time.Duration
	fdArmed, fdEnd  int
	st              *wstats
	err             string
}

var subdirName = regexp.MustCompile(`^d\d+$`)

func runOnce(backend string, c class, o popts, n int) result {
	res := result{class: c.name, backend: backend, st: &wstats{}}
	root := filepath.Join(c.base, fmt.Sprintf("fsw-%d-%s-%d", os.Getpid(), backend, n))
	var cleanup []string
	if c.symlink {
		real := root + "-real"
		must(os.MkdirAll(real, 0o755))
		must(os.Symlink(real, root))
		cleanup = append(cleanup, root, real)
	} else {
		must(os.MkdirAll(root, 0o755))
		cleanup = append(cleanup, root)
	}
	defer func() {
		for _, p := range cleanup {
			_ = os.RemoveAll(p)
		}
	}()
	subs := make([]string, o.subdirs)
	for i := range subs {
		subs[i] = "d" + strconv.Itoa(i)
	}
	if !c.late {
		for _, s := range subs {
			must(os.MkdirAll(filepath.Join(root, s), 0o755))
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	must(err)
	armPath := realRoot
	if c.rootWatch {
		armPath = filepath.Dir(realRoot)
	}

	rec := newRecorder(realRoot)
	fdBase := openFDs()
	t := time.Now()
	w, err := newWatcher(backend, armPath, rec.onEvent, o.w, res.st)
	res.armDur = time.Since(t)
	if err != nil {
		res.err = err.Error()
		return res
	}
	time.Sleep(time.Duration(o.armMs) * time.Millisecond)
	res.fdArmed = openFDs() - fdBase
	if c.late {
		for _, s := range subs {
			must(os.MkdirAll(filepath.Join(root, s), 0o755))
		}
	}

	var mu sync.Mutex
	wrote := map[string]time.Time{}
	if c.external {
		self, _ := os.Executable()
		cmd := exec.Command(self, "write", root, strconv.Itoa(o.files), strconv.Itoa(o.writeMs), strconv.Itoa(o.subdirs))
		out, _ := cmd.StdoutPipe()
		must(cmd.Start())
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			mu.Lock()
			if _, ok := wrote[sc.Text()]; !ok {
				wrote[sc.Text()] = time.Now()
			}
			mu.Unlock()
		}
		_ = cmd.Wait()
	} else {
		writeFiles(root, o.files, o.writeMs, o.subdirs, func(rel string) {
			mu.Lock()
			wrote[rel] = time.Now()
			mu.Unlock()
		})
	}
	time.Sleep(time.Duration(o.settleMs) * time.Millisecond)
	res.fdEnd = openFDs() - fdBase
	w.Close()

	rec.mu.Lock()
	defer rec.mu.Unlock()
	var lat []time.Duration
	for rel, wt := range wrote {
		if ft, ok := rec.first[rel]; ok {
			res.seen++
			d := ft.Sub(wt)
			if d < 0 {
				d = 0
			}
			lat = append(lat, d)
		}
	}
	for rel := range rec.first {
		if _, ok := wrote[rel]; !ok && !subdirName.MatchString(rel) {
			res.unknown++
		}
	}
	res.missed = o.files - res.seen
	res.events, res.foreign = rec.events, rec.foreign
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	if len(lat) > 0 {
		res.p50 = lat[len(lat)/2]
		res.p95 = lat[min(len(lat)-1, len(lat)*95/100)]
		res.max = lat[len(lat)-1]
	}
	return res
}

// writeFiles writes n files over ms milliseconds, spread over the sub-directories.
// before is called with the relative path just before each write.
func writeFiles(root string, n, ms, subdirs int, before func(rel string)) {
	t0 := time.Now()
	for i := 0; i < n; i++ {
		target := t0.Add(time.Duration(i) * time.Duration(ms) * time.Millisecond / time.Duration(n))
		if d := time.Until(target); d > 0 {
			time.Sleep(d)
		}
		rel := fmt.Sprintf("d%d/f%d.txt", i%subdirs, i)
		before(rel)
		if err := os.WriteFile(filepath.Join(root, rel), []byte("x"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
		}
	}
}

func writeMain(args []string) {
	if len(args) != 4 {
		fatal("write <root> <n> <ms> <subdirs>")
	}
	n, _ := strconv.Atoi(args[1])
	ms, _ := strconv.Atoi(args[2])
	subs, _ := strconv.Atoi(args[3])
	writeFiles(args[0], n, ms, subs, func(rel string) { fmt.Println(rel) })
}

func probeMain(args []string) {
	fl := flag.NewFlagSet("probe", flag.ExitOnError)
	runs := fl.Int("runs", 3, "runs per class and backend")
	backends := fl.String("backends", defaultBackends(), "comma-separated: fsevents,fsnotify")
	only := fl.String("classes", "", "comma-separated class names (default all)")
	homeBase := fl.String("home-base", "../../test/.tmp", "base directory of the home class (darwin)")
	o := popts{w: wopts{maxDirs: 12000}}
	fl.IntVar(&o.files, "files", 1000, "files per run")
	fl.IntVar(&o.subdirs, "subdirs", 10, "sub-directories per run")
	fl.IntVar(&o.writeMs, "write-ms", 2000, "write window")
	fl.IntVar(&o.settleMs, "settle-ms", 1500, "wait after the last write")
	fl.IntVar(&o.armMs, "arm-ms", 300, "wait after arming")
	fl.DurationVar(&o.w.latency, "latency", 50*time.Millisecond, "FSEvents latency")
	fl.BoolVar(&o.w.noDefer, "nodefer", false, "FSEvents NoDefer flag")
	_ = fl.Parse(args)

	hb, _ := filepath.Abs(*homeBase)
	must(os.MkdirAll(hb, 0o755))
	want := map[string]bool{}
	for _, c := range strings.Split(*only, ",") {
		if c != "" {
			want[c] = true
		}
	}
	fmt.Printf("# %s/%s, %d files, %d subdirs, %d ms writes, settle %d ms, arm %d ms, FSEvents latency %v nodefer=%v\n",
		runtime.GOOS, runtime.GOARCH, o.files, o.subdirs, o.writeMs, o.settleMs, o.armMs, o.w.latency, o.w.noDefer)
	fmt.Println("class\tbackend\trun\tseen\tmissed\tevents\tunknown\tforeign\tp50\tp95\tmax\tarm\tfdArmed\tfdEnd\tdirs\tflags")
	for _, c := range classes(hb) {
		if len(want) > 0 && !want[c.name] {
			continue
		}
		for _, b := range strings.Split(*backends, ",") {
			for r := 1; r <= *runs; r++ {
				res := runOnce(b, c, o, r)
				if res.err != "" {
					fmt.Printf("%s\t%s\t%d\tERROR %s\n", c.name, b, r, res.err)
					continue
				}
				fmt.Printf("%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
					c.name, b, r, res.seen, res.missed, res.events, res.unknown, res.foreign,
					ms(res.p50), ms(res.p95), ms(res.max), ms(res.armDur), res.fdArmed, res.fdEnd,
					res.st.dirs.Load(), flagsOf(res.st))
			}
		}
	}
}

func defaultBackends() string {
	if runtime.GOOS == "darwin" {
		return "fsevents,fsnotify"
	}
	return "fsnotify"
}

func flagsOf(st *wstats) string {
	var f []string
	add := func(name string, v int64) {
		if v > 0 {
			f = append(f, fmt.Sprintf("%s=%d", name, v))
		}
	}
	add("mustScan", st.mustScan.Load())
	add("userDropped", st.userDropped.Load())
	add("kernelDropped", st.kernelDropped.Load())
	add("errors", st.errors.Load())
	add("overflow", st.overflow.Load())
	add("addErrors", st.addErrors.Load())
	if st.capped.Load() {
		f = append(f, "capped")
	}
	if len(f) == 0 {
		return "-"
	}
	return strings.Join(f, ",")
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d)/float64(time.Millisecond)) }

func must(err error) {
	if err != nil {
		fatal("%v", err)
	}
}
