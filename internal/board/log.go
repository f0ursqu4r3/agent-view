package board

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/rivo/uniseg"

	"agent-view/internal/cards"
	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

// Event log limits and layout, in cells.
const (
	logCap = 500 // lines kept in memory

	// touchBucketMs is the FS grouping window; a touch older than
	// historyMs on arrival is history and writes no line.
	touchBucketMs = 2_000
	historyMs     = 10_000

	logTimeW   = 8
	logWhoW    = 16
	logLabelW  = 6
	logResultW = 4
	logDurW    = 6
	logTextX   = 2 // border, one space
	logWhoX    = logTextX + logTimeW + 2
	logLabelX  = logWhoX + logWhoW + 2
	logTargetX = logLabelX + logLabelW

	logTitle = " events · tail -f "
)

// LogLine is one line of the event log. A HOST line has the label "HOST" and
// no agent. An FS line has the label "FS" and no agent or who. A line with an
// empty Who puts its label in the who column and its target two cells after
// the label.
type LogLine struct {
	AtMs           int64
	AgentID, Who   string
	WhoRole        cells.Role
	Label          string
	LabelRole      cells.Role
	Target, Result string
	DurMs          int64
	Dim            bool
	short          string // Target without the folder, used when Target does not fit
	fs             fsKey  // the touch group of an FS line
	fsN            int    // the touches of an FS line; 0 for other lines
}

// fsKey names one group of unattributed touches.
type fsKey struct {
	host, folder string
	bucket       int64
}

// Log is the event log: a ring of the last 500 lines.
type Log struct {
	lines []LogLine
}

// Len returns the number of lines held.
func (l *Log) Len() int { return len(l.lines) }

// Add appends a line and drops the oldest one beyond 500.
func (l *Log) Add(ln LogLine) {
	l.lines = append(l.lines, ln)
	if n := len(l.lines) - logCap; n > 0 {
		l.lines = append(l.lines[:0:0], l.lines[n:]...)
	}
}

// FromChange adds the line that a store change produces, if any. History
// changes produce none. Only CallEnded, AgentAdded with a parent, and
// AgentStatus interrupted or finished produce a line. Call it after the store
// applied the change.
func (l *Log) FromChange(s *store.Store, c store.Change) {
	if c.History {
		return
	}
	switch c.Kind {
	case store.CallEnded:
		l.toolLine(s, c)
	case store.AgentAdded:
		a, ok := s.Agent(c.AgentID)
		if !ok || a.ParentID == nil {
			return
		}
		parent := *a.ParentID
		if p, ok := s.Agent(parent); ok {
			parent = p.Name
		}
		ln := agentLine(a, c.AtMs, "BOOT", cells.RAgent)
		ln.Target = "sub-agent of " + text.Clean(parent)
		l.Add(ln)
	case store.AgentStatus:
		switch c.Status {
		case "interrupted":
			if a, ok := s.Agent(c.AgentID); ok {
				l.Add(agentLine(a, c.AtMs, "STOP", cells.RFail))
			}
		case "finished":
			if a, ok := s.Agent(c.AgentID); ok {
				l.Add(agentLine(a, c.AtMs, "DONE", cells.RDim))
			}
		}
	}
}

// agentLine starts a line for an agent: its name, in the color of its
// provider (bright for a sub-agent).
func agentLine(a store.ViewAgent, atMs int64, label string, role cells.Role) LogLine {
	who := text.Clean(a.Name)
	if who == "" {
		who = text.Clean(a.ID)
	}
	whoRole := cells.RText
	switch {
	case a.ParentID != nil:
		whoRole = cells.RBright
	case a.Provider == "claude":
		whoRole = cells.RClaude
	case a.Provider == "codex":
		whoRole = cells.RCodex
	}
	return LogLine{AtMs: atMs, AgentID: a.ID, Who: who, WhoRole: whoRole, Label: label, LabelRole: role}
}

// toolLine adds the line of an ended call. A call the store no longer holds
// writes no line.
func (l *Log) toolLine(s *store.Store, c store.Change) {
	a, ok := s.Agent(c.AgentID)
	if !ok {
		return
	}
	call := findEnded(s, c.AgentID, c.CallID)
	if call == nil {
		return
	}
	ln := agentLine(a, c.AtMs, schema.Label(call.Category), cells.RoleOf(call.Category))
	ln.short = cards.Target(call)
	ln.Target = ln.short
	if call.Category == "read" || call.Category == "edit" || call.Category == "write" {
		root := ""
		if a.ProjectRoot != nil {
			root = *a.ProjectRoot
		} else if a.Cwd != nil {
			root = *a.Cwd
		}
		if _, folder := cards.Where(root, call); folder != "" && folder != "./" {
			ln.Target = folder + ln.short
		}
	}
	ln.DurMs = call.EndMs - call.StartMs
	switch {
	case call.Ok == nil:
		ln.Result = "??"
	case *call.Ok:
		ln.Result = "ok"
	default:
		ln.Result = "FAIL"
	}
	l.Add(ln)
}

// findEnded returns the ended call of an agent by ID. The call that just
// ended is usually among the newest, so a short list comes first.
func findEnded(s *store.Store, agentID, callID string) *store.ViewCall {
	for _, n := range []int{16, 2_000} {
		for _, c := range s.RecentEnded(agentID, n) {
			if c.CallID == callID {
				return c
			}
		}
	}
	return nil
}

// Host adds a HOST line: feed up or down, degraded sources, or the startup
// history summary.
func (l *Log) Host(atMs int64, host, msg string) {
	l.Add(LogLine{
		AtMs: atMs, Label: "HOST", LabelRole: cells.RAgent,
		Target: strings.TrimSpace(text.Clean(host) + " " + text.Clean(msg)),
	})
}

// AddTouches adds one dim FS line per group of touches. A group is the touches
// of one host and folder in the same 2-second bucket of time. A group that
// already has a line, because an earlier call released part of it, adds to
// that line. A touch more than 10 s older than now is history and is skipped.
func (l *Log) AddTouches(t []store.Touch, now int64) {
	type group struct {
		n  int
		at int64
	}
	var order []fsKey
	groups := map[fsKey]*group{}
	for _, tc := range t {
		if now-tc.AtMs > historyMs {
			continue
		}
		k := fsKey{tc.Host, path.Dir(tc.Path), tc.AtMs / touchBucketMs}
		g := groups[k]
		if g == nil {
			g = &group{}
			groups[k] = g
			order = append(order, k)
		}
		g.n++
		g.at = max(g.at, tc.AtMs)
	}
	for _, k := range order {
		g := groups[k]
		if i := l.fsLine(k); i >= 0 {
			ln := &l.lines[i]
			ln.fsN += g.n
			ln.AtMs = max(ln.AtMs, g.at)
			ln.Target = fsTarget(ln.fsN, k.folder)
			continue
		}
		l.Add(LogLine{
			AtMs: g.at, Label: "FS", LabelRole: cells.RDim, Dim: true,
			Target: fsTarget(g.n, k.folder), fs: k, fsN: g.n,
		})
	}
}

// fsLine returns the index of the FS line of a group, or -1.
func (l *Log) fsLine(k fsKey) int {
	for i := len(l.lines) - 1; i >= 0; i-- {
		if l.lines[i].fsN > 0 && l.lines[i].fs == k {
			return i
		}
	}
	return -1
}

// fsTarget returns the text of an FS line.
func fsTarget(n int, folder string) string {
	folder = text.Clean(folder)
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	return fmt.Sprintf("%d change(s) in %s", n, folder)
}

// Render draws the log box, exactly w by h cells: a border with the title,
// the lines (newest at the bottom, the most that fit), and the prompt on the
// last inner line. With focus set, only that agent's lines and the HOST lines
// show.
func (l *Log) Render(w, h int, focus string) *cells.Grid {
	g := cells.New(w, h)
	dim := cells.Style{Role: cells.RDim}
	g.Box(0, 0, w, h, cells.Square, dim)
	g.Text(2, 0, w-4, logTitle, dim)
	if h < 3 || w < 6 {
		return g
	}
	g.Text(logTextX, h-2, w-4, "$ ▌", cells.Style{Role: cells.RText})

	var shown []LogLine
	for i := len(l.lines) - 1; i >= 0 && len(shown) < h-3; i-- {
		ln := l.lines[i]
		if focus != "" && ln.AgentID != focus && ln.Label != "HOST" {
			continue
		}
		shown = append(shown, ln)
	}
	for i, ln := range shown {
		drawLogLine(g, h-3-i, ln)
	}
	return g
}

// drawLogLine draws one line at row y.
func drawLogLine(g *cells.Grid, y int, ln LogLine) {
	pick := func(role cells.Role) cells.Role {
		if ln.Dim {
			return cells.RDim
		}
		return role
	}
	right := g.W - 2 // the text area ends here
	g.Text(logTextX, y, logTimeW, time.UnixMilli(ln.AtMs).Format("15:04:05"), cells.Style{Role: cells.RDim})

	label := cells.Style{Role: pick(ln.LabelRole), Bold: !ln.Dim}
	if ln.Who == "" {
		// The label takes the who column and the target follows it.
		n := g.Text(logWhoX, y, max(right-logWhoX, 0), ln.Label, label)
		x := logWhoX + n + 2
		g.Text(x, y, max(right-x, 0), ln.Target, cells.Style{Role: pick(cells.RText)})
		return
	}
	g.Text(logWhoX, y, logWhoW, ln.Who, cells.Style{Role: pick(ln.WhoRole), Bold: !ln.Dim && ln.WhoRole != cells.RBright})
	g.Text(logLabelX, y, logLabelW-1, ln.Label, label)

	durX := right - logDurW
	resX := durX - logResultW - 1
	if ln.Result == "" {
		g.Text(logTargetX, y, max(right-logTargetX, 0), ln.Target, cells.Style{Role: pick(cells.RText)})
		return
	}
	target := ln.Target
	if w := max(resX-1-logTargetX, 0); ln.short != "" && cellWidth(target) > w {
		target = ln.short
	}
	g.Text(logTargetX, y, max(resX-1-logTargetX, 0), target, cells.Style{Role: pick(cells.RText)})
	res := cells.Style{Role: pick(cells.RDim)}
	switch ln.Result {
	case "ok":
		res.Role = pick(cells.ROK)
	case "FAIL":
		res = cells.Style{Role: cells.RFail, Inverse: true}
	}
	g.Text(resX, y, logResultW, ln.Result, res)
	dur := cards.Elapsed(ln.DurMs)
	g.Text(durX+max(logDurW-cellWidth(dur), 0), y, logDurW, dur, cells.Style{Role: cells.RDim})
}

// cellWidth returns the layout width of s.
func cellWidth(s string) int {
	n := 0
	for gr := uniseg.NewGraphemes(s); gr.Next(); {
		n += min(max(gr.Width(), 0), 2)
	}
	return n
}
