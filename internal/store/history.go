package store

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"agent-view/internal/text"
)

// TargetParts returns the card target and its optional additional-file count.
func (c *ViewCall) TargetParts() (string, string) {
	switch c.Category {
	case "read", "edit", "write":
		if len(c.Files) > 0 {
			name := path.Base(text.Clean(c.Files[0]))
			if len(c.Files) > 1 {
				return name, fmt.Sprintf(" +%d", len(c.Files)-1)
			}
			return name, ""
		}
	case "command":
		return strings.TrimRight("$ "+text.Clean(text.SliceUTF16(c.Command, 4096)), " "), ""
	case "search":
		return text.Clean(c.Pattern), ""
	case "agent":
		return "sub-agent", ""
	}
	return text.Clean(c.RawName), ""
}

// HistoryCall keeps the sidebar summary when the store trims the detailed call.
type HistoryCall struct {
	Category, Target string
	StartMs, EndMs   int64
	ok               *bool
	mark             callMark
}

func (c *HistoryCall) state() callMark {
	if c == nil {
		return callUnknown
	}
	return c.mark
}

// Result returns the call state shown in the sidebar.
func (c *HistoryCall) Result() string {
	switch {
	case c.mark == callOpen:
		return "open"
	case c.mark == callDropped || c.ok == nil:
		return "unknown"
	case *c.ok:
		return "ok"
	default:
		return "fail"
	}
}

func historyTarget(c *ViewCall) string {
	if c.Category == "command" {
		return "$ " + text.CleanMultiline(c.Command)
	}
	name, suffix := c.TargetParts()
	if clipped := text.SliceUTF16(name, 128); len(clipped) < len(name) {
		return clipped + "…" + suffix
	}
	return strings.Clone(name + suffix)
}

func (t *tally) recordStart(call *ViewCall) {
	if c := t.calls[call.CallID]; c != nil {
		c.mark = callOpen
		c.Target = historyTarget(call)
		return
	}
	c := &HistoryCall{Category: call.Category, Target: historyTarget(call), StartMs: call.StartMs, mark: callOpen}
	t.calls[call.CallID] = c
	i := sort.Search(len(t.history), func(i int) bool { return t.history[i].StartMs > c.StartMs })
	t.history = slices.Insert(t.history, i, c)
}

// CallHistory returns the agent's observed calls, oldest start first.
// The returned slice and its entries belong to the store. Treat them as read-only.
func (s *Store) CallHistory(k AgentKey) []*HistoryCall {
	_, a := s.stored(k)
	if a == nil {
		return nil
	}
	return a.tally.history
}
