package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"agent-view/internal/bridge"
	"agent-view/internal/cards"
	"agent-view/internal/collect"
	"agent-view/internal/hub"
	"agent-view/internal/logs"
	"agent-view/internal/store"
	"agent-view/internal/tui"
	"agent-view/internal/wire"
)

// The groups of the generated fixture. A log is named by its group, so the
// agent ID of a log tells the group of its agent.
const (
	groupOpenCall = "a"       // last record 5 min old, a call still open
	groupNoCall   = "b"       // last record 5 min old, no open call, no turn end
	groupParent   = "c"       // last record 1 h old, no open call; a present child keeps it
	groupChild    = "c-child" // the child of a "c" agent: a fresh record, a call open
	groupFresh    = "d"       // last record fresh, a call open
)

// The ages of the last record of each group.
const (
	ageStaleWork = 5 * time.Minute // more than the 2 min stale time, less than the 10 min linger
	ageOld       = time.Hour       // more than the 10 min linger
	ageFresh     = 5 * time.Second
)

// hostLogCap is how many logs one host of the fixture gets. A collector reads
// at most 120 logs, the newest by file time (the default of the log source),
// so a fixture of more logs goes to several hosts, as several machines do.
const hostLogCap = 100

// workingSizes is how many agents each group of the fixture holds. Each
// Parents agent also gets a child log, so the fixture has Parents more agents
// than the sum of the sizes.
type workingSizes struct{ OpenCall, NoCall, Parents, Fresh int }

// logFile is one generated log: its path under the host's log root, its text,
// and the time of its last record in Unix ms, which is also its file time.
type logFile struct {
	rel  string
	text string
	last int64
}

// logRecord is one Claude Code record with one content block. A record of a
// sub-agent names its agent.
func logRecord(t *testing.T, session, kid string, ms int64, typ string, block map[string]any) string {
	t.Helper()
	rec := map[string]any{
		"sessionId": session, "timestamp": wire.FormatISO(ms), "type": typ,
		"message": map[string]any{"content": []any{block}},
	}
	if kid != "" {
		rec["agentId"], rec["isSidechain"] = kid, true
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// callLog is the text of a log that holds one Bash call and no turn end. The
// call is open when the log ends with it; otherwise the last record, at
// lastMs, is its result.
func callLog(t *testing.T, session, kid string, lastMs int64, ended bool) string {
	t.Helper()
	use := map[string]any{"type": "tool_use", "id": "call1", "name": "Bash", "input": map[string]any{"command": "make check"}}
	if !ended {
		return logRecord(t, session, kid, lastMs, "assistant", use)
	}
	result := map[string]any{"type": "tool_result", "tool_use_id": "call1", "content": "ok"}
	return logRecord(t, session, kid, lastMs-1000, "assistant", use) + logRecord(t, session, kid, lastMs, "user", result)
}

// workingUnits returns the logs of the fixture, as of now (Unix ms). A unit
// is the logs that must share a host: a main log, or a parent log with its
// child log.
func workingUnits(t *testing.T, n workingSizes, now int64) [][]logFile {
	t.Helper()
	main := func(group string, i int, lastMs int64, ended bool) logFile {
		session := fmt.Sprintf("%s-%03d", group, i)
		return logFile{rel: filepath.Join("proj", session+".jsonl"), text: callLog(t, session, "", lastMs, ended), last: lastMs}
	}
	var units [][]logFile
	for i := range n.OpenCall {
		units = append(units, []logFile{main(groupOpenCall, i, now-ageStaleWork.Milliseconds(), false)})
	}
	for i := range n.NoCall {
		units = append(units, []logFile{main(groupNoCall, i, now-ageStaleWork.Milliseconds(), true)})
	}
	for i := range n.Parents {
		parent := main(groupParent, i, now-ageOld.Milliseconds(), true)
		// A sub-agent log of the same session: the collector links the
		// sub-agent to the main agent of the session ("session" link).
		session, kid := fmt.Sprintf("%s-%03d", groupParent, i), fmt.Sprintf("kid-%03d", i)
		lastMs := now - ageFresh.Milliseconds()
		child := logFile{rel: filepath.Join("proj", session, "subagents", "agent-"+kid+".jsonl"), text: callLog(t, session, kid, lastMs, false), last: lastMs}
		units = append(units, []logFile{parent, child})
	}
	for i := range n.Fresh {
		units = append(units, []logFile{main(groupFresh, i, now-ageFresh.Milliseconds(), false)})
	}
	return units
}

// writeLogUnits writes the units into new log roots in a temporary directory,
// at most hostLogCap logs for each root, and sets the file time of each log
// to the time of its last record. A unit never splits over two roots.
func writeLogUnits(t *testing.T, units [][]logFile) []logs.Root {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var roots []logs.Root
	held := hostLogCap // the first unit starts a root
	for _, u := range units {
		if held+len(u) > hostLogCap {
			path := filepath.Join(dir, "host"+strconv.Itoa(len(roots)+1), "projects")
			roots = append(roots, logs.Root{Provider: "claude", Path: path, Scope: "default"})
			held = 0
		}
		for _, f := range u {
			path := filepath.Join(roots[len(roots)-1].Path, f.rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(f.text), 0o644); err != nil {
				t.Fatal(err)
			}
			at := time.UnixMilli(f.last)
			if err := os.Chtimes(path, at, at); err != nil {
				t.Fatal(err)
			}
		}
		held += len(u)
	}
	return roots
}

// workingRun is the TUI side of a run: the model, and the store it owns,
// after both took every message of the first scan.
type workingRun struct {
	model *tui.Model
	store *store.Store
}

// feedModel runs one collector on each root and feeds a model the way runTUI
// does: the collectors are local sources of one hub, the bridge subscribes to
// the hub before the sources start, and the bridge's messages go to the model.
// There is no terminal: Send collects the messages, and the model gets them
// in order after the bridge has stopped, which sends everything it holds.
func feedModel(t *testing.T, roots []logs.Root) *workingRun {
	t.Helper()
	h := hub.New(hub.Options{})
	t.Cleanup(h.Stop)
	var sources []*hub.LocalSource
	for _, root := range roots {
		label := h.UniqueLabel("devbox")
		c := collect.New(collect.Options{Host: label, Roots: []logs.Root{root}, Diag: func(string) {}})
		src := hub.NewLocalSource(c, label)
		if err := h.AddSource(src); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, src)
	}
	var mu sync.Mutex
	var msgs []tea.Msg
	br := bridge.Start(bridge.Config{Hub: h, Send: func(m tea.Msg) {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, m)
	}})
	t.Cleanup(br.Stop)
	for _, src := range sources {
		if err := src.Start(); err != nil { // returns when the first scan is delivered
			t.Fatal(err)
		}
	}
	br.Stop()

	s := store.New(store.Options{})
	m := tui.NewModel(tui.Config{Store: s, Reduced: true})
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	mu.Lock()
	defer mu.Unlock()
	for _, msg := range msgs {
		m.Update(msg)
	}
	return &workingRun{model: m, store: s}
}

// workingCount is what one group of agents counts.
type workingCount struct {
	Present int            // agents on the board
	Working int            // agents whose status is working: what the status bar counts
	Words   map[string]int // the state word of each agent (cards.State), by word
}

// workingResult is what a run counts: the numbers that the status bar shows,
// and each group's counts.
type workingResult struct {
	BarAgents, BarWorking int
	Groups                map[string]workingCount
}

func (r workingResult) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "status bar: %d agents, %d working\n", r.BarAgents, r.BarWorking)
	for _, g := range slices.Sorted(maps.Keys(r.Groups)) {
		c := r.Groups[g]
		fmt.Fprintf(&sb, "group %-7s present %3d, status working %3d, words %v\n", g, c.Present, c.Working, c.Words)
	}
	return sb.String()
}

// groupOf returns the group of an agent by its ID. The ID ends with the
// session of the log, "<group>-<number>", and a sub-agent adds ":<id>".
func groupOf(agentID string) string {
	_, tail, _ := strings.Cut(agentID, "/claude:default:")
	session, _, isChild := strings.Cut(tail, ":")
	group, _, _ := strings.Cut(session, "-")
	if isChild {
		return group + "-child"
	}
	return group
}

// barCounts finds the agent and working counts in the first line of the screen.
var barCounts = regexp.MustCompile(`(\d+) agents? +(\d+) working`)

// countWorking reads the status bar of the model, and counts each agent of
// the store the way the rows do.
func countWorking(t *testing.T, run *workingRun) workingResult {
	t.Helper()
	first, _, _ := strings.Cut(ansi.Strip(run.model.View().Content), "\n")
	sub := barCounts.FindStringSubmatch(first)
	if sub == nil {
		t.Fatalf("no counts in the status bar: %q", first)
	}
	res := workingResult{Groups: map[string]workingCount{}}
	res.BarAgents, _ = strconv.Atoi(sub[1])
	res.BarWorking, _ = strconv.Atoi(sub[2])

	now := time.Now().UnixMilli()
	agents := run.store.Agents(now)
	type hostID struct{ host, id string }
	hasChild := map[hostID]bool{}
	for _, a := range agents {
		if a.ParentID != nil {
			hasChild[hostID{a.Host, *a.ParentID}] = true
		}
	}
	for _, a := range agents {
		c := res.Groups[groupOf(a.ID)]
		if c.Words == nil {
			c.Words = map[string]int{}
		}
		c.Present++
		if a.Status == "working" {
			c.Working++
		}
		word := cards.State(a, run.store.OpenCalls(a.ID), hasChild[hostID{a.Host, a.ID}], now, store.DefaultStaleMs).Text
		c.Words[word[strings.LastIndex(word, " ")+1:]]++
		res.Groups[groupOf(a.ID)] = c
	}
	return res
}

// measureWorking generates the logs of the fixture, runs the program's
// pipeline on them, and returns what the status bar and the rows count.
func measureWorking(t *testing.T, n workingSizes) workingResult {
	t.Helper()
	roots := writeLogUnits(t, workingUnits(t, n, wire.NowMs()))
	return countWorking(t, feedModel(t, roots))
}

// Today the status bar counts every present agent whose status is working,
// and the row of an agent says STALE when it has had no event for 2 min. An
// agent can have a working status and a STALE row at once, for three reasons
// that this fixture builds (see the follow-ups file of P3c):
//
//	a  a call is open and the last record is 5 min old: present, working, STALE
//	b  the log has no turn end, the last record is 5 min old: the same
//	c  the last record is 1 h old and no turn end: present only because its
//	   child is, and the status stays working
//
// The second case has the sizes of the table in the follow-ups file. Task 5
// of P3c changes the count and replaces this test.
func TestWorkingCountIncludesStaleRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sizes workingSizes
		want  workingResult
	}{
		{"small fixture", workingSizes{OpenCall: 2, NoCall: 2, Parents: 2, Fresh: 2},
			workingResult{BarAgents: 10, BarWorking: 10, Groups: map[string]workingCount{
				groupOpenCall: {Present: 2, Working: 2, Words: map[string]int{"STALE": 2}},
				groupNoCall:   {Present: 2, Working: 2, Words: map[string]int{"STALE": 2}},
				groupParent:   {Present: 2, Working: 2, Words: map[string]int{"STALE": 2}},
				groupChild:    {Present: 2, Working: 2, Words: map[string]int{"WORKING": 2}},
				groupFresh:    {Present: 2, Working: 2, Words: map[string]int{"WORKING": 2}},
			}}},
		{"sizes of the follow-ups table", workingSizes{OpenCall: 40, NoCall: 40, Parents: 20, Fresh: 20},
			workingResult{BarAgents: 140, BarWorking: 140, Groups: map[string]workingCount{
				groupOpenCall: {Present: 40, Working: 40, Words: map[string]int{"STALE": 40}},
				groupNoCall:   {Present: 40, Working: 40, Words: map[string]int{"STALE": 40}},
				groupParent:   {Present: 20, Working: 20, Words: map[string]int{"STALE": 20}},
				groupChild:    {Present: 20, Working: 20, Words: map[string]int{"WORKING": 20}},
				groupFresh:    {Present: 20, Working: 20, Words: map[string]int{"WORKING": 20}},
			}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := measureWorking(t, tc.sizes)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("counts differ.\ngot:\n%swant:\n%s", got, tc.want)
			}
		})
	}
}
