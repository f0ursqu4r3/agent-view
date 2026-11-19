// Package cli parses the command line of agent-view.
package cli

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// Usage is the usage text printed after a usage error.
const Usage = `usage: agent-view [--demo] [--demo-scale normal|stress] [--watch <dir>]...
                  [--linger <minutes>] [--reduced-motion]
       agent-view collect --stdio [--label <name>] [--demo] [--demo-scale normal|stress]
                  [--watch <dir>]... [--linger <minutes>]

  --demo                         show a simulated event stream
  --demo-scale normal|stress     size of the simulated stream (default normal)
  --watch <dir>                  add a file-system root (repeatable)
  --linger <minutes>             how long an agent stays after its last event
                                 (default 10; decimals such as 1.5 are allowed)
  --reduced-motion               draw without animation
  collect --stdio                write one NDJSON envelope per line to stdout
  --label <name>                 host name in the envelopes (collect only;
                                 default: this machine's host name)

Set NO_COLOR to turn color off. Set AGENT_VIEW_COLLECTOR (a JSON array of
strings) to replace the collector command.`

// Args holds the parsed command line.
type Args struct {
	Mode      string // "tui" or "collect"
	Demo      bool
	DemoScale string   // "" when not given
	Watch     []string // absolute paths
	LingerMs  int64    // 0 when not given
	Reduced   bool
	Label     string // collect only; "" means the host name
}

// UsageError is a command-line error. Msg names the offending token.
type UsageError struct{ Msg string }

func (e UsageError) Error() string { return e.Msg }

func usagef(format string, a ...any) UsageError {
	return UsageError{Msg: fmt.Sprintf(format, a...)}
}

// Parse parses the arguments after the program name. "collect" must come
// first and needs --stdio. The TUI rejects --remote, --port and serve (they
// come in phase 2), and the retired --no-open.
func Parse(argv []string) (Args, error) {
	a := Args{Mode: "tui"}
	stdio := false
	if len(argv) > 0 && argv[0] == "collect" {
		a.Mode = "collect"
		argv = argv[1:]
	}
	collect := a.Mode == "collect"
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		name, value, hasValue := strings.Cut(arg, "=")
		if !strings.HasPrefix(arg, "--") {
			if arg == "serve" && !collect {
				return Args{}, usagef("serve comes in phase 2")
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
		case "--demo", "--reduced-motion", "--stdio":
			if hasValue {
				return Args{}, usagef("%s takes no value", name)
			}
			switch {
			case name == "--demo":
				a.Demo = true
			case name == "--stdio":
				if !collect {
					return Args{}, usagef("--stdio is only valid with collect")
				}
				stdio = true
			case collect:
				return Args{}, usagef("--reduced-motion is not valid with collect")
			default:
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
			abs, err := filepath.Abs(v)
			if err != nil {
				return Args{}, usagef("invalid --watch: %q: %v", v, err)
			}
			a.Watch = append(a.Watch, abs)
		case "--linger":
			v, err := next()
			if err != nil {
				return Args{}, err
			}
			ms, ok := lingerMs(v)
			if !ok {
				return Args{}, usagef("invalid --linger: %q (minutes, above 0)", v)
			}
			a.LingerMs = ms
		case "--label":
			if !collect {
				return Args{}, usagef("--label is only valid with collect")
			}
			v, err := next()
			if err != nil {
				return Args{}, err
			}
			if v == "" {
				return Args{}, usagef("--label needs a name")
			}
			a.Label = v
		case "--remote", "--port":
			if collect {
				return Args{}, usagef("%s is not valid with collect", name)
			}
			return Args{}, usagef("%s comes in phase 2", name)
		case "--no-open":
			return Args{}, usagef("--no-open is gone: the TUI opens no browser")
		default:
			return Args{}, usagef("unknown flag: %s", name)
		}
	}
	if collect && !stdio {
		return Args{}, usagef("collect needs --stdio")
	}
	return a, nil
}

// maxLingerMs caps --linger so that adding it to a time cannot overflow.
const maxLingerMs = 1 << 53

// lingerMs converts decimal minutes above 0 to milliseconds, rounded like
// Math.round(minutes * 60000). It reports false for anything else. A value
// that rounds to 0 ms becomes 1 ms, because 0 means "not given".
func lingerMs(v string) (int64, bool) {
	m, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(m) || math.IsInf(m, 0) || m <= 0 {
		return 0, false
	}
	ms := math.Round(m * 60_000)
	if ms > maxLingerMs {
		ms = maxLingerMs
	}
	return max(int64(ms), 1), true
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
	if a.LingerMs > 0 {
		f = append(f, "--linger", strconv.FormatFloat(float64(a.LingerMs)/60_000, 'f', -1, 64))
	}
	return f
}
