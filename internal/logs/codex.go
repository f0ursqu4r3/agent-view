package logs

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agent-view/internal/text"
	"agent-view/internal/wire"
)

// patchLine matches a file line of an apply_patch text. As in JavaScript, "."
// excludes every line terminator.
var patchLine = regexp.MustCompile(`^\*\*\* (Add|Update|Delete|Move to)(?: File)?: ([^\n\r\x{2028}\x{2029}]+)$`)

// The prototype's apply_patch rule. Unknown output is not success.
var (
	patchSuccess = regexp.MustCompile(`Success\. Updated the following files:`)
	patchFailure = regexp.MustCompile(`(?i)Failed to|Error:|patch rejected`)
	exitLine     = regexp.MustCompile(`Process exited with code (-?\d+)`)
)

var patchOps = map[string]string{"Add": "create", "Update": "edit", "Delete": "delete", "Move to": "move"}

var fileChangeOps = map[string]string{"add": "create", "update": "edit", "delete": "delete"}

// nativeTypes are the item types of an item_completed record that become a
// start and end pair.
var nativeTypes = map[string]bool{"CommandExecution": true, "FileChange": true, "McpToolCall": true, "CollabAgentToolCall": true}

type codexPending struct {
	name     string
	category string
	writes   []wire.Write
}

// CodexAdapter turns Codex rollout records into normalized events. One
// instance serves every log file; identity comes from the records.
type CodexAdapter struct {
	host    string
	agents  map[string]*agentState
	emitted map[string]*emittedState
	// sources maps a log file to the thread agent that owns its direct records.
	sources map[string]string
	pending *orderedMap[codexPending]
	// started and ended remember calls that already produced a tool.start or
	// tool.end. Native and direct receipts share call IDs.
	started *boundedSet
	ended   *boundedSet
}

// NewCodexAdapter returns an adapter whose agent ids start with host.
func NewCodexAdapter(host string) *CodexAdapter {
	return &CodexAdapter{
		host:    host,
		agents:  map[string]*agentState{},
		emitted: map[string]*emittedState{},
		sources: map[string]string{},
		pending: newOrderedMap[codexPending](),
		started: newBoundedSet(MaxPending),
		ended:   newBoundedSet(MaxPending),
	}
}

// codexRecord is the state of one Ingest call.
type codexRecord struct {
	a  *CodexAdapter
	ts string
	// touched lists the agents this record touched, in first-touch order.
	touched []string
	tools   []wire.SourceEvent
}

// touch creates the agent when it is new, applies set (when not nil) and
// records ts as its last event time.
func (r *codexRecord) touch(id string, set func(*agentState)) *agentState {
	cur := r.a.agents[id]
	if cur == nil {
		cur = &agentState{
			identity:    identity{name: text.SliceUTF16(id[strings.LastIndex(id, ":")+1:], 12), role: "main", link: "root"},
			status:      "idle",
			lastEventAt: r.ts,
		}
	}
	if set != nil {
		set(cur)
	}
	if r.ts != "" {
		cur.lastEventAt = r.ts
	}
	r.a.agents[id] = cur
	seen := false
	for _, t := range r.touched {
		if t == id {
			seen = true
			break
		}
	}
	if !seen {
		r.touched = append(r.touched, id)
	}
	return cur
}

func (r *codexRecord) emit(ev wire.Event, ts string) {
	r.tools = append(r.tools, wire.SourceEvent{Ts: ts, Ev: ev})
}

// Ingest turns one decoded record into events. file is the log path and scope
// names the log root.
func (a *CodexAdapter) Ingest(record any, file, scope string) []wire.SourceEvent {
	rec := obj(record)
	if rec == nil {
		return nil
	}
	ts, _ := str(rec.Get("timestamp"))
	p := obj(rec.Get("payload")) // nil reads as an empty object
	prefix := a.host + "/codex:" + scope + ":"
	r := &codexRecord{a: a, ts: ts}
	recType := rec.Get("type")
	pType := p.Get("type")

	if recType == "session_meta" {
		threadID, ok := str(p.Get("id"))
		if !ok {
			threadID, ok = str(p.Get("session_id"))
		}
		if ok {
			id := prefix + threadID
			a.sources[file] = id
			spawn := obj(obj(obj(p.Get("source")).Get("subagent")).Get("thread_spawn"))
			parent, hasParent := str(p.Get("parent_thread_id"))
			if !hasParent {
				parent, hasParent = str(spawn.Get("parent_thread_id"))
			}
			var cwd *string
			if s, ok := str(p.Get("cwd")); ok {
				cwd = &s
			} else if known := a.agents[id]; known != nil {
				cwd = known.cwd
			}
			var name string
			if path, ok := str(spawn.Get("agent_path")); ok {
				name = path[strings.LastIndex(path, "/")+1:]
			} else if p.Get("thread_source") == "guardian_review" {
				name = "Approval review"
			} else {
				name = "Codex session"
			}
			r.touch(id, func(s *agentState) {
				s.cwd = cwd
				s.parentID = nil
				s.link, s.role = "root", "main"
				if hasParent {
					parentID := prefix + parent
					s.parentID = &parentID
					s.link, s.role = "confirmed", "sub-agent"
				}
				s.name = name
			})
		}
	}

	if recType == "event_msg" && pType == "item_completed" {
		a.nativeItem(r, file, p, prefix)
	}

	if id := a.sources[file]; id != "" {
		agent := r.touch(id, nil)
		if recType == "turn_context" {
			if cwd, ok := str(p.Get("cwd")); ok {
				agent.cwd = &cwd
			}
			if model, ok := str(p.Get("model")); ok {
				agent.model = model
			}
		}
		if pType == "task_started" {
			agent.status = "working"
		}
		// A finished task ends a turn, not the session.
		if pType == "task_complete" || pType == "task_completed" {
			agent.status = "idle"
		}
		if pType == "turn_aborted" {
			agent.status = "interrupted"
		}
		if pType == "function_call" || pType == "custom_tool_call" {
			a.directCall(r, agent, id, p)
		}
		if pType == "function_call_output" || pType == "custom_tool_call_output" {
			a.directOutput(r, id, p)
		}
	}

	// Order: identity, status, then tool events. Without a timestamp, state is
	// learned but nothing is emitted.
	var events []wire.SourceEvent
	if ts != "" {
		for _, id := range r.touched {
			flushAgent(a.emitted, "codex", id, a.agents[id], ts, &events)
		}
		for _, id := range r.touched {
			flushStatus(a.emitted, id, a.agents[id], ts, &events)
		}
		events = append(events, r.tools...)
	}
	return events
}

// parseJSON decodes a string as JSON (null on a syntax error) and returns any
// other value as it is.
func parseJSON(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	parsed, err := Decode(s)
	if err != nil {
		return nil
	}
	return parsed
}

// lastSegment returns the part of s after the last sep.
func lastSegment(s, sep string) string {
	return s[strings.LastIndex(s, sep)+len(sep):]
}

// categoryOf maps a Codex tool name to a category. categoryFor knows bare
// names; direct calls may arrive namespaced, such as functions.apply_patch.
func categoryOf(name string) string {
	direct := wire.CategoryFor("codex", name)
	if direct == "other" && strings.Contains(name, ".") {
		return wire.CategoryFor("codex", lastSegment(name, "."))
	}
	return direct
}

// codexOutputText joins the text parts of a result. Unlike Claude's blocks,
// they need no type.
func codexOutputText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	parts, ok := v.([]any)
	if !ok {
		return ""
	}
	var texts []string
	for _, el := range parts {
		if b := obj(el); b != nil {
			if text, ok := b.Get("text").(string); ok {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// commandText returns the command of a call: a string, or the strings of an
// array joined by spaces.
func commandText(v any) (string, bool) {
	switch c := v.(type) {
	case string:
		return c, true
	case []any:
		var parts []string
		for _, el := range c {
			if s, ok := el.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " "), true
	}
	return "", false
}

// patchWrites returns the writes an apply_patch call claims. Only apply_patch
// claims files. Shell text, wrapper code and command output never do.
func patchWrites(name string, input any, cwd *string) []wire.Write {
	if lastSegment(name, ".") != "apply_patch" {
		return nil
	}
	var patch string
	if s, ok := input.(string); ok {
		patch = s
	} else {
		data := obj(input)
		if s, ok := str(data.Get("patch")); ok {
			patch = s
		} else if s, ok := str(data.Get("input")); ok {
			patch = s
		}
	}
	var writes []wire.Write
	for _, line := range strings.Split(patch, "\n") {
		m := patchLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if file, ok := resolveFile(cwd, m[2]); ok {
			writes = append(writes, wire.Write{Path: file, Op: patchOps[m[1]]})
		}
	}
	return writes
}

func uniquePaths(files []string) []string {
	seen := make(map[string]bool, len(files))
	var out []string
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func writePaths(writes []wire.Write) []string {
	files := make([]string, len(writes))
	for i, w := range writes {
		files[i] = w.Path
	}
	return files
}

// directExit returns the output and the exit code of a direct shell result:
// structured metadata, else Codex's own status line.
func directExit(text string) (string, *int) {
	if strings.HasPrefix(text, "{") {
		if parsed := obj(parseJSON(text)); parsed != nil {
			if output, ok := parsed.Get("output").(string); ok {
				if code, ok := isInteger(obj(parsed.Get("metadata")).Get("exit_code")); ok {
					return output, &code
				}
				return output, nil
			}
		}
	}
	if m := exitLine.FindStringSubmatch(text); m != nil {
		// A code that does not fit an int is treated as absent. A code beyond
		// 2^53 loses precision, as JavaScript's Number does.
		if n, err := strconv.Atoi(m[1]); err == nil {
			if f := float64(n); math.Abs(f) < 1<<63 {
				code := int(f)
				return text, &code
			}
		}
	}
	return text, nil
}

func (a *CodexAdapter) directCall(r *codexRecord, agent *agentState, agentID string, p *Obj) {
	callID, ok1 := str(p.Get("call_id"))
	name, ok2 := str(p.Get("name"))
	if !ok1 || !ok2 || r.ts == "" {
		return
	}
	key := agentID + ":" + callID
	if a.started.has(key) {
		return
	}
	a.started.add(key)
	raw := p.Get("arguments")
	if raw == nil {
		raw = p.Get("input")
	}
	input := parseJSON(raw)
	if input == nil {
		input = raw
	}
	category := categoryOf(name)
	writes := patchWrites(name, input, agent.cwd)
	a.pending.set(key, codexPending{name: name, category: category, writes: writes})
	if a.pending.len() > MaxPending {
		a.pending.deleteOldest()
	}

	agent.status = "working"
	call := wire.Call{AgentID: agentID, CallID: callID, StartedAt: r.ts, Category: category, RawName: name}
	if len(writes) > 0 {
		call.Files = uniquePaths(writePaths(writes))
	}
	if in := obj(input); category == "command" && in != nil {
		command, ok := commandText(in.Get("command"))
		if !ok {
			command, ok = commandText(in.Get("cmd"))
		}
		if ok {
			call.Command = text.SliceUTF16(command, MaxCommandChars)
		}
	}
	r.emit(wire.ToolStart{Call: call}, r.ts)
}

func (a *CodexAdapter) directOutput(r *codexRecord, agentID string, p *Obj) {
	callID, ok := str(p.Get("call_id"))
	if !ok || r.ts == "" {
		return
	}
	key := agentID + ":" + callID
	call, found := a.pending.get(key)
	if !found {
		return
	}
	a.pending.delete(key)
	a.ended.add(key)
	text := codexOutputText(p.Get("output"))

	if call.category == "command" {
		output, exitCode := directExit(text)
		r.tools = append(r.tools, OutputEvents(r.ts, agentID, callID, output)...)
		okEnd := exitCode == nil || *exitCode == 0
		r.emit(wire.ToolEnd{AgentID: agentID, CallID: callID, Ok: &okEnd, ExitCode: exitCode}, r.ts)
		return
	}
	// Codex apply_patch uses an explicit success receipt. A result without it
	// is not success.
	success := true
	if lastSegment(call.name, ".") == "apply_patch" {
		success = patchSuccess.MatchString(text) && !patchFailure.MatchString(text)
	}
	end := wire.ToolEnd{AgentID: agentID, CallID: callID, Ok: &success}
	if success && len(call.writes) > 0 {
		end.Writes = call.writes
	}
	r.emit(end, r.ts)
}

// isoMs formats Unix milliseconds like Date.prototype.toISOString, which
// writes a year outside 0000 to 9999 as a sign and six digits.
func isoMs(ms int64) string {
	t := time.UnixMilli(ms).UTC()
	y := t.Year()
	if y >= 0 && y <= 9999 {
		return wire.FormatISO(ms)
	}
	sign := '+'
	if y < 0 {
		sign, y = '-', -y
	}
	return fmt.Sprintf("%c%06d-%02d-%02dT%02d:%02d:%02d.%03dZ", sign, y, int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1e6)
}

// nativeItem handles an item_completed record. It describes a finished nested
// tool: it becomes a start and end pair.
func (a *CodexAdapter) nativeItem(r *codexRecord, file string, p *Obj, prefix string) {
	item := obj(p.Get("item"))
	if item == nil {
		return
	}
	typ, ok := str(item.Get("type"))
	if !ok || !nativeTypes[typ] {
		return
	}
	callID, hasCallID := str(item.Get("id"))
	if !hasCallID {
		callID, hasCallID = str(item.Get("call_id"))
	}
	agentID := a.sources[file]
	if thread, ok := str(p.Get("thread_id")); ok {
		agentID = prefix + thread
	}
	if !hasCallID || agentID == "" {
		return
	}

	agentKnown := a.agents[agentID] != nil
	itemCwd, hasItemCwd := str(item.Get("cwd"))
	// An unknown thread gets a minimal identity. Its cwd is never guessed from
	// another thread.
	agent := r.touch(agentID, func(s *agentState) {
		if !agentKnown && hasItemCwd {
			s.cwd = &itemCwd
		}
	})
	cwd := agent.cwd
	if hasItemCwd {
		cwd = &itemCwd
	}

	ts := r.ts
	// A NaN or infinite time fails the comparison too.
	if ms, ok := p.Get("completed_at_ms").(float64); ok && math.Abs(ms) < 8.64e15 {
		ts = isoMs(int64(math.Trunc(ms)))
	}
	if ts == "" {
		return
	}

	key := agentID + ":" + callID
	alreadyStarted := a.started.has(key)
	if a.ended.has(key) {
		return
	}
	a.started.add(key)
	a.ended.add(key)
	a.pending.delete(key)

	category := wire.CategoryFor("codex", typ)
	success := item.Get("status") == "completed"
	call := wire.Call{AgentID: agentID, CallID: callID, StartedAt: ts, Category: category, RawName: typ}
	end := wire.ToolEnd{AgentID: agentID, CallID: callID}

	switch changes := obj(item.Get("changes")); {
	case typ == "CommandExecution":
		if command, ok := commandText(item.Get("command")); ok {
			call.Command = text.SliceUTF16(command, MaxCommandChars)
		}
		if code, ok := isInteger(item.Get("exit_code")); ok {
			end.ExitCode = &code
		}
		success = success && (end.ExitCode == nil || *end.ExitCode == 0)
	case typ == "FileChange" && changes != nil:
		var files []string
		var writes []wire.Write
		for _, e := range changes.Entries() {
			change := obj(e.Val)
			changeType, _ := change.Get("type").(string)
			op, known := fileChangeOps[changeType]
			if change == nil || !known {
				continue
			}
			resolved, ok := resolveFile(cwd, e.Key)
			if !ok {
				continue
			}
			files = append(files, resolved)
			writes = append(writes, wire.Write{Path: resolved, Op: op})
			if dest, ok := resolveFile(cwd, change.Get("move_path")); ok {
				files = append(files, dest)
				writes = append(writes, wire.Write{Path: dest, Op: "move"})
			}
		}
		if len(files) > 0 {
			call.Files = uniquePaths(files)
		}
		if success && len(writes) > 0 {
			end.Writes = writes
		}
	}
	end.Ok = &success

	if !alreadyStarted {
		r.emit(wire.ToolStart{Call: call}, ts)
	}
	if output, ok := item.Get("aggregated_output").(string); ok && typ == "CommandExecution" {
		r.tools = append(r.tools, OutputEvents(ts, agentID, callID, output)...)
	}
	r.emit(end, ts)
}
