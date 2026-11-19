package cards

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
)

// Clock text depends on the zone, so the tests run in UTC.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

const (
	callStart = int64(1_700_000_000_000) // 22:13:20 UTC
	callRoot  = "/work/agent-view"
)

func bptr(b bool) *bool { return &b }
func iptr(i int) *int   { return &i }

// mkCall builds a stored call. dur < 0 leaves it open.
func mkCall(cat string, dur int64, ok *bool, mod func(*store.ViewCall)) *store.ViewCall {
	c := &store.ViewCall{
		Call:    schema.Call{AgentID: "a", CallID: "c", Category: cat, RawName: "Tool"},
		StartMs: callStart,
	}
	if dur >= 0 {
		c.Ended, c.EndMs, c.Ok = true, callStart+dur, ok
	}
	if mod != nil {
		mod(c)
	}
	return c
}

func TestCallCardGolden(t *testing.T) {
	file := func(p ...string) func(*store.ViewCall) {
		return func(c *store.ViewCall) { c.Files = p }
	}
	cmd := func(s, out string) func(*store.ViewCall) {
		return func(c *store.ViewCall) { c.Command, c.Output = s, out }
	}
	tests := []struct {
		name string
		in   CallIn
	}{
		{"read_open", CallIn{Call: mkCall("read", -1, nil, file(callRoot+"/viewer/stage/tree-view.ts")), Now: callStart + 1400, Spin: 0}},
		{"edit_ended_ok", CallIn{Call: mkCall("edit", 1500, bptr(true), file(callRoot+"/viewer/stage/tree-view.ts")), Now: callStart + 9000}},
		{"command_open_output", CallIn{Call: mkCall("command", -1, nil, cmd("bun test", "one\ntwo\n\nthree\x1b[31m!\n")), Now: callStart + 5200, Spin: 1}},
		{"command_failed", CallIn{Call: mkCall("command", 3300, bptr(false), func(c *store.ViewCall) {
			cmd("pnpm lint", "checking\nerror: 2 problems")(c)
			c.ExitCode = iptr(2)
		}), Now: callStart + 9000}},
		{"ended_no_result", CallIn{Call: mkCall("read", 600_000, nil, file(callRoot+"/README.md")), Now: callStart + 700_000}},
		{"stale_open", CallIn{Call: mkCall("edit", -1, nil, file(callRoot+"/src/a.ts")), Now: callStart + 200_000, Spin: 2, Stale: true, LastEventMs: callStart - 60_000}},
		{"agent_other_open", CallIn{Call: mkCall("agent", -1, nil, nil), OtherOpen: 2, Now: callStart + 75_000, Spin: 3}},
		{"search_open", CallIn{Call: mkCall("search", -1, nil, func(c *store.ViewCall) { c.Pattern = "func (\\w+)Store" }), Now: callStart + 13_000}},
		{"other_ended", CallIn{Call: mkCall("other", 800, bptr(true), func(c *store.ViewCall) { c.RawName = "WebFetch" }), Now: callStart + 900}},
		{"two_files", CallIn{Call: mkCall("read", 900, bptr(true), file("/a/x.ts", "/a/y.ts")), Now: callStart + 9000}},
		{"wide_long_name", CallIn{Call: mkCall("write", -1, nil, file("\x1b[31m日本語-file-name-that-is-long.ts")), Now: callStart + 2000}},
		{"focused_output", CallIn{
			Call: mkCall("command", 2000, bptr(true), cmd("make", "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11\nl12")),
			Now:  callStart + 9000, OutputLines: 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.ProjectRoot = callRoot
			g := CallCard(tt.in)
			wantH := 7
			if tt.in.OutputLines > 2 {
				wantH = 5 + tt.in.OutputLines
			}
			if g.W != 28 || g.H != wantH {
				t.Fatalf("size = %dx%d, want 28x%d", g.W, g.H, wantH)
			}
			checkGolden(t, "call_"+tt.name, g)
		})
	}
}

func TestElapsedFormat(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{1400, "1.4s"}, {0, "0.0s"}, {-5, "0.0s"}, {59_999, "59.9s"}, {60_000, "1m 00s"},
		{123_000, "2m 03s"}, {3_725_000, "62m 05s"},
	}
	for _, tt := range tests {
		if got := Elapsed(tt.ms); got != tt.want {
			t.Errorf("Elapsed(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

func TestWhere(t *testing.T) {
	tests := []struct {
		name, root string
		call       *store.ViewCall
		repo, dir  string
	}{
		{"nested", callRoot, mkCall("read", 1, nil, func(c *store.ViewCall) { c.Files = []string{callRoot + "/viewer/stage/a.ts"} }), "agent-view", "viewer/stage/"},
		{"at root", callRoot, mkCall("edit", 1, nil, func(c *store.ViewCall) { c.Files = []string{callRoot + "/a.ts"} }), "agent-view", "./"},
		{"outside", callRoot, mkCall("read", 1, nil, func(c *store.ViewCall) { c.Files = []string{"/etc/hosts"} }), "agent-view", "/etc/"},
		{"sibling prefix", callRoot, mkCall("read", 1, nil, func(c *store.ViewCall) { c.Files = []string{callRoot + "-x/a.ts"} }), "agent-view", "/work/agent-view-x/"},
		{"no root", "", mkCall("read", 1, nil, func(c *store.ViewCall) { c.Files = []string{"/etc/hosts"} }), "", "/etc/"},
		{"command", callRoot, mkCall("command", 1, nil, nil), "agent-view", "(shell)"},
		{"search", callRoot, mkCall("search", 1, nil, nil), "agent-view", "*"},
		{"agent", callRoot, mkCall("agent", 1, nil, nil), "agent-view", ""},
		{"other", callRoot, mkCall("other", 1, nil, nil), "agent-view", ""},
		{"read no files", callRoot, mkCall("read", 1, nil, nil), "agent-view", ""},
		{"nil call", callRoot, nil, "agent-view", ""},
		{"trailing slash root", callRoot + "/", mkCall("read", 1, nil, func(c *store.ViewCall) { c.Files = []string{callRoot + "/a/b.ts"} }), "agent-view", "a/"},
		{"cleaned", "/w/bad\x1b[31mrepo", mkCall("read", 1, nil, nil), "badrepo", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, dir := Where(tt.root, tt.call)
			if repo != tt.repo || dir != tt.dir {
				t.Errorf("Where = (%q, %q), want (%q, %q)", repo, dir, tt.repo, tt.dir)
			}
		})
	}
}

func TestTarget(t *testing.T) {
	tests := []struct {
		call *store.ViewCall
		want string
	}{
		{mkCall("read", 1, nil, func(c *store.ViewCall) { c.Files = []string{"/a/x.ts"} }), "x.ts"},
		{mkCall("edit", 1, nil, func(c *store.ViewCall) { c.Files = []string{"/a/x.ts", "/a/y.ts", "/a/z.ts"} }), "x.ts +2"},
		{mkCall("command", 1, nil, func(c *store.ViewCall) { c.Command = "ls\n-la" }), "$ ls⏎-la"},
		{mkCall("search", 1, nil, func(c *store.ViewCall) { c.Pattern = "foo\x1b[0m" }), "foo"},
		{mkCall("agent", 1, nil, nil), "sub-agent"},
		{mkCall("other", 1, nil, func(c *store.ViewCall) { c.RawName = "WebFetch" }), "WebFetch"},
		{mkCall("read", 1, nil, nil), "Tool"},
	}
	for _, tt := range tests {
		if got := Target(tt.call); got != tt.want {
			t.Errorf("Target(%s) = %q, want %q", tt.call.Category, got, tt.want)
		}
	}
}

func TestHistoryCardGolden(t *testing.T) {
	tests := []struct {
		name string
		call *store.ViewCall
	}{
		{"ok", mkCall("read", 1500, bptr(true), func(c *store.ViewCall) { c.Files = []string{"/a/store.ts"} })},
		{"fail", mkCall("command", 3300, bptr(false), func(c *store.ViewCall) { c.Command = "pnpm lint --fix" })},
		{"unknown", mkCall("search", 600_000, nil, func(c *store.ViewCall) { c.Pattern = "needle" })},
		{"long_name", mkCall("edit", 120_000, bptr(true), func(c *store.ViewCall) { c.Files = []string{"/a/a-very-long-file-name.tsx"} })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := HistoryCard(tt.call)
			if g.W != 14 || g.H != 4 {
				t.Fatalf("size = %dx%d, want 14x4", g.W, g.H)
			}
			checkGolden(t, "history_"+tt.name, g)
		})
	}
}

func TestCollapsedRow(t *testing.T) {
	const now = int64(10_000_000)
	a := viewAgent("eo", "eos-api", "codex", "idle", nil)
	a.LastEventMs = now - 4*60_000
	idle := StateWord{"- IDLE", cells.Style{Role: cells.RDim}, false}
	last := mkCall("read", 800, bptr(true), func(c *store.ViewCall) { c.Files = []string{"/a/users.ts"} })

	g := CollapsedRow(70, a, idle, last, now)
	if g.W != 70 || g.H != 1 {
		t.Fatalf("size = %dx%d", g.W, g.H)
	}
	want := "▸ eos-api · codex · - IDLE 4m · last READ users.ts ok"
	if got := cells.Plain(g); got != want+strings.Repeat(" ", 70-len([]rune(want))) {
		t.Errorf("Plain = %q", got)
	}

	fail := mkCall("command", 800, bptr(false), func(c *store.ViewCall) { c.Command = "make" })
	if got := cells.Plain(CollapsedRow(70, a, idle, fail, now)); !strings.Contains(got, "last RUN $ make FAIL") {
		t.Errorf("fail row = %q", got)
	}
	none := mkCall("read", 800, nil, func(c *store.ViewCall) { c.Files = []string{"/a/u.ts"} })
	if got := cells.Plain(CollapsedRow(70, a, idle, none, now)); !strings.Contains(got, "last READ u.ts ??") {
		t.Errorf("?? row = %q", got)
	}
	if got := cells.Plain(CollapsedRow(40, a, idle, nil, now)); !strings.HasPrefix(got, "▸ eos-api · codex · - IDLE 4m") || strings.Contains(got, "last") {
		t.Errorf("no-call row = %q", got)
	}
	// Narrow widths clip with an ellipsis and never overflow.
	if g := CollapsedRow(20, a, idle, last, now); g.W != 20 || !strings.Contains(cells.Plain(g), "…") {
		t.Errorf("narrow row = %q", cells.Plain(g))
	}
	// Agent text is cleaned.
	bad := viewAgent("x", "ev\x1b[31mil\nname", "co\x07dex", "idle", nil)
	if got := cells.Plain(CollapsedRow(60, bad, idle, nil, now)); strings.ContainsAny(got, "\x1b\x07\n") {
		t.Errorf("not cleaned: %q", got)
	}
}

func TestCallCardHeight(t *testing.T) {
	cmd := func(n int) func(*store.ViewCall) {
		return func(c *store.ViewCall) {
			c.Command = "make"
			for i := 1; i <= n; i++ {
				c.Output += fmt.Sprintf("l%d\n\n", i) // blank lines do not count
			}
		}
	}
	tests := []struct {
		name  string
		call  *store.ViewCall
		lines int
		want  int
	}{
		{"focused 12 lines", mkCall("command", -1, nil, cmd(12)), 10, 15},
		{"focused 3 lines", mkCall("command", 900, bptr(true), cmd(3)), 10, 8},
		{"focused 2 lines", mkCall("command", -1, nil, cmd(2)), 10, 7},
		{"asks for 4 of 12", mkCall("command", -1, nil, cmd(12)), 4, 9},
		{"not focused", mkCall("command", -1, nil, cmd(12)), 0, 7},
		{"not a command", mkCall("read", -1, nil, func(c *store.ViewCall) { c.Files, c.Output = []string{"/a"}, "x\ny\nz\nw" }), 10, 7},
		{"nil call", nil, 10, 7},
	}
	for _, tc := range tests {
		if got := CallCardHeight(tc.call, tc.lines); got != tc.want {
			t.Errorf("%s: CallCardHeight = %d, want %d", tc.name, got, tc.want)
		}
		if tc.call == nil {
			continue
		}
		if g := CallCard(CallIn{Call: tc.call, Now: callStart + 1000, OutputLines: tc.lines}); g.H != tc.want {
			t.Errorf("%s: CallCard is %d high, want %d", tc.name, g.H, tc.want)
		}
	}
}

// Emoji whose grapheme width and wcwidth differ are written so the card's
// right border stays in its column by the renderer's wcwidth measure.
func TestCallCardEmojiKeepsBorders(t *testing.T) {
	family := "👨‍👩‍👧‍👦"
	tests := []*store.ViewCall{
		mkCall("read", -1, nil, func(c *store.ViewCall) { c.Files = []string{callRoot + "/⚠️ notes/❤️👍🏽.md"} }),
		mkCall("command", 900, bptr(true), func(c *store.ViewCall) {
			c.Command, c.Output = "echo "+family+" ⚠️ ❤️ 👍🏽 done", "⚠️ warning ❤️\n"+family+" 👍🏽 ok\n"
		}),
	}
	for _, c := range tests {
		g := CallCard(CallIn{Call: c, ProjectRoot: callRoot, Now: callStart + 1000})
		for y, line := range strings.Split(cells.Plain(g), "\n") {
			if w := ansi.StringWidthWc(line); w != CallW {
				t.Errorf("%s line %d %q: wcwidth %d, want %d", c.Category, y, line, w, CallW)
			}
		}
	}
}
