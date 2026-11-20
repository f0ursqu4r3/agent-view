package tui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
	return f.batchFrom("devbox", es...)
}

// batchFrom is batch from another host.
func (f *modelFix) batchFrom(host string, es ...schema.Envelope) tea.Cmd {
	f.t.Helper()
	for i := range es {
		f.seq++
		es[i].V, es[i].Host, es[i].Seq = 1, host, f.seq
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
		if ui().Selected != dk(step.want) {
			t.Fatalf("after %q: Selected = %q, want %q", step.key, ui().Selected, step.want)
		}
	}

	f.press("j") // b
	f.press("enter")
	if ui().Focus != dk("b") {
		t.Errorf("enter: Focus = %q, want b", ui().Focus)
	}
	f.press("enter")
	if ui().Focus != dk("") {
		t.Errorf("enter again: Focus = %q, want none", ui().Focus)
	}
	f.press("enter")
	f.press("esc")
	if ui().Focus != dk("") || ui().Selected != dk("b") {
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
	if f.m.ui.Focus != dk("b") {
		t.Fatalf("click on b: Focus = %q", f.m.ui.Focus)
	}
	f.send(tea.MouseClickMsg{X: x + 2, Y: y + 2, Button: tea.MouseLeft})
	if f.m.ui.Focus != dk("") {
		t.Errorf("second click on b: Focus = %q, want none", f.m.ui.Focus)
	}
	f.send(tea.MouseClickMsg{X: 119, Y: 1, Button: tea.MouseLeft})
	if f.m.ui.Focus != dk("") {
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

	if got := f.s.RecentEnded(store.AgentKey{Host: "devbox", ID: "a"}, 1); len(got) != 1 || got[0].CallID != "c1" {
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
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "exec: bun: not found"})
	if first := f.lines()[0]; !strings.Contains(first, "down") {
		t.Errorf("status: %q", first)
	}
	if !strings.Contains(f.screen(), "HOST  devbox down") {
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

// TestHostStateKeyedByRawLabel: the state of a host is stored under the
// label the store uses, so it reaches that host's segment, and the label is
// cleaned only where it is drawn. A label that cleans to nothing is a host,
// not the local collector.
func TestHostStateKeyedByRawLabel(t *testing.T) {
	const raw = "dev\x1b[31mbox"
	f := newModelFix(t, true, 120, 40)
	f.batchFrom(raw, schema.Envelope{Kind: "hello", Now: iso(f.clock)}, emptySnapshot())
	f.send(feed.StateMsg{Host: raw, State: "down", Detail: "ssh: connection refused"})

	segs := f.m.segments()
	if len(segs) != 1 || segs[0].Label != raw || segs[0].State != "down" {
		t.Fatalf("segments = %+v, want the one host %q, down", segs, raw)
	}
	first := f.lines()[0]
	if !strings.Contains(first, "devbox ● down") || strings.Contains(first, "connecting") || strings.Contains(first, "[31m") {
		t.Errorf("status bar: %q", first)
	}
	if !strings.Contains(f.screen(), "HOST  devbox down") {
		t.Errorf("log lacks the down line:\n%s", f.screen())
	}
	if d := f.m.Diagnostics(); len(d) == 0 || d[len(d)-1] != "devbox collector down: ssh: connection refused" {
		t.Errorf("diagnostics: %q", d)
	}

	f.send(feed.StateMsg{Host: raw, State: "up"})
	if segs := f.m.segments(); len(segs) != 1 || segs[0].State != "up" {
		t.Errorf("segments after up = %+v", segs)
	}

	// Another host, and a label that cleans to "": neither touches the first.
	f.send(feed.StateMsg{Host: "box", State: "connecting"})
	f.send(feed.StateMsg{Host: "\x1b[31m", State: "down"})
	got := map[string]string{}
	for _, s := range f.m.segments() {
		got[s.Label] = s.State
	}
	want := map[string]string{raw: "up", "box": "connecting", "\x1b[31m": "down"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("states = %q, want %q", got, want)
	}
}

// TestEmptyHostStateIsTheLocalLabel: the bridge always sets Host. A state
// without one is shown as "local" and does not touch the state of another
// host, not even the first host that the store knows.
func TestEmptyHostStateIsTheLocalLabel(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	f.batchFrom("box", schema.Envelope{Kind: "hello", Now: iso(f.clock)}, emptySnapshot())
	if got := f.s.Hosts(); !reflect.DeepEqual(got, []string{"box"}) {
		t.Fatalf("store hosts = %q, want [box]", got)
	}
	f.send(feed.StateMsg{Host: "box", State: "up"})
	f.send(feed.StateMsg{State: "down", Detail: "x"})

	got := map[string]string{}
	for _, s := range f.m.segments() {
		got[s.Label] = s.State
	}
	if want := map[string]string{"box": "up", "local": "down"}; !reflect.DeepEqual(got, want) {
		t.Errorf("states = %q, want %q", got, want)
	}
	if d := f.m.Diagnostics(); len(d) != 1 || !strings.HasPrefix(d[0], "local collector down") {
		t.Errorf("diagnostics: %q, want one line that starts with %q", d, "local collector down")
	}
}

// countDiag returns how many diagnostics equal line.
func countDiag(f *modelFix, line string) int {
	n := 0
	for _, d := range f.m.Diagnostics() {
		if d == line {
			n++
		}
	}
	return n
}

// TestRepeatedHostStateAddsNoSecondDiagnostic: the bridge sends every host
// state again after a resync. A state with the same detail as the host's last
// one adds no second diagnostic, and the state did not change, so the log
// gets no second HOST line.
func TestRepeatedHostStateAddsNoSecondDiagnostic(t *testing.T) {
	const diag = "devbox collector down: ssh failed: x"
	f := newModelFix(t, true, 120, 40)
	down := feed.StateMsg{Host: "devbox", State: "down", Detail: "ssh failed: x"}
	f.send(down)
	f.send(down)
	if got := countDiag(f, diag); got != 1 || len(f.m.Diagnostics()) != 1 {
		t.Errorf("diagnostics = %q, want the one line %q", f.m.Diagnostics(), diag)
	}
	if got := strings.Count(f.screen(), "HOST  devbox down"); got != 1 || f.m.log.Len() != 1 {
		t.Errorf("%d HOST line(s) on the screen, log of %d; want 1, 1:\n%s", got, f.m.log.Len(), f.screen())
	}
}

// TestChangedDetailAddsADiagnostic: the same state with another detail is a
// new diagnostic. The state did not change, so the log gets no second line.
func TestChangedDetailAddsADiagnostic(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "ssh failed: x"})
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "ssh failed: y"})
	want := []string{"devbox collector down: ssh failed: x", "devbox collector down: ssh failed: y"}
	if got := f.m.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Errorf("diagnostics = %q, want %q", got, want)
	}
	if f.m.log.Len() != 1 {
		t.Errorf("log of %d line(s), want 1", f.m.log.Len())
	}
}

// TestChangedStateSameDetailAddsADiagnostic: another state with the same
// detail is a new diagnostic, since the line names the state.
func TestChangedStateSameDetailAddsADiagnostic(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	f.send(feed.StateMsg{Host: "devbox", State: "connecting", Detail: "retry in 5 s"})
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "retry in 5 s"})
	want := []string{"devbox collector connecting: retry in 5 s", "devbox collector down: retry in 5 s"}
	if got := f.m.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Errorf("diagnostics = %q, want %q", got, want)
	}
}

// TestDownAgainAfterUpAddsADiagnostic: a host that comes up and goes down
// again for the same reason is a new event, so it adds a second diagnostic.
func TestDownAgainAfterUpAddsADiagnostic(t *testing.T) {
	const diag = "devbox collector down: ssh failed: x"
	f := newModelFix(t, true, 120, 40)
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "ssh failed: x"})
	f.send(feed.StateMsg{Host: "devbox", State: "up"})
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "ssh failed: x"})
	if got := countDiag(f, diag); got != 2 || len(f.m.Diagnostics()) != 2 {
		t.Errorf("diagnostics = %q, want %q twice", f.m.Diagnostics(), diag)
	}
	if f.m.log.Len() != 3 {
		t.Errorf("log of %d line(s), want 3 (down, up, down)", f.m.log.Len())
	}
}

// TestHostDetailClearedByAnEmptyDetailThenRepeated: a message with an empty
// detail clears the stored detail, so the first message again is a new event
// and adds a second diagnostic.
func TestHostDetailClearedByAnEmptyDetailThenRepeated(t *testing.T) {
	const diag = "devbox collector down: x"
	f := newModelFix(t, true, 120, 40)
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "x"})
	f.send(feed.StateMsg{Host: "devbox", State: "down"})
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "x"})
	if got := countDiag(f, diag); got != 2 || len(f.m.Diagnostics()) != 2 {
		t.Errorf("diagnostics = %q, want %q twice", f.m.Diagnostics(), diag)
	}
}

// TestHostDetailComparesTheCleanedText: two details that differ only by an
// escape sequence are one text once cleaned, so the second adds no line.
func TestHostDetailComparesTheCleanedText(t *testing.T) {
	const diag = "devbox collector down: x"
	f := newModelFix(t, true, 120, 40)
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "x"})
	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "\x1b[31mx"})
	if got := countDiag(f, diag); got != 1 || len(f.m.Diagnostics()) != 1 {
		t.Errorf("diagnostics = %q, want the one line %q", f.m.Diagnostics(), diag)
	}
}

// TestModelCoveragePanelShowsEveryHost: the panel holds one block per store
// host, in the store's order, each with its own coverage.
func TestModelCoveragePanelShowsEveryHost(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	for _, host := range []string{"zeta", "alpha"} { // not in sorted order
		f.batchFrom(host,
			schema.Envelope{Kind: "hello", Now: iso(f.clock)},
			schema.Envelope{Kind: "coverage", Coverage: &schema.Coverage{LogRoots: []string{"/roots/" + host}}},
		)
	}
	f.press("c")
	var ys []int
	for _, s := range []string{"host zeta", "/roots/zeta", "host alpha", "/roots/alpha"} {
		_, y, ok := f.find(s)
		if !ok {
			t.Fatalf("panel lacks %q:\n%s", s, f.screen())
		}
		ys = append(ys, y)
	}
	if !(ys[0] < ys[1] && ys[1] < ys[2] && ys[2] < ys[3]) {
		t.Errorf("lines of host zeta, its root, host alpha, its root: %v; want increasing:\n%s", ys, f.screen())
	}

	// One host: no host line.
	g := newModelFix(t, true, 120, 40)
	g.batch(schema.Envelope{Kind: "hello", Now: iso(g.clock)}, schema.Envelope{Kind: "coverage", Coverage: &schema.Coverage{LogRoots: []string{"/roots/devbox"}}})
	g.press("c")
	if scr := g.screen(); !strings.Contains(scr, "/roots/devbox") || strings.Contains(scr, "host devbox") {
		t.Errorf("one-host panel:\n%s", scr)
	}
}

// boxHosts sends two hosts, labeled as `--remote box --remote box` labels
// them, each with an agent per id and a read call on <host>.go.
func (f *modelFix) boxHosts(ids map[string]string) {
	f.t.Helper()
	for _, host := range []string{"box", "box#2"} {
		id := ids[host]
		f.batchFrom(host, schema.Envelope{Kind: "hello", Now: iso(f.clock)}, emptySnapshot(),
			agentEnv(id, "", f.clock), startEnv(id, "c-"+host, "read", "/work/repo/"+strings.ReplaceAll(host, "#", "")+".go", f.clock))
		f.send(feed.StateMsg{Host: host, State: "up"})
	}
}

// TestEqualAgentIDsOnTwoHosts: the store keeps the agents of two hosts apart
// by host and ID, so two hosts with the agent ID "session-1" give two rows,
// and the labels box and box#2 show as they are.
func TestEqualAgentIDsOnTwoHosts(t *testing.T) {
	f := newModelFix(t, true, 100, 40)
	f.boxHosts(map[string]string{"box": "session-1", "box#2": "session-1"})

	if got := f.s.Hosts(); !reflect.DeepEqual(got, []string{"box", "box#2"}) {
		t.Fatalf("hosts = %q", got)
	}
	agents := f.s.Agents(f.clock)
	if len(agents) != 2 || agents[0].Host != "box" || agents[1].Host != "box#2" || agents[0].ID != "session-1" || agents[1].ID != "session-1" {
		t.Fatalf("agents = %+v, want session-1 on box and on box#2", agents)
	}
	if got, want := f.s.LaneOrder(f.clock), []store.AgentKey{{Host: "box", ID: "session-1"}, {Host: "box#2", ID: "session-1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lane order = %v, want %v", got, want)
	}
	cards := 0
	for _, l := range f.lines() {
		if strings.Contains(l, "agent session-1") {
			cards++
		}
	}
	if cards != 2 {
		t.Errorf("%d agent cards on the screen, want 2:\n%s", cards, f.screen())
	}
	if first := f.lines()[0]; !strings.Contains(first, "box ● up") || !strings.Contains(first, "box#2 ● up") {
		t.Errorf("status bar: %q", first)
	}
	f.press("c")
	for _, s := range []string{"host box", "host box#2"} {
		if _, _, ok := f.find(s); !ok {
			t.Errorf("coverage panel lacks %q:\n%s", s, f.screen())
		}
	}
	f.press("esc")

	// A row shows the agent of its own host: the store looks up an agent by
	// host and ID, so box#2 has its header and its own call on the board.
	t.Run("rows show their own host", func(t *testing.T) {
		for _, s := range []string{"box#2 ─", "box2.go"} { // the host header, the call of box#2
			if _, _, ok := f.find(s); !ok {
				t.Errorf("board lacks %q:\n%s", s, f.screen())
			}
		}
	})

	// With different IDs, each host has its header on the board.
	g := newModelFix(t, true, 100, 40)
	g.boxHosts(map[string]string{"box": "s1", "box#2": "s2"})
	var headers []string
	for _, l := range g.lines() {
		for _, h := range []string{"box#2 ─", "box ─"} {
			if strings.Contains(l, h) {
				headers = append(headers, h)
			}
		}
	}
	if !reflect.DeepEqual(headers, []string{"box ─", "box#2 ─"}) {
		t.Errorf("host headers on the board = %q:\n%s", headers, g.screen())
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

	// With no live change: once the host has had no history change for 2 s
	// after its hello, so a scan longer than 3 s is counted whole.
	g := newModelFix(t, true, 120, 40)
	hist := func(id string, at int64) []schema.Envelope {
		return []schema.Envelope{startEnv("a", id, "read", "/work/repo/"+id+".go", at), endEnv("a", id, true, at+10)}
	}
	g.batch(append([]schema.Envelope{{Kind: "hello", Now: iso(g.clock)}, agentEnv("a", "", old)}, hist("h1", old)...)...)
	for i, id := range []string{"h2", "h3", "h4"} { // the scan goes on for 4.5 s
		g.clock += 1500
		g.batch(hist(id, old+int64(i+1)*100)...)
		g.send(clockMsg{})
		if strings.Contains(g.screen(), "loaded") {
			t.Fatalf("summary during the scan, %d ms after the hello:\n%s", g.clock-nowMs, g.screen())
		}
	}
	g.clock += 1000
	g.send(clockMsg{})
	if strings.Contains(g.screen(), "loaded") {
		t.Errorf("summary after 1 s of quiet:\n%s", g.screen())
	}
	g.clock += 1000
	g.send(clockMsg{})
	full := "HOST  devbox loaded 1 agents, 4 calls from logs"
	if n, all := strings.Count(g.screen(), full), strings.Count(g.screen(), "loaded"); n != 1 || all != 1 {
		t.Errorf("after 2 s of quiet: %d full summaries, %d in all; want 1:\n%s", n, all, g.screen())
	}
	// Once per host: later history and quiet write no second line.
	g.batch(hist("h5", old+500)...)
	g.clock += 3000
	g.send(clockMsg{})
	if n := strings.Count(g.screen(), "loaded"); n != 1 {
		t.Errorf("%d summaries after more history, want 1:\n%s", n, g.screen())
	}
}

func TestModelStatusCounts(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	f.send(feed.StateMsg{Host: "devbox", State: "up"})
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
	if f.m.ui.Focus != dk("a") || f.m.ui.Selected != dk("a") {
		t.Fatalf("focus %q, selected %q", f.m.ui.Focus, f.m.ui.Selected)
	}
	f.batch(schema.Envelope{Kind: "agent.status", AgentID: "a", Status: "finished"})
	f.clock += store.DefaultFinishedFadeMs + 1000
	f.batch(schema.Envelope{Kind: "agent.status", AgentID: "b", Status: "idle"})
	f.send(clockMsg{})
	if f.m.ui.Focus != dk("") || f.m.ui.Selected != dk("") {
		t.Errorf("after a left: focus %q, selected %q", f.m.ui.Focus, f.m.ui.Selected)
	}
	for _, h := range f.m.hits {
		if h.Key == dk("a") {
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
	if fr := f.m.anim.Frame(dk("a"), 9, 0); !fr.Flash {
		t.Errorf("failed end: no flash")
	}
	f.frames(100)

	// A parallel start older than the shown open call does not reprint it.
	f.batch(startEnv("a", "c2", "read", "/work/repo/new.go", f.clock))
	f.frames(100)
	f.batch(startEnv("a", "c3", "read", "/work/repo/older.go", f.clock-1000))
	if fr := f.m.anim.Frame(dk("a"), 9, 0); fr.CallRows != 7 || f.m.anim.Active() {
		t.Errorf("unchanged shown call reprinted: %+v", fr)
	}

	// A history failure starts nothing.
	f.batch(agentEnv("h", "", nowMs-60_000), startEnv("h", "x", "command", "", nowMs-60_000), endEnv("h", "x", false, nowMs-59_000))
	if fr := f.m.anim.Frame(dk("h"), 1, 0); fr.Flash || fr.CallRows != 7 {
		t.Errorf("history change animated: %+v", fr)
	}

	// A live sub-agent draws its gutter first; an interrupt flashes the
	// state word once and not the call card.
	f.batch(agentEnv("s", "a", f.clock))
	if fr := f.m.anim.Frame(dk("s"), 9, 0); fr.GutterRows >= 9 {
		t.Errorf("new sub-agent: gutter %d, want it drawing", fr.GutterRows)
	}
	f.frames(100)
	f.batch(schema.Envelope{Kind: "agent.status", AgentID: "s", Status: "interrupted"})
	if fr := f.m.anim.Frame(dk("s"), 9, 0); !fr.StopFlash || fr.Flash {
		t.Errorf("interrupted: StopFlash %v, Flash %v; want the state word only", fr.StopFlash, fr.Flash)
	}
	for i := 1; i <= 3; i++ {
		f.send(frameMsg{})
		if fr := f.m.anim.Frame(dk("s"), 9, 0); fr.StopFlash || fr.Flash {
			t.Errorf("interrupted, frame %d: StopFlash %v, Flash %v; want both off", i, fr.StopFlash, fr.Flash)
		}
	}
}

// TestModelFlashesOnlyTheShownCall: a failed end flashes the call card only
// when the card shows the failed call after the batch.
func TestModelFlashesOnlyTheShownCall(t *testing.T) {
	f := newModelFix(t, false, 120, 40)
	f.batch(agentEnv("a", "", f.clock))
	f.frames(100)

	// Parallel calls: the older one fails while the newer one runs.
	f.batch(startEnv("a", "old", "command", "", f.clock))
	f.clock += 100
	f.batch(startEnv("a", "new", "read", "/work/repo/new.go", f.clock))
	f.frames(100)
	f.clock += 100
	f.batch(endEnv("a", "old", false, f.clock))
	if fr := f.m.anim.Frame(dk("a"), 9, 0); fr.Flash || f.m.anim.Active() {
		t.Errorf("an older parallel call failed: the newer card flashed")
	}

	// The shown call fails: its card flashes.
	f.clock += 100
	f.batch(endEnv("a", "new", false, f.clock))
	if fr := f.m.anim.Frame(dk("a"), 9, 0); !fr.Flash {
		t.Errorf("the shown call failed: no flash")
	}
	f.frames(100)

	// The shown call fails in the batch that starts the next call: the new
	// card prints and does not flash.
	f.clock += 100
	f.batch(startEnv("a", "c3", "command", "", f.clock))
	f.frames(100)
	f.clock += 100
	f.batch(endEnv("a", "c3", false, f.clock), startEnv("a", "c4", "read", "/work/repo/c4.go", f.clock))
	if fr := f.m.anim.Frame(dk("a"), 9, 0); fr.Flash || fr.CallRows != 0 {
		t.Errorf("failure and new start in one batch: Flash %v, CallRows %d; want the new card printing, no flash", fr.Flash, fr.CallRows)
	}
}

// TestModelTimeoutEndOfOldCall: the startup scan leaves a call that never
// ended open, and the collector's next tick ends it with ok:null at now. The
// old agent stays off the board, writes no log line, and does not count as
// working: neither while it is still stored nor once it is dormant.
func TestModelTimeoutEndOfOldCall(t *testing.T) {
	for name, age := range map[string]int64{"stored": 15 * 60_000, "dormant": 20 * 3_600_000} {
		t.Run(name, func(t *testing.T) {
			f := newModelFix(t, true, 120, 40)
			f.send(feed.StateMsg{Host: "devbox", State: "up"})
			old := f.clock - age
			f.batch(schema.Envelope{Kind: "hello", Now: iso(f.clock)},
				agentEnv("a", "", old), startEnv("a", "c1", "command", "", old))
			f.batch(agentEnv("b", "", f.clock), startEnv("b", "l1", "read", "/work/repo/x.go", f.clock))
			lines := f.m.log.Len()
			if first := f.lines()[0]; !strings.Contains(first, "1 agent ") || !strings.Contains(first, "1 working") {
				t.Fatalf("status before the end: %q", first)
			}

			f.clock += 1500
			f.send(clockMsg{}) // a sweep: the 20-hour-old agent goes dormant
			f.clock += 1000
			f.batch(schema.Envelope{Kind: "tool.end", AgentID: "a", CallID: "c1"})
			if n := f.m.log.Len(); n != lines {
				t.Errorf("the timeout end wrote %d log line(s):\n%s", n-lines, f.screen())
			}
			if first := f.lines()[0]; !strings.Contains(first, "1 agent ") || !strings.Contains(first, "1 working") {
				t.Errorf("status after the end: %q", first)
			}
			for _, h := range f.m.hits {
				if h.Key == dk("a") {
					t.Errorf("a is on the board:\n%s", f.screen())
				}
			}
			if strings.Contains(f.screen(), "agent a") {
				t.Errorf("a is on the screen:\n%s", f.screen())
			}
		})
	}
}

// TestModelNoColor: with NoColor the frame holds no color code; dim text is
// faint (SGR 2), and bold and inverse stay.
func TestModelNoColor(t *testing.T) {
	sgr := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	params := func(content string) map[string]bool {
		out := map[string]bool{}
		for _, m := range sgr.FindAllStringSubmatch(content, -1) {
			for _, p := range strings.Split(m[1], ";") {
				out[p] = true
			}
		}
		return out
	}
	for _, noColor := range []bool{false, true} {
		f := newModelFix(t, true, 120, 40)
		f.m = NewModel(Config{Store: f.s, Reduced: true, NoColor: noColor, Now: func() time.Time { return time.UnixMilli(f.clock) }})
		f.m.Init()
		f.send(tea.WindowSizeMsg{Width: 120, Height: 40})
		f.batch(agentEnv("a", "", f.clock), startEnv("a", "c1", "command", "", f.clock))
		f.clock += 500
		f.batch(endEnv("a", "c1", false, f.clock))
		p := params(f.m.View().Content)
		if noColor && (p["38"] || p["48"] || !p["2"] || !p["1"] || !p["7"]) {
			t.Errorf("NoColor: SGR parameters %v; want no 38 or 48, and 2, 1, 7", p)
		}
		if !noColor && !p["38"] {
			t.Errorf("color: SGR parameters %v; want 38", p)
		}
	}
}

// describeCalls lists the IDs and outputs of calls.
func describeCalls(calls []*store.ViewCall) []string {
	out := []string{}
	for _, c := range calls {
		out = append(out, fmt.Sprintf("%s %q", c.CallID, c.Output))
	}
	return out
}

// emptySnapshot is the snapshot a fresh collector sends before its scan.
func emptySnapshot() schema.Envelope {
	return schema.Envelope{Kind: "snapshot", Agents: []schema.Agent{}, OpenCalls: []schema.Call{}, RawFiles: []byte("[]")}
}

// TestModelCollectorRestart: a restarted collector sends hello and an empty
// snapshot, then its scan again. The board and the focus stay, nothing
// counts twice, and the replay writes no log line.
func TestModelCollectorRestart(t *testing.T) {
	f := newModelFix(t, true, 120, 40)
	old := f.clock - 60_000
	scan := func() []schema.Envelope {
		return []schema.Envelope{
			agentEnv("a", "", old), agentEnv("b", "", old),
			startEnv("a", "c1", "command", "", old+1000),
			{Kind: "tool.output", AgentID: "a", CallID: "c1", Chunk: "boom\n", Ts: iso(old + 1900)},
			endEnv("a", "c1", false, old+2000),
			startEnv("a", "c2", "read", "/work/repo/open.go", old+3000),
			startEnv("b", "c3", "read", "/work/repo/b.go", old+3000), endEnv("b", "c3", true, old+4000),
		}
	}
	f.send(feed.StateMsg{Host: "devbox", State: "up"})
	f.batch(append([]schema.Envelope{{Kind: "hello", Now: iso(f.clock)}, emptySnapshot()}, scan()...)...)
	f.clock += 3000
	f.send(clockMsg{}) // the summary goes out
	f.press("j")
	f.press("enter")
	if f.m.ui.Focus != dk("a") {
		t.Fatalf("Focus = %q, want a", f.m.ui.Focus)
	}
	counters := map[string]store.Counters{"a": f.s.Counters(store.AgentKey{Host: "devbox", ID: "a"}), "b": f.s.Counters(store.AgentKey{Host: "devbox", ID: "b"})}
	agents, calls := f.s.HostStats("devbox")

	f.send(feed.StateMsg{Host: "devbox", State: "down", Detail: "exit status 1"})
	f.clock += 1000
	f.send(feed.StateMsg{Host: "devbox", State: "up"})
	lines := f.m.log.Len()
	screen := f.screen() // the clock stands still from here
	f.batch(schema.Envelope{Kind: "hello", Now: iso(f.clock)}, emptySnapshot())
	check := func(when string) {
		t.Helper()
		if f.m.ui.Focus != dk("a") || f.m.ui.Selected != dk("a") {
			t.Errorf("%s: Focus %q, Selected %q; want a", when, f.m.ui.Focus, f.m.ui.Selected)
		}
		if got := f.screen(); got != screen {
			t.Errorf("%s: the screen changed:\n%s\nwant\n%s", when, got, screen)
		}
		if n := f.m.log.Len(); n != lines {
			t.Errorf("%s: %d new log line(s)", when, n-lines)
		}
		for id, want := range counters {
			if c := f.s.Counters(store.AgentKey{Host: "devbox", ID: id}); c != want {
				t.Errorf("%s: Counters(%s) = %+v, want %+v", when, id, c, want)
			}
		}
		if a, n := f.s.HostStats("devbox"); a != agents || n != calls {
			t.Errorf("%s: HostStats = %d agents, %d calls; want %d, %d", when, a, n, agents, calls)
		}
		if got := f.s.RecentEnded(store.AgentKey{Host: "devbox", ID: "a"}, 5); len(got) != 1 || got[0].Output != "boom\n" {
			t.Errorf("%s: ended calls of a = %q; want c1 with output %q", when, describeCalls(got), "boom\n")
		}
	}
	check("after the empty snapshot")
	f.batch(scan()...)
	check("after the replay")
	if n := strings.Count(f.screen(), "loaded 2 agents, 3 calls from logs"); n != 1 {
		t.Errorf("%d summary lines, want 1:\n%s", n, f.screen())
	}
}

// TestModelHistorySettlesRowHeight checks that a batch with only history
// changes for an agent draws its row at the new height at once, while a
// live change in the same batch still animates.
func TestModelHistorySettlesRowHeight(t *testing.T) {
	f := newModelFix(t, false, 120, 40)
	old := f.clock - 30_000
	f.batch(schema.Envelope{Kind: "hello", Now: iso(f.clock)},
		agentEnv("a", "", old), agentEnv("b", "", old), agentEnv("c", "", old))
	f.frames(100)
	for _, id := range []string{"a", "b", "c"} {
		if !strings.Contains(f.screen(), "▸ agent "+id) {
			t.Fatalf("%s not collapsed:\n%s", id, f.screen())
		}
	}

	// A history start (open call) expands a's row, and a history start and
	// end in the last 60 s expands b's row, with no animation.
	f.batch(startEnv("a", "h1", "read", "/work/repo/a.go", old+1000))
	f.batch(startEnv("b", "h2", "read", "/work/repo/b.go", old+1000), endEnv("b", "h2", true, old+15_000))
	if f.m.frameOn || f.m.anim.Active() || f.m.log.Len() != 0 {
		t.Errorf("history animated: frameOn %v, active %v, log %d", f.m.frameOn, f.m.anim.Active(), f.m.log.Len())
	}
	for _, id := range []string{"a", "b"} {
		if h := hitFor(t, f.m.hits, id); h.H != 7 {
			t.Errorf("%s card shows %d of 7 lines:\n%s", id, h.H, f.screen())
		}
	}

	// A live start and a history start for c: the live change animates.
	f.batch(startEnv("c", "h3", "read", "/work/repo/old.go", old+1000), startEnv("c", "l1", "edit", "/work/repo/live.go", f.clock))
	if !f.m.frameOn || !f.m.anim.Active() {
		t.Fatalf("mixed batch did not animate")
	}
	if fr := f.m.anim.Frame(dk("c"), 9, 0); fr.RowH >= 9 || fr.CallRows != 0 {
		t.Errorf("mixed batch: %+v; want the row growing and the card printing", fr)
	}
}

// logLines returns the lines of the log box at the bottom of the screen.
func (f *modelFix) logLines() string {
	l := f.lines()
	lh := logHeight(len(l), f.m.ui.TallLog)
	return strings.Join(l[len(l)-1-lh:len(l)-1], "\n")
}

// TestSelectionAndFocusFollowTheHost: two hosts run an agent with the same
// ID. j, k, enter, esc and a click select and focus one of the two rows, and
// the log focus keeps the lines of that host only. A third agent after the
// twins shows that j moves on from the second twin, not from the first.
func TestSelectionAndFocusFollowTheHost(t *testing.T) {
	f := newModelFix(t, true, 100, 40)
	f.boxHosts(map[string]string{"box": "session-1", "box#2": "session-1"})
	one := store.AgentKey{Host: "box", ID: "session-1"}
	two := store.AgentKey{Host: "box#2", ID: "session-1"}
	three := store.AgentKey{Host: "box#2", ID: "session-2"}
	f.clock += 500
	f.batchFrom("box", endEnv("session-1", "c-box", true, f.clock))
	f.batchFrom("box#2", endEnv("session-1", "c-box#2", true, f.clock), agentEnv("session-2", "", f.clock))
	if got, want := f.s.LaneOrder(f.clock), []store.AgentKey{one, two, three}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lane order = %v, want %v", got, want)
	}
	if lg := f.logLines(); !strings.Contains(lg, "box.go") || !strings.Contains(lg, "box2.go") {
		t.Fatalf("unfocused log lacks a host's line:\n%s", lg)
	}

	for _, step := range []struct {
		key  string
		want store.AgentKey
	}{{"j", one}, {"j", two}, {"j", three}, {"k", two}} {
		f.press(step.key)
		if f.m.ui.Selected != step.want {
			t.Fatalf("after %q: Selected = %v, want %v", step.key, f.m.ui.Selected, step.want)
		}
	}
	f.press("enter")
	if f.m.ui.Focus != two {
		t.Fatalf("enter: Focus = %v, want %v", f.m.ui.Focus, two)
	}
	if lg := f.logLines(); strings.Contains(lg, "box.go") || !strings.Contains(lg, "box2.go") {
		t.Errorf("focus on %v: log wrong:\n%s", two, lg)
	}
	f.press("esc")
	if f.m.ui.Focus != (store.AgentKey{}) || f.m.ui.Selected != two {
		t.Fatalf("esc: Focus %v, Selected %v; want none and %v", f.m.ui.Focus, f.m.ui.Selected, two)
	}
	if lg := f.logLines(); !strings.Contains(lg, "box.go") || !strings.Contains(lg, "box2.go") {
		t.Errorf("esc: log lacks a host's line:\n%s", lg)
	}

	f.press("k") // select the first twin, so the click must move the selection
	var hit *Hit
	for i := range f.m.hits {
		if f.m.hits[i].Key == two {
			hit = &f.m.hits[i]
		}
	}
	if hit == nil {
		t.Fatalf("no hit for %v: %+v", two, f.m.hits)
	}
	f.send(tea.MouseClickMsg{X: hit.X, Y: hit.Y, Button: tea.MouseLeft})
	if f.m.ui.Focus != two || f.m.ui.Selected != two {
		t.Fatalf("click on %v: Focus %v, Selected %v", two, f.m.ui.Focus, f.m.ui.Selected)
	}
	if lg := f.logLines(); strings.Contains(lg, "box.go") || !strings.Contains(lg, "box2.go") {
		t.Errorf("click focus on %v: log wrong:\n%s", two, lg)
	}
}

// TestPruneForgetsOnlyTheHostThatLeft: the agent of box leaves the board
// while its twin on box#2 stays. The model forgets the agent of box only, and
// the focus and the selection on the twin stay.
func TestPruneForgetsOnlyTheHostThatLeft(t *testing.T) {
	f := newModelFix(t, true, 100, 40)
	f.boxHosts(map[string]string{"box": "session-1", "box#2": "session-1"})
	one := store.AgentKey{Host: "box", ID: "session-1"}
	two := store.AgentKey{Host: "box#2", ID: "session-1"}
	f.batchFrom("box", endEnv("session-1", "c-box", true, f.clock))
	f.press("j")
	f.press("j")
	f.press("enter")
	if f.m.ui.Focus != two || !f.m.known[one] || !f.m.known[two] {
		t.Fatalf("before: Focus %v, known %v", f.m.ui.Focus, f.m.known)
	}

	f.batchFrom("box", schema.Envelope{Kind: "agent.status", AgentID: "session-1", Status: "finished"})
	f.clock += store.DefaultFinishedFadeMs + 1000
	f.batchFrom("box#2", schema.Envelope{Kind: "agent.status", AgentID: "session-1", Status: "working"})
	f.send(clockMsg{})
	if got, want := f.s.LaneOrder(f.clock), []store.AgentKey{two}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lane order = %v, want %v", got, want)
	}
	if f.m.known[one] || !f.m.known[two] {
		t.Errorf("known = %v, want %v only", f.m.known, two)
	}
	if f.m.ui.Focus != two || f.m.ui.Selected != two {
		t.Errorf("after box left: Focus %v, Selected %v; want %v", f.m.ui.Focus, f.m.ui.Selected, two)
	}
}
