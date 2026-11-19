package cli

import (
	"errors"
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
		Demo: true, DemoScale: "stress", Watch: []string{"/a", "/b"},
		LingerMin: 5, Reduced: true,
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
	if a.Demo || a.Reduced || a.LingerMin != 0 || len(a.Watch) != 0 {
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
		{[]string{"collect"}, "collect comes in phase 2"},
		{[]string{"--no-open"}, "--no-open"},
		{[]string{"--demo-scale", "x"}, "--demo-scale"},
		{[]string{"--demo-scale"}, "--demo-scale"},
		{[]string{"--linger", "0"}, "--linger"},
		{[]string{"--linger", "-2"}, "--linger"},
		{[]string{"--linger", "1.5"}, "--linger"},
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
