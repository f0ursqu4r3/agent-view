package wire

import (
	"runtime"
	"strings"
	"testing"
)

// categoryFor maps Claude tools
func TestCategoryForClaude(t *testing.T) {
	cases := map[string]string{
		"Read": "read", "MultiEdit": "edit", "NotebookEdit": "edit", "Write": "write",
		"Bash": "command", "Glob": "search", "Task": "agent", "mcp__x__y": "other",
	}
	for name, want := range cases {
		if got := CategoryFor("claude", name); got != want {
			t.Errorf("CategoryFor(claude, %q) = %q, want %q", name, got, want)
		}
	}
}

// categoryFor maps Codex log and daemon names
func TestCategoryForCodex(t *testing.T) {
	groups := []struct {
		want  string
		names []string
	}{
		{"edit", []string{"apply_patch", "FileChange", "fileChange"}},
		{"command", []string{"shell", "exec_command", "local_shell", "CommandExecution", "commandExecution"}},
		{"agent", []string{"spawn_agent", "CollabAgentToolCall", "collabAgentToolCall"}},
		{"search", []string{"webSearch"}},
		{"other", []string{"exec", "send_message", "McpToolCall", "wait_agent"}},
	}
	for _, g := range groups {
		for _, name := range g.names {
			if got := CategoryFor("codex", name); got != g.want {
				t.Errorf("CategoryFor(codex, %q) = %q, want %q", name, got, g.want)
			}
		}
	}
	if got := CategoryFor("unknown", "Read"); got != "other" {
		t.Errorf("unknown provider gave %q, want other", got)
	}
}

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }
func intp(i int) *int       { return &i }

func testAgent() Agent {
	return Agent{ID: "a1", Provider: "claude", Name: "main", Role: "main", Link: "root",
		Status: "working", LastEventAt: "T"}
}

func TestEncodeShapes(t *testing.T) {
	full := Agent{ID: "a1", Provider: "codex", Name: "n", Role: "sub-agent", ParentID: strp("p"),
		Link: "confirmed", Cwd: strp("/w"), ProjectRoot: strp("/r"), Model: "m", Status: "idle", LastEventAt: "L"}
	rows := []struct {
		name string
		ev   Event
		want string
	}{
		{"hello", Hello{CollectorVersion: "0.2.0", Platform: "darwin-arm64", Now: "N"},
			`{"v":1,"host":"h","seq":3,"kind":"hello","collectorVersion":"0.2.0","platform":"darwin-arm64","now":"N","ts":"T"}`},
		{"hello demo", Hello{CollectorVersion: "0.2.0", Platform: "p", Now: "N", Demo: true},
			`{"v":1,"host":"h","seq":3,"kind":"hello","collectorVersion":"0.2.0","platform":"p","now":"N","demo":true,"ts":"T"}`},
		{"empty snapshot", Snapshot{},
			`{"v":1,"host":"h","seq":3,"kind":"snapshot","agents":[],"openCalls":[],"files":[],"ts":"T"}`},
		{"snapshot", Snapshot{
			Agents:    []Agent{testAgent()},
			OpenCalls: []Call{{AgentID: "a1", CallID: "c1", StartedAt: "S", Category: "read", RawName: "Read"}},
			Files:     []FileTouch{{Path: "/f", LastTouchAt: "L", Attribution: "unattributed"}},
		},
			`{"v":1,"host":"h","seq":3,"kind":"snapshot","agents":[{"id":"a1","provider":"claude","name":"main","role":"main","parentId":null,"link":"root","cwd":null,"projectRoot":null,"status":"working","lastEventAt":"T"}],"openCalls":[{"agentId":"a1","callId":"c1","startedAt":"S","category":"read","rawName":"Read"}],"files":[{"path":"/f","lastTouchAt":"L","attribution":"unattributed"}],"ts":"T"}`},
		{"agent.upsert null fields", AgentUpsert{Agent: testAgent()},
			`{"v":1,"host":"h","seq":3,"kind":"agent.upsert","agent":{"id":"a1","provider":"claude","name":"main","role":"main","parentId":null,"link":"root","cwd":null,"projectRoot":null,"status":"working","lastEventAt":"T"},"ts":"T"}`},
		{"agent.upsert full", AgentUpsert{Agent: full},
			`{"v":1,"host":"h","seq":3,"kind":"agent.upsert","agent":{"id":"a1","provider":"codex","name":"n","role":"sub-agent","parentId":"p","link":"confirmed","cwd":"/w","projectRoot":"/r","model":"m","status":"idle","lastEventAt":"L"},"ts":"T"}`},
		{"agent.status", AgentStatusChange{AgentID: "a", Status: "finished"},
			`{"v":1,"host":"h","seq":3,"kind":"agent.status","agentId":"a","status":"finished","ts":"T"}`},
		{"tool.start minimal", ToolStart{Call: Call{AgentID: "a", CallID: "c", StartedAt: "S", Category: "command", RawName: "Bash"}},
			`{"v":1,"host":"h","seq":3,"kind":"tool.start","call":{"agentId":"a","callId":"c","startedAt":"S","category":"command","rawName":"Bash"},"ts":"T"}`},
		{"tool.start full", ToolStart{Call: Call{AgentID: "a", CallID: "c", StartedAt: "S", Category: "search", RawName: "Grep",
			Files: []string{"/x"}, Command: "ls", Pattern: "p"}},
			`{"v":1,"host":"h","seq":3,"kind":"tool.start","call":{"agentId":"a","callId":"c","startedAt":"S","category":"search","rawName":"Grep","files":["/x"],"command":"ls","pattern":"p"},"ts":"T"}`},
		{"tool.update", ToolUpdate{AgentID: "a", CallID: "c", Files: []string{"/x", "/y"}},
			`{"v":1,"host":"h","seq":3,"kind":"tool.update","agentId":"a","callId":"c","files":["/x","/y"],"ts":"T"}`},
		{"tool.update nil files", ToolUpdate{AgentID: "a", CallID: "c"},
			`{"v":1,"host":"h","seq":3,"kind":"tool.update","agentId":"a","callId":"c","files":[],"ts":"T"}`},
		{"tool.output not truncated", ToolOutput{AgentID: "a", CallID: "c", Chunk: "x"},
			`{"v":1,"host":"h","seq":3,"kind":"tool.output","agentId":"a","callId":"c","chunk":"x","truncated":false,"ts":"T"}`},
		{"tool.output truncated", ToolOutput{AgentID: "a", CallID: "c", Chunk: "x", Truncated: true},
			`{"v":1,"host":"h","seq":3,"kind":"tool.output","agentId":"a","callId":"c","chunk":"x","truncated":true,"ts":"T"}`},
		{"tool.end nil ok", ToolEnd{AgentID: "a", CallID: "c"},
			`{"v":1,"host":"h","seq":3,"kind":"tool.end","agentId":"a","callId":"c","ok":null,"ts":"T"}`},
		{"tool.end full", ToolEnd{AgentID: "a", CallID: "c", Ok: boolp(false), ExitCode: intp(0),
			Writes: []Write{{Path: "/p", Op: "create"}, {Path: "/q", Op: "move"}}},
			`{"v":1,"host":"h","seq":3,"kind":"tool.end","agentId":"a","callId":"c","ok":false,"exitCode":0,"writes":[{"path":"/p","op":"create"},{"path":"/q","op":"move"}],"ts":"T"}`},
		{"tool.end empty writes", ToolEnd{AgentID: "a", CallID: "c", Ok: boolp(true), Writes: []Write{}},
			`{"v":1,"host":"h","seq":3,"kind":"tool.end","agentId":"a","callId":"c","ok":true,"writes":[],"ts":"T"}`},
		{"fs.change", FsChange{Path: "/p", Op: "rename"},
			`{"v":1,"host":"h","seq":3,"kind":"fs.change","path":"/p","op":"rename","ts":"T"}`},
		{"source.status", SourceStatus{Source: "fs:/r", SourceState: SourceState{State: "ok"}},
			`{"v":1,"host":"h","seq":3,"kind":"source.status","source":"fs:/r","state":"ok","ts":"T"}`},
		{"source.status detail", SourceStatus{Source: "fs:/r", SourceState: SourceState{State: "degraded", Detail: "why"}},
			`{"v":1,"host":"h","seq":3,"kind":"source.status","source":"fs:/r","state":"degraded","detail":"why","ts":"T"}`},
		{"coverage empty", CoverageReport{},
			`{"v":1,"host":"h","seq":3,"kind":"coverage","coverage":{"logRoots":[],"logsOpen":0,"logsDiscovered":0,"watchRoots":[],"malformedRecords":0,"fsDropped":0,"sources":{}},"ts":"T"}`},
		{"coverage full", CoverageReport{Coverage: Coverage{
			LogRoots: []string{"/l"}, LogsOpen: 1, LogsDiscovered: 2, WatchRoots: []string{"/w"},
			MalformedRecords: 3, FSDropped: 4,
			Sources: map[string]SourceState{"fs:/w": {State: "down", Detail: "d"}, "a": {State: "ok"}},
		}},
			`{"v":1,"host":"h","seq":3,"kind":"coverage","coverage":{"logRoots":["/l"],"logsOpen":1,"logsDiscovered":2,"watchRoots":["/w"],"malformedRecords":3,"fsDropped":4,"sources":{"a":{"state":"ok"},"fs:/w":{"state":"down","detail":"d"}}},"ts":"T"}`},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := string(EncodeLine(Envelope{V: SchemaVersion, Host: "h", Seq: 3, Ts: "T", Ev: row.ev}))
			if got != row.want+"\n" {
				t.Errorf("got  %q\nwant %q", got, row.want+"\n")
			}
		})
	}
}

func TestEncodeKinds(t *testing.T) {
	want := map[string]Event{
		"hello": Hello{}, "snapshot": Snapshot{}, "agent.upsert": AgentUpsert{}, "agent.status": AgentStatusChange{},
		"tool.start": ToolStart{}, "tool.update": ToolUpdate{}, "tool.output": ToolOutput{}, "tool.end": ToolEnd{},
		"fs.change": FsChange{}, "source.status": SourceStatus{}, "coverage": CoverageReport{},
	}
	for kind, ev := range want {
		if ev.Kind() != kind {
			t.Errorf("%T.Kind() = %q, want %q", ev, ev.Kind(), kind)
		}
	}
}

func TestEncodeNilEvent(t *testing.T) {
	if _, err := (Envelope{V: 1}).MarshalJSON(); err == nil {
		t.Error("MarshalJSON with nil Ev returned no error")
	}
}

func TestEncodeNoHTMLEscape(t *testing.T) {
	line := string(EncodeLine(Envelope{V: 1, Host: "<h&>", Seq: 1, Ts: "T",
		Ev: ToolOutput{AgentID: "a", CallID: "c", Chunk: "<&>"}}))
	if !strings.Contains(line, `"chunk":"<&>"`) {
		t.Errorf("chunk was escaped: %q", line)
	}
	if !strings.Contains(line, `"host":"<h&>"`) {
		t.Errorf("host was escaped: %q", line)
	}
	if !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
		t.Errorf("line must hold exactly one trailing newline: %q", line)
	}
}

func TestFormatISO(t *testing.T) {
	cases := map[int64]string{
		1767225600123: "2026-01-01T00:00:00.123Z",
		-1:            "1969-12-31T23:59:59.999Z",
		0:             "1970-01-01T00:00:00.000Z",
	}
	for ms, want := range cases {
		if got := FormatISO(ms); got != want {
			t.Errorf("FormatISO(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestNowMs(t *testing.T) {
	if NowMs() < 1767225600000 {
		t.Error("NowMs is before 2026-01-01")
	}
}

func TestPlatform(t *testing.T) {
	want := runtime.GOARCH
	switch want {
	case "amd64":
		want = "x64"
	case "386":
		want = "ia32"
	}
	if got := Platform(); got != runtime.GOOS+"-"+want {
		t.Errorf("Platform() = %q, want %q", got, runtime.GOOS+"-"+want)
	}
}
