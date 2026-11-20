# Agent View v2 — Design

Date: 2026-11-17
Status: Superseded in part by 2026-11-18-agent-view-tui-design.md

## Intent

Agent View is a personal tool. It shows, live and with strong animation, what every coding agent on a machine is doing at this moment. The main purpose is enjoyment and awareness: the owner wants to watch agents work. It is not a security audit tool.

It must cover:

- Codex and Claude Code sessions, including their sub-agents.
- This Mac first. Remote machines over SSH next.

The current repo holds a feasibility prototype: Node, log polling, and a DOM page that is rebuilt every second. v2 replaces it. v2 keeps the prototype's log parsing and its accuracy rules.

## Goals

1. Show each agent's current activity within about one second of the agent recording it.
2. Animate the activity: agents move to the files they work on, and tool calls have visible effects.
3. Show the agent hierarchy: sessions and their sub-agents.
4. Show detail on demand: a timeline of tool calls and live command output.
5. Watch more than one machine from one viewer, over SSH, with no open ports on the remote machine.
6. Start with one command. Install as one binary.

## Non-goals

- OS-level process tracing (Endpoint Security, `eslogger`, eBPF). It is not in v2.
- Showing file contents or diffs.
- Controlling agents: starting, stopping, approving, or sending prompts.
- Multi-user access, accounts, or hosting.
- Agents other than Codex and Claude Code. The design must not block them later.

## Anti-goals

These make the tool a failure even if it works:

- **False attribution.** The view must never show an agent doing something it did not record. A guessed link looks the same as a real one on screen, so the tool never guesses.
- **Side effects on agents.** Watching must never change agent settings, logs, threads, or behavior in v2.
- **Noise instead of a show.** Without a cap, a big repo or many sessions turns the stage into a hairball. The stage must stay readable.
- **Slowing the machine down.** The collector must not poll large directory trees, or use noticeable CPU when agents are idle.

## Constraints

- Runtime and toolchain: Bun, version 1.4 or later, with TypeScript.
  > Superseded by 2026-11-18-agent-view-tui-design.md
- Viewer: runs in a browser. The stage uses WebGL through PixiJS.
  > Superseded by 2026-11-18-agent-view-tui-design.md
- Platform: the local hub targets macOS. The collector must also run on Linux, because remote machines are often Linux.
- Read-only access to agent data in v2.
- Agent log formats are internal and change between releases. Adapters must fail soft and report a degraded state.
- Source facts checked on this Mac on 2026-11-17:
  - Claude Code 2.1.325 writes a transcript record for each tool call when it starts (`tool_use`) and when it ends (`tool_result`). This session's own log was checked.
  - Codex CLI 0.166 runs a shared app-server daemon (0.169.1) at `~/.codex/app-server-control/app-server-control.sock`. Its protocol has `item/started`, `item/completed`, `item/commandExecution/outputDelta`, `item/fileChange/patchUpdated`, `turn/*`, `thread/started`, and `thread/status/changed` notifications. `thread/loaded/list` lists the loaded threads. `thread/resume` with only `threadId` rejoins a running thread. `thread/unsubscribe` exists.
  - Codex session logs record `item_completed` events but no start events for nested commands.

## Approaches

### Viewer technology

| Option | What it trades | Result |
|---|---|---|
| **Web viewer, WebGL stage (PixiJS)** | The richest animation tooling and the fastest iteration on the look. Reuses existing code. Runs in a browser tab. | **Chosen** |
| Native macOS app (SwiftUI with SpriteKit/Metal) | The most native feel. Needs a Swift rewrite, is macOS-only, and is slower to iterate. Only worth it for Endpoint Security later. | Rejected |
| Terminal UI | Runs inside SSH with no setup. Animation is limited to text characters. | Rejected |

Runtime: Bun instead of Node. Bun gives one compiled binary for remote machines (`bun build --compile`, cross-target Linux), a built-in bundler for PixiJS, and a built-in HTTP and WebSocket server. All 20 prototype tests pass under `bun test` unchanged.

### Look

Three animated styles were mocked up:

- **Orbit:** sessions as stars, with sub-agents orbiting them.
- **Live file tree:** a radial tree in the style of Gource.
- **Mission control:** a timeline with lanes.

The owner picked a **mix of the live file tree and mission control**.

### Layout

Three layouts of tree and timeline were mocked up:

- **A.** Stage with a fixed timeline strip.
- **B.** Side by side.
- **C.** Full stage with a timeline drawer.

The owner picked **C**.

The mockups were in `.superpowers/brainstorm/` (local, never committed): `visual-style.html` and `layout.html`. They were the reference for motion and feel. The folder was deleted in the P2c cleanup.

## Decisions

Decided by the owner:

- Purpose: a visualizer for watching agents on their own machine. Remote machines over SSH are wanted.
- Web viewer with a WebGL stage.
- Bun as the runtime.
- Look: the live file tree as the stage, plus the mission-control timeline.
- Layout C: the full-screen stage, with the timeline in a drawer.

Proposed in the design discussion and accepted by the owner:

1. PixiJS for the stage. Plain TypeScript for the drawer and controls, with no UI framework. Ship as one compiled binary.
2. Read-only in v2.
   - Claude Code: from its logs, read on each file change.
   - Codex: from its app-server daemon, with its logs as the fallback.
   - No hooks are installed.
3. Recorded events only.
   - Files changed by shell commands show as **unattributed** grey flickers, from file-system events on watched projects.
   - They are never assigned to an agent.
4. Lifetimes.
   - A finished agent fades out after a few minutes.
   - A file node leaves the tree after about 30 minutes without a touch.
5. Build order: local view, then SSH remote machines, then optional hooks.
6. Rewrite in this repo. Reuse the prototype's log parsing and tests.

Settled after the builder review. These are design calls for the owner to check:

7. **Presence.** An agent is on the stage while it has had an event in the last 10 minutes (`--linger`, configurable), or while it is an ancestor of such an agent. Logs cannot tell "waiting for you" from "closed", so inactivity is the only exit rule. When a present agent has a new event, it comes back. At startup, only agents with events inside the linger window appear.
8. **Unattributed file nodes.**
   - `fs.change` flickers nodes already on the tree.
   - It can also add new nodes, with two limits: paths ignored by the project's `.gitignore` are dropped, and each project gets at most 20 new nodes per second.
   - The viewer hides a grey flicker when a confirmed write to the same path arrives within 2 seconds. The collector still sends both events, so no data is merged.
9. **Scope of the first build plan:** milestones M0 to M2. M3 (hooks) gets its own design before any work, because it breaks the read-only rule.

## Architecture

> Superseded by 2026-11-18-agent-view-tui-design.md

```
 per machine                                  local Mac only
┌────────────────────────────┐   NDJSON    ┌──────────────────────┐   WebSocket   ┌──────────────┐
│ collector                  │  (stdio or  │ hub                  │  (snapshot +  │ browser      │
│  sources → normalizer →    │──in-process)│  merges collectors,  │──── deltas) ──│ viewer       │
│  state model → event stream│             │  serves viewer       │               │ stage+drawer │
└────────────────────────────┘             └──────────────────────┘               └──────────────┘
        ▲ remote: `ssh -T host agent-view collect --stdio`
```

- **Collector.** Runs on each watched machine. It reads agent sources, normalizes them into one event schema, keeps a small state model, and writes an event stream.
- **Hub.** Runs on the Mac where the owner watches.
  - It runs the local collector in-process.
  - It starts one SSH child process for each remote machine.
  - It merges all streams and serves the viewer over HTTP and WebSocket.
- **Viewer.** A browser page with two parts: the PixiJS stage, and the drawer (DOM plus canvas). It keeps a client-side copy of the state, built from the snapshot and the deltas.

### Command-line interface

> Superseded by 2026-11-18-agent-view-tui-design.md

- `agent-view`: starts the hub and the local collector, then opens the browser.
- `agent-view --remote <ssh-host>`: adds a remote machine. Repeatable. The `<ssh-host>` value uses the owner's SSH config.
- `agent-view collect --stdio`: runs a collector that writes NDJSON to stdout. This is the remote entry point.
- `agent-view --demo`: runs a simulated event stream, clearly labeled as simulated.
- Other flags: `--port`, `--no-open`, `--watch <dir>` (extra file-system root).

## Sources

All sources emit normalized events (see Event schema). A source that fails reports `source.status` as `degraded` or `down`. It does not stop the collector.

### Claude Code logs

- **Roots:** `$CLAUDE_CONFIG_DIR/projects`, or `~/.claude/projects`. Sub-agent transcripts live under `subagents/agent-<id>.jsonl`.
- **Discovery:** bounded. Only logs modified in the last 24 hours are opened, up to a cap. New files are detected by a directory watch on the roots.
- **Tailing:** `fs.watch` triggers a read of new complete lines. A 2-second safety poll catches missed events. The prototype's tailer rules apply:
  - Wait for complete lines.
  - Keep split UTF-8 characters intact.
  - Handle truncation and file replacement.
  - Load only the header and the last 4 MiB of a large log.
- **Mapping:**
  - An assistant `tool_use` block becomes `tool.start`.
  - The matching `tool_result` becomes `tool.end`, with `ok = !is_error`.
  - Records with a `stop_reason` of `end_turn`, and `system` records with subtype `turn_duration`, set the agent to `idle`.
  - Command output is known only when the result is written. The `tool_result` content becomes one `tool.output` just before `tool.end`. Claude Code output does not stream in v2.
- **Identity and parents:** the prototype's rules apply (see `src/model.mjs`).
  - A confirmed parent comes from an `Agent` or `Task` tool result that has `toolUseResult.agentId`.
  - Membership in a sub-agent transcript only gives a `session` link. A `session` link is drawn dashed.

### Codex app-server daemon

This is the primary Codex source.

- **Connect:**
  - Read the control socket path from `codex app-server daemon version` (field `socketPath`).
  - Or start `codex app-server proxy`, which bridges stdio to the socket. The builder picks one in spike M0.
- **Protocol:**
  1. Send `initialize`.
  2. Send `thread/loaded/list`.
  3. Send `thread/resume` with **only** `threadId` for each loaded thread.
  4. Listen for `thread/started` to rejoin new threads.
  5. On shutdown, send `thread/unsubscribe` for each thread.
- **Mapping:**
  - `item/started` and `item/completed` become `tool.start` and `tool.end`. This covers command execution, file change, MCP tool call, and web search items.
  - `item/commandExecution/outputDelta` becomes `tool.output`.
  - `item/fileChange/patchUpdated` becomes `tool.update`, which adds files to the open call.
  - `turn/started` sets the agent to `working`. `turn/completed` sets it to `idle`.
  - `thread/closed` and `thread/archived` set it to `finished`.
  - Thread metadata gives the parent links (`parent_thread_id`).
- **Must be proven in M0 before it ships:**
  - Rejoining causes no side effects. The thread settings do not change, and no turn starts.
  - Which sessions the daemon can see. Sessions started from an interactive `codex` terminal may run outside the shared daemon. If so, the log fallback covers them.
  - If either check fails, Codex uses the log source only in M1, and the daemon source moves to a later milestone.

### Codex logs (fallback)

- **Roots:** `$CODEX_HOME/sessions`, or `~/.codex/sessions`, plus `~/.codex-accounts/*/sessions`.
- **Parsing:** reuse the prototype's `src/model.mjs` and `src/native-events.mjs`.
  - Direct `function_call` and `custom_tool_call` records become `tool.start`.
  - Their outputs become `tool.end`.
  - Native `item_completed` events for `CommandExecution` and `FileChange` become a `tool.start` and `tool.end` pair with the same timestamp. Logs have no earlier start event, so these show as short bars.
  - `task_started` sets the agent to `working`. `task_complete` and `task_completed` set it to `idle`, not `finished`. This changes the prototype's rule: a task ending is the end of a turn, not the end of the session. `turn_aborted` sets it to `interrupted`.
- **Deduplication:** when the daemon source also sees a thread, the daemon wins. Matching uses the key (`threadId`, `itemId`).

### File-system changes (unattributed)

- **Watched roots:** the project root of each active agent, plus each `--watch` directory.
  - **Project root:** the nearest ancestor of the agent's working directory that contains `.git`. If there is none, the working directory itself.
- **Mechanism:** recursive `fs.watch`. On macOS this uses FSEvents. There is no polling of directory trees.
- **Ignored:** `.git`, `node_modules`, build and cache folders, `.worktrees`, and `.superpowers`, as in the prototype list.
- **Also ignored:** paths ignored by that project's `.gitignore` files. The builder chooses the matcher.
- **Limits:**
  - At most 24 roots, chosen from the project roots of present agents (see Lifecycle). When the cap is reached, the root with the oldest event is dropped first.
  - A root is unwatched when no present agent uses it.
  - Never watch the home directory or `/`.
  - At most 50 `fs.change` events per second for each root. Extra events are dropped and counted in the coverage panel.
- **Output:** `fs.change` events, which never carry an agent ID.

## Event schema

Every line on the stream is one JSON object:

```ts
type Envelope = {
  v: 1;              // schema version
  host: string;      // collector host label
  seq: number;       // per-collector counter, increasing
  ts: string;        // ISO time from the source record when it has one, else collector time
} & Event;

type Event =
  | { kind: 'hello'; collectorVersion: string; platform: string }
  | { kind: 'snapshot'; agents: Agent[]; openCalls: Call[]; files: FileTouch[] }
  | { kind: 'agent.upsert'; agent: Agent }
  | { kind: 'agent.status'; agentId: string; status: 'working' | 'idle' | 'finished' | 'interrupted' }
  | { kind: 'tool.start'; call: Call }
  | { kind: 'tool.update'; agentId: string; callId: string; files: string[] }
  | { kind: 'tool.output'; agentId: string; callId: string; chunk: string; truncated: boolean }
  | { kind: 'tool.end'; agentId: string; callId: string; ok: boolean; exitCode?: number;
      writes?: { path: string; op: 'create' | 'edit' | 'delete' | 'move' }[] }
  | { kind: 'fs.change'; path: string; op: 'create' | 'change' | 'delete' | 'rename' }
  | { kind: 'source.status'; source: string; state: 'ok' | 'degraded' | 'down'; detail?: string };

type Agent = {
  id: string;        // `${host}/${provider}:${scope}:${sessionId}` plus `:${subAgentId}` for sub-agents
  provider: 'claude' | 'codex';
  name: string; role: 'main' | 'sub-agent';
  parentId: string | null; link: 'confirmed' | 'session' | 'root';
  cwd: string | null; projectRoot: string | null; model?: string;
  status: 'working' | 'idle' | 'finished' | 'interrupted';
  lastEventAt: string;
};

type FileTouch = {
  path: string; lastTouchAt: string;
  attribution: 'confirmed' | 'unattributed';
  agentId?: string;  // set only when attribution is 'confirmed'
};

type Call = {
  agentId: string; callId: string; startedAt: string;
  category: 'read' | 'edit' | 'write' | 'command' | 'search' | 'agent' | 'other';
  rawName: string;   // tool name as the agent recorded it
  files?: string[];  // absolute paths that the call names
  command?: string;  // first 4,096 characters
  pattern?: string;  // search pattern
};
```

Category mapping:

| Category | Claude Code | Codex |
|---|---|---|
| `read` | `Read` | — |
| `edit` | `Edit`, `MultiEdit`, `NotebookEdit` | file change |
| `write` | `Write` | — |
| `command` | `Bash` | command execution |
| `search` | `Grep`, `Glob` | — |
| `agent` | `Agent`, `Task` | sub-agent spawn |
| `other` | everything else, including MCP tools | everything else, including MCP tools and the `exec` wrapper. The commands that `exec` runs arrive as their own command-execution items, so mapping `exec` to `command` would draw each command twice. |

Rules:

- `writes` on `tool.end` lists confirmed writes only. These are the same receipts the prototype trusts.
- `tool.output` chunks are limited to 4 KiB each. Each call keeps at most its last 16 KiB.
- A new subscriber first gets `hello`, then `snapshot`, then live events.
- The hub adds nothing to the events. It only routes them and tags their origin.
- If two collectors report the same host label, the hub adds a suffix to the second one: `#2`, `#3`, and so on.
- On reconnect, the viewer asks for a new snapshot. Exactly-once delivery is not needed.

## Viewer

> Superseded by 2026-11-18-agent-view-tui-design.md

### Stage (PixiJS, full screen)

> Superseded by 2026-11-18-agent-view-tui-design.md

- **Tree:** a radial tree in the style of Gource.
  - One root per host, labeled with the host name. Several hosts sit side by side.
  - Under each root: project roots, then folders, then files.
  - Files outside any project root go under an `other` branch for that host.
- **Growth:** a file node appears the first time an event names it, and grows out from its parent. Its siblings slide to make room, with eased angles.
- **Removal:** a node fades out after 30 minutes without a touch (configurable). Folders and projects go when they have no children.
- **Agents:**
  - Each agent is a glowing dot with its initial on it. Claude Code agents use orange tones. Codex agents use blue tones. Sub-agents use lighter tones.
  - The label shows the agent name and its current activity.
  - An agent eases toward the file of its open call, and leaves a short trail.
  - An idle agent drifts to a home ring near its project.
- **Effects by category:**
  - `edit` and `write`: a solid beam with particles, and an expanding ring on the file.
  - `read`: a dashed line moving toward the agent.
  - `search`: flickering lines to the folder or files named in the call input (`path`, `glob`). Search results are not parsed.
  - `command`: pulse rings around the agent, plus the command text.
  - `agent`: a link that draws itself out to the new child agent.
  - `other`: a single soft pulse.
- **Unattributed changes:**
  - `fs.change` makes the file flicker grey. No agent moves.
  - A new path can add a grey node, within the limits in decision 8.
  - The viewer holds each grey flicker for 2 seconds. It drops the flicker if a confirmed write to the same path arrives within 2 seconds before or after.
- **Lifecycle:** this follows decision 7.
  - Working agents glow.
  - **Stale** means "working" with no event for 2 minutes. It shows as a dim, slow pulse.
  - Interrupted agents flash red once, then show as idle.
  - An agent with no event for 10 minutes (`--linger`) fades out. This applies to idle, stale, and working agents. It returns on its next event.
  - Finished agents fade out after 1 minute.
  - An open call with no event for 10 minutes is dropped. It creates no confirmed write. This covers sessions killed during a call, and results that were cut off by the 4 MiB load limit.
  - Collector discovery still reads logs from the last 24 hours, to resolve parent links. The snapshot includes only present agents and their ancestors.
- **Hierarchy:** a thin line connects a sub-agent to its parent while both are visible. A `session` link uses a dashed line.
- **Reduced motion:** with `prefers-reduced-motion`, there are no trails or particles, and moves are instant.

### Drawer

> Superseded by 2026-11-18-agent-view-tui-design.md

- **Opening:** it slides up from the bottom when you press `T` or click the handle. It is closed by default and covers about 35% of the height.
- **Left side:** one lane per agent.
  - Sub-agents are nested under their parent. Lanes follow the order of the tree.
  - Each tool call is a bar colored by category. A bar grows while the call runs.
  - The time window defaults to 16 seconds.
- **Right side:** live commands. This is the newest command output, streamed from `tool.output`, with its exit status.
- **Focus:**
  - Clicking a lane, or an agent dot on the stage, focuses that agent. The stage dims the others, and the command list filters to that agent.
  - `Esc` clears the focus.

### Heads-up display

> Superseded by 2026-11-18-agent-view-tui-design.md

- A small status corner shows each host with its connection state and source states.
- `C` opens a coverage panel: log roots, open logs, watched roots, parse errors, and degraded sources. It carries over the prototype's coverage panel.

## Accuracy rules

These rules are kept from the prototype:

1. Only a recorded tool call moves an agent or draws an effect.
2. File-system changes stay unattributed.
3. Parent links are `confirmed` only from explicit fields. Otherwise they are `session` or unknown.
4. A failed or incomplete tool call creates no confirmed write.
5. Shell commands never produce file attributions. Their effects can appear only as unattributed changes.
6. The viewer never shows file contents or diffs.
7. Command output is shown as recorded, and is not redacted. It stays on the local machine.

## Security and privacy

- The hub binds `127.0.0.1` only.
- The hub validates `Host` and `Origin` on HTTP and on the WebSocket upgrade.
- The routes are read-only. No arbitrary file paths are served.
- Remote collectors run through the owner's own `ssh`. They need no listening port and no stored credentials.
- Nothing writes to agent settings, logs, or threads in v2.

## Performance targets

> Superseded by 2026-11-18-agent-view-tui-design.md

- 60 fps on an Apple Silicon Mac with:
  - 50 agents,
  - 3,000 visible file nodes,
  - and 200 events per second in bursts.
- Label culling and level of detail apply above those numbers.
- Collector CPU stays below 2% of one core when agents are idle.
- Event-to-pixel latency stays below 1 second for Claude Code logs and the Codex daemon. It is best effort for the Codex log fallback.

## Packaging

> Superseded by 2026-11-18-agent-view-tui-design.md

- `bun build --compile` targets: `darwin-arm64`, `darwin-x64`, `linux-x64`, `linux-arm64`.
- The viewer assets are embedded in the binary.
- In M2, a remote machine needs only the binary on its `PATH`. A helper command to copy it there is optional.

## Milestones

> Superseded by 2026-11-18-agent-view-tui-design.md

**M0 — Spikes.** Each ends with a short written result.

1. **Codex daemon passive attach:**
   - Is it free of side effects?
   - Does it see sessions from the interactive terminal?
   - How fast are its events?
2. **Bun recursive `fs.watch`:** check its reliability on macOS and on Linux.
3. **PixiJS stage:** check the frame rate with 3,000 nodes, 50 agents, and the glow effects.

**M1 — Local.**

- Collector with all sources.
- Normalizer and state model.
- Hub.
- Stage and drawer.
- `--demo`.
- Prototype tests ported to `bun test`, plus new tests.

**M2 — Remote.**

- `collect --stdio`.
- `--remote`, with SSH child processes and reconnection with backoff.
- Multi-host layout.
- Cross-compiled binaries.

The first build plan covers M0 to M2.

**M3 — Optional hooks. Not in the first plan; needs its own design first.** An opt-in `agent-view hooks install` command, with a matching `uninstall`:

- A Claude Code async command hook for `PreToolUse`, `PostToolUse`, and `SubagentStart`/`SubagentStop`.
- Codex hooks.

The hooks add precision for MCP tools and sub-agent identity. They need explicit consent, because they change agent settings.

## Testing

- **Parsers:** the 20 prototype tests are ported. New fixtures are recorded from real logs and cleaned of private content. Every event kind and edge case gets a fixture.
- **Schema:** golden NDJSON files go through the normalizer and the state model. Snapshot plus deltas must give the same result as replaying the full stream.
- **Transport:** a hub test with a fake collector child process covers disconnect, reconnect, and resnapshot.
- **Viewer:** a deterministic demo stream drives a headless smoke test. It checks that the page renders, that there are no console errors, and that the drawer toggles. The builder decides how far visual testing goes.
  > Superseded by 2026-11-18-agent-view-tui-design.md
- **Live audit:** like `test/live-audit.mjs`, it runs against real local data and prints counts only.

## Risks

| Risk | Effect | Response |
|---|---|---|
| The Codex daemon cannot see terminal sessions | Codex terminal agents lose start events and live output | Log fallback. Hooks in M3. |
| Agent log formats change | A source breaks silently | Versioned adapters. `source.status` set to degraded. Fixtures for each version seen. |
| The stage gets cluttered with many projects | The show becomes noise | Growth on touch, removal after 30 minutes, label culling. |
| `thread/resume` has a hidden side effect | Watching changes a Codex thread | M0 check. Ship the daemon source only after the check passes. |
| `fs.watch` misses events on big trees | Unattributed changes are lost | They are best effort and labeled so in the coverage panel. |

## Left to the builder

- Visual tuning: colors, glow and bloom filters, easing curves, and the tree layout algorithm. The mockups are the reference.
- The internal structure of the code and module layout.
- Whether the Codex daemon is reached through the socket or `codex app-server proxy`.
- Reconnect timing, buffer sizes, and caps not stated here.
- The config file format, if one is needed.
- How the assets are embedded in the binary.
- Zoom and pan on the stage, and the zoom of the timeline window.
