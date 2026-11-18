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

Run the tests. They make no model calls:

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

## Flags

| Flag | Meaning |
|---|---|
| `--port <n>` | Hub port. Default `4317`. |
| `--no-open` | Do not open the browser. The hub prints `Agent View: <url>`. |
| `--demo` | Run a simulated event stream. |
| `--demo-scale normal\|stress` | Size of the simulated stream. Default `normal`. |
| `--watch <dir>` | Add a file-system root. Repeatable. |
| `--linger <minutes>` | How long an agent stays on the stage after its last event. Default 10. |
| `--remote <ssh-host>` | Add a remote machine over SSH. Repeatable. See Remote machines. |

`agent-view collect --stdio [--label <name>]` runs a collector that writes one NDJSON event per line to stdout. It is the entry point for remote machines.

## What the views show

- **Stage.** A radial file tree. Each host is a root. Under it are project roots, folders, and files. Claude Code agents are orange. Codex agents are blue. Sub-agents use lighter tones. An agent moves to the file of its open call. Effects follow the call category: edit and write, read, search, command, agent, and other. An unattributed file change makes the file flicker grey.
- **Drawer.** Press `T`, or click the handle, to open it. It covers about 35% of the height. The left side has one lane per agent, with sub-agents nested under their parent. Each tool call is a bar colored by category. The time window is 16 seconds. The right side lists live commands with their output and exit status.
- **Coverage.** Press `C`. The panel shows log roots, open logs, watched roots, malformed records, dropped file-system events, and degraded sources.
- **Focus.** Click a lane or an agent dot to focus that agent. The stage dims the other agents, and the command list shows only that agent. Press `Esc` to clear the focus.

A status corner shows each host with its connection state.

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

Performance targets: 60 fps with 50 agents, 3,000 file nodes, and 200 events per second. The collector uses less than 2% of one core when agents are idle.

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
- The hub checks `Host` and `Origin` on `/ws` and `/api/*`. Other values get `403`.
- The viewer bundle at `/` holds no data. Bun serves it as an HTML route, which cannot run a check.
- All routes are read-only. The hub serves no file paths.
- Agent View never writes to agent settings, logs, or threads.
- Command output and file paths stay on your machine.

## Known gaps

- The Codex daemon source is skipped. The M0 spike found no loaded Codex thread, so attaching could not be proven free of side effects. Codex uses its session logs only. Nested Codex commands have no start events and no live output. They show as short bars that end when the log records them.- File watching can miss writes in the first moments after a watch root starts.
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
