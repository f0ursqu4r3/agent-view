package tui

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"agent-view/internal/board"
	"agent-view/internal/feed"
	"agent-view/internal/schema"
	"agent-view/internal/store"
)

// modelFix drives a Model on a fake clock that its store shares.
type modelFix struct {
	t     *testing.T
	m     *Model
	s     *store.Store
	clock int64
	seq   int64
}

// newModelFix returns a model after Init and, when w > 0, a w × h size.
func newModelFix(t *testing.T, reduced bool, w, h int) *modelFix {
	t.Helper()
	f := &modelFix{t: t, clock: nowMs}
	f.s = store.New(store.Options{Now: func() int64 { return f.clock }})
	t.Cleanup(func() {
		if f.s.Panics != 0 {
			t.Errorf("Apply recovered %d panic(s)", f.s.Panics)
		}
	})
	f.m = NewModel(Config{Store: f.s, Reduced: reduced, Now: func() time.Time { return time.UnixMilli(f.clock) }})
	f.m.Init()
	if w > 0 {
		f.send(tea.WindowSizeMsg{Width: w, Height: h})
	}
	return f
}

func (f *modelFix) send(msg tea.Msg) tea.Cmd {
	f.t.Helper()
	next, cmd := f.m.Update(msg)
	if next != tea.Model(f.m) {
		f.t.Fatalf("Update returned another model")
	}
	return cmd
}

// batch sends envelopes as one EnvelopesMsg from host "devbox", stamped
// with the clock time unless they carry a time.
func (f *modelFix) batch(es ...schema.Envelope) tea.Cmd {
	f.t.Helper()
	for i := range es {
		f.seq++
		es[i].V, es[i].Host, es[i].Seq = 1, "devbox", f.seq
		if es[i].Ts == "" {
			es[i].Ts = iso(f.clock)
		}
	}
	return f.send(feed.EnvelopesMsg(es))
}

// frames sends frame ticks until the animations end, at most n.
func (f *modelFix) frames(n int) {
	f.t.Helper()
	for i := 0; i < n && f.m.frameOn; i++ {
		f.send(frameMsg{})
	}
}

func agentEnv(id, parent string, at int64) schema.Envelope {
	a := schema.Agent{
		ID: id, Provider: "claude", Name: "agent " + id, Role: "main", Link: "root",
		Cwd: ptr("/work/repo"), ProjectRoot: ptr("/work/repo"), Status: "working", LastEventAt: iso(at),
	}
	if parent != "" {
		a.ParentID, a.Role, a.Link = ptr(parent), "sub-agent", "confirmed"
	}
	return schema.Envelope{Kind: "agent.upsert", Agent: &a, Ts: iso(at)}
}

func startEnv(agentID, callID, cat, file string, at int64) schema.Envelope {
	c := schema.Call{AgentID: agentID, CallID: callID, StartedAt: iso(at), Category: cat, RawName: "Tool", Files: []string{file}}
	return schema.Envelope{Kind: "tool.start", Call: &c, Ts: iso(at)}
}

func endEnv(agentID, callID string, ok bool, at int64) schema.Envelope {
	return schema.Envelope{Kind: "tool.end", AgentID: agentID, CallID: callID, Ok: ptr(ok), Ts: iso(at)}
}

func (f *modelFix) screen() string { return ansi.Strip(f.m.View().Content) }

func (f *modelFix) lines() []string { return strings.Split(f.screen(), "\n") }

// find returns the cell column and the line of the first s on the screen.
func (f *modelFix) find(s string) (x, y int, ok bool) {
	for y, line := range f.lines() {
		if i := strings.Index(line, s); i >= 0 {
			return width(line[:i]), y, true
		}
	}
	return 0, 0, false
}

// keyMsg returns the key press whose String is k.
func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+z":
		return tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: unicode.ToLower(r), Text: k}
}

// press sends a key and returns its command.
func (f *modelFix) press(k string) tea.Cmd {
	f.t.Helper()
	msg := keyMsg(k)
	if msg.String() != k {
		f.t.Fatalf("keyMsg(%q).String() = %q", k, msg.String())
	}
	return f.send(msg)
}

// msgsOf runs a command, and the commands of a batch, and returns the type
// names of their messages. A command still running after 400 ms is listed
// as "pending".
func msgsOf(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(400 * time.Millisecond):
		return []string{"pending"}
	}
	b, ok := msg.(tea.BatchMsg)
	if !ok {
		return []string{reflect.TypeOf(msg).String()}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var out []string
	for _, c := range b {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := msgsOf(c)
			mu.Lock()
			out = append(out, got...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestModelAppliesBatches(t *testing.T) {
	f := newModelFix(t, false, 160, 80)
	fh, err := os.Open(filepath.Join("..", "..", "testdata", "feed", "demo.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		e, ok := schema.DecodeLine(sc.Bytes())
		if !ok {
			t.Fatalf("bad line: %s", sc.Text())
		}
		ts, err := time.Parse(time.RFC3339Nano, e.Ts)
		if err != nil {
			t.Fatal(err)
		}
		f.clock = ts.UnixMilli()
		f.send(feed.EnvelopesMsg{e})
		f.frames(3)
	}
	f.frames(100)
	if f.m.frameOn {
		t.Fatalf("animations still running after 100 frames")
	}
	v := f.m.View()
	if !v.AltScreen || v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("view: AltScreen %v, MouseMode %v", v.AltScreen, v.MouseMode)
	}
	scr := f.screen()
	for _, name := range []string{"Refactor draw", "Review drawer", "Update drawer", "Build workspa", "Index source ", "Check ranking"} {
		if !strings.Contains(scr, name) {
			t.Errorf("screen lacks %q:\n%s", name, scr)
		}
	}
	if first := f.lines()[0]; !strings.Contains(first, "demo") || !strings.Contains(first, "6 agents") {
		t.Errorf("status bar: %q", first)
	}
}

func TestModelKeys(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	f.batch(agentEnv("a", "", f.clock), agentEnv("b", "", f.clock), agentEnv("c", "", f.clock))
	ui := func() board.UI { return f.m.ui }

	for _, step := range []struct{ key, want string }{
		{"j", "a"}, {"j", "b"}, {"down", "c"}, {"j", "c"}, {"k", "b"}, {"up", "a"}, {"k", "a"}, {"G", "c"}, {"g", "a"},
	} {
		f.press(step.key)
		if ui().Selected != step.want {
			t.Fatalf("after %q: Selected = %q, want %q", step.key, ui().Selected, step.want)
		}
	}

	f.press("j") // b
	f.press("enter")
	if ui().Focus != "b" {
		t.Errorf("enter: Focus = %q, want b", ui().Focus)
	}
	f.press("enter")
	if ui().Focus != "" {
		t.Errorf("enter again: Focus = %q, want none", ui().Focus)
	}
	f.press("enter")
	f.press("esc")
	if ui().Focus != "" || ui().Selected != "b" {
		t.Errorf("esc: Focus %q, Selected %q", ui().Focus, ui().Selected)
	}

	f.press("space")
	if !ui().Paused || !strings.Contains(f.lines()[0], "PAUSED") {
		t.Errorf("space: paused %v, status %q", ui().Paused, f.lines()[0])
	}
	f.press("space")
	if ui().Paused || strings.Contains(f.lines()[0], "PAUSED") {
		t.Errorf("space again: paused %v, status %q", ui().Paused, f.lines()[0])
	}

	f.press("l")
	if !ui().TallLog {
		t.Errorf("l: no tall log")
	}
	f.press("l")
	f.press("c")
	if ui().Panel != board.PanelCoverage || !strings.Contains(f.screen(), "coverage") {
		t.Errorf("c: panel %v", ui().Panel)
	}
	f.press("c")
	if ui().Panel != board.PanelNone {
		t.Errorf("c again: panel %v", ui().Panel)
	}
	f.press("?")
	if ui().Panel != board.PanelHelp {
		t.Errorf("?: panel %v", ui().Panel)
	}
	f.press("esc")
	if ui().Panel != board.PanelNone {
		t.Errorf("esc: panel %v", ui().Panel)
	}

	for k, want := range map[string]string{"q": "tea.QuitMsg", "ctrl+c": "tea.QuitMsg", "ctrl+z": "tea.SuspendMsg"} {
		if got := msgsOf(f.press(k)); len(got) != 1 || got[0] != want {
			t.Errorf("%s: messages %v, want [%s]", k, got, want)
		}
	}
}

func TestModelMouseFocus(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	f.batch(agentEnv("a", "", f.clock), agentEnv("b", "", f.clock), startEnv("b", "c1", "read", "/work/repo/x.go", f.clock))
	x, y, ok := f.find("agent b")
	if !ok {
		t.Fatalf("no card for b:\n%s", f.screen())
	}
	f.send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if f.m.ui.Focus != "b" {
		t.Fatalf("click on b: Focus = %q", f.m.ui.Focus)
	}
	f.send(tea.MouseClickMsg{X: x + 2, Y: y + 2, Button: tea.MouseLeft})
	if f.m.ui.Focus != "" {
		t.Errorf("second click on b: Focus = %q, want none", f.m.ui.Focus)
	}
	f.send(tea.MouseClickMsg{X: 119, Y: 1, Button: tea.MouseLeft})
	if f.m.ui.Focus != "" {
		t.Errorf("click on empty space: Focus = %q", f.m.ui.Focus)
	}
}

func TestModelResizeMidAnimation(t *testing.T) {
	f := newModelFix(t, false, 0, 0)
	f.batch(agentEnv("a", "", f.clock)) // 0 × 0 before the first size
	f.frames(100)
	if v := f.m.View(); v.Content != "" {
		t.Errorf("view before a size: %q", v.Content)
	}
	f.send(tea.WindowSizeMsg{Width: 120, Height: 40})
	f.batch(startEnv("a", "c1", "edit", "/work/repo/src/resize.go", f.clock))
	f.send(frameMsg{})
	if !f.m.frameOn {
		t.Fatalf("no frame tick after a call start")
	}
	f.send(tea.WindowSizeMsg{Width: 79, Height: 23})
	if scr := f.screen(); !strings.Contains(scr, "too small") || len(f.lines()) != 23 {
		t.Errorf("79×23:\n%s", scr)
	}
	f.send(frameMsg{})
	f.send(tea.WindowSizeMsg{Width: 120, Height: 40})
	f.frames(100)
	scr := f.screen()
	if strings.Contains(scr, "too small") || !strings.Contains(scr, "agent a") || !strings.Contains(scr, "resize.go") {
		t.Errorf("120×40 after resize:\n%s", scr)
	}
	if n := len(f.lines()); n != 40 {
		t.Errorf("%d lines, want 40", n)
	}
}

func TestModelTicksStop(t *testing.T) {
	f := newModelFix(t, false, 120, 40)
	f.batch(agentEnv("a", "", f.clock))
	f.frames(100)
	cmd := f.batch(startEnv("a", "c1", "command", "", f.clock))
	if got := msgsOf(cmd); !has(got, "tui.frameMsg") || !has(got, "tui.spinMsg") {
		t.Errorf("call start: ticks %v, want frame and spin", got)
	}
	if got := msgsOf(f.send(spinMsg{})); len(got) != 1 || got[0] != "tui.spinMsg" {
		t.Errorf("spin with an open call: %v", got)
	}

	f.clock += 1500
	f.batch(endEnv("a", "c1", true, f.clock))
	f.frames(100)
	if f.m.frameOn {
		t.Fatal("frames still running")
	}
	if cmd := f.send(frameMsg{}); cmd != nil {
		t.Errorf("frame with nothing animating: %v", msgsOf(cmd))
	}
	if cmd := f.send(spinMsg{}); cmd != nil {
		t.Errorf("spin with no open call: %v", msgsOf(cmd))
	}
	// The clock keeps its own tick, aligned to the wall-clock second.
	if got := msgsOf(f.send(clockMsg{})); len(got) != 1 || got[0] != "pending" && got[0] != "tui.clockMsg" {
		t.Errorf("clock tick: %v, want only the clock", got)
	}
	// A new batch restarts the stopped ticks.
	if got := msgsOf(f.batch(startEnv("a", "c2", "read", "/work/repo/x.go", f.clock))); !has(got, "tui.frameMsg") || !has(got, "tui.spinMsg") {
		t.Errorf("restart: %v", got)
	}
}

func TestModelPauseKeepsStore(t *testing.T) {
	f := newModelFix(t, false, 120, 40)
	f.batch(agentEnv("a", "", f.clock))
	f.frames(100)
	f.press("space")
	lines := f.m.log.Len()
	f.clock += 200
	f.batch(startEnv("a", "c1", "edit", "/work/repo/src/paused.go", f.clock))
	f.clock += 300
	f.batch(endEnv("a", "c1", false, f.clock))
	f.send(clockMsg{})

	if got := f.s.RecentEnded("a", 1); len(got) != 1 || got[0].CallID != "c1" {
		t.Fatalf("store lacks the call: %+v", got)
	}
	if f.m.log.Len() != lines {
		t.Errorf("log grew while paused: %d → %d", lines, f.m.log.Len())
	}
	if f.m.anim.Active() || f.m.frameOn {
		t.Errorf("animation started while paused")
	}
	if scr := f.screen(); strings.Contains(scr, "paused.go") || !strings.Contains(scr, "PAUSED") {
		t.Errorf("paused board changed:\n%s", scr)
	}

	f.press("space")
	if scr := f.screen(); !strings.Contains(scr, "paused.go") || strings.Contains(scr, "PAUSED") {
		t.Errorf("resumed board lacks the call:\n%s", scr)
	}
	if f.m.log.Len() != lines || f.m.anim.Active() {
		t.Errorf("resume replayed: log %d → %d, anim active %v", lines, f.m.log.Len(), f.m.anim.Active())
	}
}

func TestModelFeedDown(t *testing.T) {
	f := newModelFix(t, false, 120, 40)
	if first := f.lines()[0]; !strings.Contains(first, "connecting") {
		t.Errorf("status before a state: %q", first)
	}
	f.send(feed.StateMsg{State: "down", Detail: "exec: bun: not found"})
	if first := f.lines()[0]; !strings.Contains(first, "down") {
		t.Errorf("status: %q", first)
	}
	if !strings.Contains(f.screen(), "HOST  local down") {
		t.Errorf("log lacks the down line:\n%s", f.screen())
	}
	f.press("c")
	if scr := f.screen(); !strings.Contains(scr, "exec: bun: not found") {
		t.Errorf("coverage panel lacks the detail:\n%s", scr)
	}
	if got := msgsOf(f.press("q")); len(got) != 1 || got[0] != "tea.QuitMsg" {
		t.Errorf("q: %v", got)
	}
	if d := f.m.Diagnostics(); len(d) == 0 || !strings.Contains(d[len(d)-1], "exec: bun: not found") {
		t.Errorf("diagnostics: %q", d)
	}
}

func TestModelDiagnosticsRing(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	for i := range 250 {
		f.send(feed.DiagMsg("line\x1b[31m " + string(rune('a'+i%26))))
	}
	d := f.m.Diagnostics()
	if len(d) != 200 {
		t.Fatalf("%d diagnostics, want 200", len(d))
	}
	if d[0] != "line "+string(rune('a'+50%26)) || strings.ContainsRune(strings.Join(d, ""), '\x1b') {
		t.Errorf("first kept %q; escapes must be cleaned", d[0])
	}
}

func TestModelHostSummary(t *testing.T) {
	summary := "HOST  devbox loaded 1 agents, 2 calls from logs"
	old := nowMs - 60_000

	// At the first live change, before its own line.
	f := newModelFix(t, true, 120, 40)
	f.batch(
		schema.Envelope{Kind: "hello", Now: iso(f.clock)},
		agentEnv("a", "", old), startEnv("a", "h1", "read", "/work/repo/h1.go", old), endEnv("a", "h1", true, old+10),
		startEnv("a", "h2", "read", "/work/repo/h2.go", old+20), endEnv("a", "h2", true, old+30),
	)
	if f.m.log.Len() != 0 {
		t.Fatalf("history wrote %d log line(s)", f.m.log.Len())
	}
	f.batch(startEnv("a", "c1", "read", "/work/repo/live.go", f.clock))
	f.clock += 100
	f.batch(endEnv("a", "c1", true, f.clock))
	scr := f.screen()
	if strings.Count(scr, summary) != 1 || !strings.Contains(scr, "live.go") {
		t.Errorf("want one summary line:\n%s", scr)
	}
	_, ys, _ := f.find(summary)
	for y, line := range f.lines() {
		if strings.Contains(line, "READ") && strings.Contains(line, "live.go") && y < ys {
			t.Errorf("summary below the live line:\n%s", scr)
		}
	}

	// 3 s after the hello, with no live change.
	g := newModelFix(t, true, 120, 40)
	g.batch(
		schema.Envelope{Kind: "hello", Now: iso(g.clock)},
		agentEnv("a", "", old), startEnv("a", "h1", "read", "/work/repo/h1.go", old), endEnv("a", "h1", true, old+10),
		startEnv("a", "h2", "read", "/work/repo/h2.go", old+20), endEnv("a", "h2", true, old+30),
	)
	g.clock += 2000
	g.send(clockMsg{})
	if strings.Contains(g.screen(), "loaded") {
		t.Errorf("summary before 3 s")
	}
	g.clock += 1000
	g.send(clockMsg{})
	g.clock += 1000
	g.send(clockMsg{})
	if n := strings.Count(g.screen(), summary); n != 1 {
		t.Errorf("summary %d times after 3 s:\n%s", n, g.screen())
	}
}

func TestModelStatusCounts(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	f.send(feed.StateMsg{State: "up"})
	f.batch(agentEnv("a", "", f.clock), agentEnv("b", "", f.clock))
	f.batch(startEnv("a", "c1", "command", "", f.clock), startEnv("b", "c2", "read", "/work/repo/x.go", f.clock))
	f.clock += 1000
	f.batch(endEnv("a", "c1", false, f.clock))
	first := f.lines()[0]
	for _, want := range []string{"devbox ● up", "2 agents", "2 working", "2 calls/min", "1 fault", "01:00:01"} {
		if !strings.Contains(first, want) {
			t.Errorf("status lacks %q: %q", want, first)
		}
	}
	f.batch(schema.Envelope{Kind: "coverage", Coverage: &schema.Coverage{Sources: map[string]schema.SourceState{
		"claude": {State: "ok"}, "codex": {State: "degraded"}, "fs": {State: "down"},
	}}})
	if first := f.lines()[0]; !strings.Contains(first, "2 sources degraded") {
		t.Errorf("degraded status: %q", first)
	}
	if !strings.Contains(f.screen(), "HOST  devbox 2 sources degraded") {
		t.Errorf("log lacks the degraded line:\n%s", f.screen())
	}

	f.clock += 61_000
	f.send(clockMsg{})
	if first := f.lines()[0]; !strings.Contains(first, "0 calls/min") || !strings.Contains(first, "1 fault") {
		t.Errorf("after 61 s: %q", first)
	}
	f.clock += store.DefaultLingerMs
	f.send(clockMsg{})
	if first := f.lines()[0]; !strings.Contains(first, "0 faults") {
		t.Errorf("after the linger: %q", first)
	}
}

func TestModelAgentLeavesBoard(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	f.batch(agentEnv("a", "", f.clock), agentEnv("b", "", f.clock))
	f.press("j")
	f.press("enter")
	if f.m.ui.Focus != "a" || f.m.ui.Selected != "a" {
		t.Fatalf("focus %q, selected %q", f.m.ui.Focus, f.m.ui.Selected)
	}
	f.batch(schema.Envelope{Kind: "agent.status", AgentID: "a", Status: "finished"})
	f.clock += store.DefaultFinishedFadeMs + 1000
	f.batch(schema.Envelope{Kind: "agent.status", AgentID: "b", Status: "idle"})
	f.send(clockMsg{})
	if f.m.ui.Focus != "" || f.m.ui.Selected != "" {
		t.Errorf("after a left: focus %q, selected %q", f.m.ui.Focus, f.m.ui.Selected)
	}
	for _, h := range f.m.hits {
		if h.AgentID == "a" {
			t.Errorf("a still on the board:\n%s", f.screen())
		}
	}
}

func TestModelTouchesReachLog(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	f.batch(agentEnv("a", "", f.clock))
	f.batch(schema.Envelope{Kind: "fs.change", Path: "/work/repo/src/a.go"})
	f.clock += 1500
	f.batch(schema.Envelope{Kind: "fs.change", Path: "/work/repo/src/b.go"})
	f.clock += 600
	f.send(clockMsg{}) // releases a.go
	f.clock += 1500
	f.send(clockMsg{}) // releases b.go into the same line
	scr := f.screen()
	if strings.Count(scr, "FS  ") != 1 || !strings.Contains(scr, "2 change(s) in /work/repo/src/") {
		t.Errorf("want one FS line with 2 changes:\n%s", scr)
	}
}

func TestModelRoutesAnimations(t *testing.T) {
	f := newModelFix(t, false, 120, 40)
	f.batch(agentEnv("a", "", f.clock))
	f.frames(100)

	// A live failed end flashes the call card.
	f.batch(startEnv("a", "c1", "command", "", f.clock))
	f.frames(100)
	f.clock += 500
	f.batch(endEnv("a", "c1", false, f.clock))
	if fr := f.m.anim.Frame("a", 9, 0); !fr.Flash {
		t.Errorf("failed end: no flash")
	}
	f.frames(100)

	// A parallel start older than the shown open call does not reprint it.
	f.batch(startEnv("a", "c2", "read", "/work/repo/new.go", f.clock))
	f.frames(100)
	f.batch(startEnv("a", "c3", "read", "/work/repo/older.go", f.clock-1000))
	if fr := f.m.anim.Frame("a", 9, 0); fr.CallRows != 7 || f.m.anim.Active() {
		t.Errorf("unchanged shown call reprinted: %+v", fr)
	}

	// A history failure starts nothing.
	f.batch(agentEnv("h", "", nowMs-60_000), startEnv("h", "x", "command", "", nowMs-60_000), endEnv("h", "x", false, nowMs-59_000))
	if fr := f.m.anim.Frame("h", 1, 0); fr.Flash || fr.CallRows != 7 {
		t.Errorf("history change animated: %+v", fr)
	}

	// A live sub-agent draws its gutter first; an interrupt flashes.
	f.batch(agentEnv("s", "a", f.clock))
	if fr := f.m.anim.Frame("s", 9, 0); fr.GutterRows >= 9 {
		t.Errorf("new sub-agent: gutter %d, want it drawing", fr.GutterRows)
	}
	f.frames(100)
	f.batch(schema.Envelope{Kind: "agent.status", AgentID: "s", Status: "interrupted"})
	if fr := f.m.anim.Frame("s", 9, 0); !fr.Flash {
		t.Errorf("interrupted: no flash")
	}
}
