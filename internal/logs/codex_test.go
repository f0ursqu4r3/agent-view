package logs

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agent-view/internal/wire"
)

// cxOrd is a JSON object that keeps its key order when marshaled.
type cxOrd [][2]any

func (o cxOrd) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(kv[0])
		v, err := json.Marshal(kv[1])
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func codexA(id string) string { return "mac/codex:work:" + id }

func cxMetaWith(id, parent, cwd, ts string) any {
	p := jm{"id": id}
	if cwd != "" {
		p["cwd"] = cwd
	}
	if parent != "" {
		p["parent_thread_id"] = parent
		p["source"] = jm{"subagent": jm{"thread_spawn": jm{"parent_thread_id": parent}}}
	}
	return jsonRec(jm{"type": "session_meta", "timestamp": ts, "payload": p})
}

func cxMeta(id, parent string) any { return cxMetaWith(id, parent, "/project", t1) }

func cxRec(typ string, payload any, ts string) any {
	return jsonRec(jm{"type": typ, "timestamp": ts, "payload": payload})
}

func cxResp(payload jm, ts ...string) any { return cxRec("response_item", payload, cxTs(ts)) }
func cxEv(payload jm, ts ...string) any   { return cxRec("event_msg", payload, cxTs(ts)) }

func cxTs(ts []string) string {
	if len(ts) > 0 {
		return ts[0]
	}
	return t1
}

func cxTurn(cwd string, ts ...string) any {
	return cxRec("turn_context", jm{"cwd": cwd, "model": "m-1"}, cxTs(ts))
}

func cxPatchCall(callID, input string) any {
	return cxResp(jm{"type": "custom_tool_call", "name": "apply_patch", "call_id": callID, "input": input})
}

func cxPatchOut(callID string, output any) any {
	return cxResp(jm{"type": "custom_tool_call_output", "call_id": callID, "output": output}, t2)
}

const cxOK = "Success. Updated the following files:\nA hello.txt"

// cxDone builds an item_completed record. thread "" leaves thread_id out.
func cxDone(item jm, thread string, extra jm, ts ...string) any {
	p := jm{"type": "item_completed", "item": item}
	if thread != "" {
		p["thread_id"] = thread
	}
	for k, v := range extra {
		p[k] = v
	}
	return cxEv(p, cxTs(ts))
}

func cxPatchItem(id string, over jm) jm {
	item := jm{
		"type": "FileChange", "id": id, "status": "completed",
		"changes": jm{"/project/shared.txt": jm{"type": "update", "unified_diff": "not retained"}},
	}
	for k, v := range over {
		item[k] = v
	}
	return item
}

func cxFeed(a *CodexAdapter, r any, file ...string) []wire.SourceEvent {
	f := "/r/main.jsonl"
	if len(file) > 0 {
		f = file[0]
	}
	return a.Ingest(r, f, "work")
}

func cxSetup() *CodexAdapter {
	a := NewCodexAdapter("mac")
	cxFeed(a, cxMeta("main", ""))
	return a
}

type cxWrite struct {
	AgentID string
	Path    string
	Op      string
}

func cxWrites(events []wire.SourceEvent) []cxWrite {
	var out []cxWrite
	for _, e := range only[wire.ToolEnd](events) {
		for _, w := range e.Writes {
			out = append(out, cxWrite{e.AgentID, w.Path, w.Op})
		}
	}
	return out
}

func cxPathOps(events []wire.SourceEvent) [][2]string {
	var out [][2]string
	for _, w := range cxWrites(events) {
		out = append(out, [2]string{w.Path, w.Op})
	}
	return out
}

type cxStatus struct {
	Ts      string
	AgentID string
	Status  string
}

func cxStatuses(events []wire.SourceEvent) []cxStatus {
	var out []cxStatus
	for _, e := range events {
		if s, ok := e.Ev.(wire.AgentStatusChange); ok {
			out = append(out, cxStatus{e.Ts, s.AgentID, s.Status})
		}
	}
	return out
}

func cxEnds(events []wire.SourceEvent) []wire.ToolEnd { return only[wire.ToolEnd](events) }

func cxOnlyEnd(t *testing.T, events []wire.SourceEvent) wire.ToolEnd {
	t.Helper()
	ends := cxEnds(events)
	if len(ends) != 1 {
		t.Fatalf("tool.end count = %d, want 1 (kinds %v)", len(ends), kinds(events))
	}
	return ends[0]
}

func cxOnlyStart(t *testing.T, events []wire.SourceEvent) wire.SourceEvent {
	t.Helper()
	var out []wire.SourceEvent
	for _, e := range events {
		if _, ok := e.Ev.(wire.ToolStart); ok {
			out = append(out, e)
		}
	}
	if len(out) != 1 {
		t.Fatalf("tool.start count = %d, want 1 (kinds %v)", len(out), kinds(events))
	}
	return out[0]
}

func cxCall(t *testing.T, events []wire.SourceEvent) wire.Call {
	t.Helper()
	return cxOnlyStart(t, events).Ev.(wire.ToolStart).Call
}

func cxOk(e wire.ToolEnd) bool { return e.Ok != nil && *e.Ok }

func cxCount(events []wire.SourceEvent, kind string) int {
	n := 0
	for _, e := range events {
		if e.Ev.Kind() == kind {
			n++
		}
	}
	return n
}

func cxStr(s string) *string { return &s }

// Codex links children before parents and preserves nested parent identity
func TestCodexLinksChildrenBeforeParents(t *testing.T) {
	a := NewCodexAdapter("mac")
	var out []wire.SourceEvent
	out = append(out, cxFeed(a, cxMeta("grandchild", "child"), "/r/g.jsonl")...)
	out = append(out, cxFeed(a, cxMeta("child", "main"), "/r/c.jsonl")...)
	out = append(out, cxFeed(a, cxMeta("main", ""), "/r/m.jsonl")...)
	byID := map[string]wire.Agent{}
	for _, u := range upserts(out) {
		byID[u.ID] = u
	}
	g := byID[codexA("grandchild")]
	checkAgent(t, g, codexA("grandchild"), codexA("child"), "confirmed", "sub-agent")
	if g.Provider != "codex" || g.ProjectRoot != nil {
		t.Errorf("grandchild = %+v", g)
	}
	checkAgent(t, byID[codexA("child")], codexA("child"), codexA("main"), "confirmed", "sub-agent")
	checkAgent(t, byID[codexA("main")], codexA("main"), "<nil>", "root", "main")
}

// Codex patch success requires a paired receipt; replay is deduplicated
func TestCodexPatchSuccessRequiresReceipt(t *testing.T) {
	a := NewCodexAdapter("mac")
	cxFeed(a, cxMeta("a", "main"), "/r/a.jsonl")
	var all []wire.SourceEvent
	for i := 0; i < 2; i++ {
		all = append(all, cxFeed(a, cxPatchCall("patch", "*** Begin Patch\n*** Add File: hello.txt\n+hi\n*** End Patch"), "/r/a.jsonl")...)
		all = append(all, cxFeed(a, cxPatchOut("patch", cxOK), "/r/a.jsonl")...)
	}
	if cxCount(all, "tool.start") != 1 || cxCount(all, "tool.end") != 1 {
		t.Errorf("kinds = %v", kinds(all))
	}
	deepEq(t, "writes", cxWrites(all), []cxWrite{{codexA("a"), "/project/hello.txt", "create"}})
}

// open patch call has category edit and files, but no writes until a receipt
func TestCodexOpenPatchCall(t *testing.T) {
	a := cxSetup()
	start := cxFeed(a, cxPatchCall("p", "*** Update File: a.txt\n*** Move to: b.txt\n*** Delete File: c.txt\n*** Add File: a.txt"))
	deepEq(t, "call", cxCall(t, start), wire.Call{
		AgentID: codexA("main"), CallID: "p", Category: "edit", RawName: "apply_patch", StartedAt: t1,
		Files: []string{"/project/a.txt", "/project/b.txt", "/project/c.txt"},
	})
	if len(cxEnds(start)) != 0 {
		t.Error("tool.end before the receipt")
	}
	end := cxFeed(a, cxPatchOut("p", cxOK))
	e := cxOnlyEnd(t, end)
	if !cxOk(e) || end[len(end)-1].Ts != t2 {
		t.Errorf("end = %+v ts %s", e, end[len(end)-1].Ts)
	}
	deepEq(t, "writes", cxPathOps(end), [][2]string{
		{"/project/a.txt", "edit"}, {"/project/b.txt", "move"}, {"/project/c.txt", "delete"}, {"/project/a.txt", "create"},
	})
}

// provider, account scope and host prevent identity collisions
func TestCodexIdentityCollisions(t *testing.T) {
	a := NewCodexAdapter("mac")
	var ids []string
	for _, scope := range []string{"work", "personal"} {
		for _, u := range upserts(a.Ingest(cxMeta("same", ""), "/r/a.jsonl", scope)) {
			ids = append(ids, u.ID)
		}
	}
	deepEq(t, "ids", ids, []string{"mac/codex:work:same", "mac/codex:personal:same"})
	var other []string
	for _, u := range upserts(NewCodexAdapter("box").Ingest(cxMeta("same", ""), "/r/a.jsonl", "work")) {
		other = append(other, u.ID)
	}
	deepEq(t, "other", other, []string{"box/codex:work:same"})
}

// Codex sibling writes use agent-scoped call IDs and preserve shared file receipts
func TestCodexSiblingWrites(t *testing.T) {
	a := NewCodexAdapter("mac")
	var out []wire.SourceEvent
	for _, id := range []string{"a", "b"} {
		out = append(out, cxFeed(a, cxMeta(id, "main"), "/r/"+id+".jsonl")...)
		out = append(out, cxFeed(a, cxResp(jm{"type": "custom_tool_call", "name": "apply_patch", "call_id": "same", "input": "*** Update File: shared.txt"}), "/r/"+id+".jsonl")...)
	}
	for _, id := range []string{"b", "a"} {
		out = append(out, cxFeed(a, cxPatchOut("same", "Success. Updated the following files:\nM shared.txt"), "/r/"+id+".jsonl")...)
	}
	agents := map[string]bool{}
	for _, w := range cxWrites(out) {
		agents[w.AgentID] = true
		if w.Path != "/project/shared.txt" || w.Op != "edit" {
			t.Errorf("write = %+v", w)
		}
	}
	deepEq(t, "agents", agents, map[string]bool{codexA("a"): true, codexA("b"): true})
	var started []string
	for _, c := range calls(out) {
		started = append(started, c.AgentID)
	}
	deepEq(t, "started", started, []string{codexA("a"), codexA("b")})
}

// Codex failed patch and unpaired result cannot claim a write
func TestCodexFailedAndUnpairedPatch(t *testing.T) {
	a := NewCodexAdapter("mac")
	cxFeed(a, cxMeta("a", ""), "/r/a.jsonl")
	cxFeed(a, cxPatchCall("p", "*** Add File: new.txt"), "/r/a.jsonl")
	failed := cxFeed(a, cxPatchOut("p", "Error: patch rejected"), "/r/a.jsonl")
	if e := cxOnlyEnd(t, failed); cxOk(e) {
		t.Errorf("end = %+v", e)
	}
	if w := cxWrites(failed); len(w) != 0 {
		t.Errorf("writes = %v", w)
	}
	unpaired := cxFeed(a, cxPatchOut("missing", "Success. Updated the following files:\nA new.txt"), "/r/a.jsonl")
	if len(unpaired) != 0 {
		t.Errorf("unpaired = %v", kinds(unpaired))
	}
	// Mixed success and failure text is not a success.
	cxFeed(a, cxPatchCall("q", "*** Add File: q.txt"), "/r/a.jsonl")
	mixed := cxFeed(a, cxPatchOut("q", cxOK+"\nFailed to apply hunk"), "/r/a.jsonl")
	if e := cxOnlyEnd(t, mixed); cxOk(e) {
		t.Errorf("mixed end = %+v", e)
	}
	if w := cxWrites(mixed); len(w) != 0 {
		t.Errorf("mixed writes = %v", w)
	}
}

// Codex resolves relative paths against the latest turn directory
func TestCodexRelativePathsUseTurnDirectory(t *testing.T) {
	a := NewCodexAdapter("mac")
	cxFeed(a, cxMetaWith("a", "", "/old", t1), "/r/a.jsonl")
	ctx := upserts(cxFeed(a, cxTurn("/new"), "/r/a.jsonl"))
	if len(ctx) != 1 || derefOr(ctx[0].Cwd, "") != "/new" || ctx[0].Model != "m-1" {
		t.Fatalf("ctx = %+v", ctx)
	}
	cxFeed(a, cxPatchCall("p", "*** Add File: new.txt"), "/r/a.jsonl")
	w := cxWrites(cxFeed(a, cxPatchOut("p", cxOK), "/r/a.jsonl"))
	if len(w) == 0 || w[0].Path != "/new/new.txt" {
		t.Errorf("writes = %v", w)
	}
}

// wrapped patch uses its native thread and receipt, without parsing wrapper code
func TestCodexWrappedPatch(t *testing.T) {
	a := cxSetup()
	outer := cxFeed(a, cxResp(jm{"type": "custom_tool_call", "name": "exec", "call_id": "outer", "input": `throw new Error("must never run")`}))
	c := cxCall(t, outer)
	if c.Category != "other" || c.RawName != "exec" || c.Files != nil {
		t.Errorf("outer call = %+v", c)
	}
	out := cxFeed(a, cxDone(cxPatchItem("nested", nil), "child", nil))
	deepEq(t, "call", cxCall(t, out), wire.Call{
		AgentID: codexA("child"), CallID: "nested", StartedAt: t1, Category: "edit", RawName: "FileChange",
		Files: []string{"/project/shared.txt"},
	})
	deepEq(t, "writes", cxWrites(out), []cxWrite{{codexA("child"), "/project/shared.txt", "edit"}})
	deepEq(t, "kinds", kinds(out), []string{"agent.upsert", "tool.start", "tool.end"})
}

// native event for an unknown thread creates a minimal agent
func TestCodexNativeUnknownThread(t *testing.T) {
	a := cxSetup()
	out := cxFeed(a, cxDone(cxPatchItem("nested", jm{"cwd": "/elsewhere"}), "abcdefghijklmnop", nil))
	deepEq(t, "upserts", upserts(out), []wire.Agent{{
		ID: codexA("abcdefghijklmnop"), Provider: "codex", Name: "abcdefghijkl", Role: "main", Link: "root",
		Cwd: cxStr("/elsewhere"), Status: "idle", LastEventAt: t1,
	}})
	// A second event for the same thread does not repeat the upsert.
	if got := upserts(cxFeed(a, cxDone(cxPatchItem("other", nil), "abcdefghijklmnop", nil))); len(got) != 0 {
		t.Errorf("repeat upserts = %d", len(got))
	}
}

// native events inside a thread log belong to the thread agent, not the log owner
func TestCodexNativeEventsBelongToLogOwner(t *testing.T) {
	a := cxSetup()
	out := cxFeed(a, cxDone(jm{"type": "CommandExecution", "id": "c", "status": "completed", "command": "ls"}, "", nil))
	if c := cxCall(t, out); c.AgentID != codexA("main") {
		t.Errorf("agent = %s", c.AgentID)
	}
	// Without a thread id and without a known log owner, nothing can be attributed.
	if got := cxFeed(NewCodexAdapter("mac"), cxDone(jm{"type": "CommandExecution", "id": "c", "status": "completed"}, "", nil)); len(got) != 0 {
		t.Errorf("events = %v", kinds(got))
	}
}

// native receipts replay once; parallel agents remain distinct
func TestCodexNativeReceiptsReplayOnce(t *testing.T) {
	a := cxSetup()
	var out []wire.SourceEvent
	for _, thread := range []string{"child-a", "child-b"} {
		for i := 0; i < 2; i++ {
			out = append(out, cxFeed(a, cxDone(cxPatchItem("nested", nil), thread, nil))...)
		}
	}
	var agents []string
	for _, w := range cxWrites(out) {
		agents = append(agents, w.AgentID)
	}
	deepEq(t, "agents", agents, []string{codexA("child-a"), codexA("child-b")})
	if cxCount(out, "tool.start") != 2 || cxCount(out, "tool.end") != 2 {
		t.Errorf("kinds = %v", kinds(out))
	}
}

// failed, declined, and incomplete patches never create confirmed writes
func TestCodexUnsuccessfulPatchesCreateNoWrites(t *testing.T) {
	a := cxSetup()
	var out []wire.SourceEvent
	for i, status := range []any{"failed", "declined", "inProgress", nil} {
		out = append(out, cxFeed(a, cxDone(cxPatchItem("s"+strconv.Itoa(i), jm{"status": status}), "main", nil))...)
	}
	if w := cxWrites(out); len(w) != 0 {
		t.Errorf("writes = %v", w)
	}
	var oks []bool
	for _, e := range cxEnds(out) {
		oks = append(oks, cxOk(e))
	}
	deepEq(t, "oks", oks, []bool{false, false, false, false})
}

// native rename retains source and destination; no CWD guess for another thread
func TestCodexNativeRename(t *testing.T) {
	a := cxSetup()
	changes := cxOrd{{"/project/old", jm{"type": "update", "move_path": "/project/new"}}, {"relative", jm{"type": "add"}}}
	out := cxFeed(a, cxDone(cxPatchItem("r", jm{"changes": changes}), "child", nil))
	deepEq(t, "rename", cxPathOps(out), [][2]string{{"/project/old", "edit"}, {"/project/new", "move"}})
	// The same relative path resolves for a thread whose own cwd is known.
	known := cxFeed(a, cxDone(cxPatchItem("r2", jm{"changes": jm{"relative": jm{"type": "add"}}}), "main", nil))
	deepEq(t, "known", cxWrites(known), []cxWrite{{codexA("main"), "/project/relative", "create"}})
	// An item's own cwd wins.
	own := cxFeed(a, cxDone(cxPatchItem("r3", jm{
		"cwd":     "/own",
		"changes": cxOrd{{"relative", jm{"type": "delete"}}, {"/abs/x", jm{"type": "add"}}},
	}), "child", nil))
	deepEq(t, "own", cxPathOps(own), [][2]string{{"/own/relative", "delete"}, {"/abs/x", "create"}})
}

// native and direct receipts with the same call ID do not double count
func TestCodexNativeAndDirectReceiptsDoNotDoubleCount(t *testing.T) {
	direct := func(a *CodexAdapter) []wire.SourceEvent {
		var out []wire.SourceEvent
		out = append(out, cxFeed(a, cxResp(jm{"type": "custom_tool_call", "name": "apply_patch", "call_id": "nested", "input": "*** Update File: shared.txt"}))...)
		out = append(out, cxFeed(a, cxPatchOut("nested", "Success. Updated the following files:\nM shared.txt"))...)
		return out
	}
	check := func(name string, out []wire.SourceEvent) {
		t.Helper()
		if cxCount(out, "tool.start") != 1 || cxCount(out, "tool.end") != 1 || len(cxWrites(out)) != 1 {
			t.Errorf("%s: kinds = %v, writes = %v", name, kinds(out), cxWrites(out))
		}
	}
	a := cxSetup()
	out := direct(a)
	out = append(out, cxFeed(a, cxDone(cxPatchItem("nested", nil), "main", nil))...)
	check("direct first", out)
	// Native first, direct second: same result.
	b := cxSetup()
	rev := cxFeed(b, cxDone(cxPatchItem("nested", nil), "main", nil))
	rev = append(rev, direct(b)...)
	check("native first", rev)
}

// a native receipt closes an open direct call once
func TestCodexNativeReceiptClosesDirectCall(t *testing.T) {
	a := cxSetup()
	cxFeed(a, cxResp(jm{"type": "custom_tool_call", "name": "apply_patch", "call_id": "nested", "input": "*** Update File: shared.txt"}))
	nat := cxFeed(a, cxDone(cxPatchItem("nested", nil), "main", nil))
	if cxCount(nat, "tool.start") != 0 || cxCount(nat, "tool.end") != 1 {
		t.Errorf("kinds = %v", kinds(nat))
	}
	if got := cxFeed(a, cxPatchOut("nested", cxOK)); len(got) != 0 {
		t.Errorf("late receipt = %v", kinds(got))
	}
}

// native CommandExecution item_completed emits tool.start and tool.end with the same ts and tool.output
func TestCodexNativeCommandExecution(t *testing.T) {
	a := cxSetup()
	const ms = 1767225605000 // 2026-01-01T00:00:05.000Z
	const iso = "2026-01-01T00:00:05.000Z"
	out := cxFeed(a, cxDone(jm{
		"type": "CommandExecution", "id": "cmd", "status": "completed", "command": jl{"sh", "-c", "echo data > file"},
		"cwd": "/project", "process_id": "42", "exit_code": 0, "aggregated_output": "Success. Updated the following files:\nA file",
	}, "main", jm{"completed_at_ms": ms}))
	deepEq(t, "kinds", kinds(out), []string{"tool.start", "tool.output", "tool.end"})
	start := cxOnlyStart(t, out)
	end := cxOnlyEnd(t, out)
	if start.Ts != iso || out[2].Ts != iso {
		t.Errorf("ts = %s, %s", start.Ts, out[2].Ts)
	}
	deepEq(t, "call", start.Ev.(wire.ToolStart).Call, wire.Call{
		AgentID: codexA("main"), CallID: "cmd", Category: "command", RawName: "CommandExecution",
		Command: "sh -c echo data > file", StartedAt: iso,
	})
	deepEq(t, "output", out[1], wire.SourceEvent{Ts: iso, Ev: wire.ToolOutput{
		AgentID: codexA("main"), CallID: "cmd", Chunk: "Success. Updated the following files:\nA file", Truncated: false,
	}})
	if !cxOk(end) || end.ExitCode == nil || *end.ExitCode != 0 {
		t.Errorf("end = %+v", end)
	}
	// Never infer writes from shell text, stdout or a zero exit.
	if end.Writes != nil {
		t.Errorf("writes = %v", end.Writes)
	}
	// Without completed_at_ms the record timestamp is used.
	fb := cxFeed(a, cxDone(jm{"type": "CommandExecution", "id": "c2", "status": "completed", "command": "ls"}, "main", nil, t2))
	if got := cxOnlyStart(t, fb).Ts; got != t2 {
		t.Errorf("fallback ts = %s", got)
	}
	// A completed_at_ms that is not finite falls back too.
	rec := cxDone(jm{"type": "CommandExecution", "id": "c3", "status": "completed", "command": "ls"}, "main", nil, t2)
	obj(obj(rec).Get("payload")).set("completed_at_ms", math.NaN())
	if got := cxOnlyStart(t, cxFeed(a, rec)).Ts; got != t2 {
		t.Errorf("NaN ts = %s", got)
	}
}

// CommandExecution ok and exitCode follow status and exit_code
func TestCodexCommandExecutionOkAndExitCode(t *testing.T) {
	a := cxSetup()
	end := func(id string, over jm) wire.ToolEnd {
		item := jm{"type": "CommandExecution", "id": id, "command": "x"}
		for k, v := range over {
			item[k] = v
		}
		return cxOnlyEnd(t, cxFeed(a, cxDone(item, "main", nil)))
	}
	check := func(name string, e wire.ToolEnd, ok bool, exit *int) {
		t.Helper()
		if cxOk(e) != ok || (e.ExitCode == nil) != (exit == nil) || (exit != nil && *e.ExitCode != *exit) {
			t.Errorf("%s: end = ok %v exit %v, want ok %v exit %v", name, cxOk(e), e.ExitCode, ok, exit)
		}
	}
	zero, two := 0, 2
	check("a", end("a", jm{"status": "completed", "exit_code": 0}), true, &zero)
	check("b", end("b", jm{"status": "completed"}), true, nil)
	check("b2", end("b2", jm{"status": "completed"}), true, nil)
	check("c", end("c", jm{"status": "completed", "exit_code": 2}), false, &two)
	check("d", end("d", jm{"status": "failed", "exit_code": 0}), false, &zero)
	check("e", end("e", jm{"status": "declined"}), false, nil)
	check("f", end("f", jm{"status": "completed", "exit_code": "0"}), true, nil)
}

// native command output and history are bounded
func TestCodexNativeCommandOutputBounded(t *testing.T) {
	a := cxSetup()
	output := strings.Repeat("a", 10000) + strings.Repeat("b", 10000) + "END"
	out := cxFeed(a, cxDone(jm{
		"type": "CommandExecution", "id": "big", "status": "completed", "command": jl{strings.Repeat("x", 5000)},
		"aggregated_output": output, "exit_code": 1,
	}, "main", nil))
	if got := len(cxCall(t, out).Command); got != 4096 {
		t.Errorf("command length = %d", got)
	}
	chunks := only[wire.ToolOutput](out)
	var truncated []bool
	var joined strings.Builder
	for _, c := range chunks {
		if len(c.Chunk) > 4096 {
			t.Errorf("chunk of %d bytes", len(c.Chunk))
		}
		truncated = append(truncated, c.Truncated)
		joined.WriteString(c.Chunk)
	}
	deepEq(t, "truncated", truncated, []bool{true, false, false, false})
	if joined.Len() != 16384 || !strings.HasSuffix(joined.String(), "END") {
		t.Errorf("joined = %d bytes", joined.Len())
	}
	if k := kinds(out); k[len(k)-1] != "tool.end" {
		t.Errorf("kinds = %v", k)
	}
	if e := cxOnlyEnd(t, out); cxOk(e) || e.ExitCode == nil || *e.ExitCode != 1 {
		t.Errorf("end = %+v", e)
	}
	// Empty output emits no chunk; multibyte characters stay whole.
	empty := cxFeed(a, cxDone(jm{"type": "CommandExecution", "id": "e", "status": "completed", "command": "true", "aggregated_output": ""}, "main", nil))
	deepEq(t, "empty kinds", kinds(empty), []string{"tool.start", "tool.end"})
	wide := strings.Repeat("日", 3000)
	w := only[wire.ToolOutput](cxFeed(a, cxDone(jm{"type": "CommandExecution", "id": "w", "status": "completed", "command": "x", "aggregated_output": wide}, "main", nil)))
	var wj strings.Builder
	for _, c := range w {
		if len(c.Chunk) > 4096 || strings.Contains(c.Chunk, "�") {
			t.Errorf("bad chunk of %d bytes", len(c.Chunk))
		}
		wj.WriteString(c.Chunk)
	}
	if wj.String() != wide {
		t.Error("wide output changed")
	}
}

// outer exec call is category other; nested CommandExecution is command
func TestCodexOuterExecIsOther(t *testing.T) {
	a := cxSetup()
	outer := cxFeed(a, cxResp(jm{"type": "custom_tool_call", "name": "exec", "call_id": "outer", "input": "wrapper"}))
	nested := cxFeed(a, cxDone(jm{"type": "CommandExecution", "id": "inner", "status": "completed", "command": "ls"}, "main", nil))
	if c := cxCall(t, outer).Category; c != "other" {
		t.Errorf("outer category = %s", c)
	}
	if c := cxCall(t, nested).Category; c != "command" {
		t.Errorf("nested category = %s", c)
	}
	end := cxFeed(a, cxResp(jm{"type": "custom_tool_call_output", "call_id": "outer", "output": "large wrapper output"}, t2))
	// The outer call is not a command: it never emits tool.output.
	deepEq(t, "kinds", kinds(end), []string{"tool.end"})
}

// direct shell calls emit command text and output; other direct calls never emit output
func TestCodexDirectShellCalls(t *testing.T) {
	a := cxSetup()
	args := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	start := cxFeed(a, cxResp(jm{"type": "function_call", "name": "exec_command", "call_id": "s", "arguments": args(jm{"cmd": strings.Repeat("x", 5000)})}))
	c := cxCall(t, start)
	if c.Category != "command" || c.RawName != "exec_command" || len(c.Command) != 4096 {
		t.Errorf("call = %+v", c)
	}
	arr := cxFeed(a, cxResp(jm{"type": "function_call", "name": "shell", "call_id": "s2", "arguments": args(jm{"command": jl{"bash", "-lc", "ls"}})}))
	if got := cxCall(t, arr).Command; got != "bash -lc ls" {
		t.Errorf("command = %q", got)
	}
	end := cxFeed(a, cxResp(jm{"type": "function_call_output", "call_id": "s", "output": "hello"}, t2))
	deepEq(t, "kinds", kinds(end), []string{"tool.output", "tool.end"})
	if got := only[wire.ToolOutput](end)[0].Chunk; got != "hello" {
		t.Errorf("chunk = %q", got)
	}
	if w := cxWrites(end); len(w) != 0 {
		t.Errorf("writes = %v", w)
	}
	// Text-part arrays are accepted.
	parts := cxFeed(a, cxResp(jm{"type": "function_call_output", "call_id": "s2", "output": jl{
		jm{"type": "input_text", "text": "one"}, jm{"type": "input_text", "text": "two"},
	}}, t2))
	if got := only[wire.ToolOutput](parts)[0].Chunk; got != "one\ntwo" {
		t.Errorf("parts chunk = %q", got)
	}
	// Read and search style calls never produce output or writes.
	cxFeed(a, cxResp(jm{"type": "function_call", "name": "webSearch", "call_id": "w", "arguments": "{}"}))
	w := cxFeed(a, cxResp(jm{"type": "function_call_output", "call_id": "w", "output": "results /project/leak.ts"}, t2))
	deepEq(t, "web kinds", kinds(w), []string{"tool.end"})
	if got := cxWrites(w); len(got) != 0 {
		t.Errorf("web writes = %v", got)
	}
}

// FileChange never emits tool.output; Mcp and Collab items emit a start and end pair
func TestCodexFileChangeMcpCollabItems(t *testing.T) {
	a := cxSetup()
	fc := cxFeed(a, cxDone(cxPatchItem("f", jm{"aggregated_output": "leak"}), "main", nil))
	deepEq(t, "fc kinds", kinds(fc), []string{"tool.start", "tool.end"})
	mcp := cxFeed(a, cxDone(jm{"type": "McpToolCall", "id": "m", "status": "completed", "result": "secret"}, "main", nil))
	deepEq(t, "mcp kinds", kinds(mcp), []string{"tool.start", "tool.end"})
	if c := cxCall(t, mcp); c.Category != "other" || c.RawName != "McpToolCall" {
		t.Errorf("mcp call = %+v", c)
	}
	if !cxOk(cxOnlyEnd(t, mcp)) {
		t.Error("mcp end not ok")
	}
	collab := cxFeed(a, cxDone(jm{"type": "CollabAgentToolCall", "id": "k", "status": "failed"}, "main", nil))
	if c := cxCall(t, collab).Category; c != "agent" {
		t.Errorf("collab category = %s", c)
	}
	if cxOk(cxOnlyEnd(t, collab)) {
		t.Error("collab end ok")
	}
	if mcp[0].Ts != mcp[1].Ts {
		t.Errorf("mcp ts = %s, %s", mcp[0].Ts, mcp[1].Ts)
	}
}

// other native item types are ignored
func TestCodexOtherNativeItemsIgnored(t *testing.T) {
	a := cxSetup()
	for _, typ := range []string{"Reasoning", "AgentMessage", "UserMessage", "SubAgentActivity", "ContextCompaction", "Extension", "ImageView", "WebSearch"} {
		if got := cxFeed(a, cxDone(jm{"type": typ, "id": typ}, "main", nil)); len(got) != 0 {
			t.Errorf("%s: events = %v", typ, kinds(got))
		}
	}
	if got := cxFeed(a, cxDone(jm{"type": "CommandExecution"}, "main", nil)); len(got) != 0 {
		t.Errorf("no id: events = %v", kinds(got))
	}
	if got := cxFeed(a, cxEv(jm{"type": "item_completed", "thread_id": "main"})); len(got) != 0 {
		t.Errorf("no item: events = %v", kinds(got))
	}
}

// task_complete and task_completed emit idle, not finished; turn_aborted emits interrupted
func TestCodexTaskStatuses(t *testing.T) {
	a := cxSetup()
	main := codexA("main")
	started := cxFeed(a, cxEv(jm{"type": "task_started"}))
	deepEq(t, "started", cxStatuses(started), []cxStatus{{t1, main, "working"}})
	// Already working: no repeat.
	if got := cxStatuses(cxFeed(a, cxEv(jm{"type": "task_started"}))); len(got) != 0 {
		t.Errorf("repeat = %v", got)
	}
	for _, typ := range []string{"task_complete", "task_completed"} {
		cxFeed(a, cxEv(jm{"type": "task_started"}))
		deepEq(t, typ, cxStatuses(cxFeed(a, cxEv(jm{"type": typ}, t2))), []cxStatus{{t2, main, "idle"}})
	}
	cxFeed(a, cxEv(jm{"type": "task_started"}))
	aborted := cxStatuses(cxFeed(a, cxEv(jm{"type": "turn_aborted"}, t2)))
	deepEq(t, "aborted", aborted, []cxStatus{{t2, main, "interrupted"}})
	// A direct tool call moves the agent back to working.
	call := cxFeed(a, cxResp(jm{"type": "function_call", "name": "shell", "call_id": "w", "arguments": "{}"}, t2))
	deepEq(t, "call", statuses(call), []string{"working"})
}

// agent.upsert fires only when identity changes; status and lastEventAt do not trigger one
func TestCodexUpsertOnlyOnIdentityChange(t *testing.T) {
	a := NewCodexAdapter("mac")
	first := upserts(cxFeed(a, cxMeta("main", "")))
	deepEq(t, "first", first, []wire.Agent{{
		ID: codexA("main"), Provider: "codex", Name: "Codex session", Role: "main", Link: "root",
		Cwd: cxStr("/project"), Status: "idle", LastEventAt: t1,
	}})
	if got := upserts(cxFeed(a, cxEv(jm{"type": "task_started"}, t2))); len(got) != 0 {
		t.Errorf("task_started upserts = %d", len(got))
	}
	models := func(as []wire.Agent) []string {
		var out []string
		for _, x := range as {
			out = append(out, x.Model)
		}
		return out
	}
	deepEq(t, "models", models(upserts(cxFeed(a, cxTurn("/project", t2)))), []string{"m-1"})
	if got := upserts(cxFeed(a, cxTurn("/project", t2))); len(got) != 0 {
		t.Errorf("same turn upserts = %d", len(got))
	}
	next := upserts(cxFeed(a, cxTurn("/next", t2)))
	if len(next) != 1 || derefOr(next[0].Cwd, "") != "/next" || next[0].Status != "working" {
		t.Errorf("next = %+v", next)
	}
	// Sub-agent names come from the thread_spawn path; guardian reviews are named.
	sub := upserts(cxFeed(a, cxRec("session_meta", jm{
		"id": "k", "cwd": "/p", "parent_thread_id": "main",
		"source": jm{"subagent": jm{"thread_spawn": jm{"agent_path": "root/reviewer"}}},
	}, t1), "/r/k.jsonl"))
	if len(sub) != 1 || sub[0].Name != "reviewer" || sub[0].Role != "sub-agent" || derefOr(sub[0].ParentID, "") != codexA("main") || sub[0].Link != "confirmed" {
		t.Errorf("sub = %+v", sub)
	}
	guard := upserts(cxFeed(a, cxRec("session_meta", jm{"id": "g", "cwd": "/p", "thread_source": "guardian_review"}, t1), "/r/g.jsonl"))
	if len(guard) != 1 || guard[0].Name != "Approval review" || guard[0].Role != "main" {
		t.Errorf("guard = %+v", guard)
	}
}

// a minimal agent from a native event is refined by its own session_meta
func TestCodexMinimalAgentRefined(t *testing.T) {
	a := cxSetup()
	cxFeed(a, cxDone(cxPatchItem("nested", nil), "kid", nil))
	out := upserts(cxFeed(a, cxMetaWith("kid", "main", "/project", t2), "/r/kid.jsonl"))
	if len(out) == 0 {
		t.Fatal("no upsert")
	}
	checkAgent(t, out[0], codexA("kid"), codexA("main"), "confirmed", "sub-agent")
}

// records without a timestamp learn identity but emit no events; invalid records emit nothing
func TestCodexRecordsWithoutTimestamp(t *testing.T) {
	a := NewCodexAdapter("mac")
	bare := cxMeta("main", "")
	delete(obj(bare).vals, "timestamp")
	if got := cxFeed(a, bare); len(got) != 0 {
		t.Errorf("bare meta = %v", kinds(got))
	}
	call := cxResp(jm{"type": "function_call", "name": "shell", "call_id": "c", "arguments": "{}"})
	delete(obj(call).vals, "timestamp")
	if got := cxFeed(a, call); len(got) != 0 {
		t.Errorf("bare call = %v", kinds(got))
	}
	if got := upserts(cxFeed(a, cxTurn("/project", t2))); len(got) != 1 {
		t.Errorf("turn upserts = %d", len(got))
	}
	if got := cxFeed(a, cxResp(jm{"type": "function_call_output", "call_id": "c", "output": "x"}, t2)); len(got) != 0 {
		t.Errorf("output = %v", kinds(got))
	}
	bads := []any{
		nil, "x", 7.0, jl{}, jsonRec(jm{}), jsonRec(jm{"type": "response_item"}),
		jsonRec(jm{"type": "response_item", "payload": "x", "timestamp": t1}),
	}
	for i, bad := range bads {
		if got := a.Ingest(bad, "/r/x.jsonl", "work"); len(got) != 0 {
			t.Errorf("bad %d = %v", i, kinds(got))
		}
	}
	// Records before any session_meta and without a thread id are not attributed.
	early := NewCodexAdapter("mac").Ingest(cxResp(jm{"type": "function_call", "name": "shell", "call_id": "c"}), "/r/y.jsonl", "work")
	if len(early) != 0 {
		t.Errorf("early = %v", kinds(early))
	}
}

// call dedupe and pending memory are bounded
func TestCodexPendingCap(t *testing.T) {
	a := cxSetup()
	for i := 0; i < 5001; i++ {
		cxFeed(a, cxPatchCall("c"+strconv.Itoa(i), "*** Add File: a.txt"))
	}
	if n := len(cxEnds(cxFeed(a, cxPatchOut("c0", cxOK)))); n != 0 {
		t.Errorf("c0 ends = %d", n)
	}
	if n := len(cxEnds(cxFeed(a, cxPatchOut("c1", cxOK)))); n != 1 {
		t.Errorf("c1 ends = %d", n)
	}
}

// fixtures: a main log and a child log replay end to end
func TestCodexFixturesReplay(t *testing.T) {
	a := NewCodexAdapter("mac")
	dir := filepath.Join("..", "..", "testdata", "fixtures", "codex")
	replay := func(file string) []wire.SourceEvent {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		var out []wire.SourceEvent
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			out = append(out, a.Ingest(mustDecode(t, line), filepath.Join(dir, file), "work")...)
		}
		return out
	}
	main := replay("main.jsonl")
	kid := replay("child.jsonl")

	first := upserts(main)[0]
	if first.ID != codexA("th-main") || derefOr(first.Cwd, "") != "/p/proj" || first.Role != "main" {
		t.Errorf("first upsert = %+v", first)
	}
	var starts [][2]string
	for _, c := range calls(main) {
		starts = append(starts, [2]string{c.CallID, c.Category})
	}
	deepEq(t, "starts", starts, [][2]string{{"c1", "command"}, {"p1", "edit"}, {"outer", "other"}, {"n1", "command"}, {"n2", "edit"}})
	var outputs [][2]string
	for _, o := range only[wire.ToolOutput](main) {
		outputs = append(outputs, [2]string{o.CallID, o.Chunk})
	}
	deepEq(t, "outputs", outputs, [][2]string{{"c1", "x"}, {"n1", "y\n"}})
	var ends []string
	for _, e := range cxEnds(main) {
		ends = append(ends, e.CallID+":"+strconv.FormatBool(cxOk(e)))
	}
	deepEq(t, "ends", ends, []string{"c1:true", "p1:true", "n1:true", "n2:true", "outer:true"})
	deepEq(t, "writes", cxPathOps(main), [][2]string{{"/p/proj/hello.txt", "create"}, {"/p/proj/a.txt", "edit"}})
	deepEq(t, "main statuses", statuses(main), []string{"working", "idle"})

	k := upserts(kid)[0]
	checkAgent(t, k, codexA("th-kid"), codexA("th-main"), "confirmed", "sub-agent")
	if k.Name != "reviewer" {
		t.Errorf("kid name = %q", k.Name)
	}
	deepEq(t, "kid statuses", statuses(kid), []string{"working", "interrupted"})
}

// FileChange.changes follows JavaScript Object.entries order: array-index keys
// ascending, then the other keys in insertion order.
func TestCodexFileChangeOrder(t *testing.T) {
	a := cxSetup()
	line := `{"type":"event_msg","timestamp":"` + t1 + `","payload":{"type":"item_completed","thread_id":"main","item":` +
		`{"type":"FileChange","id":"o","status":"completed","cwd":"/p","changes":{` +
		`"b.ts":{"type":"update"},"a.ts":{"type":"update"},"10":{"type":"update"},"2":{"type":"update"}}}}}`
	out := cxFeed(a, mustDecode(t, line))
	want := []string{"/p/2", "/p/10", "/p/b.ts", "/p/a.ts"}
	deepEq(t, "files", cxCall(t, out).Files, want)
	var got []string
	for _, w := range cxWrites(out) {
		got = append(got, w.Path)
	}
	deepEq(t, "writes", got, want)
}

// Log fields of an unexpected JSON type are treated as absent.
func TestCodexWrongFieldTypes(t *testing.T) {
	a := cxSetup()
	out := cxFeed(a, cxDone(jm{"type": "CommandExecution", "id": "x", "status": "completed", "command": "ls", "exit_code": "1"}, "main", nil))
	if e := cxOnlyEnd(t, out); !cxOk(e) || e.ExitCode != nil {
		t.Errorf("end = %+v", e)
	}
	if got := cxFeed(a, jsonRec(jm{"type": "event_msg", "timestamp": t1, "payload": "x"})); len(got) != 0 {
		t.Errorf("string payload = %v", kinds(got))
	}
	// Other wrong types: none may panic or drop the record.
	for _, raw := range []string{
		`{"type":"session_meta","timestamp":5,"payload":{"id":"z","cwd":7,"source":"cli","parent_thread_id":3}}`,
		`{"type":"response_item","timestamp":"` + t1 + `","payload":{"type":"function_call","name":"shell","call_id":"w","arguments":{"command":5}}}`,
		`{"type":"response_item","timestamp":"` + t1 + `","payload":{"type":"function_call_output","call_id":"w","output":{"text":"x"}}}`,
		`{"type":"event_msg","timestamp":"` + t1 + `","payload":{"type":"item_completed","item":{"type":"FileChange","id":"q","changes":[1]},"completed_at_ms":"5"}}`,
	} {
		cxFeed(a, mustDecode(t, raw))
	}
}

// A direct shell result gives its exit code from structured metadata, else from Codex's status line.
func TestCodexDirectExitCode(t *testing.T) {
	a := cxSetup()
	run := func(id, output string) wire.ToolEnd {
		cxFeed(a, cxResp(jm{"type": "function_call", "name": "shell", "call_id": id, "arguments": `{"command":"x"}`}))
		return cxOnlyEnd(t, cxFeed(a, cxResp(jm{"type": "function_call_output", "call_id": id, "output": output}, t2)))
	}
	exit := func(e wire.ToolEnd) string {
		if e.ExitCode == nil {
			return "none"
		}
		return strconv.Itoa(*e.ExitCode)
	}
	cases := []struct {
		name, output, exit string
		ok                 bool
	}{
		{"structured", `{"output":"hi","metadata":{"exit_code":3}}`, "3", false},
		{"structured zero", `{"output":"hi","metadata":{"exit_code":0}}`, "0", true},
		{"structured fraction", `{"output":"hi","metadata":{"exit_code":1.5}}`, "none", true},
		{"status line", "Process exited with code -2\nrest", "-2", false},
		{"too large", "Process exited with code 99999999999999999999999", "none", true},
		{"plain", "all good", "none", true},
		{"not a result object", `{"x":1}`, "none", true},
	}
	for i, c := range cases {
		e := run("e"+strconv.Itoa(i), c.output)
		if exit(e) != c.exit || cxOk(e) != c.ok {
			t.Errorf("%s: exit %s ok %v, want exit %s ok %v", c.name, exit(e), cxOk(e), c.exit, c.ok)
		}
	}
	// The structured output replaces the raw text in tool.output.
	cxFeed(a, cxResp(jm{"type": "function_call", "name": "shell", "call_id": "s", "arguments": "{}"}))
	out := cxFeed(a, cxResp(jm{"type": "function_call_output", "call_id": "s", "output": `{"output":"hi","metadata":{"exit_code":0}}`}, t2))
	if got := only[wire.ToolOutput](out)[0].Chunk; got != "hi" {
		t.Errorf("chunk = %q", got)
	}
}

// The agent name is the last segment of agent_path, even when that segment is empty.
func TestCodexAgentPathNames(t *testing.T) {
	name := func(path string) string {
		a := NewCodexAdapter("mac")
		out := upserts(cxFeed(a, cxRec("session_meta", jm{
			"id": "k", "parent_thread_id": "m",
			"source": jm{"subagent": jm{"thread_spawn": jm{"agent_path": path}}},
		}, t1)))
		return out[0].Name
	}
	deepEq(t, "trailing slash", name("a/"), "")
	deepEq(t, "no slash", name("solo"), "solo")
	deepEq(t, "empty path", name(""), "Codex session")
}

// completed_at_ms is truncated toward zero; years outside 0000-9999 use the extended ISO form.
func TestCodexCompletedAtMs(t *testing.T) {
	a := cxSetup()
	cases := []struct {
		name string
		ms   string
		want string
	}{
		{"fraction", "1767225605000.9", "2026-01-01T00:00:05.000Z"},
		{"negative fraction", "-0.5", "1970-01-01T00:00:00.000Z"},
		{"max", "8.639999999999999e15", "+275760-09-12T23:59:59.999Z"},
		{"min", "-8.639999999999999e15", "-271821-04-20T00:00:00.001Z"},
		{"limit", "8.64e15", t2},
		{"infinite", "1e999", t2},
		{"year 10000", "253402300800000", "+010000-01-01T00:00:00.000Z"},
		{"year -1", "-62198755200000", "-000001-01-01T00:00:00.000Z"},
	}
	for i, c := range cases {
		raw := `{"type":"event_msg","timestamp":"` + t2 + `","payload":{"type":"item_completed","thread_id":"main","completed_at_ms":` + c.ms +
			`,"item":{"type":"McpToolCall","id":"m` + strconv.Itoa(i) + `","status":"completed"}}}`
		if got := cxOnlyStart(t, cxFeed(a, mustDecode(t, raw))).Ts; got != c.want {
			t.Errorf("%s: ts = %s, want %s", c.name, got, c.want)
		}
	}
}
