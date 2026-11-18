# Agent View v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Node prototype with a Bun app that streams normalized agent events from one or more machines to a browser viewer. The viewer shows a full-screen animated file tree and a timeline drawer.

**Architecture:**
- **Collector:** runs on each machine. It reads Claude Code logs, Codex logs and the Codex app-server daemon, plus file-system events. It normalizes them into one versioned NDJSON event stream.
- **Hub:** runs on the Mac. It runs the local collector in-process, spawns `ssh … agent-view collect --stdio` for each remote machine, and serves the viewer over HTTP and WebSocket.
- **Viewer:** a PixiJS stage with a 2D-canvas and DOM drawer. It rebuilds state from a snapshot followed by deltas.

**Tech Stack:**

| Tool | Version | Use |
|---|---|---|
| Bun | 1.4.4 | runtime, bundler, test runner, `Bun.serve`, `bun build --compile` |
| TypeScript | 7.0.2 | type check only |
| pixi.js | 8.23.0 | stage rendering |
| ignore | 7.0.16 | `.gitignore` matching |
| playwright | 1.65.1 | dev only: viewer smoke test |

**Spec:** `docs/superpowers/specs/2026-11-17-agent-view-design.md` (approved 2026-11-17). Read it before starting any task.

## Global Constraints

- **Runtime:** Bun 1.4 or later, with TypeScript. Production dependencies: only `pixi.js` and `ignore`.
- **Network:** the hub binds `127.0.0.1` only. It validates `Host` and `Origin` on data routes (`/ws`, `/api/*`).
  - The static viewer bundle (`/` and its assets) is served as a Bun HTML route. Bun HTML routes cannot run a check, and the bundle holds no data.
- **Read-only:** never write to agent settings, logs, or threads. Never call `thread/resume` for a thread that is not in `thread/loaded/list`.
- **Attribution:**
  - Never attach an agent ID to `fs.change`.
  - Never infer writes from shell text, command output, or exit codes.
  - Failed, declined, or incomplete tools create no confirmed write.
- **Limits:**

  | Item | Value |
  |---|---|
  | `tool.output` chunk | ≤ 4 KiB |
  | Output kept per call | last 16 KiB |
  | `command` field | first 4,096 characters |
  | Large log load | header line plus last 4 MiB |
  | Log discovery | modified in the last 24 hours; at most 120 logs, divided equally between providers |
  | Watch roots | ≤ 24 |
  | `fs.change` rate | ≤ 50 per second per root |
  | New unattributed tree nodes | ≤ 20 per second per project |

- **Lifecycle:**

  | Rule | Value |
  |---|---|
  | linger (default; `--linger` in minutes) | 10 minutes |
  | stale | 2 minutes |
  | finished agent fade | 1 minute |
  | open call dropped after | 10 minutes |
  | file node lifetime | 30 minutes |
  | grey flicker hold and suppression window | 2 seconds |
  | drawer window | 16 seconds |
  | drawer height | about 35% |

- **Performance:**
  - 60 fps with 50 agents, 3,000 file nodes, and 200 events per second.
  - Collector below 2% of one core when agents are idle.
  - Event-to-pixel time below 1 second for Claude logs and the Codex daemon.
- **Colors:** Claude Code agents use orange tones (`#ff8a4c`, `#ffad78`, `#ffcda1`). Codex agents use blue tones (`#36c6ff`, `#79dbff`, `#b0ecff`). Category colors are taken from the mockups:

  | Category | Color |
  |---|---|
  | `read` | `#8f9bff` |
  | `edit` and `write` | `#ff5fa8` |
  | `command` | `#6dff9e` |
  | `search` | `#ffd166` |
  | `agent` and `other` | `#b9a6ff` |

- **Compile targets:** `bun-darwin-arm64`, `bun-darwin-x64`, `bun-linux-x64`, `bun-linux-arm64`.
- **CLI defaults:** port `4317`. Flags: `--remote <ssh-host>` (repeatable), `--demo`, `--demo-scale normal|stress`, `--port`, `--no-open`, `--watch <dir>` (repeatable), `--linger <minutes>`. Subcommand: `collect --stdio [--label <name>]`. Hub mode prints `Agent View: <url>` on start.
- **Git:** commit messages use Conventional Commits. They contain no tool or assistant attribution lines.

## Schema refinements to the spec

These are additions only. The schema version stays at `v: 1`.

- `tool.end.ok` is `boolean | null`. `null` means the call ended with no recorded result: it timed out or was dropped.
- `hello` gains `now: string` (the collector's clock, used for skew correction) and `demo?: true`.
- A new event, `{ kind: 'coverage'; coverage: Coverage }`, is emitted every 5 seconds. It feeds the `C` panel.
- Collector host labels are set by the hub with `collect --stdio --label <name>`. The hub resolves label collisions before it spawns a collector, adding `#2`, `#3`, and so on. This meets the spec's suffix rule without rewriting IDs inside events.

## Review Focus

Each item below has its test in the named task.

1. **A Claude Code session spread over several transcript files with the same `sessionId`** (resume, compaction): this must give one agent, not ghost duplicates. Test in Task 4.
2. **A remote host whose clock is minutes off:** timeline bars and presence must use skew-corrected times, so agents do not vanish or sit off-screen. Test in Task 12.
3. **A project reached through a symlink** (for example `/tmp` → `/private/tmp`, or a symlinked checkout): confirmed writes and `fs.change` for the same file must land on one tree node. Test in Task 10.
4. **A browser tab left in the background for minutes, then shown again:** the event backlog must not start hundreds of effects at once. Effects older than 2 seconds are skipped. Test in Task 14.
5. **A remote host without `agent-view` on `PATH`:** ssh exits with code 127. The hub must show "agent-view not found on <host>" and retry with backoff, not spin. Test in Task 17.

---

## File structure

```
package.json            Bun scripts, dependencies (rewritten)
tsconfig.json
src/
  cli.ts                parseArgs, main: hub mode or `collect --stdio`
  shared/
    schema.ts           event types, SCHEMA_VERSION, categoryFor, colors
    ndjson.ts           LineSplitter, encodeLine, parseLine
    lifecycle.ts        LIFECYCLE constants, presentAgentIds, isStale
  collector/
    tail.ts             LogTailer
    discovery.ts        discoverRoots, listRecentLogs, findProjectRoot
    state.ts            CollectorState
    collector.ts        Collector: sources → enrich → state → subscribers
    demo.ts             DemoSource
    sources/
      claude-logs.ts    ClaudeLogAdapter (pure record → events)
      codex-logs.ts     CodexLogAdapter (pure record → events)
      log-source.ts     LogSource: watch roots, tail logs, run adapters
      fs-changes.ts     FsChangeSource, RateLimiter, GitignoreMatcher
      codex-daemon.ts   CodexDaemonSource, mapNotification (only if M0 says go)
  hub/
    hub.ts              Hub, HubSource, LocalSource
    remote.ts           RemoteCollector
    server.ts           startServer
viewer/
  index.html  style.css  main.ts
  store.ts              ViewerStore
  stage/tree-model.ts   TreeModel (pure)
  stage/effects.ts      effectFor (pure)
  stage/stage.ts        Stage (PixiJS)
  drawer/timeline.ts    drawTimeline (2D canvas)
  drawer/drawer.ts      Drawer: toggle, lanes, command list
  hud.ts                Hud: host status, coverage panel
spikes/                 M0 probes (kept for repeat runs)
test/                   bun tests mirroring src/ and viewer/; test/e2e/viewer.smoke.ts; test/live-audit.ts
docs/superpowers/spikes/2026-11-17-m0-results.md
```

The prototype files (`src/*.mjs`, `public/`, `test/*.test.mjs`) stay in place, with their tests passing, until Task 16 removes them.

---

## Milestone M0 — Setup and spikes

### Task 1: Bun scaffold, schema, and NDJSON

**Files:**
- Modify: `package.json`, `.gitignore` (add `dist/`, `test/.tmp/`)
- Create: `tsconfig.json`, `src/shared/schema.ts`, `src/shared/ndjson.ts`
- Test: `test/shared/schema.test.ts`, `test/shared/ndjson.test.ts`

**Interfaces:**
- Produces, in `src/shared/schema.ts`: `SCHEMA_VERSION`, `Provider`, `AgentStatus`, `Category`, `Agent`, `Call`, `FileTouch`, `Coverage`, `Event`, `SourceEvent`, `Envelope`, `HostStatus`, `categoryFor(provider, rawName): Category`, `PROVIDER_COLORS`, `CATEGORY_COLORS`.
- Produces, in `src/shared/ndjson.ts`:
  - `class LineSplitter { push(chunk: Uint8Array): string[]; reset(): void; get pendingBytes(): number }`
  - `encodeLine(e: Envelope): string`
  - `parseLine(line: string): Envelope | null`

The types are fixed by the spec plus the refinements above, so they are given in full:

```ts
export const SCHEMA_VERSION = 1 as const;
export type Provider = 'claude' | 'codex';
export type AgentStatus = 'working' | 'idle' | 'finished' | 'interrupted';
export type Category = 'read' | 'edit' | 'write' | 'command' | 'search' | 'agent' | 'other';
export type WriteOp = 'create' | 'edit' | 'delete' | 'move';
export type Agent = { id: string; provider: Provider; name: string; role: 'main' | 'sub-agent';
  parentId: string | null; link: 'confirmed' | 'session' | 'root'; cwd: string | null;
  projectRoot: string | null; model?: string; status: AgentStatus; lastEventAt: string };
export type Call = { agentId: string; callId: string; startedAt: string; category: Category;
  rawName: string; files?: string[]; command?: string; pattern?: string };
export type FileTouch = { path: string; lastTouchAt: string; attribution: 'confirmed' | 'unattributed'; agentId?: string };
export type SourceState = { state: 'ok' | 'degraded' | 'down'; detail?: string };
export type Coverage = { logRoots: string[]; logsOpen: number; logsDiscovered: number; watchRoots: string[];
  malformedRecords: number; fsDropped: number; sources: Record<string, SourceState> };
export type Event =
  | { kind: 'hello'; collectorVersion: string; platform: string; now: string; demo?: true }
  | { kind: 'snapshot'; agents: Agent[]; openCalls: Call[]; files: FileTouch[] }
  | { kind: 'agent.upsert'; agent: Agent }
  | { kind: 'agent.status'; agentId: string; status: AgentStatus }
  | { kind: 'tool.start'; call: Call }
  | { kind: 'tool.update'; agentId: string; callId: string; files: string[] }
  | { kind: 'tool.output'; agentId: string; callId: string; chunk: string; truncated: boolean }
  | { kind: 'tool.end'; agentId: string; callId: string; ok: boolean | null; exitCode?: number; writes?: { path: string; op: WriteOp }[] }
  | { kind: 'fs.change'; path: string; op: 'create' | 'change' | 'delete' | 'rename' }
  | { kind: 'source.status'; source: string } & SourceState
  | { kind: 'coverage'; coverage: Coverage };
export type SourceEvent = Event & { ts: string };
export type HostStatus = { label: string; kind: 'local' | 'remote'; state: 'connecting' | 'up' | 'down'; detail?: string };
export type Envelope = SourceEvent & { v: typeof SCHEMA_VERSION; host: string; seq: number };
```

- [ ] **Step 1: Set up the project.**
  - Rewrite `package.json`:
    - `"type": "module"`, `"private": true`, `"bin": { "agent-view": "src/cli.ts" }`.
    - Scripts: `start` = `bun src/cli.ts`, `test` = `bun test`, `typecheck` = `tsc --noEmit`, `smoke` = `bun test/e2e/viewer.smoke.ts`, `audit:live` = `bun test/live-audit.ts`, `compile` = `bun scripts/compile.ts`.
    - Dependencies at the versions in Tech Stack. Dev dependencies: `@types/bun@1.4.4`, `typescript@7.0.2`, `playwright@1.65.1`.
  - Write `tsconfig.json` with `strict`, `noEmit`, `module` `preserve`, `moduleResolution` `bundler`, `lib` `["ESNext","DOM"]`, `types` `["bun"]`.
  - Run `bun install`.

- [ ] **Step 2: Write the failing tests.**

```ts
// test/shared/schema.test.ts
test('categoryFor maps Claude tools', () => {
  expect(categoryFor('claude','Read')).toBe('read'); expect(categoryFor('claude','MultiEdit')).toBe('edit');
  expect(categoryFor('claude','NotebookEdit')).toBe('edit'); expect(categoryFor('claude','Write')).toBe('write');
  expect(categoryFor('claude','Bash')).toBe('command'); expect(categoryFor('claude','Glob')).toBe('search');
  expect(categoryFor('claude','Task')).toBe('agent'); expect(categoryFor('claude','mcp__x__y')).toBe('other');
});
test('categoryFor maps Codex log and daemon names', () => {
  for (const n of ['apply_patch','FileChange','fileChange']) expect(categoryFor('codex',n)).toBe('edit');
  for (const n of ['shell','exec_command','local_shell','CommandExecution','commandExecution']) expect(categoryFor('codex',n)).toBe('command');
  for (const n of ['spawn_agent','CollabAgentToolCall','collabAgentToolCall']) expect(categoryFor('codex',n)).toBe('agent');
  expect(categoryFor('codex','webSearch')).toBe('search');
  for (const n of ['exec','send_message','McpToolCall','wait_agent']) expect(categoryFor('codex',n)).toBe('other');
});
// test/shared/ndjson.test.ts
test('LineSplitter returns only complete lines and keeps split UTF-8', () => {
  const s = new LineSplitter(); const b = new TextEncoder().encode('{"p":"日本"}\n{"q":1}');
  const cut = 8; // inside the multi-byte character
  expect(s.push(b.subarray(0, cut))).toEqual([]);
  expect(s.push(b.subarray(cut))).toEqual(['{"p":"日本"}']);
  expect(s.pendingBytes).toBe(7);
  expect(s.push(new TextEncoder().encode('\n'))).toEqual(['{"q":1}']);
});
test('parseLine rejects malformed or wrong-version lines', () => {
  expect(parseLine('{oops')).toBeNull(); expect(parseLine(JSON.stringify({v:2,kind:'hello'}))).toBeNull();
  const e = {v:1,host:'h',seq:1,ts:'2026-11-17T00:00:00Z',kind:'fs.change',path:'/a',op:'change'} as const;
  expect(parseLine(encodeLine(e))).toEqual(e); expect(encodeLine(e).endsWith('\n')).toBe(true);
});
```

- [ ] **Step 3: Run the tests and confirm they fail.**
  - Run: `bun test test/shared`
  - Expected: FAIL, with "Cannot find module".
- [ ] **Step 4: Implement `schema.ts` and `ndjson.ts`.**
  - `categoryFor` uses the table in the spec plus the names in the tests. Any unknown name returns `'other'`.
  - `LineSplitter` keeps a byte buffer and splits on byte `10`. It decodes only complete lines, so split UTF-8 characters are safe.
- [ ] **Step 5: Run the tests and confirm they pass.**
  - Run: `bun test test/shared && bun run typecheck`
  - Expected: PASS. The 20 prototype tests also still pass under `bun test`.
- [ ] **Step 6: Commit.**
  - Command: `git add -A && git commit -m "build: move to Bun and TypeScript; add event schema and NDJSON codec"`

### Task 2: M0 spikes

These probes are throwaway, so there is no TDD. Each probe writes its findings into `docs/superpowers/spikes/2026-11-17-m0-results.md`, and the later tasks read three decisions from that file:

- `DAEMON_SOURCE`: `go` or `no-go`.
- `DAEMON_TRANSPORT`: `proxy` or `socket`.
- `FS_WATCH_MODE`: `recursive` or `per-dir`, for each path class.

**Files:**
- Create:
  - `spikes/codex-daemon-probe.ts`
  - `spikes/fs-watch-probe.ts`
  - `spikes/pixi-stress/index.html`, `spikes/pixi-stress/main.ts`, `spikes/pixi-stress/serve.ts`
  - `docs/superpowers/spikes/2026-11-17-m0-results.md`

- [ ] **Step 1: Write the Codex daemon probe.**
  - Spawn `codex app-server proxy` and speak JSON-RPC, one JSON object per line:
    1. Send `initialize` with `{clientInfo:{name:'agent-view-probe',version:'0.0.0'}}`.
    2. Send the `initialized` notification.
    3. Send `thread/loaded/list`.
    4. Send `thread/resume {threadId}` for each loaded thread **only**.
  - For 120 seconds, record per-method counts and the first 3 payload shapes of `item/started`, `item/completed`, `item/commandExecution/outputDelta`, `thread/started`, and `turn/*`.
  - On exit, send `thread/unsubscribe` for each thread.
  - Print nothing from command output. Print shapes with all string values replaced by their length.
- [ ] **Step 2: Run the daemon probe.** Ask the owner to have one Codex desktop or daemon thread working and one interactive `codex` terminal session working while the probe runs. Record in the results file:
  - **(a) Side effects:** after the resume, there is no `turn/started` caused by the probe, no `thread/settings/updated`, and the thread's model and settings in `thread/read` are unchanged.
  - **(b) Visibility:** whether the terminal session appears in `thread/loaded/list` or `thread/started`.
  - **(c) Latency:** the time from `item/started` to receipt.
  - **Decision:**
    - `DAEMON_SOURCE=go` only if (a) passes.
    - `DAEMON_TRANSPORT=proxy`, unless the proxy adds more than 100 ms of latency. If it does, use `socket`.
    - Note the result of (b) as a known coverage gap.
- [ ] **Step 3: Write and run the file-watch probe.** Use Bun's `fs.watch(root,{recursive:true})` and `fs.watch(dir)` (per directory) on four path classes:
  1. a directory under `$HOME`,
  2. `realpathSync(os.tmpdir())`,
  3. `/private/tmp`,
  4. a symlink to a directory under `$HOME`.

  For each class and mode, write 1,000 files in 2 seconds, then report the events received, the events missed, and the p95 latency.
  - **Known before the probe:** on this Mac, on 2026-11-17, Bun's recursive watch reported no events under `/private/tmp`, while Node's did. Under `$HOME` it worked.
  - **Decision:** `FS_WATCH_MODE` for each class. A class goes `per-dir` if its recursive mode misses more than 1% of events.
- [ ] **Step 4: Write and run the PixiJS stress test.**
  - Build 3,000 file circles with text labels. Show a label only when its node was touched in the last 3 seconds, or when the zoom is above 1.5.
  - Add 50 agent sprites with additive-blend glow textures, built from pre-rendered radial gradients, not filters.
  - Fire 200 effect lines per second.
  - Serve with `Bun.serve` and an HTML import. Measure the average `app.ticker.FPS` over 10 seconds at 2560×1440 in Chrome.
  - **Decision:** record the glow technique and any culling needed to reach 60 fps.
- [ ] **Step 5: Commit.**
  - Command: `git add spikes docs/superpowers/spikes && git commit -m "spike: Codex daemon attach, Bun file watching, PixiJS stage performance"`

---

## Milestone M1 — Local

### Task 3: LogTailer

**Files:**
- Create: `src/collector/tail.ts`
- Test: `test/collector/tail.test.ts`. Temporary directories go under `test/.tmp/`, created with `mkdtemp`.

**Interfaces:**
- Consumes: `LineSplitter` (Task 1).
- Produces:
  - `const TAIL_LIMIT = 4 * 1024 * 1024`
  - `class LogTailer { constructor(onRecord: (record: unknown, file: string) => void, opts?: { limit?: number; onWarning?: (msg: string) => void }); read(file: string): Promise<void>; forget(file: string): void; readonly malformed: number; readonly openFiles: number }`

- [ ] **Step 1: Write the failing tests.** Port the prototype test `tailer waits for complete JSON lines and preserves split UTF-8`, then add these:

```ts
test('replaced file (new inode) and truncated file restart from offset 0', ...)       // expect re-read records after replace/truncate
test('large file loads first line plus last limit bytes and skips the cut line', async () => {
  // limit: 1024 option; header line + 50 x 100-byte lines; expect records = header + only the lines fully inside the last 1024 bytes
});
test('malformed lines are counted, not thrown', ...)                                    // expect(t.malformed).toBe(1)
test('a record larger than the limit is skipped with one warning', ...)                 // onWarning called once; next record still parsed
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/collector/tail.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement `LogTailer`.** Port the `Tailer` in `src/collector.mjs`. Keep one cursor per file: `{offset, ino, splitter, skipFirst}`. Reset the cursor when the inode changes or when `size < offset`.
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/collector/tail.test.ts`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git commit -am "feat(collector): port log tailer to TypeScript"`

### Task 4: Claude Code log adapter

**Files:**
- Create: `src/collector/sources/claude-logs.ts`, `test/fixtures/claude/*.jsonl`. Fixtures are hand-reduced records with the real field names, and with no real paths or content.
- Test: `test/collector/claude-logs.test.ts`

**Interfaces:**
- Consumes: `SourceEvent`, `categoryFor` (Task 1).
- Produces: `class ClaudeLogAdapter { constructor(host: string); ingest(record: unknown, file: string, scope: string): SourceEvent[] }`

**Agent IDs:**
- Main session: `${host}/claude:${scope}:${sessionId}`.
- Sub-agent: the main session ID plus `:${subAgentId}`.
- The sub-agent ID comes from `record.agentId`, or from the file name `subagents/agent-<id>.jsonl`.

- [ ] **Step 1: Write the failing tests.**
  - Port these prototype tests, changing their assertions to the events they now produce:
    - `Claude session membership does not claim an immediate parent; Agent result confirms nested links`
    - `two agents writing the same file retain separate confirmed receipts`
    - `failed and unfinished file tools do not claim writes`
    - `shell commands and opaque wrapper tools cannot invent file attribution`
  - Add these:

```ts
test('tool_use emits tool.start with category, files resolved against cwd, command capped at 4096', ...)
test('tool_result emits tool.output (≤4 KiB chunks, last 16 KiB) then tool.end ok = !is_error with writes for Write/Edit', ...)
test('end_turn and turn_duration emit agent.status idle', ...)
test('Grep/Glob tool.start carries pattern and files from input path/glob only', ...)
test('Review Focus 1: two transcript files with the same sessionId yield one agent id', () => {
  const a = new ClaudeLogAdapter('mac');
  const e1 = a.ingest({sessionId:'s1',cwd:'/p',type:'user',timestamp:T1,message:{content:'x'}}, '/r/proj/one.jsonl','default');
  const e2 = a.ingest({sessionId:'s1',cwd:'/p',type:'user',timestamp:T2,message:{content:'y'}}, '/r/proj/two.jsonl','default');
  const ids = new Set([...e1,...e2].filter(e=>e.kind==='agent.upsert').map(e=>e.agent.id));
  expect([...ids]).toEqual(['mac/claude:default:s1']);
});
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/collector/claude-logs.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement `ClaudeLogAdapter.ingest`.** Port the Claude branch of `Model.ingest`, `startCall`, and `finishCall` from `src/model.mjs`.
  - Pending calls are keyed by `${agentId}:${tool_use_id}`. Keep at most 5,000.
  - Emit `agent.upsert` only when a field changes.
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/collector/claude-logs.test.ts`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git add src/collector/sources/claude-logs.ts test && git commit -m "feat(collector): Claude Code log adapter"`

### Task 5: Codex log adapter

**Files:**
- Create: `src/collector/sources/codex-logs.ts`, `test/fixtures/codex/*.jsonl`
- Test: `test/collector/codex-logs.test.ts`

**Interfaces:**
- Produces: `class CodexLogAdapter { constructor(host: string); ingest(record: unknown, file: string, scope: string): SourceEvent[] }`
- **Agent ID:** `${host}/codex:${scope}:${threadId}`.
- **Call ID:** the native item `id` when there is one, otherwise `call_id`. The daemon uses the same item `id`, so `CollectorState` deduplicates the two sources (Task 6).

- [ ] **Step 1: Write the failing tests.**
  - Port all 7 tests in `test/native-events.test.mjs`, and the Codex tests in `test/model.test.mjs` and `test/edge-cases.test.mjs`:
    - `Codex links children before parents…`
    - `Codex patch success requires a paired receipt…`
    - `provider and account namespaces prevent identity collisions`
    - `Codex sibling writes…`
    - `Codex failed patch…`
    - `Codex resolves relative paths…`
  - Assert on the emitted events. Then add these:

```ts
test('task_complete and task_completed emit idle, not finished; turn_aborted emits interrupted', ...)
test('native CommandExecution item_completed emits tool.start and tool.end with the same ts and tool.output', ...)
test('outer exec call is category other; nested CommandExecution is command', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/collector/codex-logs.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement the adapter.** Port the Codex branch of `src/model.mjs` and all of `src/native-events.mjs`. Keep the `apply_patch` success rule exactly: the output matches `/Success\. Updated the following files:/`, and does not match `/Failed to|Error:|patch rejected/i`.
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/collector/codex-logs.test.ts`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git add src/collector/sources/codex-logs.ts test && git commit -m "feat(collector): Codex log adapter"`

### Task 6: Lifecycle rules and CollectorState

**Files:**
- Create: `src/shared/lifecycle.ts`, `src/collector/state.ts`
- Test: `test/shared/lifecycle.test.ts`, `test/collector/state.test.ts`, `test/fixtures/golden/session.ndjson`

**Interfaces:**
- Produces, in `lifecycle.ts`:
  - `LIFECYCLE = { lingerMs: 600_000, staleMs: 120_000, finishedFadeMs: 60_000, callTimeoutMs: 600_000, fileTtlMs: 1_800_000 }`
  - `type LifecycleOptions = { -readonly [K in keyof typeof LIFECYCLE]: number }`
  - `presentAgentIds(agents: Agent[], now: number, opts?: Partial<LifecycleOptions>): Set<string>`
  - `isStale(agent: Agent, now: number, opts?: Partial<LifecycleOptions>): boolean`
- Produces, in `state.ts`: `class CollectorState { constructor(opts?: Partial<LifecycleOptions>); apply(e: SourceEvent): SourceEvent | null; tick(now: number): SourceEvent[]; snapshot(now: number): Extract<Event,{kind:'snapshot'}>; load(s: Extract<Event,{kind:'snapshot'}>): void; watchRoots(now: number, max?: number): string[]; agent(id: string): Agent | undefined }`

- [ ] **Step 1: Write the failing tests.**
  - Port `history loading cannot push old events ahead of new events`: `lastEventAt` is the maximum of the old and new values.
  - Add these:

```ts
test('present: agent with event inside linger; ancestors of present agents; finished only for 1 minute', ...)
test('isStale only for working agents with no event for 2 minutes', ...)
test('apply drops duplicate tool.start and tool.end for the same agentId+callId (daemon/log overlap)', () => {
  const s = new CollectorState(); expect(s.apply(start)).not.toBeNull(); expect(s.apply(start)).toBeNull();
});
test('tool.start sets the agent working; tool.end writes add confirmed FileTouch; fs.change adds unattributed FileTouch without agentId', ...)
test('tick ends calls open longer than 10 minutes with ok:null and no writes', ...)
test('snapshot holds only present agents and their ancestors, their open calls, and files touched within 30 minutes', ...)
test('snapshot + later deltas equals full replay (golden file)', ...)     // replay golden NDJSON two ways; compare snapshot(now)
test('watchRoots: projectRoots of present agents, newest first, max 24, never home or /', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/shared/lifecycle.test.ts test/collector/state.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement `lifecycle.ts` and `CollectorState`.** The state stores agents, open calls by `${agentId}|${callId}`, ended-call keys (an LRU of 20,000 for deduplication), and file touches by path. `tool.update` adds files to the open call and touches them as confirmed.
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/shared test/collector/state.test.ts`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(collector): lifecycle rules and collector state model"`

### Task 7: Log discovery and LogSource

**Files:**
- Create: `src/collector/discovery.ts`, `src/collector/sources/log-source.ts`
- Test: `test/collector/discovery.test.ts`, `test/collector/log-source.test.ts`

**Interfaces:**
- Produces, in `discovery.ts`:
  - `type LogRoot = { provider: Provider; path: string; scope: string }`
  - `discoverRoots(env?: Record<string,string|undefined>, home?: string): Promise<LogRoot[]>`
  - `listRecentLogs(roots: LogRoot[], sinceMs: number, maxLogs: number): Promise<(LogRoot & { file: string; mtimeMs: number })[]>`
  - `findProjectRoot(cwd: string): Promise<string>`, which returns the nearest ancestor with `.git`, or `cwd`
- Produces, in `log-source.ts`: `class LogSource { constructor(opts: { host: string; roots: LogRoot[]; emit: (events: SourceEvent[]) => void; onStatus: (source: string, s: SourceState) => void; maxLogs?: number; sinceMs?: number; pollMs?: number; rescanMs?: number }); start(): Promise<void>; stop(): void; coverage(): Pick<Coverage,'logRoots'|'logsOpen'|'logsDiscovered'|'malformedRecords'> }`
- Defaults: `maxLogs` 120, `sinceMs` 24 hours, `pollMs` 2000, `rescanMs` 10000.

- [ ] **Step 1: Write the failing tests.**

```ts
test('discoverRoots: CODEX_HOME, ~/.codex-accounts/* (scope = account dir), CLAUDE_CONFIG_DIR; realpath dedupe', ...)  // port of prototype discoverRoots behavior
test('listRecentLogs: only .jsonl modified within sinceMs, newest first, quota split equally per provider', ...)
test('findProjectRoot returns nearest .git ancestor, else cwd', ...)
test('LogSource emits Claude events within 1 second of an append (fs.watch path)', async () => { /* append a tool_use line; await events with 1000 ms timeout */ });
test('LogSource safety poll picks up appends when watch callbacks are suppressed', ...)   // inject a no-op watch
test('LogSource reports source.status degraded for an unreadable root and keeps running', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/collector/discovery.test.ts test/collector/log-source.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement discovery and `LogSource`.**
  - Each root has one watcher, using the mode from `FS_WATCH_MODE` in the M0 results for its path class.
  - A `.jsonl` change triggers `tailer.read(file)`.
  - A new file triggers a rescan.
  - The adapter is chosen by `root.provider`.
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/collector/discovery.test.ts test/collector/log-source.test.ts`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(collector): log discovery and live log source"`

### Task 8: File-system change source

**Files:**
- Create: `src/collector/sources/fs-changes.ts`
- Test: `test/collector/fs-changes.test.ts`

**Interfaces:**
- Produces:
  - `IGNORED_DIRS: ReadonlySet<string>`: the prototype list plus `.superpowers`, `target`, and `out`.
  - `class RateLimiter { constructor(perSecond: number, now?: () => number); allow(key: string): boolean }`
  - `class GitignoreMatcher { constructor(root: string); isIgnored(absPath: string): boolean }`. It loads the root `.gitignore`, `.git/info/exclude`, and nested `.gitignore` files along the path, with a cache. It uses the `ignore` package.
  - `class FsChangeSource { constructor(opts: { emit: (events: SourceEvent[]) => void; onStatus: (source: string, s: SourceState) => void; maxPerSecond?: number; home?: string; now?: () => number }); setRoots(roots: string[]): void; stop(): void; readonly dropped: number; readonly roots: string[] }`
- **Operation mapping:** the watch event `change` becomes `change`. The watch event `rename` becomes `create` if the path exists after a `stat`, otherwise `delete`.
- **Paths:** emitted paths are absolute and realpath-based. The root is realpath'd once, and the relative name is joined to it.

- [ ] **Step 1: Write the failing tests.**

```ts
test('a write under a watched root emits fs.change create with no agentId', ...)
test('node_modules, .git and .gitignored paths emit nothing', ...)
test('RateLimiter allows 50 per second per key and counts the rest as dropped', () => { /* fake clock: 60 allow() calls at t=0 → 50 true; t=1001 → true */ });
test('setRoots refuses home and /, and replaces watchers on diff', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/collector/fs-changes.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement it.** Use the M0 `FS_WATCH_MODE`. In `per-dir` mode, keep a bounded scan of at most 12,000 directories for each root. Report roots over the limit as degraded.
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/collector/fs-changes.test.ts`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(collector): unattributed file change source"`

### Task 9: Codex daemon source

**Run this task only if the M0 result is `DAEMON_SOURCE=go`.** Otherwise, write "skipped: <reason from M0>" in the results file, and go to Task 10.

**Files:**
- Create: `src/collector/sources/codex-daemon.ts`, `test/fixtures/codex-daemon/*.jsonl` (recorded probe shapes with dummy values), `test/fakes/fake-app-server.ts`
- Test: `test/collector/codex-daemon.test.ts`

**Interfaces:**
- Produces:
  - `mapNotification(host: string, scope: string, msg: { method: string; params: unknown }, threads: Map<string, { parentId: string | null; cwd: string | null }>): SourceEvent[]`
  - `class CodexDaemonSource { constructor(opts: { host: string; scope?: string; emit: (events: SourceEvent[]) => void; onStatus: (source: string, s: SourceState) => void; command?: string[] }); start(): Promise<void>; stop(): Promise<void> }`
- Default `command`: `['codex','app-server','proxy']`, or the socket path, according to `DAEMON_TRANSPORT`.

- [ ] **Step 1: Write the failing tests.**

```ts
test('mapNotification: item/started commandExecution → tool.start command; item/completed → tool.end with exitCode', ...)
test('mapNotification: outputDelta → tool.output chunks ≤ 4 KiB; fileChange/patchUpdated → tool.update files', ...)
test('mapNotification: turn/started working, turn/completed idle, thread/closed and thread/archived finished', ...)
test('source sends initialize, initialized, thread/loaded/list, then resume with only threadId for loaded threads', async () => {
  // fake-app-server records requests; assert resume params deep-equal { threadId } and never for unloaded ids
});
test('source sends thread/unsubscribe for each thread on stop', ...)
test('source reports down when the command is missing and retries with backoff', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/collector/codex-daemon.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement it.** Use the M0 transport. Send an `agent.upsert` from the thread metadata before the first item event of each thread.
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/collector/codex-daemon.test.ts`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(collector): Codex app-server daemon source"`

### Task 10: Collector, demo source, and `collect --stdio`

**Files:**
- Create: `src/collector/collector.ts`, `src/collector/demo.ts`, `src/cli.ts`
- Test: `test/collector/collector.test.ts`, `test/cli.test.ts`

**Interfaces:**
- Consumes: Tasks 3–9.
- Produces:
  - `class Collector { constructor(opts: { host: string; demo?: boolean; watch?: string[]; lifecycle?: Partial<LifecycleOptions>; roots?: LogRoot[] }); start(): Promise<void>; stop(): Promise<void>; subscribe(fn: (e: Envelope) => void): () => void; coverage(): Coverage }`
  - `class DemoSource { constructor(opts: { host: string; emit: (events: SourceEvent[]) => void; scale?: 'normal' | 'stress' }); start(): void; stop(): void }`. The `stress` scale gives 50 agents, 3,000 files, and 200 events per second.
  - `type CliArgs = { mode: 'hub' | 'collect'; remotes: string[]; demo: boolean; demoScale: 'normal' | 'stress'; port: number; open: boolean; watch: string[]; lingerMs: number; label: string | null }`
  - `parseArgs(argv: string[]): CliArgs`
  - `main(argv: string[]): Promise<void>`
- **Collector flow:**
  1. Source events go to `enrich`. It resolves `projectRoot` with a cached `findProjectRoot`, and rewrites confirmed file paths to realpath-based paths with a cached project-root prefix.
  2. Then `state.apply`.
  3. Then the envelope gets `{v:1, host, seq}`.
  4. Then subscribers receive it.
- **Timers:**
  - A 1-second `tick` runs `state.tick(now)` and `fs.setRoots([...opts.watch, ...state.watchRoots(now)].slice(0, 24))`.
  - Coverage is emitted every 5 seconds.
- **Subscribers** first get `hello` (with `now`), then `snapshot(now)`, then live events.

- [ ] **Step 1: Write the failing tests.**

```ts
test('subscribe gets hello (with now) then snapshot then live events with increasing seq', ...)
test('demo collector labels hello demo: true and emits agents within 1 second', ...)
test('Review Focus 3: a write through a symlinked project dir and its fs.change resolve to the same path', async () => {
  // test/.tmp/real/proj (.git) and test/.tmp/link -> real; Claude tool_use Write file_path under link/proj
  // expect tool.end writes[0].path === fs.change path === realpath of file
});
test('parseArgs: defaults, --remote repeatable, --linger minutes → ms, collect --stdio --label', () => {
  expect(parseArgs([])).toMatchObject({mode:'hub',port:4317,open:true,lingerMs:600_000,remotes:[]});
  expect(parseArgs(['collect','--stdio','--label','devbox'])).toMatchObject({mode:'collect',label:'devbox'});
});
test('`bun src/cli.ts collect --stdio --demo` writes valid NDJSON: hello, snapshot, events', async () => { /* spawn, read 3 lines, parseLine each */ });
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/collector/collector.test.ts test/cli.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement `Collector`, `DemoSource`, and the `collect --stdio` path of `main`.** Hub mode is added in Task 11.
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(collector): collector pipeline, demo source, collect --stdio"`

### Task 11: Hub and server

**Files:**
- Create: `src/hub/hub.ts`, `src/hub/server.ts`, `viewer/index.html` (placeholder page that loads `main.ts`), `viewer/main.ts` (placeholder)
- Modify: `src/cli.ts` (hub mode: local collector, server, `open`/`xdg-open` unless `--no-open`)
- Test: `test/hub/hub.test.ts`, `test/hub/server.test.ts`

**Interfaces:**
- Consumes: `HostStatus` (Task 1), `Collector` (Task 10).
- Produces:
  - `interface HubSource { readonly label: string; readonly kind: 'local' | 'remote'; subscribe(fn: (e: Envelope) => void): () => void; status(): HostStatus; start(): Promise<void>; stop(): Promise<void> }`
  - `class LocalSource implements HubSource { constructor(collector: Collector, label: string) }`
  - `class Hub { addSource(src: HubSource): void; uniqueLabel(wanted: string): string; subscribe(fn: (e: Envelope) => void): () => void; snapshotAll(): Envelope[]; hosts(): HostStatus[]; stop(): Promise<void> }`
  - `startServer(opts: { hub: Hub; port: number }): { port: number; url: string; stop(): void }`
- **Hub mirror:** one `CollectorState` for each host label, fed by the envelopes. A `snapshot` envelope calls `load`. A new subscriber gets the latest `hello` and `snapshotAll()` first.
- **Routes:**
  - `/`: the HTML import of `viewer/index.html`.
  - `/ws`: WebSocket. Server to client, one `Envelope` JSON per message, plus `{type:'hosts', hosts}` when host status changes. Client to server, `{type:'resnapshot'}`.
  - `/api/snapshot`: GET, JSON `{ hosts, envelopes }`.

- [ ] **Step 1: Write the failing tests.**

```ts
test('uniqueLabel appends #2, #3 for repeated labels', () => { const h = new Hub(); expect(h.uniqueLabel('mac')).toBe('mac'); expect(h.uniqueLabel('mac')).toBe('mac#2'); });
test('new subscriber receives hello + snapshot per host before live events', ...)
test('server rejects /ws and /api/snapshot with a foreign Host or Origin (403)', async () => { /* fetch with headers Host: evil.test:PORT; Origin: http://evil.test */ });
test('server serves the bundled viewer at / and streams envelopes over /ws', ...)
test('resnapshot message resends hello + snapshot', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/hub`
  - Expected: FAIL.
- [ ] **Step 3: Implement `Hub`, `LocalSource`, `startServer`, and hub mode in `main`.**
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/hub && bun src/cli.ts --demo --no-open --port 4318 & sleep 2; curl -s 127.0.0.1:4318/api/snapshot | head -c 200; kill %1`
  - Expected: PASS. The `curl` prints JSON with `"hosts"`.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(hub): hub with per-host mirrors, local HTTP and WebSocket server"`

### Task 12: Viewer store

**Files:**
- Create: `viewer/store.ts`
- Test: `test/viewer/store.test.ts`

**Interfaces:**
- Consumes: `Envelope`, `presentAgentIds`, `isStale`, `LIFECYCLE`.
- Produces:
  - `type ViewAgent = Agent & { host: string; stale: boolean; lastEventMs: number }`
  - `type ViewCall = Call & { host: string; startMs: number; endMs: number | null; ok: boolean | null; output: string; outputTruncated: boolean }`
  - `type Touch = { host: string; path: string; projectRoot: string | null; attribution: 'confirmed' | 'unattributed'; agentId?: string; atMs: number }`
  - `class ViewerStore { constructor(opts?: { now?: () => number; lifecycle?: Partial<LifecycleOptions> }); apply(e: Envelope): void; setHosts(h: HostStatus[]): void; agents(now: number): ViewAgent[]; laneOrder(now: number): string[]; calls(now: number, windowMs?: number): ViewCall[]; openCall(agentId: string): ViewCall | undefined; commands(focus: string | null, limit?: number): ViewCall[]; takeTouches(now: number): Touch[]; focus: string | null; setFocus(id: string | null): void; coverage(host: string): Coverage | undefined; hosts(): HostStatus[]; isDemo(): boolean }`
- **`tool.update`** appends files to the open `ViewCall`, and queues confirmed touches for them.
- **Skew:** each host's offset is `viewerNow - Date.parse(hello.now)`. Every source `ts` from that host is shifted by this offset into `*Ms` fields.
- **`takeTouches(now)`** returns confirmed touches at once. It holds unattributed touches for 2 seconds. It drops a held unattributed touch if a confirmed touch for the same host and path falls within ±2 seconds.
- **`laneOrder`:** hosts in order of appearance; inside each host, main agents in order of first appearance, each followed by its sub-agents in depth-first order.

- [ ] **Step 1: Write the failing tests.**

```ts
test('snapshot replaces that host state; deltas update it; other hosts untouched', ...)
test('agents() excludes agents past linger and marks stale working agents', ...)
test('Review Focus 2: a host whose hello.now is 5 minutes behind still shows its new events as present and in the timeline window', () => {
  const s = new ViewerStore({ now: () => NOW });
  s.apply(env('remote', { kind: 'hello', now: iso(NOW - 300_000), collectorVersion:'x', platform:'linux' }));
  s.apply(env('remote', { kind: 'tool.start', call: { ...call, startedAt: iso(NOW - 300_000 + 1000) } }, NOW - 300_000 + 1000));
  expect(s.calls(NOW, 16_000).length).toBe(1);
});
test('unattributed touch is held 2 s and suppressed by a confirmed touch of the same path', ...)
test('commands(focus) returns the newest 3 command calls, filtered by agent when focused; output keeps last 16 KiB', ...)
test('laneOrder nests sub-agents under parents per host', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/viewer/store.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement `ViewerStore`.**
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/viewer/store.test.ts`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(viewer): client state store with skew correction and flicker suppression"`

### Task 13: Tree model

**Files:**
- Create: `viewer/stage/tree-model.ts`
- Test: `test/viewer/tree-model.test.ts`

**Interfaces:**
- Produces:
  - `type NodeKind = 'host' | 'project' | 'dir' | 'file' | 'other'`
  - `type TreeNode = { id: string; name: string; kind: NodeKind; depth: number; parent: TreeNode | null; children: TreeNode[]; bornAt: number; lastTouchAt: number; ang: number; tgt: number }`
  - `class TreeModel { constructor(opts?: { fileTtlMs?: number; newNodesPerSecond?: number }); touch(t: Touch): TreeNode | null; prune(now: number): TreeNode[]; layout(dt: number): void; hosts(): TreeNode[]; find(host: string, path: string): TreeNode | undefined }`
- **Node IDs:** `${host}|${absPath}`. The host node ID is `${host}|`.
- **Project placement:**
  - A confirmed touch uses `t.projectRoot`.
  - An unattributed touch uses the longest known project prefix on that host.
  - A path with no project goes under the host's `other` node.
- **Growth and removal:**
  - `touch` returns `null` when an unattributed touch would create a node over the cap of 20 per second for each project.
  - `prune` removes files untouched for 30 minutes, and empty folders and projects. It returns the removed nodes so the stage can fade them.
- **`layout`:** in depth-first order, visible leaves get target angles `-π/2 + (i+0.5)/n·2π` within their host. Each `ang` eases toward its `tgt` with `1 - e^(-3·dt)`. A folder's angle is the mean of its visible leaves' angles.

- [ ] **Step 1: Write the failing tests.**

```ts
test('confirmed touch creates host → project → dirs → file chain', ...)
test('unattributed touch maps to the longest known project prefix; unknown → other', ...)
test('unattributed new-node cap: 21st new file in the same second returns null; confirmed never capped', ...)
test('prune removes files untouched 30 min and empty parents, returns removed nodes', ...)
test('layout converges ang to tgt and keeps leaf order stable when a leaf is added', ...)
test('same path on two hosts gives two nodes', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/viewer/tree-model.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement `TreeModel`.**
- [ ] **Step 4: Run the tests and confirm they pass.**
  - Run: `bun test test/viewer/tree-model.test.ts`
  - Expected: PASS.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(viewer): growing radial tree model"`

### Task 14: Stage (PixiJS) and effects

**Files:**
- Create: `viewer/stage/effects.ts`, `viewer/stage/stage.ts`
- Modify: `viewer/main.ts`, `viewer/index.html`, `viewer/style.css`
- Test: `test/viewer/effects.test.ts`

**Interfaces:**
- Produces:
  - `type Effect = { type: 'beam' | 'dashed' | 'sweep' | 'pulse' | 'link' | 'soft'; color: string; progress: number }`
  - `effectFor(call: ViewCall, now: number, reducedMotion: boolean): Effect | null`
  - `class Stage { constructor(host: HTMLElement, store: ViewerStore, tree: TreeModel); start(): Promise<void>; onAgentClick(fn: (agentId: string) => void): void; setReducedMotion(on: boolean): void; destroy(): void }`
- **Effect rules:**

  | Category | Effect |
  |---|---|
  | `edit` and `write` | `beam` |
  | `read` | `dashed` |
  | `search` | `sweep` |
  | `command` | `pulse` |
  | `agent` | `link` |
  | `other` | `soft` |

  - Colors come from `CATEGORY_COLORS`.
  - `progress` is `(now - startMs) / duration`.
  - The result is `null` for a call that ended more than 1.5 seconds ago, or that started more than 2 seconds ago with its first frame not yet drawn (the background-tab backlog).
- **Stage drawing:**
  - Host roots sit side by side. Each host has a center at `x = (i+0.5)/n · width`.
  - Nodes and agents follow the motion of the `layout.html` mockup: eased moves toward the target file, trails, and a home ring.
  - Glow uses the technique recorded in M0.
  - Labels are culled as M0 decided.
  - Stale agents get a dim, slow pulse. Interrupted agents flash red once.
  - Removed nodes fade out over 600 ms.
  - A thin line joins each sub-agent to its parent; `session` links are dashed.
  - `prefers-reduced-motion` gives no trails or particles, and instant moves.

- [ ] **Step 1: Write the failing tests.**

```ts
test('effectFor maps categories to effect types and colors', ...)
test('effectFor returns null 1.5 s after end', ...)
test('Review Focus 4: after a 5-minute background gap, calls that started more than 2 s ago produce no effect', () => {
  expect(effectFor({ ...call, startMs: NOW - 300_000, endMs: null }, NOW, false)).toBeNull();
});
test('reducedMotion turns beam into a static line effect with progress 1', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/viewer/effects.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement `effects.ts` and `Stage`.** In `main.ts`, connect the WebSocket to `/ws`. Reconnect with backoff from 1 to 10 seconds, and send `resnapshot` on reconnect. Pass envelopes to the store, and feed `store.takeTouches()` into `tree.touch` every frame.
- [ ] **Step 4: Run the tests, then check the stage by eye.**
  - Run: `bun test test/viewer/effects.test.ts`, then `bun src/cli.ts --demo`.
  - Expected: PASS. The stage looks like `.superpowers/brainstorm/*/content/layout.html` option C.
  - Then run `bun src/cli.ts --demo --demo-scale stress`. The FPS counter (dev overlay, `?fps=1`) reads ≥ 58 average over 10 seconds.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(viewer): PixiJS stage with growing tree, agent motion and effects"`

### Task 15: Drawer, HUD, and viewer smoke test

**Files:**
- Create: `viewer/drawer/timeline.ts`, `viewer/drawer/drawer.ts`, `viewer/hud.ts`, `test/e2e/viewer.smoke.ts`
- Modify: `viewer/main.ts`, `viewer/index.html`, `viewer/style.css`
- Test: `test/viewer/timeline.test.ts`

**Interfaces:**
- Produces:
  - `drawTimeline(ctx: CanvasRenderingContext2D, size: { w: number; h: number }, store: ViewerStore, now: number, opts: { laneHeight: number; windowMs: number; focus: string | null }): void`
  - `laneAt(y: number, top: number, laneHeight: number, lanes: string[]): string | null`
  - `class Drawer { constructor(root: HTMLElement, store: ViewerStore); toggle(): void; readonly open: boolean; render(now: number): void }`
  - `class Hud { constructor(root: HTMLElement, store: ViewerStore); toggleCoverage(): void; render(): void }`
- **Behavior:**
  - The drawer is closed at start. It covers 35% of the height, and slides in 450 ms with `cubic-bezier(.2,.8,.2,1)`.
  - Left side: lanes with a 16-second window, from `drawTimeline`.
  - Right side: the command list from `store.commands(focus, 3)`.
  - Keys: `T` toggles the drawer, `Esc` clears the focus, `C` toggles coverage.
  - A click on a lane, or on an agent on the stage, calls `store.setFocus`.
  - The HUD corner shows each host's state. It shows `SIMULATED` when `store.isDemo()` is true. A remote host that is down shows its `detail` text.

- [ ] **Step 1: Write the failing test.**

```ts
test('laneAt maps y to lane id and returns null outside lanes', () => {
  expect(laneAt(26 + 23 * 2 + 5, 26, 23, ['a','b','c','d'])).toBe('c'); expect(laneAt(10, 26, 23, ['a'])).toBeNull();
});
```

- [ ] **Step 2: Run the test and confirm it fails.**
  - Run: `bun test test/viewer/timeline.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement the drawer, the HUD, and the key handling.**
- [ ] **Step 4: Write the smoke test `test/e2e/viewer.smoke.ts`** with Playwright and Chromium:
  1. Start `startServer` with a demo collector on port 0.
  2. Open the page and wait 2 seconds.
  3. Assert that there are no console errors.
  4. Assert that the stage canvas exists.
  5. Assert that the `.hud` text contains `SIMULATED`.
  6. Press `T` and assert that `#drawer` has class `open`.
  7. Press `C` and assert that `#coverage` is visible.
  8. Click the first lane and assert that the store focus is set. Read the focus through `window.__agentView.store.focus`, which exists only in demo mode.
- [ ] **Step 5: Run all the checks.**
  - Run: `bun test && bunx playwright install chromium && bun run smoke`
  - Expected: all PASS, and the smoke test prints `smoke ok`.
- [ ] **Step 6: Commit.**
  - Command: `git add -A && git commit -m "feat(viewer): timeline drawer, HUD, coverage panel, smoke test"`

### Task 16: M1 wrap-up: live audit, README, and prototype removal

**Files:**
- Create: `test/live-audit.ts`
- Modify: `README.md`
- Delete: `src/*.mjs`, `public/`, `test/*.test.mjs`, `test/live-audit.mjs`

**Interfaces:**
- Consumes: `/api/snapshot` (Task 11).

- [ ] **Step 1: Write `test/live-audit.ts`.** Port the logic of `test/live-audit.mjs`:
  - Fetch `/api/snapshot` from a running hub.
  - For each provider, assert at least one agent and no parent cycles.
  - Assert that every confirmed `FileTouch` has an `agentId` that exists.
  - Print counts only.
- [ ] **Step 2: Run it against real data, and check idle CPU.**
  - Run: `bun src/cli.ts --no-open & sleep 5; bun run audit:live`
  - Expected: JSON counts with no assertion errors.
  - Then stop all agent activity for 60 seconds and run `ps -o %cpu= -p $!; kill %1`.
  - Expected: below `2.0`. The spec target is a collector below 2% of one core when agents are idle.
- [ ] **Step 3: Remove the prototype files.** Rewrite `README.md` from the spec:
  - run commands,
  - sources and limits,
  - the accuracy rules,
  - the local-access notes,
  - the M0 known gaps.
- [ ] **Step 4: Run all checks.**
  - Run: `bun test && bun run typecheck && bun run smoke`
  - Expected: PASS. No test references any `.mjs` file.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "chore: replace prototype with v2; add live audit and README"`

---

## Milestone M2 — Remote

### Task 17: Remote collectors over SSH

**Files:**
- Create: `src/hub/remote.ts`
- Modify: `src/cli.ts` (adds one `RemoteCollector` for each `--remote`, with the label `hub.uniqueLabel(sshHost)`)
- Test: `test/hub/remote.test.ts`

**Interfaces:**
- Produces: `class RemoteCollector implements HubSource { constructor(opts: { sshHost: string; label: string; command?: (sshHost: string, label: string) => string[]; backoffMs?: [number, number]; spawn?: typeof Bun.spawn }) }`
- Default command: `['ssh','-T','-o','BatchMode=yes','-o','ServerAliveInterval=15', sshHost, 'agent-view','collect','--stdio','--label', label]`
- Backoff starts at 1,000 ms, doubles up to 30,000 ms, and resets after 60 seconds up.
- **Status:**
  - `up` after the first valid `hello`.
  - `down` with `detail` on exit:
    - exit code 127 gives `agent-view not found on <sshHost>`;
    - exit code 255 gives `ssh failed: <last stderr line>`;
    - any other code gives `collector exited <code>`.

- [ ] **Step 1: Write the failing tests.** The `command` option runs `bun src/cli.ts collect --stdio --demo --label <label>` locally instead of `ssh`.

```ts
test('remote source parses NDJSON from the child and reports up after hello', ...)
test('child exit triggers reconnect with backoff and a fresh hello + snapshot', ...)
test('Review Focus 5: exit 127 sets down with "agent-view not found on devbox" and retries with growing delay', async () => {
  // command: () => ['sh','-c','exit 127']; backoffMs [50, 400]; observe status detail and ≥3 spawn times with increasing gaps
});
test('malformed child lines are skipped and counted, stream continues', ...)
```

- [ ] **Step 2: Run the tests and confirm they fail.**
  - Run: `bun test test/hub/remote.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement `RemoteCollector` and the `--remote` wiring.** The stage already lays out several hosts (Task 14).
- [ ] **Step 4: Run the tests, then check by hand.**
  - Run: `bun test test/hub/remote.test.ts`
  - Expected: PASS.
  - Then, with a real host that has the binary, run `bun src/cli.ts --remote <host>`. A second root appears with the host's label.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "feat(hub): remote collectors over SSH with backoff"`

### Task 18: Compiled binaries

**Files:**
- Create: `scripts/compile.ts`
- Modify: `README.md` (install and remote setup sections)
- Test: `test/compile.test.ts`

**Interfaces:**
- Produces: `bun run compile` writes `dist/agent-view-<os>-<arch>` for the four Global Constraints targets.

- [ ] **Step 1: Write the failing test.** It compiles for the current platform into `test/.tmp/`, then checks the binary:
  1. It runs `collect --stdio --demo`, and the first line parses as `hello`.
  2. It runs `--demo --no-open --port 0`, prints its URL, and `GET /` returns HTML with a bundled script. This proves the viewer is embedded.
- [ ] **Step 2: Run the test and confirm it fails.**
  - Run: `bun test test/compile.test.ts`
  - Expected: FAIL.
- [ ] **Step 3: Implement `scripts/compile.ts`.** Call `bun build --compile --target=<t> src/cli.ts --outfile dist/agent-view-<os>-<arch>` for each target.
- [ ] **Step 4: Run all checks.**
  - Run: `bun test && bun run compile && ls dist`
  - Expected: PASS, and four binaries are listed.
- [ ] **Step 5: Commit.**
  - Command: `git add -A && git commit -m "build: cross-compiled single-binary releases"`
