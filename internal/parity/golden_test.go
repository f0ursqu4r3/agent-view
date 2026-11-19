package parity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The golden files hold results that the TypeScript collector, hub and audit
// script gave once, before the TypeScript code was removed (see
// testdata/golden/parity/README.md). The TestGolden tests compare the Go side
// with them and need no Bun.

const (
	// homeMark replaces the temporary HOME in every golden string.
	homeMark = "<HOME>"
	// localMark replaces the hub's local host label (the machine's name).
	localMark = "<LOCAL>"
)

// goldenDir is where the golden files live.
func goldenDir() string { return filepath.Join(repoRoot, "testdata", "golden", "parity") }

// rewriteStrings applies f to every string in v, map keys included.
func rewriteStrings(v any, f func(string) string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[f(k)] = rewriteStrings(e, f)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = rewriteStrings(e, f)
		}
		return out
	case string:
		return f(x)
	default:
		return v
	}
}

// freezer turns the run-specific parts of a string into stable marks: the
// temporary HOME (both its spelling and its real path) becomes <HOME>; with
// t0 set, an RFC 3339 time becomes T+<ms> after t0; with local set, the local
// host label, alone or before "/" in an agent ID, becomes <LOCAL>.
type freezer struct {
	homes []string // longest first
	t0    time.Time
	local string
}

func newFreezer(t *testing.T, home string, t0 time.Time) freezer {
	t.Helper()
	homes := []string{home}
	if real, err := filepath.EvalSymlinks(home); err == nil && real != home {
		homes = append(homes, real)
	}
	// Replace the longer form first: /var/x is a suffix of /private/var/x.
	if len(homes) == 2 && len(homes[1]) > len(homes[0]) {
		homes[0], homes[1] = homes[1], homes[0]
	}
	return freezer{homes: homes, t0: t0}
}

func (z freezer) str(s string) string {
	if !z.t0.IsZero() {
		if at, err := time.Parse(time.RFC3339Nano, s); err == nil {
			ms := at.UnixMilli() - z.t0.UnixMilli()
			if ms < 0 {
				return fmt.Sprintf("T-%d", -ms)
			}
			return fmt.Sprintf("T+%d", ms)
		}
	}
	if z.local != "" {
		if s == z.local {
			return localMark
		}
		if rest, ok := strings.CutPrefix(s, z.local+"/"); ok {
			s = localMark + "/" + rest
		}
	}
	for _, h := range z.homes {
		s = strings.ReplaceAll(s, h, homeMark)
	}
	return s
}

func (z freezer) value(v any) any { return rewriteStrings(v, z.str) }

func (z freezer) envs(envs []envelope) []envelope {
	if envs == nil {
		return nil
	}
	out := make([]envelope, len(envs))
	for i, e := range envs {
		out[i] = z.value(e).(envelope)
	}
	return out
}

func (z freezer) stream(s stream) stream {
	out := stream{ordered: z.envs(s.ordered), statuses: z.envs(s.statuses), timeouts: z.envs(s.timeouts)}
	if s.coverage != nil {
		out.coverage = z.value(s.coverage).(envelope)
	}
	return out
}

// streamFile is the JSON form of a stream in fixtures.json and hub.json.
type streamFile struct {
	Hosts    []any      `json:"hosts"`
	Ordered  []envelope `json:"ordered"`
	Statuses []envelope `json:"statuses"`
	Timeouts []envelope `json:"timeouts"`
	Coverage envelope   `json:"coverage"`
}

// encodeJSON writes v with sorted keys, two-space indent (with indent set),
// no HTML escaping and a trailing newline. TestGoldenDetectsChange uses it to
// write a changed copy of a golden in the golden's own format.
func encodeJSON(v any, indent bool) ([]byte, error) {
	// A round trip through any sorts the keys of structs too.
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// readStreamFile reads fixtures.json or hub.json.
func readStreamFile(path string) (streamFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return streamFile{}, err
	}
	var f streamFile
	if err := json.Unmarshal(data, &f); err != nil {
		return streamFile{}, fmt.Errorf("%s: %v", filepath.Base(path), err)
	}
	return f, nil
}

func (f streamFile) stream() stream {
	return stream{ordered: f.Ordered, statuses: f.Statuses, timeouts: f.Timeouts, coverage: f.Coverage}
}

// readNDJSON reads a demo golden: one envelope per line.
func readNDJSON(path string) ([]envelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []envelope
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for i := 0; sc.Scan(); i++ {
		var e envelope
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s line %d: %v", filepath.Base(path), i, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// readAny reads audit.json.
func readAny(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.Base(path), err)
	}
	return v, nil
}

// diffStreamGolden compares a frozen Go stream (and hosts, for the hub) with
// a stream golden. The golden plays the TypeScript side of diffStreams.
func diffStreamGolden(hosts []any, s stream, path string) ([]string, error) {
	f, err := readStreamFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	if !reflect.DeepEqual(hosts, f.Hosts) {
		out = append(out, fmt.Sprintf("hosts: Go %s; golden %s", canonical(hosts), canonical(f.Hosts)))
	}
	return append(out, diffStreams(s, f.stream())...), nil
}

// diffEventsGolden compares the Go demo events with a demo golden.
func diffEventsGolden(events []envelope, path string) ([]string, error) {
	want, err := readNDJSON(path)
	if err != nil {
		return nil, err
	}
	var out []string
	if len(events) != len(want) {
		out = append(out, fmt.Sprintf("events: Go %d, golden %d", len(events), len(want)))
	}
	for i := range min(len(events), len(want)) {
		if reflect.DeepEqual(events[i], want[i]) {
			continue
		}
		out = append(out, fmt.Sprintf("event %d: Go %s; golden %s", i, canonical(events[i]), canonical(want[i])))
		if len(out) >= maxDiffs {
			out = append(out, "more differences not shown")
			break
		}
	}
	return out, nil
}

// diffAnyGolden compares the Go audit report with audit.json.
func diffAnyGolden(report any, path string) ([]string, error) {
	want, err := readAny(path)
	if err != nil {
		return nil, err
	}
	if reflect.DeepEqual(report, want) {
		return nil, nil
	}
	return []string{fmt.Sprintf("report: Go %s; golden %s", canonical(report), canonical(want))}, nil
}

func reportDiffs(t *testing.T, diffs []string, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		t.Error(d)
	}
}

// demoCases are the demo traces that the demo goldens hold.
var demoCases = []struct {
	scale string
	ms    float64
}{{"normal", 60_000}, {"stress", 10_000}}

func demoGoldenName(scale string, ms float64) string {
	return fmt.Sprintf("demo-%s-%d.ndjson", scale, int64(ms))
}

// The Go collector gives the stored TypeScript stream for the fixture logs.
func TestGoldenFixtures(t *testing.T) {
	home := fixtureHome(t)
	r := runCollector([]string{goBin, "collect", "--stdio", "--label", "parity"}, home)
	if r.err != nil || r.timedOut {
		t.Fatalf("Go collector: err %v, timed out %v (stderr %q)", r.err, r.timedOut, r.stderr)
	}
	s, err := normalize(r.lines)
	if err != nil {
		t.Fatalf("Go output: %v", err)
	}
	s = newFreezer(t, home, time.Time{}).stream(s)
	diffs, err := diffStreamGolden(nil, s, filepath.Join(goldenDir(), "fixtures.json"))
	reportDiffs(t, diffs, err)
	t.Logf("compared %d ordered envelopes with the golden", len(s.ordered))
}

// The Go demo emits the stored TypeScript virtual-clock trace, ts included.
func TestGoldenDemoTrace(t *testing.T) {
	for _, c := range demoCases {
		t.Run(c.scale, func(t *testing.T) {
			events := goDemoTrace(t, c.scale, c.ms)
			diffs, err := diffEventsGolden(events, filepath.Join(goldenDir(), demoGoldenName(c.scale, c.ms)))
			reportDiffs(t, diffs, err)
			t.Logf("compared %d events with the golden", len(events))
		})
	}
}

// The Go hub serves the stored TypeScript /api/snapshot body.
func TestGoldenHubSnapshot(t *testing.T) {
	in := hubEnv(t)
	goHub := startHub(t, "Go", in.env, goBin, "serve", "--port", "0", "--remote", fakeHost)
	body := waitStable(t, goHub)[0]
	requireHubContent(t, body)
	hosts, s := in.freeze(t, body)
	diffs, err := diffStreamGolden(hosts, s, filepath.Join(goldenDir(), "hub.json"))
	reportDiffs(t, diffs, err)
	t.Logf("compared %d hosts and %d envelopes with the golden", len(hosts), len(s.ordered))
}

// agent-view audit prints the stored TypeScript live-audit report.
func TestGoldenAudit(t *testing.T) {
	in := hubEnv(t)
	goHub := startHub(t, "Go", in.env, goBin, "serve", "--port", "0", "--remote", fakeHost)
	waitStable(t, goHub)
	var report any
	if err := json.Unmarshal([]byte(runGoAudit(t, goHub.port)), &report); err != nil {
		t.Fatalf("Go report: %v", err)
	}
	diffs, err := diffAnyGolden(report, filepath.Join(goldenDir(), "audit.json"))
	reportDiffs(t, diffs, err)
}

// The goldens hold real content, so a comparison with them cannot pass empty.
func TestGoldenNonVacuous(t *testing.T) {
	fixtures, err := readStreamFile(filepath.Join(goldenDir(), "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(fixtures.Ordered); n < 100 {
		t.Errorf("fixtures.json holds %d ordered envelopes, want at least 100", n)
	}
	if len(fixtures.Statuses) == 0 || len(fixtures.Timeouts) == 0 || fixtures.Coverage == nil {
		t.Errorf("fixtures.json: statuses %d, timeouts %d, coverage %v; want all present",
			len(fixtures.Statuses), len(fixtures.Timeouts), fixtures.Coverage != nil)
	}
	for _, c := range []struct {
		scale string
		ms    float64
		want  int
	}{{"normal", 60_000, 400}, {"stress", 10_000, 2_000}} {
		events, err := readNDJSON(filepath.Join(goldenDir(), demoGoldenName(c.scale, c.ms)))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) < c.want {
			t.Errorf("%s holds %d lines, want at least %d", demoGoldenName(c.scale, c.ms), len(events), c.want)
		}
	}
	hub, err := readStreamFile(filepath.Join(goldenDir(), "hub.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hub.Hosts) != 2 {
		t.Errorf("hub.json holds %d hosts, want 2", len(hub.Hosts))
	}
	agents := 0
	for _, e := range hub.Ordered {
		if e["kind"] == "snapshot" {
			list, _ := e["agents"].([]any)
			agents += len(list)
		}
	}
	if agents < 4 {
		t.Errorf("hub.json holds %d agents, want at least 4", agents)
	}
	audit, err := readAny(filepath.Join(goldenDir(), "audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	claude, _ := audit.(map[string]any)["counts"].(map[string]any)["claude"].(map[string]any)
	if n, _ := claude["agents"].(float64); n <= 0 {
		t.Errorf("audit.json holds %v Claude agents, want more than 0", claude["agents"])
	}
}

// changeOne changes the first number or string value it finds below v,
// skipping the "kind" field, and reports whether it changed one.
func changeOne(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			switch e := x[k].(type) {
			case string:
				if k != "kind" {
					x[k] = e + "-changed"
					return true
				}
			case float64:
				x[k] = e + 1
				return true
			default:
				if changeOne(e) {
					return true
				}
			}
		}
	case []any:
		for _, e := range x {
			if changeOne(e) {
				return true
			}
		}
	}
	return false
}

// A copy of each golden with one value changed fails the comparison that the
// unchanged golden passes.
func TestGoldenDetectsChange(t *testing.T) {
	type golden struct {
		name    string
		ndjson  bool
		compare func(path string) ([]string, error)
	}
	// Each comparison takes the unchanged golden's own content as the Go side.
	fixtures, err := readStreamFile(filepath.Join(goldenDir(), "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	hub, err := readStreamFile(filepath.Join(goldenDir(), "hub.json"))
	if err != nil {
		t.Fatal(err)
	}
	audit, err := readAny(filepath.Join(goldenDir(), "audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	goldens := []golden{
		{"fixtures.json", false, func(p string) ([]string, error) { return diffStreamGolden(nil, fixtures.stream(), p) }},
		{"hub.json", false, func(p string) ([]string, error) { return diffStreamGolden(hub.Hosts, hub.stream(), p) }},
		{"audit.json", false, func(p string) ([]string, error) { return diffAnyGolden(audit, p) }},
	}
	for _, c := range demoCases {
		name := demoGoldenName(c.scale, c.ms)
		events, err := readNDJSON(filepath.Join(goldenDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		goldens = append(goldens, golden{name, true, func(p string) ([]string, error) { return diffEventsGolden(events, p) }})
	}

	for _, g := range goldens {
		t.Run(g.name, func(t *testing.T) {
			orig := filepath.Join(goldenDir(), g.name)
			if d, err := g.compare(orig); err != nil || len(d) != 0 {
				t.Fatalf("the unchanged golden: err %v, diffs %v", err, d)
			}
			data, err := os.ReadFile(orig)
			if err != nil {
				t.Fatal(err)
			}
			var changed []byte
			if g.ndjson {
				lines := strings.SplitAfter(string(data), "\n")
				mid := len(lines) / 2
				var e any
				if err := json.Unmarshal([]byte(lines[mid]), &e); err != nil {
					t.Fatal(err)
				}
				if !changeOne(e) {
					t.Fatal("no value to change")
				}
				line, err := encodeJSON(e, false)
				if err != nil {
					t.Fatal(err)
				}
				lines[mid] = string(line)
				changed = []byte(strings.Join(lines, ""))
			} else {
				var v any
				if err := json.Unmarshal(data, &v); err != nil {
					t.Fatal(err)
				}
				if !changeOne(v) {
					t.Fatal("no value to change")
				}
				if changed, err = encodeJSON(v, true); err != nil {
					t.Fatal(err)
				}
			}
			copyPath := filepath.Join(t.TempDir(), g.name)
			if err := os.WriteFile(copyPath, changed, 0o644); err != nil {
				t.Fatal(err)
			}
			d, err := g.compare(copyPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(d) == 0 {
				t.Error("the changed golden passed the comparison")
			}
			t.Logf("first difference: %s", d[0])
		})
	}
}

// The freezer replaces both spellings of HOME, in keys and values, turns
// times into offsets from t0, and replaces the local host label.
func TestFreezer(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	z := freezer{homes: []string{"/private/var/h", "/var/h"}, t0: t0, local: "box"}
	in := map[string]any{
		"host":                         "box",
		"id":                           "box/claude:default:s",
		"other":                        "boxer/x",
		"logs:/var/h/.claude/projects": map[string]any{"state": "ok"},
		"roots":                        []any{"/private/var/h/.codex/sessions", "/srv"},
		"at":                           "2026-01-01T00:00:01.500Z",
		"before":                       "2025-12-31T23:59:59.000Z",
		"n":                            1.0,
	}
	want := map[string]any{
		"host":                         "<LOCAL>",
		"id":                           "<LOCAL>/claude:default:s",
		"other":                        "boxer/x",
		"logs:<HOME>/.claude/projects": map[string]any{"state": "ok"},
		"roots":                        []any{"<HOME>/.codex/sessions", "/srv"},
		"at":                           "T+1500",
		"before":                       "T-1000",
		"n":                            1.0,
	}
	if got := z.value(in); !reflect.DeepEqual(got, want) {
		t.Errorf("freeze = %s, want %s", canonical(got), canonical(want))
	}
	if got := (freezer{homes: []string{"/var/h"}}).str("2026-01-01T00:00:01.500Z"); got != "2026-01-01T00:00:01.500Z" {
		t.Errorf("without t0 a time stays, got %q", got)
	}
}
