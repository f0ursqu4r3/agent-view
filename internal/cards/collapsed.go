package cards

import (
	"agent-view/internal/cells"
	"agent-view/internal/schema"
	"agent-view/internal/store"
	"agent-view/internal/text"
)

// CollapsedRow draws a collapsed row, one line:
// ▸ name · provider · state age · last LABEL target ok|FAIL|??
// The age is the time since the agent's last event. Text that does not fit
// is cut with an ellipsis.
func CollapsedRow(width int, a store.ViewAgent, st StateWord, last *store.ViewCall, now int64) *cells.Grid {
	g := cells.New(width, 1)
	dim := cells.Style{Role: cells.RDim}
	x := 0
	dropped := false
	put := func(s string, style cells.Style) {
		if s == "" {
			return
		}
		if x >= width {
			dropped = true
			return
		}
		x += g.Text(x, 0, width-x, s, style)
	}
	// A segment cut off entirely still shows as a cut.
	defer func() {
		if dropped && width > 0 {
			g.Text(width-1, 0, 1, "…", dim)
		}
	}()
	prov := providerRole(a.Provider)
	put("▸ ", dim)
	put(text.Clean(a.Name), cells.Style{Role: prov, Bold: true})
	put(" · ", dim)
	put(text.Clean(a.Provider), dim)
	put(" · ", dim)
	put(st.Text, st.Style)
	put(" "+seenAge(now-a.LastEventMs), dim)
	if last == nil {
		return g
	}
	put(" · last ", dim)
	put(schema.Label(last.Category), cells.Style{Role: cells.RoleOf(last.Category), Bold: true})
	put(" ", dim)
	put(Target(last), cells.Style{Role: cells.RText})
	if !last.Ended {
		put(" running", dim)
		return g
	}
	word, style := resultWord(last, true)
	put(" "+word, style)
	return g
}
