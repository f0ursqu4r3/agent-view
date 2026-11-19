// Package cli parses the command line of agent-view.
package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// Usage is the usage text printed after a usage error.
const Usage = `usage: agent-view [--demo] [--demo-scale normal|stress] [--watch <dir>]...
                  [--linger <minutes>] [--reduced-motion]

  --demo                         show a simulated event stream
  --demo-scale normal|stress     size of the simulated stream (default normal)
  --watch <dir>                  add a file-system root (repeatable)
  --linger <minutes>             how long an agent stays after its last event (default 10)
  --reduced-motion               draw without animation

Set NO_COLOR to turn color off. Set AGENT_VIEW_COLLECTOR (a JSON array of
strings) to replace the collector command.`

// Args holds the parsed command line.
type Args struct {
	Demo      bool
	DemoScale string // "" when not given
	Watch     []string
	LingerMin int // 0 when not given
	Reduced   bool
}

// UsageError is a command-line error. Msg names the offending token.
type UsageError struct{ Msg string }

func (e UsageError) Error() string { return e.Msg }

func usagef(format string, a ...any) UsageError {
	return UsageError{Msg: fmt.Sprintf(format, a...)}
}

// Parse parses the arguments after the program name. Phase 1 rejects
// --remote, --port, serve, and collect, and the retired --no-open.
func Parse(argv []string) (Args, error) {
	var a Args
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		name, value, hasValue := strings.Cut(arg, "=")
		if !strings.HasPrefix(arg, "--") {
			switch arg {
			case "serve", "collect":
				return Args{}, usagef("%s comes in phase 2", arg)
			}
			return Args{}, usagef("unexpected argument: %s", arg)
		}
		// next returns the value of a flag that takes one.
		next := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(argv) {
				return "", usagef("%s needs a value", name)
			}
			i++
			return argv[i], nil
		}
		switch name {
		case "--demo", "--reduced-motion":
			if hasValue {
				return Args{}, usagef("%s takes no value", name)
			}
			if name == "--demo" {
				a.Demo = true
			} else {
				a.Reduced = true
			}
		case "--demo-scale":
			v, err := next()
			if err != nil {
				return Args{}, err
			}
			if v != "normal" && v != "stress" {
				return Args{}, usagef("invalid --demo-scale: %q (use normal or stress)", v)
			}
			a.DemoScale = v
		case "--watch":
			v, err := next()
			if err != nil {
				return Args{}, err
			}
			if v == "" {
				return Args{}, usagef("--watch needs a directory")
			}
			a.Watch = append(a.Watch, v)
		case "--linger":
			v, err := next()
			if err != nil {
				return Args{}, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return Args{}, usagef("invalid --linger: %q (whole minutes, above 0)", v)
			}
			a.LingerMin = n
		case "--remote", "--port":
			return Args{}, usagef("%s comes in phase 2", name)
		case "--no-open":
			return Args{}, usagef("--no-open is gone: the TUI opens no browser")
		default:
			return Args{}, usagef("unknown flag: %s", name)
		}
	}
	return a, nil
}

// CollectorFlags returns the flags that go to the collector child.
// --reduced-motion stays in the TUI.
func (a Args) CollectorFlags() []string {
	var f []string
	if a.Demo {
		f = append(f, "--demo")
	}
	if a.DemoScale != "" {
		f = append(f, "--demo-scale", a.DemoScale)
	}
	for _, w := range a.Watch {
		f = append(f, "--watch", w)
	}
	if a.LingerMin > 0 {
		f = append(f, "--linger", strconv.Itoa(a.LingerMin))
	}
	return f
}
