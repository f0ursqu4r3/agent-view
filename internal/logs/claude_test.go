package logs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"agent-view/internal/wire"
)

const (
	t1 = "2026-01-01T00:00:01.000Z"
	t2 = "2026-01-01T00:00:02.000Z"
)

type jm = map[string]any
type jl = []any

// jsonRec decodes a Go value as the tailer would decode the same JSON.
func jsonRec(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	out, err := Decode(string(b))
	if err != nil {
		panic(err)
	}
	return out
}

// rec builds a record like the TypeScript test helper. agentID "" is a main
// record. ts defaults to t1.
func rec(agentID string, content any, extra jm, ts ...string) any {
	stamp := t1
	if len(ts) > 0 {
		stamp = ts[0]
	}
	r := jm{"sessionId": "root", "cwd": "/project", "timestamp": stamp, "type": "assistant", "message": jm{"content": content}}
	if agentID != "" {
		r["agentId"] = agentID
	}
	for k, v := range extra {
		r[k] = v
	}
	return jsonRec(r)
}

func use(id, name string, input jm) jm {
	return jm{"type": "tool_use", "id": id, "name": name, "input": input}
}

func result(id string, rest ...any) jm {
	r := jm{"type": "tool_result", "tool_use_id": id, "content": "x"}
	if len(rest) > 0 {
		r["content"] = rest[0]
	}
	if len(rest) > 1 && rest[1] == true {
		r["is_error"] = true
	}
	return r
}

func feed(a *ClaudeAdapter, r any) []wire.SourceEvent {
	return a.Ingest(r, "/r/proj/main.jsonl", "default")
}

func kinds(events []wire.SourceEvent) []string {
	out := []string{}
	for _, e := range events {
		out = append(out, e.Ev.Kind())
	}
	return out
}

func only[T wire.Event](events []wire.SourceEvent) []T {
	var out []T
	for _, e := range events {
		if v, ok := e.Ev.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

func upserts(events []wire.SourceEvent) []wire.Agent {
	var out []wire.Agent
	for _, u := range only[wire.AgentUpsert](events) {
		out = append(out, u.Agent)
	}
	return out
}

func statuses(events []wire.SourceEvent) []string {
	out := []string{}
	for _, s := range only[wire.AgentStatusChange](events) {
		out = append(out, s.Status)
	}
	return out
}

func calls(events []wire.SourceEvent) []wire.Call {
	var out []wire.Call
	for _, s := range only[wire.ToolStart](events) {
		out = append(out, s.Call)
	}
	return out
}

func strPtr(s string) *string { return &s }

func derefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func checkAgent(t *testing.T, got wire.Agent, id, parentID, link, role string) {
	t.Helper()
	if got.ID != id || derefOr(got.ParentID, "<nil>") != parentID || got.Link != link || got.Role != role {
		t.Errorf("agent = %+v (parent %s), want id=%s parent=%s link=%s role=%s", got, derefOr(got.ParentID, "<nil>"), id, parentID, link, role)
	}
}

func deepEq[T any](t *testing.T, name string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", name, got, want)
	}
}

// Claude session membership does not claim an immediate parent; Agent result confirms nested links
func TestClaudeMembershipAndConfirmedLinks(t *testing.T) {
	a := NewClaudeAdapter("mac")
	nested := upserts(feed(a, rec("nested", jl{}, nil)))
	if len(nested) != 1 {
		t.Fatalf("nested upserts = %d", len(nested))
	}
	checkAgent(t, nested[0], "mac/claude:default:root:nested", "mac/claude:default:root", "session", "sub-agent")

	feed(a, rec("child", jl{use("spawn", "Agent", jm{"description": "Nested test"})}, nil))
	out := feed(a, rec("child", jl{result("spawn", jl{})}, jm{"toolUseResult": jm{"agentId": "nested"}}))
	var confirmed *wire.Agent
	for _, u := range upserts(out) {
		if u.ID == "mac/claude:default:root:nested" {
			confirmed = &u
		}
	}
	if confirmed == nil {
		t.Fatal("no confirmed upsert")
	}
	checkAgent(t, *confirmed, "mac/claude:default:root:nested", "mac/claude:default:root:child", "confirmed", "sub-agent")
	if confirmed.Name != "Nested test" {
		t.Errorf("name = %q", confirmed.Name)
	}
	// The confirmed link survives the nested agent's own later records.
	if got := upserts(feed(a, rec("nested", jl{}, nil))); len(got) != 0 {
		t.Errorf("later upserts = %d", len(got))
	}
	k := kinds(out)
	if slices.Index(k, "agent.upsert") >= slices.Index(k, "tool.end") {
		t.Errorf("order = %v", k)
	}
}

// failed Agent result does not confirm a parent link
func TestClaudeFailedAgentResultDoesNotConfirm(t *testing.T) {
	a := NewClaudeAdapter("mac")
	feed(a, rec("child", jl{use("spawn", "Agent", jm{"description": "D"})}, nil))
	out := feed(a, rec("child", jl{result("spawn", "x", true)}, jm{"toolUseResult": jm{"agentId": "nested"}}))
	for _, u := range upserts(out) {
		if strings.HasSuffix(u.ID, ":nested") {
			t.Errorf("unexpected upsert %s", u.ID)
		}
	}
}

// a record with several tool_result blocks confirms no parent; every call still ends
func TestClaudeSeveralResultsConfirmNoParent(t *testing.T) {
	a := NewClaudeAdapter("mac")
	feed(a, rec("p", jl{use("s1", "Agent", jm{"description": "One"}), use("s2", "Task", jm{"description": "Two"})}, nil))
	out := feed(a, rec("p", jl{result("s1"), result("s2")}, jm{"toolUseResult": jm{"agentId": "kid"}}))
	for _, u := range upserts(out) {
		if u.Link == "confirmed" || strings.HasSuffix(u.ID, ":kid") {
			t.Errorf("unexpected upsert %+v", u)
		}
	}
	var ends [][2]any
	for _, e := range only[wire.ToolEnd](out) {
		ends = append(ends, [2]any{e.CallID, *e.Ok})
	}
	deepEq(t, "ends", ends, [][2]any{{"s1", true}, {"s2", true}})
}

// two agents writing the same file retain separate confirmed receipts
func TestClaudeSameFileSeparateReceipts(t *testing.T) {
	a := NewClaudeAdapter("mac")
	type receipt struct{ Agent, Path string }
	var writes []receipt
	for _, agent := range []string{"a", "b"} {
		feed(a, rec(agent, jl{use("write", "Write", jm{"file_path": "same.txt", "content": "x"})}, nil))
		out := feed(a, rec(agent, jl{result("write")}, nil))
		for _, e := range only[wire.ToolEnd](out) {
			for _, w := range e.Writes {
				writes = append(writes, receipt{e.AgentID, w.Path})
			}
		}
	}
	deepEq(t, "writes", writes, []receipt{
		{"mac/claude:default:root:a", "/project/same.txt"},
		{"mac/claude:default:root:b", "/project/same.txt"},
	})
}

// failed and unfinished file tools do not claim writes
func TestClaudeFailedAndUnfinishedNoWrites(t *testing.T) {
	a := NewClaudeAdapter("mac")
	feed(a, rec("a", jl{use("a", "Edit", jm{"file_path": "a.txt"})}, nil))
	failed := feed(a, rec("a", jl{result("a", "failed", true)}, nil))
	end := only[wire.ToolEnd](failed)[0]
	if end.Ok == nil || *end.Ok || end.Writes != nil {
		t.Errorf("end = %+v", end)
	}
	// An unfinished call carries files at start but no end, so no write is ever emitted.
	open := feed(a, rec("a", jl{use("b", "Write", jm{"file_path": "b.txt"})}, nil))
	deepEq(t, "files", calls(open)[0].Files, []string{"/project/b.txt"})
	if len(only[wire.ToolEnd](open)) != 0 {
		t.Error("unexpected tool.end")
	}
}

// shell commands and opaque wrapper tools cannot invent file attribution
func TestClaudeNoInventedFileAttribution(t *testing.T) {
	a := NewClaudeAdapter("mac")
	start := feed(a, rec("", jl{use("s", "Bash", jm{"command": "echo x > file"})}, nil))
	if calls(start)[0].Files != nil {
		t.Error("Bash call has files")
	}
	end := feed(a, rec("", jl{result("s", "ok")}, nil))
	if only[wire.ToolEnd](end)[0].Writes != nil {
		t.Error("Bash end has writes")
	}
	wrapped := feed(a, rec("", jl{use("m", "mcp__x__write", jm{"file_path": "f.txt"})}, nil))
	if c := calls(wrapped)[0]; c.Category != "other" || c.Files != nil {
		t.Errorf("wrapped call = %+v", c)
	}
	wend := feed(a, rec("", jl{result("m")}, nil))
	if only[wire.ToolEnd](wend)[0].Writes != nil {
		t.Error("wrapped end has writes")
	}
}

// tool_use emits tool.start with category, files resolved against cwd, complete command text
func TestClaudeToolUseStart(t *testing.T) {
	a := NewClaudeAdapter("mac")
	out := feed(a, rec("", jl{
		use("r", "Read", jm{"file_path": "src/a.ts"}),
		use("c", "Bash", jm{"command": strings.Repeat("x", 5000)}),
		use("abs", "Read", jm{"file_path": "/other/b.ts"}),
	}, nil))
	cs := calls(out)
	if len(cs) != 3 {
		t.Fatalf("calls = %d", len(cs))
	}
	deepEq(t, "read", cs[0], wire.Call{AgentID: "mac/claude:default:root", CallID: "r", StartedAt: t1, Category: "read", RawName: "Read", Files: []string{"/project/src/a.ts"}})
	if cs[1].Category != "command" || cs[1].Command != strings.Repeat("x", 5000) || cs[1].Files != nil {
		t.Errorf("command call: category=%s len=%d files=%v", cs[1].Category, len(cs[1].Command), cs[1].Files)
	}
	deepEq(t, "abs files", cs[2].Files, []string{"/other/b.ts"})
	for _, e := range out {
		if e.Ts != t1 {
			t.Errorf("ts = %s", e.Ts)
		}
	}
	deepEq(t, "statuses", statuses(out), []string{"working"})
}

// tool_result emits tool.output (≤4 KiB chunks, last 16 KiB) then tool.end ok = !is_error with writes for Write/Edit
func TestClaudeToolResultOutputAndEnd(t *testing.T) {
	a := NewClaudeAdapter("mac")
	feed(a, rec("", jl{
		use("c", "Bash", jm{"command": "ls"}), use("w", "Write", jm{"file_path": "o.txt"}),
		use("e", "Edit", jm{"file_path": "e.txt"}), use("r", "Read", jm{"file_path": "r.txt"}),
	}, nil))

	big := strings.Repeat("a", 10000) + strings.Repeat("b", 10000)
	cOut := feed(a, rec("", jl{result("c", big)}, jm{"type": "user"}, t2))
	deepEq(t, "kinds", kinds(cOut), []string{"tool.output", "tool.output", "tool.output", "tool.output", "tool.end"})
	chunks := only[wire.ToolOutput](cOut)
	var trunc []bool
	joined := ""
	for _, c := range chunks {
		if len(c.Chunk) > 4096 {
			t.Errorf("chunk of %d bytes", len(c.Chunk))
		}
		trunc = append(trunc, c.Truncated)
		joined += c.Chunk
	}
	deepEq(t, "truncated", trunc, []bool{true, false, false, false})
	if len(joined) != 16384 || joined != big[len(big)-16384:] {
		t.Errorf("joined = %d bytes", len(joined))
	}
	end := only[wire.ToolEnd](cOut)[0]
	if end.AgentID != "mac/claude:default:root" || end.CallID != "c" || end.Ok == nil || !*end.Ok || cOut[4].Ts != t2 {
		t.Errorf("end = %+v ts=%s", end, cOut[4].Ts)
	}

	wOut := feed(a, rec("", jl{result("w", "x")}, jm{}, t2))
	deepEq(t, "kinds", kinds(wOut), []string{"tool.end"})
	deepEq(t, "writes", only[wire.ToolEnd](wOut)[0].Writes, []wire.Write{{Path: "/project/o.txt", Op: "edit"}})
	eOut := feed(a, rec("", jl{result("e", "x", true)}, jm{}, t2))
	if e := only[wire.ToolEnd](eOut)[0]; e.Ok == nil || *e.Ok || e.Writes != nil {
		t.Errorf("failed edit end = %+v", e)
	}
	// Read results (file contents) never become output or writes.
	rOut := feed(a, rec("", jl{result("r", "file contents")}, jm{}, t2))
	deepEq(t, "kinds", kinds(rOut), []string{"tool.end"})
	if only[wire.ToolEnd](rOut)[0].Writes != nil {
		t.Error("read end has writes")
	}
}

// tool.output accepts text-block arrays and keeps multibyte characters whole
func TestClaudeOutputTextBlocksAndMultibyte(t *testing.T) {
	a := NewClaudeAdapter("mac")
	feed(a, rec("", jl{use("c", "Bash", jm{"command": "ls"}), use("d", "Bash", jm{"command": "ls"})}, nil))
	blocks := feed(a, rec("", jl{result("c", jl{jm{"type": "text", "text": "one"}, jm{"type": "text", "text": "two"}})}, jm{}, t2))
	var got []string
	for _, c := range only[wire.ToolOutput](blocks) {
		got = append(got, c.Chunk)
	}
	deepEq(t, "blocks", got, []string{"one\ntwo"})

	wide := strings.Repeat("日", 3000)
	out := only[wire.ToolOutput](feed(a, rec("", jl{result("d", wide)}, jm{}, t2)))
	joined := ""
	for _, c := range out {
		if len(c.Chunk) > 4096 || strings.Contains(c.Chunk, "�") {
			t.Errorf("bad chunk: %d bytes", len(c.Chunk))
		}
		joined += c.Chunk
	}
	if joined != wide {
		t.Errorf("joined = %d bytes", len(joined))
	}
	// Empty output emits no chunk.
	feed(a, rec("", jl{use("e", "Bash", jm{"command": "true"})}, nil))
	deepEq(t, "kinds", kinds(feed(a, rec("", jl{result("e", "")}, jm{}, t2))), []string{"tool.end"})
}

// end_turn and turn_duration emit agent.status idle
func TestClaudeEndTurnAndTurnDurationIdle(t *testing.T) {
	a := NewClaudeAdapter("mac")
	feed(a, rec("", jl{use("c", "Bash", jm{"command": "ls"})}, nil))
	end := feed(a, rec("", jl{jm{"type": "text", "text": "x"}}, jm{"message": jm{"stop_reason": "end_turn", "content": jl{}}}, t2))
	deepEq(t, "status", only[wire.AgentStatusChange](end), []wire.AgentStatusChange{{AgentID: "mac/claude:default:root", Status: "idle"}})
	for _, e := range end {
		if e.Ts != t2 {
			t.Errorf("ts = %s", e.Ts)
		}
	}
	// Already idle: no repeat.
	again := feed(a, rec("", jl{}, jm{"message": jm{"stop_reason": "end_turn", "content": jl{}}}, t2))
	if len(only[wire.AgentStatusChange](again)) != 0 {
		t.Error("repeated idle")
	}

	feed(a, rec("", jl{use("d", "Bash", jm{"command": "ls"})}, nil))
	dur := feed(a, jsonRec(jm{"sessionId": "root", "cwd": "/project", "timestamp": t2, "type": "system", "subtype": "turn_duration"}))
	deepEq(t, "statuses", statuses(dur), []string{"idle"})
}

// Grep/Glob tool.start carries pattern and files from input path/glob only
func TestClaudeGrepGlobStart(t *testing.T) {
	a := NewClaudeAdapter("mac")
	out := feed(a, rec("", jl{
		use("g1", "Grep", jm{"pattern": "foo", "path": "src", "glob": "*.ts"}),
		use("g2", "Glob", jm{"pattern": "**/*.ts"}),
	}, nil))
	cs := calls(out)
	if cs[0].Category != "search" || cs[0].Pattern != "foo" || !slices.Equal(cs[0].Files, []string{"/project/src"}) {
		t.Errorf("g1 = %+v", cs[0])
	}
	if cs[1].Category != "search" || cs[1].Pattern != "**/*.ts" || cs[1].Files != nil {
		t.Errorf("g2 = %+v", cs[1])
	}
	// Search results never produce files or output.
	end := feed(a, rec("", jl{result("g1", "/project/src/leak.ts\n/project/src/other.ts")}, jm{}, t2))
	deepEq(t, "kinds", kinds(end), []string{"tool.end"})
	if only[wire.ToolEnd](end)[0].Writes != nil {
		t.Error("search end has writes")
	}
}

// Review Focus 1: two transcript files with the same sessionId yield one agent id
func TestClaudeSameSessionTwoFilesOneAgent(t *testing.T) {
	a := NewClaudeAdapter("mac")
	e1 := a.Ingest(jsonRec(jm{"sessionId": "s1", "cwd": "/p", "type": "user", "timestamp": t1, "message": jm{"content": "x"}}), "/r/proj/one.jsonl", "default")
	e2 := a.Ingest(jsonRec(jm{"sessionId": "s1", "cwd": "/p", "type": "user", "timestamp": t2, "message": jm{"content": "y"}}), "/r/proj/two.jsonl", "default")
	var ids []string
	for _, u := range append(upserts(e1), upserts(e2)...) {
		if !slices.Contains(ids, u.ID) {
			ids = append(ids, u.ID)
		}
	}
	deepEq(t, "ids", ids, []string{"mac/claude:default:s1"})
	if len(upserts(e2)) != 0 {
		t.Error("second file re-emitted the agent")
	}
}

// provider scope and host prevent identity collisions
func TestClaudeScopeAndHostIdentity(t *testing.T) {
	a := NewClaudeAdapter("mac")
	r := jsonRec(jm{"sessionId": "s1", "cwd": "/p", "type": "user", "timestamp": t1, "message": jm{"content": "x"}})
	var ids []string
	for _, scope := range []string{"work", "personal"} {
		for _, u := range upserts(a.Ingest(r, "/r/a.jsonl", scope)) {
			ids = append(ids, u.ID)
		}
	}
	deepEq(t, "ids", ids, []string{"mac/claude:work:s1", "mac/claude:personal:s1"})
}

// agent.upsert fires only when identity fields change; status and lastEventAt do not trigger one
func TestClaudeUpsertOnIdentityChangeOnly(t *testing.T) {
	a := NewClaudeAdapter("mac")
	first := upserts(feed(a, rec("", jl{}, nil)))
	deepEq(t, "first", first, []wire.Agent{{
		ID: "mac/claude:default:root", Provider: "claude", Name: "Claude Code session", Role: "main", Link: "root",
		Cwd: strPtr("/project"), Status: "idle", LastEventAt: t1,
	}})
	if got := upserts(feed(a, rec("", jl{use("c", "Bash", jm{"command": "ls"})}, nil, t2))); len(got) != 0 {
		t.Errorf("upserts after tool_use = %d", len(got))
	}
	moved := upserts(feed(a, rec("", jl{}, jm{"cwd": "/elsewhere"}, t2)))
	if len(moved) != 1 || *moved[0].Cwd != "/elsewhere" || moved[0].Status != "working" {
		t.Errorf("moved = %+v", moved)
	}
	titled := upserts(feed(a, jsonRec(jm{"sessionId": "root", "type": "custom-title", "customTitle": "Named", "timestamp": t2})))
	if len(titled) != 1 || titled[0].Name != "Named" {
		t.Errorf("titled = %+v", titled)
	}
	modeled := upserts(feed(a, rec("", jl{}, jm{"message": jm{"model": "m-1", "content": jl{}}}, t2)))
	if len(modeled) != 1 || modeled[0].Model != "m-1" {
		t.Errorf("modeled = %+v", modeled)
	}
}

// records without a timestamp learn identity but emit no tool events
func TestClaudeNoTimestampLearnsIdentityOnly(t *testing.T) {
	a := NewClaudeAdapter("mac")
	bare := rec("", jl{use("c", "Bash", jm{"command": "ls"})}, nil).(*Obj)
	delete(bare.vals, "timestamp")
	bare.keys = slices.DeleteFunc(bare.keys, func(k string) bool { return k == "timestamp" })
	if got := feed(a, bare); len(got) != 0 {
		t.Errorf("events = %v", kinds(got))
	}
	// Identity is emitted on the next timestamped record.
	if got := upserts(feed(a, rec("", jl{}, nil, t2))); len(got) != 1 {
		t.Errorf("next upserts = %d", len(got))
	}
	// The untimestamped call never started, so a result for it emits nothing.
	if got := feed(a, rec("", jl{result("c")}, nil, t2)); len(got) != 0 {
		t.Errorf("events = %v", kinds(got))
	}
}

// invalid records and unknown results emit nothing
func TestClaudeInvalidRecordsEmitNothing(t *testing.T) {
	a := NewClaudeAdapter("mac")
	for name, r := range map[string]any{
		"nil": nil, "string": "x", "array": jl{}, "no session": jsonRec(jm{"type": "user", "timestamp": t1}),
	} {
		if got := a.Ingest(r, "/f.jsonl", "default"); len(got) != 0 {
			t.Errorf("%s: %v", name, kinds(got))
		}
	}
	if len(only[wire.ToolEnd](feed(a, rec("", jl{result("missing")}, nil)))) != 0 {
		t.Error("tool.end for an unknown call")
	}
}

// pending calls are capped at 5000
func TestClaudePendingCap(t *testing.T) {
	a := NewClaudeAdapter("mac")
	for i := 0; i < 5001; i++ {
		feed(a, rec("", jl{use("c"+strconv.Itoa(i), "Read", jm{"file_path": "a"})}, nil))
	}
	if len(only[wire.ToolEnd](feed(a, rec("", jl{result("c0")}, jm{}, t2)))) != 0 {
		t.Error("c0 should have been evicted")
	}
	if len(only[wire.ToolEnd](feed(a, rec("", jl{result("c1")}, jm{}, t2)))) != 1 {
		t.Error("c1 should still be pending")
	}
}

// fixtures: sub-agent id comes from the file name and the session replays end to end
func TestClaudeFixturesReplay(t *testing.T) {
	a := NewClaudeAdapter("mac")
	dir := filepath.Join("..", "..", "testdata", "fixtures", "claude")
	replay := func(file string) []wire.SourceEvent {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		var out []wire.SourceEvent
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			out = append(out, a.Ingest(mustDecode(t, line), filepath.Join(dir, file), "default")...)
		}
		return out
	}
	main := replay("session.jsonl")
	sub := replay(filepath.Join("subagents", "agent-sub1.jsonl"))

	var cats []string
	var files [][]string
	for _, c := range calls(main) {
		cats = append(cats, c.Category)
		files = append(files, c.Files)
	}
	deepEq(t, "categories", cats, []string{"read", "command", "write", "search", "edit"})
	deepEq(t, "files", files, [][]string{{"/p/proj/src/a.ts"}, nil, {"/p/proj/out.txt"}, {"/p/proj/src"}, {"/p/proj/bad.txt"}})
	var oks []bool
	var writes []wire.Write
	for _, e := range only[wire.ToolEnd](main) {
		oks = append(oks, *e.Ok)
		writes = append(writes, e.Writes...)
	}
	deepEq(t, "oks", oks, []bool{true, true, true, true, false})
	deepEq(t, "writes", writes, []wire.Write{{Path: "/p/proj/out.txt", Op: "edit"}})
	var chunks []string
	for _, c := range only[wire.ToolOutput](main) {
		chunks = append(chunks, c.Chunk)
	}
	deepEq(t, "chunks", chunks, []string{"x"})
	deepEq(t, "main statuses", statuses(main), []string{"working", "idle"})

	checkAgent(t, upserts(sub)[0], "mac/claude:default:s1:sub1", "mac/claude:default:s1", "session", "sub-agent")
	if got := calls(sub)[0].AgentID; got != "mac/claude:default:s1:sub1" {
		t.Errorf("sub call agent = %s", got)
	}
	deepEq(t, "sub statuses", statuses(sub), []string{"working", "idle"})
}

func TestClaudeWrongFieldTypes(t *testing.T) {
	a := NewClaudeAdapter("mac")
	// Without a usable timestamp, a record learns identity but emits nothing.
	for _, line := range []string{
		`{"sessionId":"root","timestamp":123,"type":"assistant","message":{"content":[]}}`,
		`{"sessionId":"root","type":"assistant","message":"text"}`,
		`{"sessionId":"root","type":"assistant","message":{"content":{}}}`,
		`{"sessionId":"root","timestamp":["x"],"cwd":7,"agentId":9,"type":"custom-title","customTitle":{}}`,
	} {
		if got := feed(a, mustDecode(t, line)); len(got) != 0 {
			t.Errorf("%s: events = %v", line, kinds(got))
		}
	}
	// A later record of the same session with a valid timestamp gives the upsert.
	got := feed(a, mustDecode(t, `{"sessionId":"root","timestamp":"`+t2+`","type":"assistant","message":{"content":[]}}`))
	if u := upserts(got); len(u) != 1 || u[0].ID != "mac/claude:default:root" || u[0].LastEventAt != t2 || u[0].Cwd != nil {
		t.Errorf("upserts = %+v", u)
	}
	// With a valid timestamp, wrong-typed fields are absent: no panic, no tool events.
	b := NewClaudeAdapter("mac")
	for _, line := range []string{
		`{"sessionId":"root","timestamp":"` + t1 + `","message":"text"}`,
		`{"sessionId":"root","timestamp":"` + t1 + `","message":{"content":{},"model":5,"stop_reason":[]}}`,
		`{"sessionId":"root","timestamp":"` + t1 + `","message":{"content":[null,1,"x",[],{"type":"tool_use","id":5,"name":{}},{"type":"tool_result","tool_use_id":[]}]}}`,
		`{"sessionId":"root","timestamp":"` + t1 + `","message":{"content":[{"type":"tool_use","id":"c","name":"Bash","input":"x"},{"type":"tool_result","tool_use_id":"c","content":{},"is_error":"yes"}]}}`,
	} {
		if got := only[wire.ToolOutput](feed(b, mustDecode(t, line))); len(got) != 0 {
			t.Errorf("%s: unexpected output %v", line, got)
		}
	}
}
