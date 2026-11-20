package store

import (
	"iter"
	"slices"

	"agent-view/internal/ordered"
	"agent-view/internal/schema"
)

// callTable is a host's call list in storing order, indexed per agent: the open
// calls newest StartMs first, and the ended calls newest EndMs first; on equal
// times, the last stored first. The lists hold exactly the calls of the list,
// so OpenCalls and RecentEnded read them without a sort. A stored call ends
// only through end, because the lists are ordered by its times.
type callTable struct {
	list  *ordered.Map[callKey, *ViewCall]
	open  map[string][]*ViewCall
	ended map[string][]*ViewCall
}

func newCallTable() *callTable {
	return &callTable{list: ordered.NewMap[callKey, *ViewCall](), open: map[string][]*ViewCall{}, ended: map[string][]*ViewCall{}}
}

func (t *callTable) len() int                           { return t.list.Len() }
func (t *callTable) has(k callKey) bool                 { return t.list.Has(k) }
func (t *callTable) get(k callKey) (*ViewCall, bool)    { return t.list.Get(k) }
func (t *callTable) all() iter.Seq2[callKey, *ViewCall] { return t.list.All() }

// set stores a call under its key. A call stored under the key before is
// replaced, and the call keeps that position in the list.
func (t *callTable) set(c *ViewCall) {
	k := callKey{c.AgentID, c.CallID}
	if old, ok := t.list.Get(k); ok {
		t.unindex(old)
	}
	t.list.Set(k, c)
	t.index(c)
}

// delete removes a call. The loop body of all may delete the current key.
func (t *callTable) delete(k callKey) {
	if c, ok := t.list.Get(k); ok {
		t.unindex(c)
		t.list.Delete(k)
	}
}

// end ends a stored call at at and moves it to the ended list of its agent.
func (t *callTable) end(c *ViewCall, e schema.Envelope, at int64) {
	t.unindex(c)
	endCall(c, e, at)
	t.index(c)
}

// listOf returns the per-agent lists that hold c, and the order of those lists.
func (t *callTable) listOf(c *ViewCall) (map[string][]*ViewCall, func(a, b *ViewCall) int) {
	if c.Ended {
		return t.ended, byEnd
	}
	return t.open, byStart
}

var (
	byStart = newestFirst(func(c *ViewCall) int64 { return c.StartMs })
	byEnd   = newestFirst(func(c *ViewCall) int64 { return c.EndMs })
)

func (t *callTable) index(c *ViewCall) {
	lists, order := t.listOf(c)
	list := lists[c.AgentID]
	i, _ := slices.BinarySearchFunc(list, c, order)
	lists[c.AgentID] = slices.Insert(list, i, c)
}

func (t *callTable) unindex(c *ViewCall) {
	lists, order := t.listOf(c)
	list := lists[c.AgentID]
	// seq is unique within a host, so the order is total and finds c itself.
	i, found := slices.BinarySearchFunc(list, c, order)
	if !found || list[i] != c {
		return
	}
	if list = slices.Delete(list, i, i+1); len(list) == 0 {
		delete(lists, c.AgentID)
	} else {
		lists[c.AgentID] = list
	}
}
