package logs

import (
	"regexp"
	"strings"

	"agent-view/internal/ordered"
	"agent-view/internal/text"
	"agent-view/internal/wire"
)

// subagentFile matches the transcript file of a sub-agent. The group is the
// sub-agent id. As in JavaScript, "." excludes every line terminator.
var subagentFile = regexp.MustCompile(`[/\\]subagents[/\\]agent-([^\n\r\x{2028}\x{2029}]+)\.jsonl$`)

type pendingCall struct {
	category string
	input    *Obj
	files    []string
	writes   bool // a successful end claims an "edit" write on every file
}

// ClaudeAdapter turns Claude Code transcript records into normalized events.
// One instance serves every transcript file; identity comes from the records.
type ClaudeAdapter struct {
	host    string
	agents  map[string]*agentState
	emitted map[string]*emittedState
	pending *ordered.Map[string, pendingCall]
}

// NewClaudeAdapter returns an adapter whose agent ids start with host.
func NewClaudeAdapter(host string) *ClaudeAdapter {
	return &ClaudeAdapter{
		host:    host,
		agents:  map[string]*agentState{},
		emitted: map[string]*emittedState{},
		pending: ordered.NewMap[string, pendingCall](),
	}
}

// Ingest turns one decoded record into events. file is the transcript path
// and scope names the log root.
func (a *ClaudeAdapter) Ingest(record any, file, scope string) []wire.SourceEvent {
	rec := obj(record)
	if rec == nil {
		return nil
	}
	sessionID, ok := str(rec.Get("sessionId"))
	if !ok {
		return nil
	}
	ts, _ := str(rec.Get("timestamp"))
	var events []wire.SourceEvent

	root := a.host + "/claude:" + scope + ":" + sessionID
	child, _ := str(rec.Get("agentId"))
	if child == "" {
		if m := subagentFile.FindStringSubmatch(file); m != nil {
			child = m[1]
		}
	}
	id := root
	if child != "" {
		id = root + ":" + child
	}
	existing := a.agents[id]
	var cwd *string
	if s, ok := str(rec.Get("cwd")); ok {
		cwd = &s
	} else if existing != nil {
		cwd = existing.cwd
	}
	message := obj(rec.Get("message"))
	recType := rec.Get("type")
	var model string
	if message != nil && recType == "assistant" {
		model, _ = str(message.Get("model"))
	}

	next := identity{cwd: cwd, role: "main", link: "root"}
	if child != "" {
		next.role = "sub-agent"
		next.link = "session"
	}
	switch {
	case existing != nil:
		next.name = existing.name
	case child != "":
		next.name = "Sub-agent " + text.SliceUTF16(child, 8)
	default:
		next.name = "Claude Code session"
	}
	if existing != nil && existing.link == "confirmed" {
		next.parentID = existing.parentID
		next.link = "confirmed"
	} else if child != "" {
		next.parentID = &root
	}
	if model != "" && model != "<synthetic>" {
		next.model = model
	}

	agent := a.touch(id, ts, next)
	if recType == "custom-title" {
		if s, ok := str(rec.Get("customTitle")); ok {
			agent.name = s
		}
	}
	if recType == "agent-name" {
		if s, ok := str(rec.Get("agentName")); ok {
			agent.name = s
		}
	}

	var blocks []*Obj
	if content, ok := message.Get("content").([]any); ok {
		for _, el := range content {
			if b := obj(el); b != nil {
				blocks = append(blocks, b)
			}
		}
	}
	results := 0
	for _, b := range blocks {
		if b.Get("type") == "tool_result" {
			results++
		}
	}
	single := results == 1
	var toolEvents []wire.SourceEvent
	for _, b := range blocks {
		switch b.Get("type") {
		case "tool_use":
			a.toolUse(agent, id, cwd, b, ts, &toolEvents)
		case "tool_result":
			a.toolResult(agent, id, root, b, rec, single, ts, &toolEvents)
		}
	}

	endsTurn := (recType == "system" && rec.Get("subtype") == "turn_duration") ||
		(message != nil && message.Get("stop_reason") == "end_turn")

	// Order: identity, working status, tool events, then idle status.
	if ts != "" {
		a.flushAgent(id, agent, ts, &events)
		a.flushStatus(id, agent, ts, &events)
		events = append(events, toolEvents...)
	}
	if endsTurn {
		agent.status = "idle"
	}
	if ts != "" {
		a.flushStatus(id, agent, ts, &events)
	}
	return events
}

// touch creates the agent when it is new, sets its identity fields (the model
// only when fields has one) and records ts as its last event time.
func (a *ClaudeAdapter) touch(id, ts string, fields identity) *agentState {
	cur := a.agents[id]
	if cur == nil {
		cur = &agentState{
			identity:    identity{name: text.SliceUTF16(id[strings.LastIndex(id, ":")+1:], 12), role: "main", link: "root"},
			status:      "idle",
			lastEventAt: ts,
		}
	}
	model := cur.model
	cur.identity = fields
	if fields.model == "" {
		cur.model = model
	}
	if ts != "" {
		cur.lastEventAt = ts
	}
	a.agents[id] = cur
	return cur
}

// flushAgent emits agent.upsert when an identity field differs from what was
// last emitted.
func (a *ClaudeAdapter) flushAgent(id string, s *agentState, ts string, out *[]wire.SourceEvent) {
	flushAgent(a.emitted, "claude", id, s, ts, out)
}

func (a *ClaudeAdapter) flushStatus(id string, s *agentState, ts string, out *[]wire.SourceEvent) {
	flushStatus(a.emitted, id, s, ts, out)
}

// callFiles names the files of a call. Only explicit file tools claim files.
// Shell text and search results never do.
func callFiles(name, category string, input *Obj, cwd *string) (files []string, writes bool) {
	switch {
	case category == "read":
		if f, ok := resolveFile(cwd, input.Get("file_path")); ok {
			files = []string{f}
		}
	case name == "Write" || name == "Edit" || name == "MultiEdit" || name == "NotebookEdit":
		path := input.Get("file_path")
		if path == nil {
			path = input.Get("notebook_path")
		}
		if f, ok := resolveFile(cwd, path); ok {
			return []string{f}, true
		}
	case category == "search":
		if f, ok := resolveFile(cwd, input.Get("path")); ok {
			files = []string{f}
		}
	}
	return files, false
}

// outputText joins the text blocks of a tool result.
func outputText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	blocks, ok := content.([]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, el := range blocks {
		b := obj(el)
		if b == nil || b.Get("type") != "text" {
			continue
		}
		if text, ok := b.Get("text").(string); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func (a *ClaudeAdapter) toolUse(agent *agentState, agentID string, cwd *string, b *Obj, ts string, out *[]wire.SourceEvent) {
	callID, ok1 := str(b.Get("id"))
	name, ok2 := str(b.Get("name"))
	if !ok1 || !ok2 || ts == "" {
		return
	}
	key := agentID + ":" + callID
	if a.pending.Has(key) {
		return
	}
	input := obj(b.Get("input")) // nil reads as an empty object
	category := wire.CategoryFor("claude", name)
	files, writes := callFiles(name, category, input, cwd)
	a.pending.Set(key, pendingCall{category: category, input: input, files: files, writes: writes})
	if a.pending.Len() > MaxPending {
		if oldest, _, ok := a.pending.First(); ok {
			a.pending.Delete(oldest)
		}
	}

	agent.status = "working"
	call := wire.Call{AgentID: agentID, CallID: callID, StartedAt: ts, Category: category, RawName: name, Files: files}
	if command, ok := input.Get("command").(string); ok && category == "command" {
		call.Command = command
	}
	if pattern, ok := input.Get("pattern").(string); ok && category == "search" {
		call.Pattern = pattern
	}
	*out = append(*out, wire.SourceEvent{Ts: ts, Ev: wire.ToolStart{Call: call}})
}

func (a *ClaudeAdapter) toolResult(agent *agentState, agentID, root string, b, rec *Obj, single bool, ts string, out *[]wire.SourceEvent) {
	callID, ok := str(b.Get("tool_use_id"))
	if !ok || ts == "" {
		return
	}
	key := agentID + ":" + callID
	call, found := a.pending.Get(key)
	if !found {
		return
	}
	a.pending.Delete(key)
	success := b.Get("is_error") != true

	// An Agent/Task result that names the sub-agent confirms the parent link.
	// toolUseResult belongs to the record, not to a block: trust it only when
	// the record has one result block.
	var spawned string
	if single {
		if r := obj(rec.Get("toolUseResult")); r != nil {
			spawned, _ = str(r.Get("agentId"))
		}
	}
	if call.category == "agent" && spawned != "" && success {
		childID := root + ":" + spawned
		known := a.agents[childID]
		cwd := agent.cwd
		if known != nil && known.cwd != nil {
			cwd = known.cwd
		}
		name, ok := str(call.input.Get("description"))
		if !ok {
			name = "Sub-agent " + text.SliceUTF16(spawned, 8)
		}
		parent := agentID
		child := a.touch(childID, ts, identity{
			name: name, role: "sub-agent", parentID: &parent, link: "confirmed", cwd: cwd,
		})
		a.flushAgent(childID, child, ts, out)
	}

	if call.category == "command" {
		*out = append(*out, OutputEvents(ts, agentID, callID, outputText(b.Get("content")))...)
	}

	var writes []wire.Write
	if success && len(call.files) > 0 && call.writes {
		for _, p := range call.files {
			writes = append(writes, wire.Write{Path: p, Op: "edit"})
		}
	}
	*out = append(*out, wire.SourceEvent{Ts: ts, Ev: wire.ToolEnd{AgentID: agentID, CallID: callID, Ok: &success, Writes: writes}})
}
