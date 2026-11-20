# Agent View

Agent View is a terminal program. It shows what the coding agents on this machine and on your remote hosts do now. It reads the session logs of Claude Code and Codex. It is one Go program in one binary. It is read-only: it does not start agents and does not change agent files.

## What it shows

Each agent is one row of text cards:

- **Agent card:** the agent name, the provider, a state word (`WORKING`, `THINKING`, `WAITING`, `STALE`, `IDLE`, `DONE`, `STOPPED`), the call count, the fault count, and how long the program has seen the agent.
- **Call card:** the current tool call. It shows the category (`READ`, `EDIT`, `WRITE`, `RUN`, `GREP`, `SPAWN`, `TOOL`), the target, the elapsed time, and the last output lines of a command.
- **History cards:** the latest ended calls, newest on the left.
- **Where line:** the repository and folder of the agent.

Sub-agents sit under their parent, joined by tree lines. An idle agent shrinks to one line. The terminal must be at least 80 columns by 24 lines.

A status bar shows each host and its state. It also shows the number of agents, the working agents, the calls per minute and the faults.

An event log at the bottom shows one line for each of these events:

- an ended call
- a new sub-agent
- a stopped or finished agent
- a state change of a host
- a group of file changes that no agent made

When the program starts, it reads the logs that changed in the last 24 hours (the last 4 MiB of each log, at most 120 logs). You see the recent past at once.

| Source | Where it reads |
|---|---|
| Claude Code logs | `$CLAUDE_CONFIG_DIR/projects`, or `~/.claude/projects` |
| Codex logs | `$CODEX_HOME/sessions`, or `~/.codex/sessions`, and `~/.codex-accounts/*/sessions` |
| File changes | The project root of each active agent, and each `--watch` directory |

## Install

Build the four binaries. This needs a Mac (see "Build"):

```sh
go run ./tools/dist
```

The binaries are `dist/agent-view-darwin-arm64`, `dist/agent-view-darwin-x64`, `dist/agent-view-linux-x64` and `dist/agent-view-linux-arm64`. Copy the one for your machine to a directory on your `PATH`:

```sh
cp dist/agent-view-darwin-arm64 ~/.local/bin/agent-view
```

To build for this machine only, run `go build -o agent-view ./cmd/agent-view`.

## Run

```sh
agent-view            # show the agents of this machine
agent-view --demo     # show a simulated event stream
```

| Flag | Meaning |
|---|---|
| `--demo` | Show a simulated event stream. |
| `--demo-scale normal\|stress` | Size of the simulated stream. Default `normal`. |
| `--watch <dir>` | Add a file-system root. Repeatable. |
| `--remote <ssh-host>` | Add a remote host. Repeatable. See "Hosts". |
| `--linger <minutes>` | How long an agent stays after its last event. Default 10. Decimals such as `1.5` are allowed. |
| `--reduced-motion` | Draw without animation. Spinners still turn. |

Set `NO_COLOR` to any non-empty value to turn color off. Bold, dim and inverse text still apply.

The program needs a terminal on stdin and stdout. Without one, it prints `agent-view needs a terminal; use agent-view serve for a headless hub` and exits with code 2. A usage error prints the usage and exits with code 2. Other failures exit with code 1.

While the TUI runs, agent-view itself writes nothing to the terminal. Diagnostics go to a buffer. The program prints them to stderr after it restores the terminal. A remote command that opens `/dev/tty` can still write to the terminal.

### Keys

| Key | Action |
|---|---|
| `j` / `↓`, `k` / `↑` | Select the next or previous row. |
| `g` / `G` | Select the first or last row. |
| `enter`, or a click on an agent card | Focus the selected or clicked agent. Press `enter` again to clear the focus. |
| `esc` | Clear the focus, or close a panel. |
| `space` | Pause or resume the board and the log. The program keeps reading events. |
| `l` | Switch the tall log on or off. |
| `c` | Open or close the coverage panel and the diagnostics. |
| `?` | Open or close the help panel. |
| `ctrl+z` | Suspend. |
| `q`, `ctrl+c` | Quit. |

`SIGTERM` and `SIGHUP` also end the program. It restores the terminal and stops its collectors and `ssh` children.

## Hosts

`--remote <ssh-host>` adds a host. Use a host name or `user@host`, with no spaces or shell characters. For other names, add an alias to `~/.ssh/config`.

```sh
agent-view --remote devbox --remote build@lab
```

For each remote host, the program runs this command:

```sh
ssh -T -o BatchMode=yes -o ServerAliveInterval=15 <ssh-host> agent-view collect --stdio --label '<label>'
```

Set up each remote host as follows:

1. Build the binary for the remote host (for example `agent-view-linux-x64`).
2. Copy it to the remote host as `agent-view`. Use a directory that is on the `PATH` of a non-login shell, for example `/usr/local/bin`. `ssh` runs the command in a non-login shell, so a `PATH` that only a login file sets does not apply.
3. Make SSH work without a prompt. `BatchMode=yes` forbids a password or passphrase prompt, so use a key and load it in your SSH agent.
4. Check the setup: `ssh -o BatchMode=yes <ssh-host> command -v agent-view`.

A remote host that cannot connect shows as `down` with a reason, for example `agent-view not found on <ssh-host>` or `ssh failed: <message>`. The program tries again with a delay that starts at 1 s and doubles up to 30 s. The other hosts are not affected. Each reconnect sends a new scan, and the program does not count a known call twice.

Two hosts with the same label get a suffix: `--remote box --remote box` shows `box` and `box#2`. A remote host sends nothing to a third party. Its output and file paths travel over your SSH connection.

## Headless

These commands need no terminal.

- `agent-view serve [--port <n>] [--remote <ssh-host>]... [--demo] [--demo-scale normal|stress] [--watch <dir>]... [--linger <minutes>]` runs the hub with the local collector and the remotes. It prints `Agent View: http://127.0.0.1:<port>` and serves one route, `GET /api/snapshot`. The reply is `{"hosts":[...],"envelopes":[...]}`. The default port is 4317. `--port 0` picks a free port. `--port` is valid only with `serve` and `audit`. `SIGINT`, `SIGTERM` and `SIGHUP` stop it with exit code 0.
- `agent-view audit [--port <n>]` reads `/api/snapshot` of a running hub, checks it, and prints a JSON report that has counts only. It exits with code 1 and a message when the check fails.
- `agent-view collect --stdio [--label <name>] [--demo] [--demo-scale normal|stress] [--watch <dir>]... [--linger <minutes>]` writes one NDJSON envelope on each line to stdout. A remote host runs it over SSH. `--label` sets the host label in the envelopes. The default is the host name that the system reports.

`serve`, `audit` and `collect` must be the first argument.

```sh
agent-view serve --demo &
curl -s http://127.0.0.1:4317/api/snapshot
agent-view audit
```

## Build

`go run ./tools/dist [-out dist] [-targets <list>] [-max-bytes <n>]` builds `darwin-arm64`, `darwin-x64`, `linux-x64` and `linux-arm64`. Each build uses `-trimpath` and `-ldflags "-s -w"`. The tool prints the size of each binary, fails when one is at or above 20 MB (20,971,520 bytes), and runs a smoke test on the binary that matches the build machine.

- **macOS:** the FSEvents file watcher uses cgo. Install the Xcode Command Line Tools. The two darwin targets need a Mac.
- **Linux:** the build uses `CGO_ENABLED=0` and `fsnotify` (inotify). Any machine can build the two Linux targets. On a machine that is not a Mac, run `go run ./tools/dist -targets linux-x64,linux-arm64`.

Sizes: 8.6 to 9.4 MB for each target (see "Performance").

## Tests

```sh
go test -count=1 ./...
go vet ./...
GOOS=linux CGO_ENABLED=0 go vet ./...
```

- Tests write only inside their own temporary directories. They make no model calls. They replace `ssh` with a local program, so no test opens an SSH connection.
- The golden files of the cards and of the screen are in `testdata/cards` and `testdata/compose`. To write them again, run `go test ./internal/cards/ ./internal/tui/ -update`. Check the diff before you commit it.
- `testdata/golden/parity` holds what the former TypeScript program produced for the same inputs. `go test ./internal/parity/` compares the Go program with these files. Nobody edits them by hand.
- The end-to-end tests in `cmd/agent-view` build the binary and run it in a `tmux` pane, on a private `tmux` socket for each test. They skip when `tmux` is not installed.
- `tools/perf/measure.sh [--skip <s>] <binary> <seconds> [args...]` runs a program in a `tmux` pane and prints `cpu_avg_pct`, `cpu_max_pct` and `rss_peak_mb`. It needs `bash`, `tmux`, `perl` and `awk`.

## Performance

The numbers come from one Apple M4 Max, on macOS. The CPU and memory numbers come from the `darwin-arm64` binary. The frame and latency numbers come from Go tests and benchmarks on the same machine. The sizes row covers all four targets. The full method, every run and the caveats are in [docs/superpowers/spikes/2026-11-19-p2c-performance.md](docs/superpowers/spikes/2026-11-19-p2c-performance.md).

| Target | Measured |
|---|---|
| Binary under 20 MB for each target | 8.7 MB (darwin-arm64), 9.4 MB (darwin-x64), 9.3 MB (linux-x64), 8.6 MB (linux-arm64) |
| 50 agents and 200 events per second: board and grid under 4 ms per frame | 1.52 ms for one batch of 200 envelopes and one frame (0.89 ms for the frame alone) |
| Whole process under 2% of one core with idle agents | See below |
| Event-to-screen latency under 1 second | From the log append to the TUI model: p50 64 ms, p95 65 to 66 ms. The screen adds at most one frame (50 ms at 20 FPS). |
| Memory under 150 MB with the 24-hour scan of 120 logs | Peak RSS 71.6 MB with 120 logs, 64.3 to 70.2 MB with 83 logs |

Idle CPU is under 2% of one core in steady state (0.2–0.3% measured). The first scan after launch costs about 1.1–1.5 CPU seconds. The 60 s average from launch is 2.0% with 83 logs, which is at the 2% target, and 2.8% with 120 logs, which is over it.

- A writer that keeps its log file open is seen by the 2 s poll, not by the file watcher. The latency for such a writer is up to 2 s. In the runs, no Claude Code or Codex process held a log open when sampled, so the file watcher saw their writes.
- Linux CPU, memory and latency are not measured. Only the binary sizes are.

## Known limitations

- A crash on a goroutine outside `main` and Bubble Tea prints the crash text on the terminal. It cannot restore the terminal state.
- Two hosts that send the same agent ID share one row, because a row is identified by the agent ID today. Agent IDs carry the unique host label, so this needs a source that sends an ID without the label.
- With two or more hosts, the content of the coverage panel can be taller than the panel. The panel then cuts the newest diagnostic lines first and shows a `… N more lines` marker, so the failure reason of a host can be hidden.

## Package layout

| Path | Contents |
|---|---|
| `cmd/agent-view` | The command: the TUI, `serve`, `audit` and `collect --stdio`. It joins the packages below. |
| `internal/cli` | Command-line parsing and the usage text. |
| `internal/collect`, `internal/logs`, `internal/fschange`, `internal/watch`, `internal/fsutil` | The collector: log discovery, tailing and decoding for both providers, file-system changes, and the file watchers (FSEvents on macOS, `fsnotify` elsewhere). |
| `internal/demo` | The simulated stream of `--demo`. |
| `internal/hub`, `internal/remote`, `internal/sshhost` | The hub that merges the local collector and the remotes, the SSH child processes with backoff, and host validation. |
| `internal/server`, `internal/audit` | The `/api/snapshot` server and the audit of `serve`. |
| `internal/wire`, `internal/schema`, `internal/lifecycle` | The NDJSON envelopes, the event schema, and the time and presence rules. |
| `internal/bridge`, `internal/feed`, `internal/quiet`, `internal/safe` | The path from the hub to the TUI, the quiet terminal, and panic safety. |
| `internal/store`, `internal/board`, `internal/anim` | The state of the agents, the layout of rows and logs, and the animation. |
| `internal/cards`, `internal/cells`, `internal/text`, `internal/tui` | The cards, the cell grid, the text sanitizer, and the Bubble Tea program. |
| `internal/parity` | The tests that compare the Go program with the stored TypeScript results. |
| `tools/dist`, `tools/perf` | The build script with its size check, and the CPU and memory measurement. |
| `docs/superpowers` | The specs, plans, spike results and follow-ups. |

## Security and privacy

- Agent View reads logs and never writes to agent settings, logs or threads. Command output is shown as recorded and is not redacted. It can hold file contents if a command printed them.
- The TUI opens no port. `serve` binds `127.0.0.1` only and serves only `GET /api/snapshot`. Any other path gets `404`. For `/api/snapshot`, `serve` checks `Host` and `Origin` of every request: a foreign value gets `403`, and another method gets `405`. This stops DNS rebinding and reads from other web sites. The hub serves no file paths.
- Remote collectors run through your own `ssh`. They need no listening port and no stored credentials.
- Every string from agent data goes through one sanitizer before it reaches the screen. It removes ANSI escape sequences, control characters, and bidirectional override and isolate characters. It replaces a newline in a one-line field with `⏎` and invalid UTF-8 with `U+FFFD`. A crafted file name cannot move the cursor, set the window title or write to the clipboard.
- `audit` prints counts only: no path, name, command or output.
