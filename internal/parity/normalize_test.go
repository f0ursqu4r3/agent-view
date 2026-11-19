package parity

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// envelope is one parsed NDJSON line.
type envelope = map[string]any

// stream is a collector's output, normalized for comparison.
type stream struct {
	// ordered holds every envelope that is compared in order.
	ordered []envelope
	// statuses holds the source.status envelopes, sorted by canonical JSON.
	statuses []envelope
	// timeouts holds the tool.end envelopes with ok null, sorted by
	// canonical JSON. A timeout lands at a tick, so its position depends on
	// timing.
	timeouts []envelope
	// coverage is the last coverage envelope, or nil.
	coverage envelope
}

// normalize parses the lines of one collector run. It drops seq and ts from
// every envelope, and now, collectorVersion and platform from hello. It
// removes an empty-string command or pattern from a call: TypeScript writes
// "" and Go omits the key. Local mode also drops fs.change, the fs: statuses
// and the coverage's watchRoots and fs: sources, because copied logs name
// real projects.
func normalize(lines []string, local bool) (stream, error) {
	var s stream
	for i, line := range lines {
		var env envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			return stream{}, fmt.Errorf("line %d is not a JSON object: %v", i, err)
		}
		delete(env, "seq")
		delete(env, "ts")
		switch kind, _ := env["kind"].(string); kind {
		case "hello":
			delete(env, "now")
			delete(env, "collectorVersion")
			delete(env, "platform")
		case "coverage":
			if cov, ok := env["coverage"].(map[string]any); ok && local {
				delete(cov, "watchRoots")
				if sources, ok := cov["sources"].(map[string]any); ok {
					maps.DeleteFunc(sources, func(k string, _ any) bool { return strings.HasPrefix(k, "fs:") })
				}
			}
			s.coverage = env
			continue
		case "source.status":
			if source, _ := env["source"].(string); local && strings.HasPrefix(source, "fs:") {
				continue
			}
			s.statuses = append(s.statuses, env)
			continue
		case "fs.change":
			if local {
				continue
			}
		case "tool.end":
			if ok, present := env["ok"]; present && ok == nil {
				s.timeouts = append(s.timeouts, env)
				continue
			}
		case "tool.start":
			dropEmptyCallText(env["call"])
		case "snapshot":
			if calls, ok := env["openCalls"].([]any); ok {
				for _, c := range calls {
					dropEmptyCallText(c)
				}
			}
		}
		s.ordered = append(s.ordered, env)
	}
	sortCanonical(s.statuses)
	sortCanonical(s.timeouts)
	return s, nil
}

// dropEmptyCallText removes a command or pattern that is the empty string.
func dropEmptyCallText(call any) {
	c, ok := call.(map[string]any)
	if !ok {
		return
	}
	for _, k := range []string{"command", "pattern"} {
		if v, present := c[k]; present && v == "" {
			delete(c, k)
		}
	}
}

// canonical is the JSON text of v with sorted object keys.
func canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	return string(b)
}

func sortCanonical(envs []envelope) {
	sort.SliceStable(envs, func(i, j int) bool { return canonical(envs[i]) < canonical(envs[j]) })
}

// ids returns the agent ID and the call ID an envelope names, if any.
func ids(env envelope) (agentID, callID string) {
	field := func(v any, k string) string {
		m, _ := v.(map[string]any)
		s, _ := m[k].(string)
		return s
	}
	agentID, callID = field(env, "agentId"), field(env, "callId")
	if call, ok := env["call"]; ok {
		agentID, callID = field(call, "agentId"), field(call, "callId")
	}
	if agent, ok := env["agent"]; ok {
		agentID = field(agent, "id")
	}
	return agentID, callID
}

// describe names an envelope. With idsOnly it gives only the kind, the agent
// ID and the call ID, never log content.
func describe(env envelope, idsOnly bool) string {
	if !idsOnly {
		return canonical(env)
	}
	kind, _ := env["kind"].(string)
	agentID, callID := ids(env)
	return fmt.Sprintf("kind=%s agentId=%s callId=%s", kind, agentID, callID)
}

// maxDiffs caps the differences reported for each part of a stream.
const maxDiffs = 10

// diffStreams lists the differences between the Go stream and the
// TypeScript stream. Parsed values are compared, so key order never matters.
// With idsOnly every line gives only an index, a kind and IDs.
func diffStreams(goS, tsS stream, idsOnly bool) []string {
	var out []string
	add := func(format string, a ...any) {
		out = append(out, fmt.Sprintf(format, a...))
	}

	if len(goS.ordered) != len(tsS.ordered) {
		add("ordered envelopes: Go %d, TypeScript %d", len(goS.ordered), len(tsS.ordered))
	}
	n := 0
	for i := range max(len(goS.ordered), len(tsS.ordered)) {
		var g, s envelope
		if i < len(goS.ordered) {
			g = goS.ordered[i]
		}
		if i < len(tsS.ordered) {
			s = tsS.ordered[i]
		}
		if g != nil && s != nil && reflect.DeepEqual(g, s) {
			continue
		}
		if n++; n > maxDiffs {
			add("ordered envelopes: more differences not shown")
			break
		}
		switch {
		case g == nil:
			add("index %d: missing in Go; TypeScript %s", i, describe(s, idsOnly))
		case s == nil:
			add("index %d: Go %s; missing in TypeScript", i, describe(g, idsOnly))
		default:
			add("index %d: Go %s; TypeScript %s", i, describe(g, idsOnly), describe(s, idsOnly))
		}
	}

	out = append(out, diffMultiset("source.status", goS.statuses, tsS.statuses, idsOnly)...)
	out = append(out, diffMultiset("timed-out tool.end", goS.timeouts, tsS.timeouts, idsOnly)...)

	if !reflect.DeepEqual(goS.coverage, tsS.coverage) {
		if idsOnly {
			add("last coverage differs in %v", differingKeys(goS.coverage, tsS.coverage))
		} else {
			add("last coverage: Go %s; TypeScript %s", canonical(goS.coverage), canonical(tsS.coverage))
		}
	}
	return out
}

// diffMultiset lists the envelopes that only one side has, counting repeats.
func diffMultiset(name string, goEnvs, tsEnvs []envelope, idsOnly bool) []string {
	count := map[string]int{}
	byKey := map[string]envelope{}
	for _, e := range goEnvs {
		k := canonical(e)
		count[k]++
		byKey[k] = e
	}
	for _, e := range tsEnvs {
		k := canonical(e)
		count[k]--
		byKey[k] = e
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(count)) {
		switch c := count[k]; {
		case c > 0:
			out = append(out, fmt.Sprintf("%s: %d more in Go: %s", name, c, describe(byKey[k], idsOnly)))
		case c < 0:
			out = append(out, fmt.Sprintf("%s: %d more in TypeScript: %s", name, -c, describe(byKey[k], idsOnly)))
		}
		if len(out) == maxDiffs {
			out = append(out, name+": more differences not shown")
			break
		}
	}
	return out
}

// differingKeys names the coverage fields that differ, without their values.
func differingKeys(goEnv, tsEnv envelope) []string {
	goCov, _ := goEnv["coverage"].(map[string]any)
	tsCov, _ := tsEnv["coverage"].(map[string]any)
	keys := map[string]bool{}
	for k := range goCov {
		keys[k] = true
	}
	for k := range tsCov {
		keys[k] = true
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if !reflect.DeepEqual(goCov[k], tsCov[k]) {
			out = append(out, k)
		}
	}
	if goEnv == nil || tsEnv == nil {
		out = append(out, "(missing envelope)")
	}
	return out
}

func mustNormalize(t *testing.T, local bool, lines ...string) stream {
	t.Helper()
	s, err := normalize(lines, local)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// hello and every envelope lose seq and ts; hello also loses now, collectorVersion and platform
func TestNormalizeDropsTimes(t *testing.T) {
	s := mustNormalize(t, false,
		`{"v":1,"host":"h","seq":0,"kind":"hello","collectorVersion":"0.2.0","platform":"darwin-arm64","now":"2026-01-01T00:00:00.000Z","ts":"2026-01-01T00:00:00.000Z"}`,
		`{"v":1,"host":"h","seq":3,"kind":"agent.status","agentId":"a","status":"idle","ts":"2026-01-01T00:00:01.000Z"}`,
	)
	want := []envelope{
		{"v": 1.0, "host": "h", "kind": "hello"},
		{"v": 1.0, "host": "h", "kind": "agent.status", "agentId": "a", "status": "idle"},
	}
	if !reflect.DeepEqual(s.ordered, want) {
		t.Errorf("ordered = %v, want %v", s.ordered, want)
	}
}

// key order never matters, and a changed field is reported at its index
func TestDiffStreamsComparesParsedValues(t *testing.T) {
	a := mustNormalize(t, false, `{"kind":"tool.end","agentId":"a","callId":"c","ok":true}`)
	b := mustNormalize(t, false, `{"ok":true,"callId":"c","agentId":"a","kind":"tool.end"}`)
	if d := diffStreams(a, b, false); len(d) != 0 {
		t.Errorf("same values, other key order: %v", d)
	}
	c := mustNormalize(t, false, `{"kind":"tool.end","agentId":"a","callId":"c","ok":false}`)
	d := diffStreams(a, c, false)
	if len(d) != 1 || !strings.HasPrefix(d[0], "index 0:") {
		t.Errorf("diff = %v, want one difference at index 0", d)
	}
	e := mustNormalize(t, false, `{"kind":"tool.end","agentId":"a","callId":"c","ok":true}`, `{"kind":"agent.status","agentId":"a","status":"idle"}`)
	if d := diffStreams(a, e, false); len(d) != 2 || !strings.Contains(d[1], "missing in Go") {
		t.Errorf("diff = %v, want a count line and one missing envelope", d)
	}
}

// source.status and timed-out tool.end compare as multisets; only the last coverage counts
func TestNormalizeMultisetsAndCoverage(t *testing.T) {
	a := mustNormalize(t, false,
		`{"kind":"coverage","coverage":{"logsOpen":0}}`,
		`{"kind":"source.status","source":"logs:/a","state":"ok"}`,
		`{"kind":"tool.end","agentId":"a","callId":"1","ok":null}`,
		`{"kind":"tool.end","agentId":"a","callId":"2","ok":false}`,
		`{"kind":"source.status","source":"logs:/b","state":"ok"}`,
		`{"kind":"tool.end","agentId":"a","callId":"3","ok":null}`,
		`{"kind":"coverage","coverage":{"logsOpen":2,"sources":{"logs:/a":{"state":"ok"},"logs:/b":{"state":"ok"}}}}`,
	)
	b := mustNormalize(t, false,
		`{"kind":"tool.end","agentId":"a","callId":"3","ok":null}`,
		`{"kind":"source.status","source":"logs:/b","state":"ok"}`,
		`{"kind":"source.status","source":"logs:/a","state":"ok"}`,
		`{"kind":"tool.end","agentId":"a","callId":"2","ok":false}`,
		`{"kind":"tool.end","agentId":"a","callId":"1","ok":null}`,
		`{"kind":"coverage","coverage":{"sources":{"logs:/b":{"state":"ok"},"logs:/a":{"state":"ok"}},"logsOpen":2}}`,
	)
	if d := diffStreams(a, b, false); len(d) != 0 {
		t.Errorf("diff = %v, want none", d)
	}
	if len(a.ordered) != 1 || len(a.statuses) != 2 || len(a.timeouts) != 2 {
		t.Errorf("ordered %d, statuses %d, timeouts %d; want 1, 2, 2", len(a.ordered), len(a.statuses), len(a.timeouts))
	}
	// A repeated status counts twice.
	c := mustNormalize(t, false,
		`{"kind":"source.status","source":"logs:/a","state":"ok"}`,
		`{"kind":"source.status","source":"logs:/a","state":"ok"}`,
		`{"kind":"source.status","source":"logs:/b","state":"ok"}`,
		`{"kind":"tool.end","agentId":"a","callId":"2","ok":false}`,
		`{"kind":"tool.end","agentId":"a","callId":"1","ok":null}`,
		`{"kind":"tool.end","agentId":"a","callId":"3","ok":null}`,
		`{"kind":"coverage","coverage":{"logsOpen":2,"sources":{"logs:/a":{"state":"ok"},"logs:/b":{"state":"ok"}}}}`,
	)
	if d := diffStreams(a, c, false); len(d) != 1 || !strings.Contains(d[0], "1 more in TypeScript") {
		t.Errorf("diff = %v, want one extra status in TypeScript", d)
	}
}

// an empty command or pattern is removed from tool.start and from snapshot openCalls
func TestNormalizeEmptyCallText(t *testing.T) {
	s := mustNormalize(t, false,
		`{"kind":"tool.start","call":{"callId":"1","command":"","pattern":""}}`,
		`{"kind":"tool.start","call":{"callId":"2","command":"ls","pattern":"x"}}`,
		`{"kind":"snapshot","agents":[],"openCalls":[{"callId":"1","command":""},{"callId":"3","pattern":""}],"files":[]}`,
	)
	want := []envelope{
		{"kind": "tool.start", "call": map[string]any{"callId": "1"}},
		{"kind": "tool.start", "call": map[string]any{"callId": "2", "command": "ls", "pattern": "x"}},
		{"kind": "snapshot", "agents": []any{}, "openCalls": []any{map[string]any{"callId": "1"}, map[string]any{"callId": "3"}}, "files": []any{}},
	}
	if !reflect.DeepEqual(s.ordered, want) {
		t.Errorf("ordered = %v, want %v", s.ordered, want)
	}
}

// local mode drops fs.change, fs: statuses, watchRoots and fs: coverage sources
func TestNormalizeLocal(t *testing.T) {
	lines := []string{
		`{"kind":"fs.change","path":"/x","op":"change"}`,
		`{"kind":"source.status","source":"fs:/x","state":"ok"}`,
		`{"kind":"source.status","source":"logs:/a","state":"ok"}`,
		`{"kind":"coverage","coverage":{"watchRoots":["/x"],"fsDropped":0,"sources":{"fs:/x":{"state":"ok"},"logs:/a":{"state":"ok"}}}}`,
	}
	s := mustNormalize(t, true, lines...)
	if len(s.ordered) != 0 || len(s.statuses) != 1 {
		t.Errorf("ordered %v, statuses %v; want none and logs:/a", s.ordered, s.statuses)
	}
	wantCov := envelope{"kind": "coverage", "coverage": map[string]any{"fsDropped": 0.0, "sources": map[string]any{"logs:/a": map[string]any{"state": "ok"}}}}
	if !reflect.DeepEqual(s.coverage, wantCov) {
		t.Errorf("coverage = %v, want %v", s.coverage, wantCov)
	}
	full := mustNormalize(t, false, lines...)
	if len(full.ordered) != 1 || len(full.statuses) != 2 {
		t.Errorf("fixture mode: ordered %d, statuses %d; want 1 and 2", len(full.ordered), len(full.statuses))
	}
}

// with idsOnly a difference names only the index, the kind and the IDs
func TestDiffStreamsIDsOnly(t *testing.T) {
	a := mustNormalize(t, true, `{"kind":"tool.output","agentId":"a","callId":"c","chunk":"secret one","truncated":false}`)
	b := mustNormalize(t, true, `{"kind":"tool.output","agentId":"a","callId":"c","chunk":"secret two","truncated":false}`)
	d := diffStreams(a, b, true)
	if len(d) != 1 || strings.Contains(d[0], "secret") || !strings.Contains(d[0], "index 0: Go kind=tool.output agentId=a callId=c") {
		t.Errorf("diff = %v, want one line with IDs only", d)
	}
	c := mustNormalize(t, true, `{"kind":"tool.start","call":{"agentId":"a","callId":"c","command":"secret"}}`)
	e := mustNormalize(t, true, `{"kind":"agent.upsert","agent":{"id":"b","cwd":"/secret"}}`)
	d = diffStreams(c, e, true)
	if len(d) != 1 || strings.Contains(d[0], "secret") || !strings.Contains(d[0], "kind=tool.start agentId=a callId=c") || !strings.Contains(d[0], "kind=agent.upsert agentId=b callId=") {
		t.Errorf("diff = %v, want IDs only", d)
	}
	f := mustNormalize(t, true, `{"kind":"coverage","coverage":{"logRoots":["/secret"],"logsOpen":1}}`)
	g := mustNormalize(t, true, `{"kind":"coverage","coverage":{"logRoots":["/secret"],"logsOpen":2}}`)
	if d := diffStreams(f, g, true); len(d) != 1 || strings.Contains(d[0], "secret") || !strings.Contains(d[0], "logsOpen") {
		t.Errorf("diff = %v, want the differing coverage key only", d)
	}
}
