# Agent View

Agent View shows, live, what the coding agents on your machine are doing. It reads Codex and Claude Code session logs. It draws each agent as a glowing dot on a growing file tree. Agents move to the files they work on, and tool calls make visible effects. A drawer shows a timeline of tool calls and live command output. Agent View is read-only. It does not start agents, change agent settings, or send data to a remote service.

## Requirements

- Bun 1.4 or later.
- macOS for the hub. The collector also runs on Linux.

## Run

Install the packages:

```bash
bun install
```

Start the hub and open the viewer at `http://127.0.0.1:4317`:

```bash
bun start
```

Show a simulated event stream. The viewer labels it as simulated:

```bash
bun start -- --demo
```

Add a directory to file monitoring:

```bash
bun start -- --watch /abs/path
```

Run the tests. They make no model calls and run no `ssh`. The hub-mode CLI tests set `AGENT_VIEW_REMOTE_COMMAND`, a test-only variable that replaces the `ssh` command of each remote:

```bash
bun test
```

Run the viewer smoke test in a headless browser:

```bash
bun run smoke
```

Check real sessions on a running hub. This prints counts only. Pass a port as the last argument if the hub does not use `4317`:

```bash
bun run audit:live
```

## Go TUI (phase 1)

A terminal viewer in Go is in progress. It draws each agent as a row of text cards. By default it starts the TypeScript collector as a child process, so Bun is still required. Run it from the repository root. The same binary also contains a Go collector (`collect --stdio`, below).

Build it:

```bash
go build -o agent-view ./cmd/agent-view
```

On macOS the build needs a C toolchain (the Xcode Command Line Tools), because the FSEvents file watcher uses cgo. On Linux it builds with `CGO_ENABLED=0`.

Run it with a simulated event stream:

```bash
./agent-view --demo
```

Run it against your real sessions:

```bash
./agent-view
```

Flags: `--demo`, `--demo-scale normal|stress`, `--watch <dir>`, `--linger <minutes>`, and `--reduced-motion`. `--linger` takes decimal minutes above 0, for example `1.5`. `--reduced-motion` turns animation off. `--remote` and `--port` are usage errors in the TUI until the TUI reads from the hub. The TUI needs a terminal. Without one, it exits with code 2 and points to `agent-view serve`.

Set `NO_COLOR` to any non-empty value (`1`, `yes`, even `0`) to turn color off. Bold and inverse still apply, and dim text uses the terminal's faint style.

Set `AGENT_VIEW_COLLECTOR` to a JSON array of strings to replace the collector command, flags included. For example, `AGENT_VIEW_COLLECTOR='["./my-collector","--stdio"]' ./agent-view`.

### Go hub: `serve`

`agent-view serve` runs the headless hub. It merges the local collector and any remote collectors. It serves one route, `GET /api/snapshot`, on `127.0.0.1`. It needs no terminal and no Bun.

```bash
./agent-view serve --demo
# Agent View: http://127.0.0.1:4317
curl -s http://127.0.0.1:4317/api/snapshot
```

The browser viewer at `/` and `/ws` are gone. No route but `/api/snapshot` exists, and no browser opens. A request needs a `Host` of `127.0.0.1:<port>` or `localhost:<port>` and no `Origin`, or an `Origin` of the same address. The reply is `{"hosts":[...],"envelopes":[...]}`.

Flags: `--port <n>` (digits only, at most 65535; the default is 4317; `0` picks a free port), `--remote <ssh-host>` (repeatable; a host name or `user@host`), `--demo`, `--demo-scale normal|stress`, `--watch <dir>` (repeatable), and `--linger <minutes>`. `serve` must be the first argument. The hub runs `ssh -T <ssh-host> agent-view collect --stdio` for each remote, through your SSH config. The command uses `-o BatchMode=yes`, so the remote host needs non-interactive authentication, such as a key or an agent. A host that accepts only a password shows as `down` with `ssh failed: <message>`.

`serve` prints `Agent View: <url>` to stdout when it listens. If the port is in use, it prints `agent-view: cannot start the server on port <n>: <message>` and exits with code 1. A remote that fails to connect shows as a `down` host and never stops the hub. A local collector failure prints a message and the hub keeps serving. SIGINT, SIGTERM and SIGHUP stop the server and the hub and exit with code 0, within 2 s. Each remote child gets SIGTERM. If a remote child ignores SIGTERM, `serve` sends SIGKILL to its process group before the 2 s run out.

### Go audit: `audit`

`agent-view audit --port <n>` checks a running hub (default port 4317; `0` is a usage error). It reads `/api/snapshot`, runs the checks of `bun run audit:live`, and prints counts only: no path, name, command, or output. It prints a JSON report and exits with code 0, or prints `agent-view: audit: <message>` and exits with code 1.

```bash
./agent-view serve --demo &
./agent-view audit --port 4317
```

### Go collector: `collect --stdio`

`agent-view collect --stdio` runs the Go collector. It writes one NDJSON envelope per line to stdout, as `bun src/cli.ts collect --stdio` does. It needs no terminal and no Bun. A remote machine runs it over SSH.

```bash
./agent-view collect --stdio --label devbox
```

Flags: `--label <name>` (the host name in the envelopes; the default is the machine's host name), `--demo`, `--demo-scale normal|stress`, `--watch <dir>` (repeatable), and `--linger <minutes>`. `collect` must be the first argument and needs `--stdio`. `--remote`, `--port`, and `--reduced-motion` are usage errors with `collect`. Usage errors print to stderr and exit with code 2.

SIGINT, SIGTERM, and SIGHUP stop the collector with exit code 0. If the reader of stdout goes away, the first failed write stops it, also with exit code 0. The process exits within 2 s of the signal or of the failed write. An idle collector writes a coverage line every 5 s, so the first failed write can come up to 5 s after the reader goes away.

To run the TUI on the Go collector, set `AGENT_VIEW_COLLECTOR` to a JSON array of strings. The array replaces the whole collector command, so the TUI flags (`--demo`, `--watch`, `--linger`) do not reach the collector. Put them in the array:

```bash
AGENT_VIEW_COLLECTOR='["./agent-view","collect","--stdio"]' ./agent-view
AGENT_VIEW_COLLECTOR='["./agent-view","collect","--stdio","--demo"]' ./agent-view
```

#### Parity

The parity test runs the Go collector and the TypeScript collector on the same logs and compares their NDJSON streams. It needs Bun; without Bun every parity test skips. It removes `seq`, `ts`, and the `hello` fields `now`, `collectorVersion`, and `platform`. It removes a `command` or `pattern` that is the empty string from a call: TypeScript writes `""` and Go leaves the key out. It compares `source.status` envelopes and timed-out `tool.end` envelopes (`ok: null`) without their order, and only the last `coverage` envelope. It compares all other envelopes in order. It also compares the demo events on a virtual clock, and the gitignore matcher on every row of `testdata/gitignore-cases.json`. The fixture logs are `test/fixtures/` and `testdata/parity/`.

The hub parity test runs `agent-view serve --port 0` and `bun src/cli.ts --no-open --port 0` on the same inputs: a temporary home with one Claude log and one Codex log from the last minute, and one fake remote (`--remote fakehost`, set with `AGENT_VIEW_REMOTE_COMMAND`) that prints a fixed stream. It waits until both hubs show both hosts `up` and their bodies stay the same for 3 s. Then it compares the `/api/snapshot` bodies: `hosts` exactly, and every envelope in order, each host's `coverage` included. It removes the same fields and empty strings as above. It also runs `agent-view audit` and `bun test/live-audit.ts` on the Go hub and compares the two reports. The tests take about 12 s:

```bash
go test -count=1 ./internal/parity/
```

The local mode is opt-in. It copies your newest 120 logs (from the last 23 hours) to a temporary home and compares the two collectors on the copy. It does not compare `fs.change`, `fs:` statuses, or the watch roots, because the copied logs name real projects. On a mismatch it prints only the index, the kind, the agent ID, and the call ID, never log content. It removes the copy at the end:

```bash
AGENT_VIEW_PARITY_LOCAL=1 go test -count=1 -run TestParityLocal ./internal/parity/
```

Measured on 2026-11-18 on an Apple M4 Max (Go 1.27.3, macOS arm64):

| Check | Target | Result |
|---|---|---|
| Frame, 50 agents, 200 envelopes per frame, 200 × 60 (`go test ./internal/tui/ -run '^$' -bench ComposeStress -benchtime 200x`) | under 4 ms | 3.8 ms with the envelopes (3.5 to 3.9 ms at 50, 200, and 1000 iterations), 2.1 ms for the frame alone |
| Binary, `-trimpath -ldflags "-s -w"` | under 20 MB | 4.8 MB (linux/amd64), 4.6 MB (darwin/arm64) |
| Idle CPU of the Go process, 60 s, no events | under 2% of one core | 0.3% |
| Memory (RSS) of the Go process, real logs, at 60 s | under 150 MB | 26 MB (peak seen: 40 MB, 8 s after start) |

The frame time is the time of `Compose`. It does not include the Bubble Tea render.

With three agents working, the Go process used 3.5% of one core over 60 s. This is probably the spinner, which redraws 8 times per second while a call is open. It was not profiled.

| Key | Action |
|---|---|
| `j` / `↓`, `k` / `↑` | Move the selection between rows. |
| `g` / `G` | First / last row. |
| `Enter`, or a mouse click on an agent card | Focus the selected (or clicked) agent. Enter again clears it. |
| `Esc` | Clear the focus, close a panel. |
| `Space` | Pause the board and the log. Events still arrive. |
| `l` | Toggle the tall log. |
| `c` | Coverage panel. |
| `?` | Help. |
| `q`, `Ctrl-C` | Quit. |

## Flags

| Flag | Meaning |
|---|---|
| `--port <n>` | Hub port. Default `4317`. |
| `--no-open` | Do not open the browser. The hub prints `Agent View: <url>`. |
| `--demo` | Run a simulated event stream. |
| `--demo-scale normal\|stress` | Size of the simulated stream. Default `normal`. |
| `--watch <dir>` | Add a file-system root. Repeatable. |
| `--linger <minutes>` | How long an agent stays on the stage and in the timeline after its last event. The hub sends it to the viewer. Default 10. |
| `--remote <ssh-host>` | Add a remote machine over SSH. Repeatable. See Remote machines. |

`agent-view collect --stdio [--label <name>]` runs a collector that writes one NDJSON event per line to stdout. It is the entry point for remote machines.

## What the views show

- **Stage.** A radial file tree. Each host is a root. Under it are project roots, folders, and files. Claude Code agents are orange. Codex agents are blue. Sub-agents use lighter tones. An agent moves to the file of its open call. Effects follow the call category: edit and write, read, search, command, agent, and other. An unattributed file change makes the file flicker grey.
- **Drawer.** Press `T`, or click the handle, to open it. It covers about 35% of the height. The left side has one lane per agent, with sub-agents nested under their parent. Each tool call is a bar colored by category. The time window is 16 seconds. The right side lists live commands with their output and exit status.
- **Coverage.** Press `C`. The panel shows log roots, open logs, watched roots, malformed records, dropped file-system events, and degraded sources.
- **Focus.** Click a lane or an agent dot to focus that agent. The stage dims the other agents, and the command list shows only that agent. Press `Esc` to clear the focus.

A status corner shows each host with its connection state. When a source of a host is degraded or down, the host line says so, for example "2 sources degraded".

## Sources and limits

| Source | Where it reads |
|---|---|
| Claude Code logs | `$CLAUDE_CONFIG_DIR/projects`, or `~/.claude/projects`. Sub-agent transcripts are under `subagents/agent-<id>.jsonl`. |
| Codex logs | `$CODEX_HOME/sessions`, or `~/.codex/sessions`, and `~/.codex-accounts/*/sessions`. |
| File-system changes | The project root of each active agent, and each `--watch` directory. Recursive `fs.watch`. No polling of directory trees. |

A source that fails reports `degraded` or `down`. It does not stop the collector.

| Item | Value |
|---|---|
| `tool.output` chunk | at most 4 KiB |
| Output kept per call | last 16 KiB |
| `command` field | first 4,096 characters |
| Large log load | header line plus last 4 MiB |
| Log discovery | modified in the last 24 hours; at most 120 logs, divided equally between providers |
| Watch roots | at most 24 |
| `fs.change` rate | at most 50 per second per root; extra events are dropped and counted in coverage |
| New unattributed tree nodes | at most 20 per second per project |
| Linger (default) | 10 minutes |
| Stale | 2 minutes without an event while working |
| Finished agent fade | 1 minute |
| Open call dropped after | 10 minutes |
| File node lifetime | 30 minutes |
| Grey flicker hold and suppression window | 2 seconds |

The collector never watches the home directory or `/`. It skips `.git`, `node_modules`, build and cache folders, `.worktrees`, `.superpowers`, and paths that a project `.gitignore` ignores.

Performance targets: 60 fps with 50 agents, 3,000 file nodes, and 200 events per second. The collector must use less than 2% of one core when agents are idle.

Measured idle CPU (Apple M4 Max, 2026-11-17): the hub with its local collector on real log roots (2,497 Claude Code logs and 2,175 Codex logs), while the agents of the measuring session kept writing logs. CPU time divided by wall time was 0.22% of one core over 60 seconds, and 0.43% over 240 seconds, which includes the 120-second safety rescans. The build before the rescan change used 2.48% and 2.17% in the same run. With empty log roots the hub used 0.06%.

## Accuracy rules

1. Only a recorded tool call moves an agent or draws an effect.
2. File-system changes stay unattributed. The collector never assigns them to an agent, and never attaches an agent ID to `fs.change`.
3. A parent link is `confirmed` only from an explicit field. Otherwise it is `session` (drawn dashed) or unknown.
   - Claude Code: an `Agent` or `Task` tool result with `toolUseResult.agentId`.
   - Codex: `parent_thread_id` in the session metadata.
4. A failed, declined, or incomplete tool call creates no confirmed write.
5. Shell commands never produce file attributions. Their effects can appear only as unattributed changes.
6. The viewer never shows file contents or diffs.
7. Command output is shown as recorded. It is not redacted. It can contain file contents if the command printed them.
8. A tool with no recorded result ends with `ok: null`. It creates no confirmed write.

## Local access and privacy

- The hub binds `127.0.0.1` only.
- The data routes are `/ws` and `/api/snapshot`. The hub checks `Host` and `Origin` on them. Other values get `403`. Other `/api` paths get `404`.
- The viewer bundle at `/` and its assets hold no data. In source runs (`bun start`), Bun's development server answers `403` to a foreign `Host` on `/`. The compiled binary serves `/` and its assets for any `Host`.
- All routes are read-only. The hub serves no file paths.
- Agent View never writes to agent settings, logs, or threads.
- Command output and file paths stay on your machine.

## Known gaps

- The Codex daemon source is skipped. Attaching to the shared Codex app-server daemon is not free of side effects: its `initialize` call sets the daemon-wide originator and User-Agent suffix (see `docs/superpowers/spikes/2026-11-17-m0-results.md`). The daemon also hosts only terminal `codex` sessions; the Codex desktop app and VS Code run their own app-servers. Codex uses its session logs only. Nested Codex commands have no start events and no live output. They show as short bars that end when the log records them.
- File watching can miss writes in the first moments after a watch root starts.
- The Codex `exec` wrapper shows as `other`. The commands it runs arrive as their own command items.
- Claude Code command output does not stream. It appears when the tool result is written.

## Install

Build the binaries for macOS and Linux, on arm64 and x64. The binaries are in `dist/` and have the viewer embedded. Building for other platforms can download Bun runtimes:

```bash
bun run compile
```

Build one target only:

```bash
bun scripts/compile.ts bun-darwin-arm64
```

Install the binary for this machine. Use the file name that matches your OS and CPU, for example `dist/agent-view-darwin-arm64`. Copy it to a directory on your `PATH` as `agent-view`:

```bash
cp dist/agent-view-darwin-arm64 ~/.local/bin/agent-view
```

Then run `agent-view` from any directory:

```bash
agent-view
```

## Remote machines

Agent View can show agents on other machines. The hub runs `ssh -T <ssh-host> agent-view collect --stdio` through your own SSH config. The remote machine needs no open port and no stored credentials.

1. Build the binaries on the hub machine:

```bash
bun run compile
```

2. Copy the matching binary to the remote machine as `agent-view`. Use the Linux binary for a Linux host and the macOS binary for a Mac. The target directory must be on the `PATH` that SSH uses for commands:

```bash
scp dist/agent-view-linux-x64 <host>:~/.local/bin/agent-view
```

3. Check that SSH works without a prompt, and that the command is on the remote `PATH`. The hub runs SSH with `BatchMode=yes`, so it cannot ask for a password or a passphrase. Use a key, and load it in your SSH agent:

```bash
ssh -o BatchMode=yes <host> command -v agent-view
```

4. Start the hub with the remote machine. Add `--remote` once for each host:

```bash
agent-view --remote <host>
```

Notes:

- If the remote machine has no `agent-view` on its `PATH`, the status corner shows "agent-view not found on <host>".
- The host name must match `[A-Za-z0-9_][A-Za-z0-9._@:-]*`. For any other name, add an alias to `~/.ssh/config` and use the alias.
- Command output and file paths from the remote machine travel over the SSH connection to your hub. The remote machine sends nothing elsewhere.
