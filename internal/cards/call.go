package cards

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/rivo/uniseg"

	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

// Call card size and layout, in cells.
const (
	CallW = 28
	CallH = 7

	callTextX = 2 // border, one space
	callTextW = CallW - 4
	callBarW  = 12
)

// MaxOutputLines is the most output lines a call card shows. The focused
// card asks for this many.
const MaxOutputLines = 10

// CallCardHeight returns the height of the card CallCard draws for c with
// the given OutputLines: CallH, or for a command with more than 2 output
// lines when outputLines is above 2, 5 plus the lines shown (at most
// outputLines, at most MaxOutputLines). A nil call gives CallH.
func CallCardHeight(c *store.ViewCall, outputLines int) int {
	if c == nil || outputLines <= 2 || c.Category != "command" {
		return CallH
	}
	n := len(text.CleanLines(c.Output))
	if n <= 2 {
		return CallH
	}
	return 5 + min(n, outputLines, MaxOutputLines)
}

var spinFrames = [4]string{"|", "/", "-", `\`}

// CallIn is the input of CallCard. Call must not be nil.
type CallIn struct {
	Call        *store.ViewCall
	ProjectRoot string // the agent's project root, else its cwd, else ""
	OtherOpen   int    // the number of other open calls of the agent
	Now         int64
	Spin        int // the spinner frame counter
	Stale       bool
	LastEventMs int64
	OutputLines int // 0 for the normal card; above 2, the focused card (see CallCardHeight)
}

// Elapsed formats a duration: 1.4s under 60 s, then 2m 03s.
func Elapsed(ms int64) string {
	ms = max(ms, 0)
	if ms < 60_000 {
		tenths := ms / 100
		return fmt.Sprintf("%d.%ds", tenths/10, tenths%10)
	}
	s := ms / 1000
	return fmt.Sprintf("%dm %02ds", s/60, s%60)
}

// isFileCategory reports whether a call category names files.
func isFileCategory(cat string) bool {
	return cat == "read" || cat == "edit" || cat == "write"
}

// Where returns the repo and the folder of a call, for call card line 2 and
// the "where" line. projectRoot is the agent's project root, else its cwd,
// else "". The repo is the root's base name. The folder is the first file's
// folder relative to the root with a trailing "/" ("./" at the root), or the
// absolute folder when the file is outside the root. Commands give "(shell)",
// searches "*", other calls "".
func Where(projectRoot string, c *store.ViewCall) (repo, folder string) {
	root := path.Clean(text.Clean(projectRoot))
	if root == "." || root == "/" {
		root = ""
	} else {
		repo = path.Base(root)
	}
	if c == nil {
		return repo, ""
	}
	switch c.Category {
	case "command":
		return repo, "(shell)"
	case "search":
		return repo, "*"
	}
	if !isFileCategory(c.Category) || len(c.Files) == 0 {
		return repo, ""
	}
	file := text.Clean(c.Files[0])
	if root != "" && !path.IsAbs(file) {
		file = path.Join(root, file)
	}
	dir := path.Dir(file)
	switch {
	case root != "" && dir == root:
		return repo, "./"
	case root != "" && strings.HasPrefix(dir, root+"/"):
		return repo, dir[len(root)+1:] + "/"
	case dir == ".":
		return repo, "./"
	case dir == "/":
		return repo, "/"
	}
	return repo, dir + "/"
}

// fileParts returns the cleaned base name of a file call and its "+N" suffix.
func fileParts(c *store.ViewCall) (name, suffix string) {
	if len(c.Files) == 0 {
		return "", ""
	}
	name = path.Base(text.Clean(c.Files[0]))
	if len(c.Files) > 1 {
		suffix = fmt.Sprintf(" +%d", len(c.Files)-1)
	}
	return name, suffix
}

// Target returns the line-1 text of a call without decoration: the file base
// name (and +N), $ command, pattern, sub-agent name, or raw name.
func Target(c *store.ViewCall) string {
	name, suffix := targetParts(c)
	return name + suffix
}

// targetParts splits Target into the name and the dim "+N" suffix.
func targetParts(c *store.ViewCall) (name, suffix string) {
	switch c.Category {
	case "read", "edit", "write":
		if name, suffix = fileParts(c); name != "" {
			return name, suffix
		}
	case "command":
		return strings.TrimRight("$ "+text.Clean(c.Command), " "), ""
	case "search":
		return text.Clean(c.Pattern), ""
	case "agent":
		return "sub-agent", ""
	}
	return text.Clean(c.RawName), ""
}

// cellWidth returns the layout width of s, as Grid.Text counts it.
func cellWidth(s string) int { return cells.Width(s) }

// clipWidth returns the longest prefix of s that fits in w cells.
func clipWidth(s string, w int) string {
	n := 0
	var b strings.Builder
	for gr := uniseg.NewGraphemes(s); gr.Next(); {
		gw := cells.GraphemeWidth(gr.Str())
		if n+gw > w {
			break
		}
		n += gw
		b.WriteString(gr.Str())
	}
	return b.String()
}

// fitTarget returns the target split into name and suffix so that both fit in
// max cells. A file name that is too long keeps its extension when the
// extension fits; the suffix always stays.
func fitTarget(c *store.ViewCall, max int) (name, suffix string) {
	name, suffix = targetParts(c)
	if !isFileCategory(c.Category) {
		return name, ""
	}
	avail := max - cellWidth(suffix)
	if cellWidth(name) <= avail {
		return name, suffix
	}
	if ext := path.Ext(name); ext != "" && ext != name && cellWidth(ext)+2 <= avail {
		stem := strings.TrimSuffix(name, ext)
		return clipWidth(stem, avail-cellWidth(ext)-1) + "…" + ext, suffix
	}
	return name, suffix // Grid.Text cuts it with an ellipsis
}

// putTarget writes the target at (x, y) in at most max cells and returns the
// cells used.
func putTarget(g *cells.Grid, x, y, max int, c *store.ViewCall) int {
	name, suffix := fitTarget(c, max)
	n := g.Text(x, y, max, name, cells.Style{Role: cells.RBright})
	if suffix != "" {
		n += g.Text(x+n, y, max-n, suffix, cells.Style{Role: cells.RDim})
	}
	return n
}

// line2 returns the second line of a call card.
func line2(root string, c *store.ViewCall) string {
	repo, folder := Where(root, c)
	join := func(prefix string) string {
		if repo == "" {
			return prefix
		}
		return prefix + " · " + repo
	}
	switch c.Category {
	case "command":
		return join("shell")
	case "search":
		return join("grep")
	case "agent":
		return "spawn sub-agent"
	}
	if isFileCategory(c.Category) {
		return folder
	}
	return ""
}

// CallCard draws a call card, 28 wide and CallCardHeight high: 7, or taller
// for a command with more output lines than 2 when OutputLines is above 2
// (the focused card shows up to 10 lines).
func CallCard(in CallIn) *cells.Grid {
	c := in.Call
	h := CallCardHeight(c, in.OutputLines)
	extra := h - 5 // output or "started" lines
	g := cells.New(CallW, h)

	cat := cells.RoleOf(c.Category)
	dim := cells.Style{Role: cells.RDim}
	bright := cells.Style{Role: cells.RBright}
	failed := c.Ended && c.Ok != nil && !*c.Ok

	border := cells.Style{Role: cat}
	switch {
	case failed:
		border = cells.Style{Role: cells.RFail}
	case c.Ended:
		border = dim
	}
	g.Box(0, 0, CallW, h, cells.Square, border)

	// Title: label (and +N open) on the left, elapsed time on the right.
	end := in.Now
	if c.Ended {
		end = c.EndMs
	}
	elapsed := Elapsed(end - c.StartMs)
	right := cellWidth(elapsed)
	titleMax := CallW - 2 - 6 - right - 1
	g.Text(2, 0, 1, " ", border)
	n := g.Text(3, 0, titleMax, schema.Label(c.Category), cells.Style{Role: cat, Bold: true})
	if in.OtherOpen > 0 && n < titleMax {
		n += g.Text(3+n, 0, titleMax-n, fmt.Sprintf(" +%d open", in.OtherOpen), dim)
	}
	g.Text(3+n, 0, 1, " ", border)
	rx := CallW - 4 - right
	g.Text(rx, 0, 1, " ", border)
	rs := bright
	if c.Ended {
		rs = dim
	}
	g.Text(rx+1, 0, right, elapsed, rs)
	g.Text(rx+1+right, 0, 1, " ", border)

	// Lines 1 and 2.
	putTarget(g, callTextX, 1, callTextW, c)
	g.Text(callTextX, 2, callTextW, text.Clean(line2(in.ProjectRoot, c)), dim)

	// Output or start time.
	if c.Category == "command" {
		lines := text.CleanLines(c.Output)
		if len(lines) > extra {
			lines = lines[len(lines)-extra:]
		}
		style := cells.Style{Role: cells.RCommand}
		for i, l := range lines {
			st := style
			if failed && i == len(lines)-1 {
				st = cells.Style{Role: cells.RFail}
			}
			g.Text(callTextX, 3+i, callTextW, "> "+l, st)
		}
	} else {
		g.Text(callTextX, 3, callTextW, "started "+time.UnixMilli(c.StartMs).Format("15:04:05"), dim)
	}

	footer(g, h-2, in, failed)
	return g
}

// footer draws the last content line of a call card.
func footer(g *cells.Grid, y int, in CallIn, failed bool) {
	c := in.Call
	dim := cells.Style{Role: cells.RDim}
	if need := humanRequest(c); need != "" {
		g.Text(callTextX, y, callTextW, "Needs "+strings.ToLower(need), cells.Style{Role: cells.RSearch, Bold: true})
		return
	}
	switch {
	case !c.Ended && in.Stale:
		g.Text(callTextX, y, callTextW, "no events "+seenAge(in.Now-in.LastEventMs), dim)
	case !c.Ended:
		secs := max(in.Now-c.StartMs, 0) / 1000
		filled := int(secs%callBarW) + 1
		for i := 0; i < callBarW; i++ {
			if i < filled {
				g.Text(callTextX+i, y, 1, "█", cells.Style{Role: cells.RoleOf(c.Category)})
			} else {
				g.Text(callTextX+i, y, 1, "░", cells.Style{Role: cells.RFaint})
			}
		}
		frame := spinFrames[((in.Spin%4)+4)%4]
		g.Text(callTextX+callBarW+3, y, callTextW-callBarW-3, frame+" running", dim)
	case failed:
		n := g.Text(callTextX, y, callTextW, "[FAIL]", cells.Style{Role: cells.RFail, Inverse: true})
		if c.ExitCode != nil {
			g.Text(callTextX+n, y, callTextW-n, fmt.Sprintf(" exit %d", *c.ExitCode), cells.Style{Role: cells.RFail})
		}
	case c.Ok == nil:
		n := g.Text(callTextX, y, callTextW, "[ ?? ]", dim)
		g.Text(callTextX+n, y, callTextW-n, " no result", dim)
	default:
		g.Text(callTextX, y, callTextW, "[ OK ]", cells.Style{Role: cells.ROK, Bold: true})
	}
}
