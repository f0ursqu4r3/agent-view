package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-view/internal/demo"
	"agent-view/internal/lifecycle"
	"agent-view/internal/logs"
	"agent-view/internal/wire"
)

// colDeadline bounds the waits on real files and watchers. FSEvents can lag
// by seconds when fseventsd is busy (see the Task 7 report).
const colDeadline = 8 * time.Second

// recorder records envelopes. The collector calls fn on its loop goroutine.
type recorder struct {
	mu   sync.Mutex
	envs []wire.Envelope
}

func (r *recorder) fn(e wire.Envelope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.envs = append(r.envs, e)
}

func (r *recorder) all() []wire.Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.envs)
}

// until waits in real time until ok holds for the recorded envelopes.
func (r *recorder) until(t *testing.T, ok func([]wire.Envelope) bool, d time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok(r.all()) {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// advanceUntil moves the demo clock in 50 ms steps, at most ms, until ok
// holds once the collector has handled what the demo emitted.
func advanceUntil(t *testing.T, c *Collector, sched *demo.FakeScheduler, r *recorder, ok func([]wire.Envelope) bool, ms float64, what string) {
	t.Helper()
	for elapsed := 0.0; ; elapsed += 50 {
		c.await(nil)
		if ok(r.all()) {
			return
		}
		if elapsed >= ms {
			t.Fatalf("timeout waiting for %s", what)
		}
		sched.Advance(50)
	}
}

// find returns the first event of type T that matches, and its index (-1
// when none does).
func find[T wire.Event](envs []wire.Envelope, match func(T) bool) (T, int) {
	for i, e := range envs {
		if v, ok := e.Ev.(T); ok && (match == nil || match(v)) {
			return v, i
		}
	}
	var zero T
	return zero, -1
}

func count[T wire.Event](envs []wire.Envelope) int {
	n := 0
	for _, e := range envs {
		if _, ok := e.Ev.(T); ok {
			n++
		}
	}
	return n
}

func startOf(callID string) func(wire.ToolStart) bool {
	return func(e wire.ToolStart) bool { return e.Call.CallID == callID }
}

func endOf(callID string) func(wire.ToolEnd) bool {
	return func(e wire.ToolEnd) bool { return e.CallID == callID }
}

// realTempDir is a temporary directory with symlinks resolved (/var is a
// symlink on macOS).
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func claudeRoot(dir string) logs.Root {
	return logs.Root{Provider: "claude", Path: filepath.Join(dir, "logs", "projects"), Scope: "default"}
}

func jsonLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func toolUseLine(t *testing.T, id, name string, input map[string]any, cwd string) string {
	return jsonLine(t, map[string]any{
		"sessionId": "sess", "cwd": cwd, "timestamp": wire.FormatISO(wire.NowMs()), "type": "assistant",
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": name, "input": input},
		}},
	})
}

func toolResultLine(t *testing.T, id, cwd string) string {
	return jsonLine(t, map[string]any{
		"sessionId": "sess", "cwd": cwd, "timestamp": wire.FormatISO(wire.NowMs()), "type": "user",
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"},
		}},
	})
}

// writeLog writes text to <root>/p/sess.jsonl and returns the file's path.
func writeLog(t *testing.T, root logs.Root, text string) string {
	t.Helper()
	log := filepath.Join(root.Path, "p", "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return log
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func startCollector(t *testing.T, c *Collector) {
	t.Helper()
	t.Cleanup(c.Stop)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
}

// newDemo returns a demo collector on a fake clock that starts at the real
// time, so the demo agents are present for the snapshot.
func newDemo() (*Collector, *demo.FakeScheduler) {
	sched := demo.NewFakeScheduler(float64(wire.NowMs()))
	return New(Options{Host: "demo-host", Demo: true, Sched: sched}), sched
}

func snapshotOf(t *testing.T, e wire.Envelope) wire.Snapshot {
	t.Helper()
	s, ok := e.Ev.(wire.Snapshot)
	if !ok {
		t.Fatalf("kind = %s, want snapshot", e.Ev.Kind())
	}
	return s
}

// subscribe gets hello (with now) then snapshot then live events with increasing seq
func TestSubscribeHelloSnapshotLive(t *testing.T) {
	dir := realTempDir(t)
	root := claudeRoot(dir)
	log := writeLog(t, root, toolUseLine(t, "t0", "Read", map[string]any{"file_path": "/project/a.ts"}, "/project"))
	c := New(Options{Host: "mac", Roots: []logs.Root{root}})
	startCollector(t, c)
	rec := &recorder{}
	before := wire.NowMs()
	unsubscribe := c.Subscribe(rec.fn)
	defer unsubscribe()

	envs := rec.all()
	if len(envs) < 2 {
		t.Fatalf("got %d envelopes after Subscribe, want hello and snapshot", len(envs))
	}
	hello, ok := envs[0].Ev.(wire.Hello)
	if !ok || envs[0].V != 1 || envs[0].Host != "mac" || hello.Platform != wire.Platform() {
		t.Fatalf("first envelope = %+v, want a v1 hello from mac on %s", envs[0], wire.Platform())
	}
	if hello.CollectorVersion == "" {
		t.Error("hello has no collectorVersion")
	}
	if line := string(wire.EncodeLine(envs[0])); strings.Contains(line, `"demo"`) {
		t.Errorf("hello line has a demo key: %s", line)
	}
	now, ok := lifecycle.ParseISO(hello.Now)
	if !ok || now < before-1 || now > wire.NowMs() {
		t.Errorf("hello.now = %q, want between %d and now", hello.Now, before)
	}
	snap := snapshotOf(t, envs[1])
	var ids, callIDs []string
	for _, a := range snap.Agents {
		ids = append(ids, a.ID)
	}
	for _, call := range snap.OpenCalls {
		callIDs = append(callIDs, call.CallID)
	}
	if !slices.Equal(ids, []string{"mac/claude:default:sess"}) || !slices.Equal(callIDs, []string{"t0"}) {
		t.Fatalf("snapshot agents = %v, open calls = %v", ids, callIDs)
	}

	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(toolUseLine(t, "t1", "Read", map[string]any{"file_path": "/project/b.ts"}, "/project"))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	rec.until(t, func(envs []wire.Envelope) bool {
		_, i := find(envs, startOf("t1"))
		return i >= 0
	}, colDeadline, "tool.start t1")
	// hello and snapshot carry the current seq without taking a number; live envelopes strictly increase.
	envs = rec.all()
	if envs[1].Seq != envs[0].Seq {
		t.Errorf("snapshot seq %d != hello seq %d", envs[1].Seq, envs[0].Seq)
	}
	for i := 2; i < len(envs); i++ {
		if envs[i].Seq <= envs[i-1].Seq {
			t.Errorf("seq %d at %d does not increase from %d", envs[i].Seq, i, envs[i-1].Seq)
		}
	}
	if last := envs[len(envs)-1].Seq; last <= envs[1].Seq {
		t.Errorf("last seq %d, want > %d", last, envs[1].Seq)
	}
}

// demo collector labels hello demo: true and emits agents within 1 second
func TestDemoCollectorHello(t *testing.T) {
	c, _ := newDemo()
	rec := &recorder{}
	c.Subscribe(rec.fn)
	startCollector(t, c)
	envs := rec.all()
	if hello, ok := envs[0].Ev.(wire.Hello); !ok || !hello.Demo || envs[0].Host != "demo-host" {
		t.Fatalf("first envelope = %+v, want a demo hello from demo-host", envs[0])
	}
	snapshotOf(t, envs[1])
	// Start returns once the demo's first events are delivered: no clock time passes.
	if n := count[wire.AgentUpsert](envs); n < 6 {
		t.Fatalf("got %d agent.upsert when Start returned, want 6", n)
	}
	var agents []wire.Agent
	for _, e := range rec.all() {
		if u, ok := e.Ev.(wire.AgentUpsert); ok {
			agents = append(agents, u.Agent)
		}
	}
	providers := map[string]int{}
	for _, a := range agents {
		providers[a.Provider]++
	}
	if providers["claude"] != 3 || providers["codex"] != 3 {
		t.Errorf("providers = %v, want 3 claude and 3 codex", providers)
	}
	for _, a := range agents {
		if a.Role != "sub-agent" {
			continue
		}
		if a.Link != "confirmed" || a.ParentID == nil ||
			!slices.ContainsFunc(agents, func(p wire.Agent) bool { return p.ID == *a.ParentID }) {
			t.Errorf("sub-agent %s: link %s, parent %v", a.ID, a.Link, a.ParentID)
		}
	}
	// A demo collector runs only the demo source: it reports no roots.
	cov := c.Coverage()
	if len(cov.LogRoots) != 0 || len(cov.WatchRoots) != 0 || cov.LogsOpen != 0 || cov.LogsDiscovered != 0 || cov.FSDropped != 0 {
		t.Errorf("coverage = %+v, want no roots and zero counts", cov)
	}
}

// a late subscriber to a demo collector gets those agents in its snapshot
func TestDemoLateSubscriberSnapshot(t *testing.T) {
	c, _ := newDemo()
	startCollector(t, c)
	rec := &recorder{}
	c.Subscribe(rec.fn)
	if snap := snapshotOf(t, rec.all()[1]); len(snap.Agents) != 6 {
		t.Errorf("snapshot has %d agents, want 6", len(snap.Agents))
	}
}

// Review Focus 3: a write through a symlinked project dir and its fs.change resolve to the same path
func TestSymlinkedWriteAndFsChangeAgree(t *testing.T) {
	dir := realTempDir(t)
	proj := filepath.Join(dir, "real", "proj")
	mkdirs(t, filepath.Join(proj, ".git"))
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(dir, "link", "proj", "a.ts")
	realFile := filepath.Join(proj, "a.ts")
	cwd := filepath.Join(dir, "link", "proj")
	root := claudeRoot(dir)
	writeLog(t, root, toolUseLine(t, "w1", "Write", map[string]any{"file_path": viaLink, "content": "x"}, cwd)+toolResultLine(t, "w1", cwd))

	c := New(Options{Host: "mac", Roots: []logs.Root{root}})
	rec := &recorder{}
	c.Subscribe(rec.fn)
	startCollector(t, c)
	rec.until(t, func(envs []wire.Envelope) bool { _, i := find(envs, endOf("w1")); return i >= 0 }, colDeadline, "tool.end w1")
	// The agent's project root resolves through the symlink, and that root becomes a watch root. The batch
	// with the agent.upsert sets the watch roots before Start returns: no tick is needed.
	if roots := c.Coverage().WatchRoots; !slices.Contains(roots, proj) {
		t.Fatalf("watch roots = %v, want %s", roots, proj)
	}
	up, i := find[wire.AgentUpsert](rec.all(), nil)
	if i < 0 || up.Agent.ProjectRoot == nil || *up.Agent.ProjectRoot != proj {
		t.Fatalf("agent.upsert = %+v, want projectRoot %s", up, proj)
	}

	// A watcher can miss a write made in the first moments after it starts, so write again until one is seen.
	isChange := func(e wire.FsChange) bool { return e.Path == realFile }
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = os.WriteFile(viaLink, []byte("changed "+time.Now().String()), 0o644)
			}
		}
	})
	rec.until(t, func(envs []wire.Envelope) bool { _, i := find(envs, isChange); return i >= 0 }, 10*time.Second, "fs.change")
	close(stop)
	wg.Wait()

	envs := rec.all()
	end, _ := find(envs, endOf("w1"))
	change, _ := find(envs, isChange)
	if len(end.Writes) == 0 || end.Writes[0].Path != change.Path {
		t.Errorf("tool.end writes = %+v, want the fs.change path %s", end.Writes, change.Path)
	}
	if real, err := filepath.EvalSymlinks(viaLink); err != nil || change.Path != real {
		t.Errorf("fs.change path = %s, want %s (%v)", change.Path, real, err)
	}
	st, _ := find(envs, startOf("w1"))
	if !slices.Equal(st.Call.Files, []string{realFile}) {
		t.Errorf("tool.start files = %v, want [%s]", st.Call.Files, realFile)
	}
}

// events keep their order through enrichment
func TestOrderThroughEnrichment(t *testing.T) {
	dir := realTempDir(t)
	proj := filepath.Join(dir, "proj")
	mkdirs(t, filepath.Join(proj, ".git"))
	root := claudeRoot(dir)
	var text strings.Builder
	for i := range 30 {
		id := "c" + strconv.Itoa(i)
		text.WriteString(toolUseLine(t, id, "Read", map[string]any{"file_path": filepath.Join(proj, "f"+strconv.Itoa(i)+".ts")}, proj))
		text.WriteString(toolResultLine(t, id, proj))
	}
	writeLog(t, root, text.String())
	c := New(Options{Host: "mac", Roots: []logs.Root{root}})
	rec := &recorder{}
	c.Subscribe(rec.fn)
	startCollector(t, c)
	// Start returns once the first scan's events are delivered.
	if n := count[wire.ToolEnd](rec.all()); n != 30 {
		t.Fatalf("got %d tool.end when Start returned, want 30", n)
	}
	var order, want []string
	for _, e := range rec.all() {
		switch ev := e.Ev.(type) {
		case wire.ToolStart:
			order = append(order, "s"+ev.Call.CallID)
		case wire.ToolEnd:
			order = append(order, "e"+ev.CallID)
		}
	}
	for i := range 30 {
		want = append(want, "sc"+strconv.Itoa(i), "ec"+strconv.Itoa(i))
	}
	if !slices.Equal(order, want) {
		t.Errorf("order = %v\nwant %v", order, want)
	}
}

// S1: a joining subscriber does not leave a gap in another subscriber's seq
func TestJoiningSubscriberLeavesNoGap(t *testing.T) {
	c, sched := newDemo()
	a := &recorder{}
	c.Subscribe(a.fn)
	startCollector(t, c)
	advanceUntil(t, c, sched, a, func(envs []wire.Envelope) bool { return len(envs) > 6 }, 3000, "events for A")
	b := &recorder{}
	c.Subscribe(b.fn)
	bEnvs := b.all()
	if bEnvs[0].Seq != bEnvs[1].Seq {
		t.Errorf("B hello seq %d != snapshot seq %d", bEnvs[0].Seq, bEnvs[1].Seq)
	}
	advanceUntil(t, c, sched, a, func(envs []wire.Envelope) bool { return len(envs) > 30 }, 8000, "more events for A")
	aEnvs := a.all()
	for i := 3; i < len(aEnvs); i++ {
		if aEnvs[i].Seq != aEnvs[i-1].Seq+1 {
			t.Errorf("A seq %d at %d follows %d", aEnvs[i].Seq, i, aEnvs[i-1].Seq)
		}
	}
	// B's hello and snapshot carry the seq of the last live envelope A had seen when B joined.
	if !slices.ContainsFunc(aEnvs, func(e wire.Envelope) bool { return e.Seq == bEnvs[0].Seq }) {
		t.Errorf("A never saw seq %d", bEnvs[0].Seq)
	}
}

// I1: an open call that times out yields a live tool.end with ok null and no writes
func TestTimedOutCallEnds(t *testing.T) {
	dir := realTempDir(t)
	proj := filepath.Join(dir, "proj")
	mkdirs(t, filepath.Join(proj, ".git"))
	root := claudeRoot(dir)
	writeLog(t, root, toolUseLine(t, "slow1", "Write", map[string]any{"file_path": filepath.Join(proj, "a.ts"), "content": "x"}, proj))
	c := New(Options{Host: "mac", Roots: []logs.Root{root}, Lifecycle: lifecycle.Options{CallTimeoutMs: 300}})
	rec := &recorder{}
	c.Subscribe(rec.fn)
	startCollector(t, c)
	rec.until(t, func(envs []wire.Envelope) bool { _, i := find(envs, endOf("slow1")); return i >= 0 }, colDeadline, "timed-out tool.end")
	envs := rec.all()
	end, endIdx := find(envs, endOf("slow1"))
	if end.Ok != nil {
		t.Errorf("ok = %v, want null", *end.Ok)
	}
	if line := string(wire.EncodeLine(envs[endIdx])); strings.Contains(line, `"writes"`) {
		t.Errorf("tool.end line has writes: %s", line)
	}
	if end.AgentID != "mac/claude:default:sess" {
		t.Errorf("agentId = %s", end.AgentID)
	}
	if _, startIdx := find(envs, startOf("slow1")); startIdx < 0 || endIdx < startIdx {
		t.Errorf("tool.start at %d, tool.end at %d", startIdx, endIdx)
	}
	// The call is closed in the state too: a later subscriber sees no open call.
	late := &recorder{}
	c.Subscribe(late.fn)
	if snap := snapshotOf(t, late.all()[1]); len(snap.OpenCalls) != 0 {
		t.Errorf("open calls = %+v, want none", snap.OpenCalls)
	}
}

// I2: a Write into a not-yet-existing subdirectory of a symlinked project resolves under the real project root
func TestWriteIntoMissingDirResolves(t *testing.T) {
	dir := realTempDir(t)
	proj := filepath.Join(dir, "real", "proj")
	mkdirs(t, filepath.Join(proj, ".git"))
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(dir, "link", "proj", "newdir", "sub", "a.ts")
	want := filepath.Join(proj, "newdir", "sub", "a.ts")
	cwd := filepath.Join(dir, "link", "proj")
	root := claudeRoot(dir)
	// Claude logs tool_use before the tool creates the directory, and the result comes right after.
	writeLog(t, root, toolUseLine(t, "w1", "Write", map[string]any{"file_path": viaLink, "content": "x"}, cwd)+toolResultLine(t, "w1", cwd))
	c := New(Options{Host: "mac", Roots: []logs.Root{root}})
	rec := &recorder{}
	c.Subscribe(rec.fn)
	startCollector(t, c)
	rec.until(t, func(envs []wire.Envelope) bool { _, i := find(envs, endOf("w1")); return i >= 0 }, colDeadline, "tool.end w1")
	envs := rec.all()
	st, _ := find(envs, startOf("w1"))
	end, _ := find(envs, endOf("w1"))
	if !slices.Equal(st.Call.Files, []string{want}) {
		t.Errorf("tool.start files = %v, want [%s]", st.Call.Files, want)
	}
	if len(end.Writes) != 1 || end.Writes[0].Path != want {
		t.Errorf("tool.end writes = %+v, want [%s]", end.Writes, want)
	}
}

// a throwing subscriber does not break the others, and unsubscribe stops delivery
func TestPanickingSubscriberAndUnsubscribe(t *testing.T) {
	c, sched := newDemo()
	good := &recorder{}
	c.Subscribe(func(wire.Envelope) { panic("boom") })
	unsubscribe := c.Subscribe(good.fn)
	startCollector(t, c)
	advanceUntil(t, c, sched, good, func(envs []wire.Envelope) bool { return count[wire.AgentUpsert](envs) > 0 }, 2000, "agent.upsert")
	unsubscribe()
	n := len(good.all())
	sched.Advance(700)
	c.await(nil)
	if got := len(good.all()); got != n {
		t.Errorf("got %d envelopes after unsubscribe, had %d", got, n)
	}
}

// stop is idempotent and nothing is emitted after it resolves
func TestStopIdempotent(t *testing.T) {
	c, sched := newDemo()
	rec := &recorder{}
	c.Subscribe(rec.fn)
	startCollector(t, c)
	advanceUntil(t, c, sched, rec, func(envs []wire.Envelope) bool { return len(envs) > 8 }, 3000, "some events")
	// Leave demo batches in the queue when Stop runs.
	sched.Advance(2000)
	var wg sync.WaitGroup
	wg.Go(c.Stop)
	wg.Go(c.Stop)
	wg.Wait()
	c.Stop()
	n := len(rec.all())
	sched.Advance(900)
	time.Sleep(100 * time.Millisecond)
	if got := len(rec.all()); got != n {
		t.Errorf("got %d envelopes after Stop, had %d", got, n)
	}
}

// a non-demo collector emits coverage with log roots and source status
func TestCoverageWithLogRootsAndStatus(t *testing.T) {
	dir := realTempDir(t)
	root := claudeRoot(dir)
	mkdirs(t, root.Path)
	c := New(Options{Host: "mac", Roots: []logs.Root{root}})
	rec := &recorder{}
	c.Subscribe(rec.fn)
	startCollector(t, c)
	rec.until(t, func(envs []wire.Envelope) bool { return count[wire.CoverageReport](envs) > 0 }, 3*time.Second, "coverage")
	cov, _ := find[wire.CoverageReport](rec.all(), nil)
	if !slices.Equal(cov.Coverage.LogRoots, []string{root.Path}) {
		t.Errorf("logRoots = %v, want [%s]", cov.Coverage.LogRoots, root.Path)
	}
	ok := wire.SourceState{State: "ok"}
	if got := cov.Coverage.Sources["logs:"+root.Path]; got != ok {
		t.Errorf("coverage source = %+v, want ok", got)
	}
	if got := c.Coverage().Sources["logs:"+root.Path]; got != ok {
		t.Errorf("Coverage() source = %+v, want ok", got)
	}
}

// Go only: Stop waits for a subscriber function that is running, and
// nothing is delivered after it returns.
func TestStopWaitsForDelivery(t *testing.T) {
	c, sched := newDemo()
	rec := &recorder{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	c.Subscribe(func(e wire.Envelope) {
		rec.fn(e)
		if _, ok := e.Ev.(wire.AgentUpsert); ok {
			once.Do(func() {
				close(entered)
				<-release
			})
		}
	})
	started := make(chan struct{})
	go func() {
		c.Start()
		close(started)
	}()
	<-entered
	stopped := make(chan struct{})
	go func() {
		c.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		close(release)
		t.Fatal("Stop returned while a subscriber function ran")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for _, ch := range []chan struct{}{stopped, started} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("Stop or Start did not return")
		}
	}
	n := len(rec.all())
	sched.Advance(2000)
	time.Sleep(50 * time.Millisecond)
	if got := len(rec.all()); got != n {
		t.Errorf("got %d envelopes after Stop, had %d", got, n)
	}
}

// Go only: Stop may run while Start is still starting the sources. Both
// return, and nothing is delivered after Stop.
func TestStopDuringStart(t *testing.T) {
	dir := realTempDir(t)
	root := claudeRoot(dir)
	writeLog(t, root, toolUseLine(t, "t0", "Read", map[string]any{"file_path": "/project/a.ts"}, "/project"))
	c := New(Options{Host: "mac", Roots: []logs.Root{root}})
	rec := &recorder{}
	c.Subscribe(rec.fn)
	started := make(chan error, 1)
	go func() { started <- c.Start() }()
	c.Stop()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
	n := len(rec.all())
	time.Sleep(100 * time.Millisecond)
	if got := len(rec.all()); got != n {
		t.Errorf("got %d envelopes after Stop, had %d", got, n)
	}
}

// Go only: realPath follows Node's path.dirname: a trailing slash names the
// last segment once, and ".." resolves through a symlink.
func TestRealPathTrailingSlash(t *testing.T) {
	dir := realTempDir(t)
	mkdirs(t, filepath.Join(dir, "real", "sub"))
	for link, target := range map[string]string{"link": "real", "deep": filepath.Join("real", "sub")} {
		if err := os.Symlink(filepath.Join(dir, target), filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	p := newPaths()
	for in, want := range map[string]string{
		filepath.Join(dir, "link", "sub") + "/":   filepath.Join(dir, "real", "sub"),
		filepath.Join(dir, "link", "new", "x.ts"): filepath.Join(dir, "real", "new", "x.ts"),
		// ".." leaves the symlink's target, not the symlink.
		dir + "/deep/../y.ts": filepath.Join(dir, "real", "y.ts"),
		"relative/a.ts":       "relative/a.ts",
	} {
		if got := p.realPath(in); got != want {
			t.Errorf("realPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// Go only: the path cache drops the least recently used entry.
func TestPathCacheLRU(t *testing.T) {
	c := newPathCache(2)
	c.set("a", "A")
	c.set("b", "B")
	c.get("a")
	c.set("c", "C")
	if _, ok := c.get("b"); ok {
		t.Error("b was not evicted")
	}
	for k, want := range map[string]string{"a": "A", "c": "C"} {
		if got, ok := c.get(k); !ok || got != want {
			t.Errorf("get(%s) = %q, %v", k, got, ok)
		}
	}
}
