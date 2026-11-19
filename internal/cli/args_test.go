package cli

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseFlags(t *testing.T) {
	a, err := Parse([]string{
		"--demo", "--demo-scale", "stress", "--watch", "/a", "--watch", "/b",
		"--linger", "5", "--reduced-motion",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Args{
		Mode: "tui", Demo: true, DemoScale: "stress", Watch: []string{"/a", "/b"},
		LingerMs: 300_000, Reduced: true,
	}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("Parse = %+v, want %+v", a, want)
	}
	wantFlags := []string{
		"--demo", "--demo-scale", "stress", "--watch", "/a", "--watch", "/b", "--linger", "5",
	}
	if got := a.CollectorFlags(); !reflect.DeepEqual(got, wantFlags) {
		t.Fatalf("CollectorFlags = %q, want %q", got, wantFlags)
	}
}

func TestParseDefaults(t *testing.T) {
	a, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode != "tui" || a.Label != "" {
		t.Fatalf("defaults = %+v", a)
	}
	if a.Demo || a.Reduced || a.LingerMs != 0 || len(a.Watch) != 0 {
		t.Fatalf("defaults = %+v", a)
	}
	if got := a.CollectorFlags(); len(got) != 0 {
		t.Fatalf("CollectorFlags = %q, want none", got)
	}
}

func TestParseEqualsForm(t *testing.T) {
	a, err := Parse([]string{"--demo-scale=normal", "--watch=/x", "--linger=3"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--demo-scale", "normal", "--watch", "/x", "--linger", "3"}
	if got := a.CollectorFlags(); !reflect.DeepEqual(got, want) {
		t.Fatalf("CollectorFlags = %q, want %q", got, want)
	}
}

func TestReducedMotionNotPassedToCollector(t *testing.T) {
	a, err := Parse([]string{"--reduced-motion"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.CollectorFlags(); len(got) != 0 {
		t.Fatalf("CollectorFlags = %q, want none", got)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"--remote", "h"}, "--remote comes in phase 2"},
		{[]string{"--remote=h"}, "--remote comes in phase 2"},
		{[]string{"--port", "1"}, "--port comes in phase 2"},
		{[]string{"--demo", "serve"}, "serve must be the first argument"},
		{[]string{"--demo", "audit"}, "audit must be the first argument"},
		{[]string{"serve", "audit"}, "audit must be the first argument"},
		{[]string{"serve", "--port"}, "--port"},
		{[]string{"serve", "--port", "abc"}, "Invalid port: abc"},
		{[]string{"serve", "--port", "70000"}, "Invalid port: 70000"},
		{[]string{"serve", "--port", "-1"}, "Invalid port: -1"},
		{[]string{"serve", "--port", "1e3"}, "Invalid port: 1e3"},
		{[]string{"serve", "--port", ""}, "Invalid port: "},
		{[]string{"serve", "--port", "99999999999999999999"}, "Invalid port"},
		{[]string{"serve", "--remote"}, "--remote"},
		{[]string{"serve", "--label", "x"}, "--label is only valid with collect"},
		{[]string{"serve", "--stdio"}, "--stdio is only valid with collect"},
		{[]string{"serve", "--reduced-motion"}, "--reduced-motion is not valid with serve"},
		{[]string{"serve", "--no-open"}, "--no-open"},
		{[]string{"serve", "--demo-scale", "huge"}, "--demo-scale"},
		{[]string{"serve", "--linger", "0"}, "--linger"},
		{[]string{"serve", "stray"}, "stray"},
		{[]string{"audit", "--port", "0"}, "audit needs a port above 0"},
		{[]string{"audit", "--port", "70000"}, "Invalid port: 70000"},
		{[]string{"audit", "--demo"}, "--demo is not valid with audit"},
		{[]string{"audit", "--remote", "h"}, "--remote is not valid with audit"},
		{[]string{"audit", "--watch", "/x"}, "--watch is not valid with audit"},
		{[]string{"audit", "--linger", "2"}, "--linger is not valid with audit"},
		{[]string{"audit", "--demo-scale", "stress"}, "--demo-scale is not valid with audit"},
		{[]string{"audit", "--reduced-motion"}, "--reduced-motion is not valid with audit"},
		{[]string{"audit", "--label", "x"}, "--label is not valid with audit"},
		{[]string{"audit", "--stdio"}, "--stdio is not valid with audit"},
		{[]string{"audit", "stray"}, "stray"},
		{[]string{"collect"}, "collect needs --stdio"},
		{[]string{"--demo", "collect", "--stdio"}, "collect"},
		{[]string{"--label", "x"}, "--label is only valid with collect"},
		{[]string{"--stdio"}, "--stdio is only valid with collect"},
		{[]string{"collect", "--stdio", "--label", ""}, "--label needs a name"},
		{[]string{"collect", "--stdio", "--label="}, "--label needs a name"},
		{[]string{"collect", "--stdio", "--label"}, "--label"},
		{[]string{"collect", "--stdio", "--remote", "h"}, "--remote"},
		{[]string{"collect", "--stdio", "--port", "1"}, "--port"},
		{[]string{"collect", "--stdio", "--reduced-motion"}, "--reduced-motion"},
		{[]string{"collect", "--stdio=1"}, "--stdio"},
		{[]string{"collect", "stray", "--stdio"}, "stray"},
		{[]string{"--no-open"}, "--no-open"},
		{[]string{"--demo-scale", "x"}, "--demo-scale"},
		{[]string{"--demo-scale"}, "--demo-scale"},
		{[]string{"--linger", "0"}, "--linger"},
		{[]string{"--linger", "-2"}, "--linger"},
		{[]string{"--linger", ""}, "--linger"},
		{[]string{"--linger", "Inf"}, "--linger"},
		{[]string{"--linger", "NaN"}, "--linger"},
		{[]string{"--linger", "abc"}, "--linger"},
		{[]string{"--linger"}, "--linger"},
		{[]string{"--watch"}, "--watch"},
		{[]string{"--bogus"}, "--bogus"},
		{[]string{"stray"}, "stray"},
		{[]string{"--demo=1"}, "--demo"},
	}
	for _, c := range cases {
		_, err := Parse(c.argv)
		var ue UsageError
		if !errors.As(err, &ue) {
			t.Errorf("Parse(%q) error = %v, want UsageError", c.argv, err)
			continue
		}
		if !strings.Contains(ue.Msg, c.want) {
			t.Errorf("Parse(%q) message %q lacks %q", c.argv, ue.Msg, c.want)
		}
	}
}

// parseArgs: defaults, --remote repeatable, --linger minutes → ms, collect --stdio --label
func TestParseCollect(t *testing.T) {
	a, err := Parse([]string{"collect", "--stdio", "--label", "devbox"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode != "collect" || a.Label != "devbox" {
		t.Fatalf("Parse = %+v", a)
	}
	a, err = Parse([]string{"collect", "--stdio"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode != "collect" || a.Label != "" {
		t.Fatalf("Parse = %+v", a)
	}
	a, err = Parse([]string{"collect", "--stdio", "--demo", "--demo-scale", "stress", "--linger=2", "--label=x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode != "collect" || !a.Demo || a.DemoScale != "stress" || a.LingerMs != 120_000 || a.Label != "x" {
		t.Fatalf("Parse = %+v", a)
	}
}

// parseArgs: --watch values become absolute paths
func TestParseWatchAbsolute(t *testing.T) {
	rel := filepath.Join("rel", "dir")
	want, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"--watch", rel, "--watch", "/abs/dir"},
		{"collect", "--stdio", "--watch", rel, "--watch", "/abs/dir"},
	} {
		a, err := Parse(argv)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a.Watch, []string{want, "/abs/dir"}) {
			t.Fatalf("Parse(%q).Watch = %q", argv, a.Watch)
		}
	}
}

func TestParseLingerDecimal(t *testing.T) {
	a, err := Parse([]string{"--linger", "1.5"})
	if err != nil {
		t.Fatal(err)
	}
	if a.LingerMs != 90_000 {
		t.Fatalf("LingerMs = %d, want 90000", a.LingerMs)
	}
	if got := a.CollectorFlags(); !reflect.DeepEqual(got, []string{"--linger", "1.5"}) {
		t.Fatalf("CollectorFlags = %q", got)
	}
	for _, v := range []string{"0", "x", "-1", "0.0"} {
		_, err := Parse([]string{"--linger", v})
		var ue UsageError
		if !errors.As(err, &ue) {
			t.Errorf("--linger %q error = %v, want UsageError", v, err)
		}
	}
}

// parseArgs: defaults, --remote repeatable, --linger minutes → ms, collect --stdio --label
// (the serve and audit defaults, and --remote and --port)
func TestParseServeAudit(t *testing.T) {
	a, err := Parse([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Args{Mode: "serve", Port: 4317}); !reflect.DeepEqual(a, want) {
		t.Fatalf("Parse(serve) = %+v, want %+v", a, want)
	}
	a, err = Parse([]string{"audit"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Args{Mode: "audit", Port: 4317}); !reflect.DeepEqual(a, want) {
		t.Fatalf("Parse(audit) = %+v, want %+v", a, want)
	}
	a, err = Parse([]string{
		"serve", "--remote", "a", "--remote=b", "--port", "0", "--demo", "--demo-scale", "stress",
		"--linger", "2", "--watch", "/abs/dir",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Args{
		Mode: "serve", Port: 0, Remotes: []string{"a", "b"}, Demo: true, DemoScale: "stress",
		LingerMs: 120_000, Watch: []string{"/abs/dir"},
	}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("Parse(serve ...) = %+v, want %+v", a, want)
	}
	for in, want := range map[string]int{"65535": 65535, "007": 7, "0": 0, "00000000000000000000": 0} {
		a, err := Parse([]string{"serve", "--port=" + in})
		if err != nil || a.Port != want {
			t.Errorf("serve --port=%s: port %d, err %v, want %d", in, a.Port, err, want)
		}
	}
	a, err = Parse([]string{"audit", "--port", "4000"})
	if err != nil || a.Port != 4000 || a.Mode != "audit" {
		t.Fatalf("Parse(audit --port 4000) = %+v, %v", a, err)
	}
}

// parseArgs rejects --remote values that could inject ssh options or shell text
func TestParseRemoteRejectsInjection(t *testing.T) {
	const want = "Invalid --remote value (use a host name or user@host, no spaces or shell characters)"
	for _, bad := range []string{"-oProxyCommand=x", "host;id", "a b", "$(x)", "`id`", "", "a\u001b[31mb"} {
		_, err := Parse([]string{"serve", "--remote", bad})
		var ue UsageError
		if !errors.As(err, &ue) {
			t.Errorf("--remote %q: error = %v, want UsageError", bad, err)
			continue
		}
		if ue.Msg != want {
			t.Errorf("--remote %q: message %q, want %q", bad, ue.Msg, want)
		}
		if bad != "" && strings.Contains(ue.Msg, bad) {
			t.Errorf("--remote %q: message repeats the value", bad)
		}
	}
	a, err := Parse([]string{"serve", "--remote", "user@host.example"})
	if err != nil || !reflect.DeepEqual(a.Remotes, []string{"user@host.example"}) {
		t.Fatalf("Parse = %+v, %v", a, err)
	}
}

// remoteCommandOverride: unset is null; a JSON array of strings is the argv; anything else is an error
func TestRemoteCommandOverride(t *testing.T) {
	for _, empty := range []string{""} {
		argv, err := RemoteCommandOverride(empty)
		if argv != nil || err != nil {
			t.Fatalf("RemoteCommandOverride(%q) = %q, %v, want nil, nil", empty, argv, err)
		}
	}
	argv, err := RemoteCommandOverride(`["sh","-c","exit 3"]`)
	if err != nil || !reflect.DeepEqual(argv, []string{"sh", "-c", "exit 3"}) {
		t.Fatalf("RemoteCommandOverride = %q, %v", argv, err)
	}
	for _, bad := range []string{"sh", "[]", "[1]", `{"a":1}`, "null", `[null]`, `["a",1]`, `["a"] x`, " "} {
		_, err := RemoteCommandOverride(bad)
		var ue UsageError
		if !errors.As(err, &ue) || ue.Msg != "AGENT_VIEW_REMOTE_COMMAND must be a JSON array of strings (test only)" {
			t.Errorf("RemoteCommandOverride(%q) error = %v, want the UsageError", bad, err)
		}
	}
}
