// Package anim is the animation engine of the board. It turns store changes
// into per-agent frame values. It is pure state and frame counting: it has no
// timers, no goroutines, and no store access. One Step is one 50 ms frame.
package anim

import "agent-view/internal/store"

// Frame counts of the effects, from the spec "Animation".
const (
	callPrintFrames = 7 // call card prints one line per frame
	callCardRows    = 7
	leaveFrames     = 4 // leaving card collapses
	histPrintFrames = 4 // history card prints at the head
	whereFrames     = 6 // "where" type-out
	flashFrames     = 6 // fail flash: 3 on/off pairs
	shiftStart      = 15
	shiftStep       = 3
	shiftFrames     = shiftStart / shiftStep // frames from -15 to 0
	defaultRowH     = 9                      // full row height, until Frame reports one
)

// AgentFrame holds the values the composer applies for one agent.
//
// CallRows is the number of printed lines of the call card (0 to 7).
// LeavingRows is the number of visible lines of the card that is leaving
// (7 down to 0). HistPrintRows is the printed height of the newest history
// card (0 to 4). WhereChars is the number of frames the "where" type-out has
// run (0 to 6); the composer shows len(text)*WhereChars/6 characters.
// GutterRows is the number of drawn gutter lines of a new sub-agent, at most
// the row height. RowH is the animated row height. Flash is true on the
// inverse frames of the fail flash. HistOffset has one entry per history
// card: the column offset of that card, 0 when it is in place (index 0 is
// the card that prints; its offset is always 0).
type AgentFrame struct {
	CallRows, LeavingRows, HistPrintRows, WhereChars, GutterRows, RowH int
	Flash                                                              bool
	HistOffset                                                         []int
}

type agentState struct {
	callRows int  // 0..7
	leaving  int  // frames left of the collapse, 0..4
	histRows int  // 0..4
	where    int  // 0..6
	flash    int  // frames played, 6 = done
	shift    int  // frames left of the history shift, 0..5
	gutterOn bool // the gutter still draws
	gutter   int  // gutter lines drawn

	seen   bool // Frame has been called
	rowH   int
	target int  // last target row height from Frame
	settle bool // the next Frame snaps the row height and the gutter
}

func newState() *agentState {
	return &agentState{
		callRows: callPrintFrames,
		histRows: histPrintFrames,
		where:    whereFrames,
		flash:    flashFrames,
		target:   defaultRowH,
	}
}

func (a *agentState) active() bool {
	return a.gutterOn || a.callRows < callPrintFrames || a.leaving > 0 ||
		a.histRows < histPrintFrames || a.where < whereFrames ||
		a.flash < flashFrames || a.shift > 0 || (a.seen && a.rowH != a.target)
}

// Engine holds the animation state of all agents. It is not safe for
// concurrent use.
type Engine struct {
	reduced bool
	agents  map[string]*agentState
}

// New returns an engine. With reduced, every change is instant: Frame always
// returns done values and Active is false.
func New(reduced bool) *Engine {
	return &Engine{reduced: reduced, agents: map[string]*agentState{}}
}

func (e *Engine) state(id string) *agentState {
	a := e.agents[id]
	if a == nil {
		a = newState()
		e.agents[id] = a
	}
	return a
}

// OnChange starts the effects of a store change. History changes are
// ignored. shownCallChanged is set by the caller when the call shown on the
// agent's call card changes; only then does a CallStarted animate. A newer
// animated start replaces the effects still running for that agent, so in a
// burst only the newest start animates and the earlier ones are done at
// once. An AgentAdded here prints the row only; use OnAgentAdded for a
// sub-agent. A failed end needs OnFail.
func (e *Engine) OnChange(c store.Change, shownCallChanged bool) {
	if e.reduced || c.History {
		return
	}
	switch c.Kind {
	case store.CallStarted:
		if !shownCallChanged {
			return
		}
		a := e.state(c.AgentID)
		a.callRows = 0
		a.leaving = leaveFrames
		a.histRows = 0
		a.shift = shiftFrames
		a.where = 0
	case store.AgentAdded:
		e.OnAgentAdded(c.AgentID, false)
	}
}

// OnAgentAdded starts the appearance of a new agent. With hasParent, the
// gutter draws downward one line per frame and then the row prints; without,
// the row prints.
func (e *Engine) OnAgentAdded(agentID string, hasParent bool) {
	if e.reduced {
		return
	}
	a := e.state(agentID)
	a.callRows = 0
	a.gutterOn = hasParent
	a.gutter = 0
}

// OnFail starts the 6-frame flash of a call that ended in failure.
func (e *Engine) OnFail(agentID string) {
	if e.reduced {
		return
	}
	e.state(agentID).flash = 0
}

// Settle makes the next Frame of an agent jump to the row height it is
// given, and finish a gutter that is still drawing, so a change that is not
// live (a history event) does not animate the row. The other effects of the
// agent keep running. An agent without state has nothing to settle.
func (e *Engine) Settle(agentID string) {
	if a := e.agents[agentID]; a != nil {
		a.settle = true
	}
}

// Forget drops the state of an agent that no longer receives Frame calls.
func (e *Engine) Forget(agentID string) {
	delete(e.agents, agentID)
}

// Step advances every effect by one 50 ms frame.
func (e *Engine) Step() {
	for _, a := range e.agents {
		if a.gutterOn {
			a.gutter++
			if a.gutter >= a.target {
				a.gutterOn = false
			}
		} else if a.callRows < callPrintFrames {
			a.callRows++
		}
		if a.leaving > 0 {
			a.leaving--
		}
		if a.histRows < histPrintFrames {
			a.histRows++
		}
		if a.where < whereFrames {
			a.where++
		}
		if a.flash < flashFrames {
			a.flash++
		}
		if a.shift > 0 {
			a.shift--
		}
		if a.seen {
			switch {
			case a.rowH < a.target:
				a.rowH++
			case a.rowH > a.target:
				a.rowH--
			}
		}
	}
}

// Active reports whether any agent has an unfinished effect.
func (e *Engine) Active() bool {
	if e.reduced {
		return false
	}
	for _, a := range e.agents {
		if a.active() {
			return true
		}
	}
	return false
}

// Frame returns the values to draw for an agent. targetRowH is the row
// height the board wants now; the first call for an agent starts at it. Frame
// records the target and the first sight, so call it for every drawn agent.
// histCount is the number of history cards. Done values are returned when
// nothing animates.
func (e *Engine) Frame(agentID string, targetRowH, histCount int) AgentFrame {
	if histCount < 0 {
		histCount = 0
	}
	f := AgentFrame{
		CallRows:      callPrintFrames,
		HistPrintRows: histPrintFrames,
		WhereChars:    whereFrames,
		GutterRows:    targetRowH,
		RowH:          targetRowH,
		HistOffset:    make([]int, histCount),
	}
	if e.reduced {
		return f
	}
	a := e.state(agentID)
	a.target = targetRowH
	if !a.seen || a.settle {
		a.seen = true
		a.rowH = targetRowH
	}
	if a.settle && a.gutterOn {
		a.gutterOn = false
		a.gutter = targetRowH
	}
	a.settle = false
	f.RowH = a.rowH
	f.CallRows = a.callRows
	f.HistPrintRows = a.histRows
	f.WhereChars = a.where
	f.Flash = a.flash < flashFrames && a.flash%2 == 0
	if a.leaving > 0 {
		f.LeavingRows = (callCardRows*a.leaving + leaveFrames - 1) / leaveFrames
	}
	if a.gutterOn {
		f.GutterRows = min(a.gutter, targetRowH)
	}
	for i := 1; i < histCount; i++ {
		f.HistOffset[i] = -shiftStep * a.shift
	}
	return f
}
