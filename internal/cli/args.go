// Package cli parses the command line of agent-view.
package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"agent-view/internal/sshhost"
)

// DefaultPort is the port of serve and audit when --port is not given.
const DefaultPort = 4317

// Usage is the usage text printed after a usage error.
const Usage = `usage: agent-view [--demo] [--demo-scale normal|stress] [--watch <dir>]...
                  [--remote <ssh-host>]... [--linger <minutes>]
                  [--reduced-motion]
       agent-view collect --stdio [--label <name>] [--demo] [--demo-scale normal|stress]
                  [--watch <dir>]... [--linger <minutes>]
       agent-view serve [--port <n>] [--remote <ssh-host>]... [--demo]
                  [--demo-scale normal|stress] [--watch <dir>]...
                  [--linger <minutes>]
       agent-view audit [--port <n>]

  --demo                         show a simulated event stream
  --demo-scale normal|stress     size of the simulated stream (default normal)
  --watch <dir>                  add a file-system root (repeatable)
  --linger <minutes>             how long an agent stays after its last event
                                 (default 10; decimals such as 1.5 are allowed)
  --reduced-motion               draw without animation
  collect --stdio                write one NDJSON envelope per line to stdout
  --label <name>                 host name in the envelopes (collect only;
                                 default: this machine's host name)
  serve                          run the headless hub: GET /api/snapshot on
                                 127.0.0.1
  --port <n>                     hub port (serve and audit; default 4317;
                                 serve accepts 0 for a free port)
  --remote <ssh-host>            add a remote machine over SSH (repeatable)
  audit                          check a running hub and print counts

Set NO_COLOR to turn color off.`

// Args holds the parsed command line.
type Args struct {
	Mode      string // "tui", "collect", "serve" or "audit"
	Demo      bool
	DemoScale string   // "" when not given
	Watch     []string // absolute paths
	LingerMs  int64    // 0 when not given
	Reduced   bool
	Label     string   // collect only; "" means the host name
	Port      int      // serve and audit; DefaultPort when not given
	Remotes   []string // TUI and serve; ssh hosts, in order
}

// UsageError is a command-line error. Msg names the offending token.
type UsageError struct{ Msg string }

func (e UsageError) Error() string { return e.Msg }

func usagef(format string, a ...any) UsageError {
	return UsageError{Msg: fmt.Sprintf(format, a...)}
}

// Parse parses the arguments after the program name. "collect", "serve" and
// "audit" must come first; collect needs --stdio. The TUI takes --remote, as
// serve does. It rejects --port (serve and audit only) and the retired
// --no-open.
func Parse(argv []string) (Args, error) {
	a := Args{Mode: "tui"}
	stdio := false
	if len(argv) > 0 {
		switch argv[0] {
		case "collect", "serve", "audit":
			a.Mode = argv[0]
			argv = argv[1:]
		}
	}
	if a.Mode == "serve" || a.Mode == "audit" {
		a.Port = DefaultPort
	}
	mode := a.Mode
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		name, value, hasValue := strings.Cut(arg, "=")
		if !strings.HasPrefix(arg, "--") {
			if arg == "serve" || arg == "audit" {
				return Args{}, usagef("%s must be the first argument", arg)
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
		// audit takes only --port.
		switch name {
		case "--demo", "--reduced-motion", "--stdio", "--demo-scale", "--watch", "--linger", "--label", "--remote":
			if mode == "audit" {
				return Args{}, usagef("%s is not valid with audit", name)
			}
		}
		switch name {
		case "--demo", "--reduced-motion", "--stdio":
			if hasValue {
				return Args{}, usagef("%s takes no value", name)
			}
			switch name {
			case "--demo":
				a.Demo = true
			case "--stdio":
				if mode != "collect" {
					return Args{}, usagef("--stdio is only valid with collect")
				}
				stdio = true
			default:
				if mode != "tui" {
					return Args{}, usagef("--reduced-motion is not valid with %s", mode)
				}
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
			if mode != "collect" {
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
		case "--remote":
			if mode == "collect" {
				return Args{}, usagef("--remote is not valid with collect")
			}
			v, err := next()
			if err != nil {
				return Args{}, err
			}
			if !sshhost.Valid(v) {
				return Args{}, usagef("Invalid --remote value (use a host name or user@host, no spaces or shell characters)")
			}
			a.Remotes = append(a.Remotes, v)
		case "--port":
			if mode != "serve" && mode != "audit" {
				if mode == "collect" {
					return Args{}, usagef("--port is not valid with collect")
				}
				return Args{}, usagef("--port is only valid with serve or audit")
			}
			v, err := next()
			if err != nil {
				return Args{}, err
			}
			n, ok := port(v)
			if !ok {
				return Args{}, usagef("Invalid port: %s", v)
			}
			if n == 0 && mode == "audit" {
				return Args{}, usagef("Invalid port: %s (audit needs a port above 0)", v)
			}
			a.Port = n
		case "--no-open":
			return Args{}, usagef("--no-open is gone: the TUI opens no browser")
		default:
			return Args{}, usagef("unknown flag: %s", name)
		}
	}
	if mode == "collect" && !stdio {
		return Args{}, usagef("collect needs --stdio")
	}
	return a, nil
}

// port converts ASCII digits, at most 65535, to a port number. Leading zeros
// are allowed, as in the TypeScript.
func port(v string) (int, bool) {
	if !allDigits(v) {
		return 0, false
	}
	t := strings.TrimLeft(v, "0")
	if len(t) > 5 {
		return 0, false
	}
	n, _ := strconv.Atoi(t) // "" (all zeros) is 0
	return n, n <= 65535
}

func allDigits(v string) bool {
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return v != ""
}

// RemoteCommandOverride ports remoteCommandOverride of src/cli.ts. TEST ONLY:
// AGENT_VIEW_REMOTE_COMMAND holds a JSON array of strings, and serve runs that
// argv for every remote instead of ssh. An empty value means no override and
// returns nil. Anything but a non-empty JSON array of strings is a UsageError.
func RemoteCommandOverride(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	bad := usagef("AGENT_VIEW_REMOTE_COMMAND must be a JSON array of strings (test only)")
	var items []any
	if err := json.Unmarshal([]byte(value), &items); err != nil || len(items) == 0 {
		return nil, bad
	}
	argv := make([]string, len(items))
	for i, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, bad
		}
		argv[i] = s
	}
	return argv, nil
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
