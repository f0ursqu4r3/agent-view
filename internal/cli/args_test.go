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
		{[]string{"serve"}, "serve comes in phase 2"},
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
