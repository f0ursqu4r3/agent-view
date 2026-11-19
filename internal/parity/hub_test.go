package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"agent-view/internal/wire"
)

const (
	// fakeHost is the --remote host of both hubs.
	fakeHost = "fakehost"
	// streamHost is the host the fake remote's envelopes name. The hubs
	// rewrite it to fakeHost.
	streamHost = "elsewhere"
	// hubStable is how long a hub's normalized body must stay the same.
	hubStable = 3 * time.Second
	// hubMaxWait bounds the wait for the hubs.
	hubMaxWait = 60 * time.Second
	// hubPoll is the time between two reads of the snapshots.
	hubPoll = 200 * time.Millisecond
	// urlWait bounds the wait for a hub's URL line.
	urlWait = 30 * time.Second
	// childWait bounds the wait for a hub's child to end after the hub ends.
	childWait = 2 * time.Second
)

// hubURL matches the line both hubs print when they listen.
var hubURL = regexp.MustCompile(`(?m)^Agent View: (http://127\.0\.0\.1:(\d+))$`)

// isoAt formats t as the collectors do: UTC with milliseconds.
func isoAt(t time.Time) string { return wire.FormatISO(t.UnixMilli()) }

// writeLines writes each line with a newline, creating the directory.
func writeLines(t *testing.T, file string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// hubHome builds a HOME with one Claude session log and one Codex rollout.
// Their records are from the last minute, so their agents are inside the
// linger window. The cwd does not exist, so no agent gets a project root and
// neither collector watches a directory.
func hubHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	base := time.Now().Add(-40 * time.Second)
	fill := func(formats []string) []string {
		lines := make([]string, len(formats))
		for i, f := range formats {
			lines[i] = fmt.Sprintf(f, isoAt(base.Add(time.Duration(i)*time.Second)))
		}
		return lines
	}
	// A confirmed write, then a command that is still open.
	writeLines(t, filepath.Join(home, ".claude", "projects", "hub", "hub-session.jsonl"), fill([]string{
		`{"sessionId":"hub-session","cwd":"/p/hub","timestamp":"%s","type":"user","message":{"content":"x"}}`,
		`{"sessionId":"hub-session","cwd":"/p/hub","timestamp":"%s","type":"assistant","message":{"model":"m-1","content":[{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"out.txt","content":"x"}}]}}`,
		`{"sessionId":"hub-session","cwd":"/p/hub","timestamp":"%s","type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`,
		`{"sessionId":"hub-session","cwd":"/p/hub","timestamp":"%s","type":"assistant","message":{"model":"m-1","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"make test"}}]}}`,
	}))
	// A finished command, then a patch that adds a file.
	writeLines(t, filepath.Join(home, ".codex", "sessions", "hub", "rollout-hub.jsonl"), fill([]string{
		`{"timestamp":"%s","type":"session_meta","payload":{"id":"hub-thread","cwd":"/p/hub","source":"cli","thread_source":"user"}}`,
		`{"timestamp":"%s","type":"turn_context","payload":{"cwd":"/p/hub","model":"m-2"}}`,
		`{"timestamp":"%s","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"%s","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"echo x\"}"}}`,
		`{"timestamp":"%s","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"x"}}`,
		`{"timestamp":"%s","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"p1","input":"*** Begin Patch\n*** Add File: hello.txt\n+hi\n*** End Patch"}}`,
		`{"timestamp":"%s","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"p1","output":[{"type":"input_text","text":"Success. Updated the following files:\nA hello.txt"}]}}`,
	}))
	return home
}

// writeRemoteStream writes what the fake remote prints: a hello, an empty
// snapshot, three agent.upsert (one sub-agent), two tool.start, one tool.end
// and a coverage, with timestamps from the last minute. Every envelope names
// streamHost. It returns the file.
func writeRemoteStream(t *testing.T) string {
	t.Helper()
	base := time.Now().Add(-30 * time.Second)
	at := func(s int) string { return isoAt(base.Add(time.Duration(s) * time.Second)) }
	ptr := func(s string) *string { return &s }
	ok := true
	events := []wire.Event{
		wire.Hello{CollectorVersion: "0.0.1", Platform: "linux-x64", Now: at(0)},
		wire.Snapshot{},
		wire.AgentUpsert{Agent: wire.Agent{
			ID: "remote-main", Provider: "claude", Name: "Remote session", Role: "main", Link: "root",
			Cwd: ptr("/srv/app"), ProjectRoot: ptr("/srv/app"), Model: "m-1", Status: "working", LastEventAt: at(1),
		}},
		wire.AgentUpsert{Agent: wire.Agent{
			ID: "remote-sub", Provider: "claude", Name: "Remote helper", Role: "sub-agent", ParentID: ptr("remote-main"),
			Link: "confirmed", Cwd: ptr("/srv/app"), ProjectRoot: ptr("/srv/app"), Status: "working", LastEventAt: at(2),
		}},
		wire.AgentUpsert{Agent: wire.Agent{
			ID: "remote-codex", Provider: "codex", Name: "Remote rollout", Role: "main", Link: "root",
			Cwd: ptr("/srv/tool"), Status: "idle", LastEventAt: at(3),
		}},
		wire.ToolStart{Call: wire.Call{
			AgentID: "remote-main", CallID: "rc1", StartedAt: at(4), Category: "command", RawName: "Bash", Command: "make test",
		}},
		wire.ToolStart{Call: wire.Call{
			AgentID: "remote-sub", CallID: "rc2", StartedAt: at(5), Category: "write", RawName: "Write",
			Files: []string{"/srv/app/notes.md"},
		}},
		wire.ToolEnd{AgentID: "remote-sub", CallID: "rc2", Ok: &ok, Writes: []wire.Write{{Path: "/srv/app/notes.md", Op: "edit"}}},
		wire.CoverageReport{Coverage: wire.Coverage{
			LogRoots: []string{"/home/dev/.claude/projects"}, LogsOpen: 2, LogsDiscovered: 3, MalformedRecords: 1,
			Sources: map[string]wire.SourceState{
				"logs:/home/dev/.claude/projects": {State: "ok"},
				"fs:/srv/app":                     {State: "degraded", Detail: "watch limit reached"},
			},
		}},
	}
	var lines []byte
	for i, ev := range events {
		// hello and snapshot carry the current seq without taking a number.
		seq := max(0, i-1)
		lines = append(lines, wire.EncodeLine(wire.Envelope{
			V: wire.SchemaVersion, Host: streamHost, Seq: int64(seq), Ts: at(seq), Ev: ev,
		})...)
	}
	file := filepath.Join(t.TempDir(), "remote.ndjson")
	if err := os.WriteFile(file, lines, 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// hubEnv writes the inputs of a hub run and returns the environment of the
// hubs: HOME is the new home, and every remote prints the stream and stays up.
func hubEnv(t *testing.T) []string {
	t.Helper()
	home := hubHome(t)
	file := writeRemoteStream(t)
	if strings.Contains(file, "'") {
		t.Fatalf("the temporary path %q holds a quote", file)
	}
	argv, err := json.Marshal([]string{"sh", "-c", "cat '" + file + "'; exec sleep 600"})
	if err != nil {
		t.Fatal(err)
	}
	return append(childEnv(home), "AGENT_VIEW_REMOTE_COMMAND="+string(argv))
}

// syncBuffer is a bytes.Buffer that a child writes while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// hubProc is one running hub.
type hubProc struct {
	name           string
	cmd            *exec.Cmd
	stdout, stderr syncBuffer
	exited         chan struct{} // closed when Wait returns
	url            string
	port           string
}

// startHub starts a hub from the repository root and waits for its URL. The
// hub is stopped when the test ends.
func startHub(t *testing.T, name string, env []string, argv ...string) *hubProc {
	t.Helper()
	h := &hubProc{name: name, cmd: exec.Command(argv[0], argv[1:]...), exited: make(chan struct{})}
	h.cmd.Dir = repoRoot
	h.cmd.Env = env
	h.cmd.Stdout, h.cmd.Stderr = &h.stdout, &h.stderr
	h.cmd.WaitDelay = exitWait // a child that holds the pipes cannot block Wait
	if err := h.cmd.Start(); err != nil {
		t.Fatalf("start the %s hub: %v", name, err)
	}
	go func() {
		_ = h.cmd.Wait()
		close(h.exited)
	}()
	t.Cleanup(func() { h.stop(t) })

	deadline := time.After(urlWait)
	for {
		if m := hubURL.FindStringSubmatch(h.stdout.String()); m != nil {
			h.url, h.port = m[1], m[2]
			return h
		}
		select {
		case <-h.exited:
			t.Fatalf("the %s hub exited before it printed its URL (stdout %q, stderr %q)", name, h.stdout.String(), h.stderr.String())
		case <-deadline:
			t.Fatalf("the %s hub printed no URL within %v (stdout %q, stderr %q)", name, urlWait, h.stdout.String(), h.stderr.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// stop ends the hub: SIGTERM, then SIGKILL after exitWait. The hub must stop
// its remote children itself: a child still running after the hub is gone
// gets SIGKILL, and the test fails.
func (h *hubProc) stop(t *testing.T) {
	children := childPIDs(t, h.cmd.Process.Pid)
	_ = h.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-h.exited:
	case <-time.After(exitWait):
		t.Errorf("the %s hub still runs %v after SIGTERM", h.name, exitWait)
		_ = h.cmd.Process.Kill()
		<-h.exited
	}
	for _, pid := range children {
		if !gone(pid, childWait) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("the %s hub left its child %d running", h.name, pid)
		}
	}
}

// childPIDs returns the processes whose parent is pid. Without pgrep it
// returns none.
func childPIDs(t *testing.T, pid int) []int {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return nil // no match
	}
	if err != nil {
		t.Logf("pgrep: %v; the children of a hub are not checked", err)
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// gone waits up to d for pid to end. Any error from signal 0 means the
// process is gone or no longer ours.
func gone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// hubBody is one /api/snapshot body, normalized. Each envelope keeps its
// place in s.ordered, coverage included; statuses and timeouts follow the
// P2b-1 rules.
type hubBody struct {
	hosts []any
	s     stream
}

// normalizeHub parses a snapshot body. Each envelope goes through the P2b-1
// normalizer on its own: a body holds one coverage for each host, and a P2b-1
// stream would keep only the last.
func normalizeHub(body []byte) (hubBody, error) {
	var top struct {
		Hosts     []any             `json:"hosts"`
		Envelopes []json.RawMessage `json:"envelopes"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return hubBody{}, fmt.Errorf("snapshot body: %v", err)
	}
	b := hubBody{hosts: top.Hosts}
	for i, raw := range top.Envelopes {
		one, err := normalize([]string{string(raw)}, false)
		if err != nil {
			return hubBody{}, fmt.Errorf("envelope %d: %v", i, err)
		}
		b.s.ordered = append(b.s.ordered, one.ordered...)
		if one.coverage != nil {
			b.s.ordered = append(b.s.ordered, one.coverage)
		}
		b.s.statuses = append(b.s.statuses, one.statuses...)
		b.s.timeouts = append(b.s.timeouts, one.timeouts...)
	}
	sortCanonical(b.s.statuses)
	sortCanonical(b.s.timeouts)
	return b, nil
}

// key is the canonical text of the normalized body.
func (b hubBody) key() string {
	return canonical([]any{b.hosts, b.s.ordered, b.s.statuses, b.s.timeouts})
}

// up reports whether the hosts are the local host and fakeHost, both up.
func (b hubBody) up() bool {
	if len(b.hosts) != 2 {
		return false
	}
	local, _ := b.hosts[0].(map[string]any)
	remote, _ := b.hosts[1].(map[string]any)
	return local["kind"] == "local" && local["state"] == "up" &&
		remote["kind"] == "remote" && remote["label"] == fakeHost && remote["state"] == "up"
}

// snapshot reads the hub's /api/snapshot.
func (h *hubProc) snapshot() (hubBody, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(h.url + "/api/snapshot")
	if err != nil {
		return hubBody{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return hubBody{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return hubBody{}, fmt.Errorf("status %d: %q", resp.StatusCode, body)
	}
	return normalizeHub(body)
}

// waitStable reads the snapshot of every hub until each one reports the local
// host and fakeHost up and its normalized body has not changed for hubStable
// (hubMaxWait at most). It returns the last bodies.
func waitStable(t *testing.T, hubs ...*hubProc) []hubBody {
	t.Helper()
	bodies := make([]hubBody, len(hubs))
	keys := make([]string, len(hubs))
	since := make([]time.Time, len(hubs))
	deadline := time.Now().Add(hubMaxWait)
	for {
		now := time.Now()
		ready := true
		for i, h := range hubs {
			b, err := h.snapshot()
			if err != nil {
				t.Fatalf("the %s hub: %v (stderr %q)", h.name, err, h.stderr.String())
			}
			bodies[i] = b
			if k := b.key(); k != keys[i] {
				keys[i], since[i] = k, now
			}
			if !b.up() || now.Sub(since[i]) < hubStable {
				ready = false
			}
		}
		if ready {
			return bodies
		}
		if now.After(deadline) {
			for i, h := range hubs {
				t.Logf("the %s hub: hosts %s, unchanged for %v", h.name, canonical(bodies[i].hosts), now.Sub(since[i]))
			}
			t.Fatalf("within %v the hubs did not report both hosts up with a body unchanged for %v", hubMaxWait, hubStable)
		}
		time.Sleep(hubPoll)
	}
}

// requireHubContent fails unless every host is up and has an agent, so two
// empty hubs cannot match. The local snapshot must hold the Claude and the
// Codex agent, and the fake remote's snapshot everything its stream sent.
func requireHubContent(t *testing.T, b hubBody) {
	t.Helper()
	snapshots := map[string]envelope{}
	for _, e := range b.s.ordered {
		if host, _ := e["host"].(string); e["kind"] == "snapshot" {
			snapshots[host] = e
		}
	}
	count := func(e envelope, key string) int {
		list, _ := e[key].([]any)
		return len(list)
	}
	if len(b.hosts) != 2 {
		t.Errorf("the Go body has %d hosts, want 2", len(b.hosts))
	}
	for _, h := range b.hosts {
		m, _ := h.(map[string]any)
		label, _ := m["label"].(string)
		if m["state"] != "up" {
			t.Errorf("host %q is %v in the Go body, want up", label, m["state"])
		}
		if count(snapshots[label], "agents") == 0 {
			t.Errorf("host %q has no agent in the Go body", label)
		}
		if m["kind"] == "local" {
			providers := map[any]bool{}
			agents, _ := snapshots[label]["agents"].([]any)
			for _, a := range agents {
				agent, _ := a.(map[string]any)
				providers[agent["provider"]] = true
			}
			if !providers["claude"] || !providers["codex"] {
				t.Errorf("the local snapshot has the providers %v, want claude and codex", providers)
			}
		}
	}
	remote := snapshots[fakeHost]
	for _, c := range []struct {
		key  string
		want int
	}{{"agents", 3}, {"openCalls", 1}, {"files", 1}} {
		if got := count(remote, c.key); got != c.want {
			t.Errorf("the %s snapshot in the Go body has %d %s, want %d", fakeHost, got, c.key, c.want)
		}
	}
}

// The Go hub and the TypeScript hub serve the same /api/snapshot for the same
// local logs and the same fake remote.
func TestParityHubSnapshot(t *testing.T) {
	requireBun(t)
	env := hubEnv(t)
	goHub := startHub(t, "Go", env, goBin, "serve", "--port", "0", "--remote", fakeHost)
	tsHub := startHub(t, "TypeScript", env, bunPath, "src/cli.ts", "--no-open", "--port", "0", "--remote", fakeHost)
	bodies := waitStable(t, goHub, tsHub)
	goBody, tsBody := bodies[0], bodies[1]

	if !reflect.DeepEqual(goBody.hosts, tsBody.hosts) {
		t.Errorf("hosts: Go %s; TypeScript %s", canonical(goBody.hosts), canonical(tsBody.hosts))
	}
	for _, d := range diffStreams(goBody.s, tsBody.s, false) {
		t.Error(d)
	}
	requireHubContent(t, goBody)
	t.Logf("compared %d hosts and %d envelopes", len(tsBody.hosts), len(tsBody.s.ordered))
}

// runGoAudit runs "agent-view audit --port <port>" and returns its stdout.
func runGoAudit(t *testing.T, port string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "audit", "--port", port)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("agent-view audit: %v\n%s", err, stderr.String())
	}
	return string(out)
}

// The TypeScript live audit and agent-view audit print the same report for
// the Go hub.
func TestParityAudit(t *testing.T) {
	requireBun(t)
	goHub := startHub(t, "Go", hubEnv(t), goBin, "serve", "--port", "0", "--remote", fakeHost)
	waitStable(t, goHub)

	goOut := runGoAudit(t, goHub.port)
	tsOut := strings.Join(runBun(t, "test/live-audit.ts", goHub.port), "\n")
	var goReport, tsReport any
	if err := json.Unmarshal([]byte(goOut), &goReport); err != nil {
		t.Fatalf("Go report: %v", err)
	}
	if err := json.Unmarshal([]byte(tsOut), &tsReport); err != nil {
		t.Fatalf("TypeScript report: %v", err)
	}
	if !reflect.DeepEqual(goReport, tsReport) {
		t.Errorf("report: Go %s; TypeScript %s", canonical(goReport), canonical(tsReport))
	}

	var counts struct {
		Counts map[string]struct {
			Agents int `json:"agents"`
		} `json:"counts"`
	}
	if err := json.Unmarshal([]byte(goOut), &counts); err != nil {
		t.Fatal(err)
	}
	if n := counts.Counts["claude"].Agents; n < 1 {
		t.Errorf("the Go report has %d Claude agents, want at least 1", n)
	}
	t.Logf("report: %s", canonical(goReport))
}
