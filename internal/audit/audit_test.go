package audit

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"agent-view/internal/wire"
)

const minuteMs = 60_000

var nowMs = int64(1_800_000_000_000)

func str(s string) *string { return &s }

func agent(id, provider string, parent *string, link string) wire.Agent {
	return wire.Agent{ID: id, Provider: provider, Name: "n-" + id, Role: "main", ParentID: parent,
		Link: link, Status: "running", LastEventAt: wire.FormatISO(nowMs)}
}

func file(agentID, attribution string, ageMs int64) wire.FileTouch {
	return wire.FileTouch{Path: "/p/" + agentID, LastTouchAt: wire.FormatISO(nowMs - ageMs),
		Attribution: attribution, AgentID: agentID}
}

func snapshot(agents []wire.Agent, files []wire.FileTouch) wire.Envelope {
	return wire.Envelope{V: 1, Host: "h", Seq: 1, Ts: wire.FormatISO(nowMs),
		Ev: wire.Snapshot{Agents: agents, Files: files}}
}

func coverage(open, discovered, malformed int, sources map[string]wire.SourceState) wire.Envelope {
	return wire.Envelope{V: 1, Host: "h", Seq: 2, Ts: wire.FormatISO(nowMs),
		Ev: wire.CoverageReport{Coverage: wire.Coverage{LogsOpen: open, LogsDiscovered: discovered,
			MalformedRecords: malformed, Sources: sources}}}
}

// body builds the /api/snapshot reply the way the server does.
func body(t *testing.T, envs ...wire.Envelope) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString(`{"hosts":[],"envelopes":[`)
	for i, e := range envs {
		if i > 0 {
			b.WriteByte(',')
		}
		eb, err := e.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		b.Write(eb)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

// TestAuditCounts counts agents, links and confirmed writes per provider.
func TestAuditCounts(t *testing.T) {
	agents := []wire.Agent{
		agent("c1", "claude", nil, "none"),
		agent("c2", "claude", str("c1"), "confirmed"),
		agent("c3", "claude", str("c1"), "session"),
		agent("x1", "codex", nil, "none"),
	}
	files := []wire.FileTouch{
		file("c1", "confirmed", 0),
		file("x1", "confirmed", 0),
		{Path: "/p/other", LastTouchAt: wire.FormatISO(nowMs), Attribution: "inferred"},
	}
	b := body(t,
		snapshot(agents, files),
		coverage(2, 3, 4, map[string]wire.SourceState{"claude": {State: "ok"}, "codex": {State: "down"}}),
		coverage(1, 1, 0, map[string]wire.SourceState{"claude": {State: "ok"}, "r": {State: "degraded"}}),
	)
	got, err := Check(b, nowMs)
	if err != nil {
		t.Fatal(err)
	}
	want := Report{
		Counts: map[string]ProviderCounts{
			"claude": {Agents: 3, ConfirmedParents: 1, SessionLinks: 1, ConfirmedWrites: 1},
			"codex":  {Agents: 1, ConfirmedParents: 0, SessionLinks: 0, ConfirmedWrites: 1},
		},
		LoadedLogs: 3, DiscoveredLogs: 4, MalformedRecords: 4, DegradedSources: 2,
	}
	if !equalReports(got, want) {
		t.Errorf("report = %+v, want %+v", got, want)
	}
	wantJSON := `{
  "counts": {
    "claude": {
      "agents": 3,
      "confirmedParents": 1,
      "sessionLinks": 1,
      "confirmedWrites": 1
    },
    "codex": {
      "agents": 1,
      "confirmedParents": 0,
      "sessionLinks": 0,
      "confirmedWrites": 1
    }
  },
  "loadedLogs": 3,
  "discoveredLogs": 4,
  "malformedRecords": 4,
  "degradedSources": 2,
  "writesByLapsedAgents": 0
}
`
	if s := string(got.JSON()); s != wantJSON {
		t.Errorf("JSON = %q, want %q", s, wantJSON)
	}
}

func equalReports(a, b Report) bool {
	if len(a.Counts) != len(b.Counts) {
		return false
	}
	for k, v := range a.Counts {
		if b.Counts[k] != v {
			return false
		}
	}
	return a.LoadedLogs == b.LoadedLogs && a.DiscoveredLogs == b.DiscoveredLogs &&
		a.MalformedRecords == b.MalformedRecords && a.DegradedSources == b.DegradedSources &&
		a.WritesByLapsedAgents == b.WritesByLapsedAgents
}

// TestAuditRejects fails each check with the TypeScript message.
func TestAuditRejects(t *testing.T) {
	ok := agent("a", "claude", nil, "none")
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"not an array", []byte(`{"hosts":[]}`), "envelopes is an array"},
		{"null array", []byte(`{"envelopes":null}`), "envelopes is an array"},
		{"object array", []byte(`{"envelopes":{}}`), "envelopes is an array"},
		{"no agents", body(t, snapshot(nil, nil)), "at least one agent"},
		{"own parent", body(t, snapshot([]wire.Agent{agent("a", "claude", str("a"), "confirmed")}, nil)),
			"agent is its own parent"},
		{"parent cycle", body(t, snapshot([]wire.Agent{
			agent("a", "claude", str("b"), "confirmed"),
			agent("b", "claude", str("a"), "confirmed"),
		}, nil)), "parent cycle"},
		{"no agent id", body(t, snapshot([]wire.Agent{ok}, []wire.FileTouch{file("", "confirmed", 0)})),
			"confirmed write has no agent ID"},
		{"recent unknown agent", body(t, snapshot([]wire.Agent{ok}, []wire.FileTouch{file("gone", "confirmed", 8*minuteMs)})),
			"recent confirmed write has an unknown agent"},
		{"bad time", body(t, snapshot([]wire.Agent{ok}, []wire.FileTouch{{Path: "/p", LastTouchAt: "soon",
			Attribution: "confirmed", AgentID: "gone"}})),
			"recent confirmed write has an unknown agent"},
	}
	for _, c := range cases {
		_, err := Check(c.body, nowMs)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: error = %v, want %q", c.name, err, c.want)
		}
	}
}

// TestAuditRejectsBadEnvelope names the index of an envelope that fails to
// decode and never repeats its content.
func TestAuditRejectsBadEnvelope(t *testing.T) {
	good, err := snapshot([]wire.Agent{agent("a", "claude", nil, "none")}, nil).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	b := []byte(`{"envelopes":[` + string(good) + `,{"v":1,"kind":"coverage","host":"SECRET","seq":"7","ts":"t"}]}`)
	_, err = Check(b, nowMs)
	if err == nil || !strings.HasPrefix(err.Error(), "envelope 1: ") {
		t.Fatalf("error = %v, want prefix %q", err, "envelope 1: ")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error repeats content: %v", err)
	}
	_, err = Check([]byte(`not json`), nowMs)
	if err == nil {
		t.Error("a body that is not JSON passed")
	}
}

// TestAuditLapsedWrite counts a write by an unknown agent when it is old
// enough, and fails when it is recent.
func TestAuditLapsedWrite(t *testing.T) {
	a := agent("a", "claude", nil, "none")
	b := body(t, snapshot([]wire.Agent{a}, []wire.FileTouch{file("gone", "confirmed", 10*minuteMs)}))
	got, err := Check(b, nowMs)
	if err != nil {
		t.Fatal(err)
	}
	if got.WritesByLapsedAgents != 1 {
		t.Errorf("writesByLapsedAgents = %d, want 1", got.WritesByLapsedAgents)
	}
	if got.Counts["claude"].ConfirmedWrites != 0 {
		t.Errorf("a write by an unknown agent counted for a provider: %+v", got.Counts)
	}
	b = body(t, snapshot([]wire.Agent{a}, []wire.FileTouch{file("gone", "confirmed", 8*minuteMs)}))
	if _, err := Check(b, nowMs); err == nil || err.Error() != "recent confirmed write has an unknown agent" {
		t.Errorf("8 minute old write: error = %v", err)
	}
	b = body(t, snapshot([]wire.Agent{a}, []wire.FileTouch{file("gone", "confirmed", 9*minuteMs)}))
	if _, err := Check(b, nowMs); err != nil {
		t.Errorf("9 minute old write: error = %v", err)
	}
}

func portOf(t *testing.T, s *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestFetch reads /api/snapshot and fails unless the status is 200.
func TestFetch(t *testing.T) {
	status := http.StatusOK
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/snapshot" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(`{"envelopes":[]}`))
	}))
	defer s.Close()
	got, err := Fetch(portOf(t, s))
	if err != nil || string(got) != `{"envelopes":[]}` {
		t.Errorf("Fetch = %q, %v", got, err)
	}
	status = http.StatusForbidden
	if _, err := Fetch(portOf(t, s)); err == nil || err.Error() != "snapshot status" {
		t.Errorf("status 403: error = %v", err)
	}
	if _, err := Fetch(0); err == nil {
		t.Error("port 0 passed")
	}
}
