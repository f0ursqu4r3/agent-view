package schema

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"
)

// wrap adds the envelope fields (v, host, seq) that the fixture lines lack.
func wrap(line string, seq int) []byte {
	return []byte(fmt.Sprintf(`{"v":1,"host":"h","seq":%d,%s`, seq, strings.TrimPrefix(line, "{")))
}

func fixtureLines(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("../../test/fixtures/golden/session.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func TestDecodeLineAcceptsEveryKind(t *testing.T) {
	// The fixture has no hello, snapshot, source.status, or coverage lines; add them here.
	lines := append(fixtureLines(t),
		`{"kind":"hello","collectorVersion":"1.2.3","platform":"darwin","now":"2026-01-01T00:00:00.000Z","demo":true,"ts":"2026-01-01T00:00:00.000Z"}`,
		`{"kind":"snapshot","agents":[{"id":"a","provider":"codex","name":"n","role":"main","parentId":null,"link":"root","cwd":null,"projectRoot":null,"status":"working","lastEventAt":"2026-01-01T00:00:00.000Z"}],"openCalls":[{"agentId":"a","callId":"c","startedAt":"2026-01-01T00:00:00.000Z","category":"other","rawName":"x"}],"files":[],"ts":"2026-01-01T00:00:00.000Z"}`,
		`{"kind":"source.status","source":"claude","state":"degraded","detail":"slow","ts":"2026-01-01T00:00:00.000Z"}`,
		`{"kind":"coverage","coverage":{"logRoots":["/r"],"logsOpen":2,"logsDiscovered":3,"watchRoots":["/w"],"malformedRecords":4,"fsDropped":5,"sources":{"claude":{"state":"ok"},"codex":{"state":"down","detail":"gone"}}},"ts":"2026-01-01T00:00:00.000Z"}`,
	)
	got := map[string]Envelope{}
	for i, l := range lines {
		e, ok := DecodeLine(wrap(l, i+1))
		if !ok {
			t.Fatalf("line %d rejected: %s", i, l)
		}
		if e.V != 1 || e.Host != "h" || e.Seq != int64(i+1) {
			t.Fatalf("line %d envelope fields = %d %q %d", i, e.V, e.Host, e.Seq)
		}
		if e.Ts == "" {
			t.Fatalf("line %d: empty Ts", i)
		}
		got[e.Kind] = e
	}
	for _, k := range []string{"hello", "snapshot", "agent.upsert", "agent.status", "tool.start", "tool.update", "tool.output", "tool.end", "fs.change", "source.status", "coverage"} {
		if _, ok := got[k]; !ok {
			t.Errorf("kind %q not decoded", k)
		}
	}

	if e := got["hello"]; e.CollectorVersion != "1.2.3" || e.Platform != "darwin" || e.Now == "" || !e.Demo {
		t.Errorf("hello = %+v", e)
	}
	if e := got["snapshot"]; len(e.Agents) != 1 || e.Agents[0].Provider != "codex" || e.Agents[0].ParentID != nil ||
		e.Agents[0].Cwd != nil || len(e.OpenCalls) != 1 || e.OpenCalls[0].Category != "other" {
		t.Errorf("snapshot = %+v", e)
	}
	if e := got["agent.status"]; e.AgentID != "mac/claude:default:s1:sub1" || e.Status == "" {
		t.Errorf("agent.status = %+v", e)
	}
	if e := got["tool.output"]; e.Chunk != "editing" || e.Truncated || e.CallID != "c2" {
		t.Errorf("tool.output = %+v", e)
	}
	if e := got["fs.change"]; e.Path == "" || e.Op != "change" {
		t.Errorf("fs.change = %+v", e)
	}
	if e := got["source.status"]; e.Source != "claude" || e.State != "degraded" || e.Detail != "slow" {
		t.Errorf("source.status = %+v", e)
	}
	c := got["coverage"].Coverage
	if c == nil || c.LogsOpen != 2 || c.LogsDiscovered != 3 || c.MalformedRecords != 4 || c.FSDropped != 5 ||
		len(c.LogRoots) != 1 || len(c.WatchRoots) != 1 ||
		c.Sources["codex"] != (SourceState{State: "down", Detail: "gone"}) {
		t.Errorf("coverage = %+v", c)
	}

	// Fixture-derived kinds: the last occurrence of each kind wins in got, so decode specific lines.
	e, _ := DecodeLine(wrap(fixtureLines(t)[1], 1))
	if e.Agent == nil || e.Agent.Role != "sub-agent" || e.Agent.ParentID == nil || *e.Agent.ParentID != "mac/claude:default:s1" ||
		e.Agent.Link != "confirmed" || e.Agent.ProjectRoot == nil || *e.Agent.ProjectRoot != "/work/proj" {
		t.Errorf("agent.upsert = %+v", e.Agent)
	}
	e, _ = DecodeLine(wrap(fixtureLines(t)[11], 1))
	if e.Call == nil || e.Call.RawName != "Grep" || e.Call.Pattern != "foo" || len(e.Call.Files) != 1 {
		t.Errorf("tool.start = %+v", e.Call)
	}
	e, _ = DecodeLine(wrap(fixtureLines(t)[7], 1))
	if len(e.Writes) != 2 || e.Writes[1] != (Write{Path: "/work/proj/new.ts", Op: "create"}) {
		t.Errorf("tool.end writes = %+v", e.Writes)
	}
}

func TestDecodeLineRejects(t *testing.T) {
	for _, l := range []string{`not json`, `[]`, `{"v":2,"kind":"hello"}`, `{"kind":"hello"}`, ``} {
		if _, ok := DecodeLine([]byte(l)); ok {
			t.Errorf("DecodeLine(%q) ok = true", l)
		}
	}
}

func TestDecodeLineKeepsUnknownKind(t *testing.T) {
	e, ok := DecodeLine([]byte(`{"v":1,"host":"h","seq":1,"kind":"future.thing","extra":1}`))
	if !ok || e.Kind != "future.thing" {
		t.Errorf("got %+v ok=%v", e, ok)
	}
}

func TestToolEndOkTriState(t *testing.T) {
	for _, tc := range []struct {
		json string
		want *bool
	}{{`"ok":true`, ptr(true)}, {`"ok":false`, ptr(false)}, {`"ok":null`, nil}} {
		e, ok := DecodeLine([]byte(`{"v":1,"host":"h","seq":1,"kind":"tool.end","agentId":"a","callId":"c",` + tc.json + `,"exitCode":3}`))
		if !ok {
			t.Fatalf("%s rejected", tc.json)
		}
		if (e.Ok == nil) != (tc.want == nil) || (tc.want != nil && *e.Ok != *tc.want) {
			t.Errorf("%s: Ok = %v, want %v", tc.json, e.Ok, tc.want)
		}
		if e.ExitCode == nil || *e.ExitCode != 3 {
			t.Errorf("%s: ExitCode = %v", tc.json, e.ExitCode)
		}
	}
	e, _ := DecodeLine([]byte(`{"v":1,"host":"h","seq":1,"kind":"tool.end","agentId":"a","callId":"c","ok":true}`))
	if e.ExitCode != nil {
		t.Errorf("absent exitCode = %v, want nil", e.ExitCode)
	}
}

func ptr[T any](v T) *T { return &v }

func TestFilesDependsOnKind(t *testing.T) {
	u, ok := DecodeLine([]byte(`{"v":1,"host":"h","seq":1,"kind":"tool.update","agentId":"a","callId":"c","files":["/a"]}`))
	if !ok {
		t.Fatal("update rejected")
	}
	if got := u.UpdateFiles(); len(got) != 1 || got[0] != "/a" {
		t.Errorf("UpdateFiles = %v", got)
	}
	if u.SnapshotFiles() != nil {
		t.Errorf("SnapshotFiles on tool.update = %v, want nil", u.SnapshotFiles())
	}

	s, ok := DecodeLine([]byte(`{"v":1,"host":"h","seq":2,"kind":"snapshot","agents":[],"openCalls":[],"files":[{"path":"/p","lastTouchAt":"2026-01-01T00:00:00.000Z","attribution":"confirmed","agentId":"a"},{"path":"/q","lastTouchAt":"2026-01-01T00:00:00.000Z","attribution":"unattributed"}]}`))
	if !ok {
		t.Fatal("snapshot rejected")
	}
	ft := s.SnapshotFiles()
	if len(ft) != 2 || ft[0].Attribution != "confirmed" || ft[0].AgentID != "a" || ft[1].AgentID != "" {
		t.Errorf("SnapshotFiles = %+v", ft)
	}
	if s.UpdateFiles() != nil {
		t.Errorf("UpdateFiles on snapshot = %v, want nil", s.UpdateFiles())
	}

	// Shape mismatch returns nil.
	if (Envelope{Kind: "snapshot", RawFiles: []byte(`["/a"]`)}).SnapshotFiles() != nil {
		t.Error("snapshot with string files should be nil")
	}
	if (Envelope{Kind: "tool.update", RawFiles: []byte(`[{"path":"/a"}]`)}).UpdateFiles() != nil {
		t.Error("update with object files should be nil")
	}
	if (Envelope{Kind: "tool.update"}).UpdateFiles() != nil {
		t.Error("update without files should be nil")
	}
}

func TestLabel(t *testing.T) {
	for cat, want := range map[string]string{
		"read": "READ", "edit": "EDIT", "write": "WRITE", "command": "RUN",
		"search": "GREP", "agent": "SPAWN", "other": "TOOL", "x": "TOOL", "": "TOOL",
	} {
		if got := Label(cat); got != want {
			t.Errorf("Label(%q) = %q, want %q", cat, got, want)
		}
	}
}
