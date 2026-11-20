# Agent View TUI in Go — Design

Date: 2026-11-18
Status: approved 2026-11-18
Supersedes in `2026-11-17-agent-view-design.md`: the runtime constraint (Bun, TypeScript, browser viewer), "Architecture", "Command-line interface", "Viewer", "Packaging", "Performance targets", "Milestones", and the viewer parts of "Testing". Still in force from that spec: the intent and anti-goals, the sources and how they are read, the event schema, the lifecycle rules, the accuracy rules, security and privacy, and the risks table.

## Intent

The owner does not like the radial file tree ("branch" view). It puts files at the center. The owner wants agents at the center:

1. **Who:** which agents run now.
2. **What:** what each agent does at this moment.
3. **Where:** the repo and folder it works in. This is context, not the main picture.

The owner tried many visual directions in the brainstorm (agent board, lanes, constellation, strategy map, colony, roguelike, pixel cards, cyberpunk, textured pixel art). The owner chose a **terminal card table**: each agent is a row of text cards drawn with box-drawing characters, animated in character steps. The accepted prototype is `.superpowers/brainstorm/52439-1795026034/content/term-cards.html` (local only, not committed). It is the visual reference.

Because the design is pure text, the owner decided:

- Agent View becomes a **real terminal application** (TUI). The browser viewer goes away. The 2026-11-17 spec rejected a TUI only because "animation is limited to text characters". That reason no longer applies.
- Agent View is **rewritten in Go**. The owner's reasons: **binary size** (the Bun binaries are 90–110 MB; remote machines need a copy), **performance**, and **later extensibility** (a mature TUI ecosystem to add views and widgets later).

## Goals

1. Show every present agent as a row: agent card, current call card, history cards, and a "where" line.
2. Show what an agent does within about 1 second of the record, as before.
3. Animate changes in character steps: cards print line by line, history moves right, failures flash.
4. Show the agent hierarchy with tree lines (`├──`, `└──`).
5. Stay readable with 50 agents: idle agents collapse to one line, and the board scrolls.
6. Run in any modern terminal, in a tmux pane, and over SSH. Start with one command. Install as one binary of **under 20 MB**.
7. Show the recent past at startup (see "Startup history").
8. Keep everything the TypeScript version does today: Claude Code and Codex sources, sub-agents, unattributed file changes, remote hosts over SSH, `--demo`, coverage.

## Non-goals

- A browser viewer.
- Timeline scrubbing and log scrollback beyond the visible log. (Nice-to-have only.)
- Themes other than the default palette. The green and amber phosphor themes from the prototype can come later; the palette is one table.
- A TUI that attaches to an agent-view process on another machine. Remote hosts work as today: the local process starts `ssh <host> agent-view collect --stdio`.
- New sources or new event kinds. The event schema stays as it is.
- Everything in the 2026-11-17 non-goals stays a non-goal.

## Anti-goals

- **False attribution.** Unchanged. No card, line, or animation may show an agent doing something it did not record. Unattributed file changes never sit on an agent's row.
- **A fake progress bar.** The data has no percent-done. No element may suggest one.
- **Terminal damage.** Agent data (file names, commands, command output) must never write raw escape codes or control characters to the owner's terminal. A crafted file name must not move the cursor, change colors outside its cell, set the window title, or write to the clipboard.
- **A corrupted screen.** While the TUI runs, nothing else writes to the terminal.
- **A port that changes behavior.** The Go collector must produce the same events as the TypeScript collector for the same input. A silent difference in parsing is a false-attribution risk.
- **Noise** and **slowing the machine down**: unchanged. The TUI draws no frame when nothing changes.

## Constraints

- Go 1.27 (installed here: 1.27.3). One Go module at the repository root.
- Dependencies allowed: Bubble Tea v2 (`charm.land/bubbletea/v2`, 2.1.x), Lip Gloss v2 (`charm.land/lipgloss/v2`, 2.1.x), Bubbles v2 if a widget helps, `fsnotify` (1.10.x), and `fsnotify/fsevents` (cgo, darwin builds only, phase 2, if the spike passes). Anything else needs a reason in the plan.
- Targets: `darwin-arm64`, `darwin-x64`, `linux-x64`, `linux-arm64`. Linux builds use `CGO_ENABLED=0`. Darwin builds are made on a Mac.
- Terminals: modern emulators (iTerm2, Terminal.app, Ghostty, kitty, WezTerm, Alacritty, VS Code, tmux inside these). Minimum size 80 columns × 24 lines.
- macOS hosts the TUI; Linux must also run it (over SSH) and run the collector.
- Read-only access to agent data, as before.

## Approaches

### Replace or add (owner's choice)

| Option | Trade-off | Result |
|---|---|---|
| **Replace the file tree** | Agents-first everywhere. The spatial picture of files is lost; paths stay in the "where" line and the log. | **Chosen** |
| Add as a second view | Nothing is lost. Two renderers, and the disliked tree stays. | Rejected |

### Platform (owner's choice)

| Option | Trade-off | Result |
|---|---|---|
| **Real TUI** | Lives next to the agents (tmux, SSH). Terminals draw box characters without gaps. Animation limited to cell steps, which the design already uses. | **Chosen** |
| DOM text grid in the browser | Smallest change. Box characters can show gaps. Needs a browser tab. | Rejected |
| Canvas character grid in the browser | Exact grid. More code, still a browser. | Rejected |

### Language (owner's choice)

| Option | Trade-off | Result |
|---|---|---|
| **Go** | Binaries about 10–20 MB; low CPU; mature TUI ecosystem (Charm). Cost: rewrite about 3,000 lines of TypeScript and port 279 tests; risk of parsing differences (handled by parity tests). | **Chosen** |
| Stay on TypeScript and Bun | No rewrite; idle CPU target already met (0.22% measured). Binaries 90–110 MB. Claude recommended this; the owner chose Go for size, performance, and extensibility. | Rejected |
| Rust (Ratatui) | Smallest and fastest. Slowest to iterate for a personal tool. | Rejected |

### TUI library

| Option | Trade-off | Result |
|---|---|---|
| **Bubble Tea v2 + Lip Gloss v2** | Elm-style app (model, update, view), widget library for later features, v2 renderer diffs cells and uses synchronized output. Lip Gloss v2 layers place cards at exact positions. Adds about 3–5 MB. | **Chosen** |
| tcell v3 | Low-level cell API, most control, maps directly onto the card grid. No app structure and no widgets, so every later feature is ours. | Rejected; second choice |
| Own renderer | Go libraries already do it well. | Rejected |

### Order of the work

| Option | Trade-off | Result |
|---|---|---|
| **Phase 1 TUI, phase 2 port** | Phase 1: a Go TUI fed by the current TypeScript collector as a child process (NDJSON on stdout). The owner sees the new screen against real data early. Phase 2: port the collector, hub, and remote code to Go with event parity checks, then delete the TypeScript. Two runtimes during phase 1, local machine only. | **Chosen** |
| Port first | Old code goes sooner. Nothing new to see until both are done; the browser viewer must keep working meanwhile. | Rejected |
| One big rewrite | One switch-over, longest wait for feedback. | Rejected |

## Architecture

### Phase 1

```
┌───────────────────────────────── agent-view (Go) ─────────────────────────────────┐
│ child: bun src/cli.ts collect --stdio ──NDJSON──▶ decoder ──▶ store ──▶ board ──▶ │
│        (stderr ──▶ diagnostics)                                  cell grid ──▶ tea│
└───────────────────────────────────────────────────────────────────────────────────┘
```

- The Go binary starts the TypeScript collector as a child process and reads one envelope per line from its stdout. The child command defaults to `bun src/cli.ts collect --stdio` run from the repository root, with the flags passed through (`--demo`, `--demo-scale`, `--watch`, `--linger`). `AGENT_VIEW_COLLECTOR` (a JSON array of strings) overrides the command, for tests and for a compiled TypeScript binary.
- The child's stderr goes to the diagnostics buffer. If the child exits, the status bar shows the host as `down` and the log says so; the TUI keeps running and retries with backoff (1 s, doubling, max 30 s).
- Local machine only. `--remote` is rejected in phase 1 with a message.
- The TypeScript code is not changed in phase 1. `bun src/cli.ts` (browser viewer) keeps working until phase 2 ends.

### Phase 2

```
 per remote machine                         this machine, one Go process
┌──────────────────────────┐  NDJSON   ┌───────────────────────────────────────────────────┐
│ agent-view collect       │─ (ssh) ──▶│ hub ── subscribe ──▶ store ──▶ board ──▶ cells ──▶ │
│  --stdio (Go)            │           │  ▲                                          tea   │
└──────────────────────────┘           │  └─ local collector (goroutines, in-process)      │
                                       └───────────────────────────────────────────────────┘
```

- Port, module by module, with the same behavior: event schema types and categories, lifecycle, NDJSON, tailing, log discovery, Claude Code and Codex log sources, file-system changes, collector state model, demo stream, hub (merge, per-host mirror, late-subscriber snapshot), remote collectors over SSH (validation, backoff, reconnect).
- The hub's `subscribe` keeps its contract: hello, snapshot, and coverage for each host first, then live envelopes. The TUI subscribes before the collectors start (see "Startup history").
- File watching: inotify on Linux (`fsnotify`). On macOS, start with a spike: FSEvents through `fsnotify/fsevents` (cgo) versus `fsnotify`'s kqueue. kqueue holds one file descriptor per watched folder or file, which scales badly. Ship FSEvents if the spike passes (no missed events in the M0 scenarios, CPU and descriptor counts within targets).
- `agent-view serve [--port <n>]`: the headless hub with `GET /api/snapshot` on `127.0.0.1` (standard library HTTP; same `Host` checks as today). `/ws` is dropped, because no client uses it. The live audit becomes `agent-view audit [--port <n>]` (or a Go test helper) and keeps printing counts only.
- At the end of phase 2, delete the TypeScript code, `package.json`, `bun.lock`, `tsconfig.json`, `scripts/compile.ts`, the browser viewer, and `spikes/pixi-stress/`.

### Inside the TUI (both phases)

- **Store:** a Go port of `viewer/store.ts` (`ViewerStore`): presence, linger, stale, calls, focus, flicker suppression, lane order, skew correction, resnapshot need. New: the last N ended calls of one agent, newest first; per-agent counters (see "Startup history"). Its tests are ported with it.
- **Board model:** a pure function of store state, time, terminal size, UI state (selection, focus, paused, log size), and animation state. It returns the rows and the cards in each row.
- **Cell model:** pure functions draw cards into a cell grid. A cell is one character plus a style (foreground, background, bold, dim, inverse). They never produce ANSI codes. The view turns the grid into the frame for Bubble Tea (through Lip Gloss v2 layers or one styled string; builder's choice).
- **Program:** Bubble Tea v2 owns raw mode, the alternate screen, mouse (SGR), resize, synchronized output, and the cell diff. Events from the store and animation ticks arrive as messages.
- **Exit:** `q`, `Ctrl-C`, `SIGTERM`, `SIGHUP`, or a panic restore the terminal (Bubble Tea does this; a panic handler must too), stop the collectors and children, then print held diagnostics to stderr. `Ctrl-Z` suspends and resumes cleanly.
- **Quiet terminal:** while the TUI runs, nothing writes to stdout or stderr. All library and collector logging goes to the diagnostics buffer.

## Command-line interface

- `agent-view` — start the collector (phase 1: the child; phase 2: in-process, plus `--remote` hosts) and the TUI. If stdin or stdout is not a TTY, exit with code 2 and a message that names `agent-view serve` (phase 2).
- `agent-view collect --stdio [--label <name>]` — phase 2: the Go collector for remote machines. Same NDJSON as today.
- `agent-view serve [--port <n>]` — phase 2: the headless hub.
- Flags: `--demo`, `--demo-scale normal|stress`, `--watch <dir>`, `--remote <ssh-host>` (phase 2), `--linger <minutes>`, `--reduced-motion`. `--port` only with `serve`. `--no-open` is gone.
- `NO_COLOR` set to any value turns color off. Bold, dim, and inverse still apply.
- Exit codes: 0 normal, 2 usage error, 1 other failure.

## Screen layout

Reference: the prototype. Sizes are in cells.

```
 AGENT VIEW  kmbp16 ● up  6 agents  3 working  42 calls/min  1 fault                 10:11:22   ← status bar

 ┏━ agent-view ━━━━━━━━━┓ ┌─ EDIT ─────────── 1.4s ─┐ ┌ RUN ───────┐ ┌ READ ──────┐
 ┃  ▄▀█▀▄    claude     ┃ │ tree-view.ts             │ │bun test    │ │store.ts    │
 ┃  █▀█▀█    • WORKING  ┃ │ viewer/stage/            │ │ok 3.1s     │ │ok 0.8s     │
 ┃  ▀▄▀▄▀    calls 63   ┃ │                          │ └────────────┘ └────────────┘
 ┃  ▀   ▀    fault 0    ┃ │ started 10:11:20         │
 ┃           seen  31m  ┃ │ ██░░░░░░░░░░   | running │
 ┗━┳━━━━━━━━━━━━━━━━━━━━┛ └──────────────────────────┘
   ┃                      agent-view:viewer/stage/ $ ▌
   └── ╭─ Verify review ──╮ …                                               ← sub-agent row
 ▸ eos-api · codex · idle 4m · last READ users.ts ok                        ← collapsed row
 ┌─ events · tail -f ───────────────────────────────────────────────────────────────────────┐
 │ 10:11:18 agent-view       READ   viewer/stage/tree-view.ts       ok    1.5s             │
 │ 10:11:19 machina-ui       RUN    $ pnpm lint                     FAIL  3.3s             │
 │ 10:11:21 Verify review    BOOT   sub-agent of agent-view         ok                     │
 │ $ ▌                                                                                     │
 └─────────────────────────────────────────────────────────────────────────────────────────┘
 space pause  j/k select  enter focus  esc clear  c coverage  l log  ? help  q quit        ← key bar
```

- **Status bar (line 1):** product name; one segment per host with its state (`up`, `connecting`, `down`, `N sources degraded`); counts of agents, working agents, calls per minute, and faults in the linger window; `PAUSED` when paused; the clock.
- **Board:** the rows. It scrolls when it is taller than the space.
- **Event log:** a box at the bottom. Default height: 8 event lines plus the prompt line. `l` toggles a tall log (half the screen).
- **Key bar (last line).**

### Rows

- Order: the store's lane order — hosts by first appearance; each main agent followed by its sub-agents, depth first.
- With more than one host, a host header line starts each host group.
- **Full row:** 9 lines: a 7-line card band, the "where" line, one blank line.
  - Agent card: 24 wide, 7 high, at column 0 (main) or column 5 (sub-agent).
  - Current call card: 28 wide, 7 high, one column right of the agent card.
  - History cards: 14 wide, 4 high, bottom-aligned with the call card, one column apart. As many as fit, between 2 and 6 (fewer only below 80 columns of free space). Newest at the left. Older cards are dimmer.
  - "Where" line: under the call card. Shell-prompt style: `<repo>:<folder>/ $ ▌`. Commands show `(shell)`, searches `*`.
- **Collapsed row:** 1 line: `▸ name · provider · state age · last <LABEL> <target> <ok|FAIL|??>`.
- A row is full when the agent has an open call, or had a call end in the last 60 seconds, or is selected, or is focused. Otherwise it is collapsed.
- **Tree lines:** a parent with sub-agents gets a tee (`┳` on a heavy border, `┬` on a light one) in its bottom border at column 2. Each sub-agent row has a gutter at column 2: `│` above, `├──` (or `└──` for the last sub-agent) at its card's middle line, `│` below unless it is the last. A `session` parent link (not `confirmed`) draws the gutter with a dashed vertical (`┆`). Collapsed sub-agent rows keep the gutter.

### Agent card

- Border: heavy (`┏━┓`) for main agents, rounded (`╭─╮`) for sub-agents. Border color: provider color while working; dim otherwise; bright when selected or focused.
- Title: the agent name in the provider color, bold.
- Sprite: an 8 × 8 mirrored bitmap from a hash of the agent ID, drawn as 4 lines of half blocks (`▀ ▄ █`) in the provider color. The same agent always has the same sprite.
- Text column (10 wide): provider (and `sub`), state, `calls N`, `fault N`, `seen <age>`. See "Startup history" for what they count. Longer state words are cut with `…`.
- **State words.** The first matching row wins.

  | Condition | Word | Style |
  |---|---|---|
  | status `interrupted` | `! STOPPED` | red, flashes once |
  | status `finished` | `✓ DONE` | dim |
  | status `idle` | `- IDLE` | dim |
  | the shown open call is an `agent` call, and a sub-agent of this agent is present | `• WAITING` | cyan |
  | an open call, and an event for this agent in the last 2 min | `• WORKING` | green, bold |
  | an open call, no event for 2 min | `◌ STALE` | dim; the call card's spinner stops and its footer says `no events <age>` |
  | status `working`, no open call, an event in the last 2 min | `• THINKING` | cyan |
  | status `working`, no event for 2 min | `◌ STALE` | dim |

  `WAITING` uses only the parent's own open `agent` call and a present child, so it shows no guessed activity. A parent that waits for a sub-agent is not shown as stale.
- **Fault:** a call that ended with `ok` false. A call with `ok: null` is not a fault.

### Call card

- Border: light square (`┌─┐`). Color: the category color while open, dim when ended, red when failed.
- Title: the category label (`READ`, `EDIT`, `WRITE`, `RUN`, `GREP`, `SPAWN`, `TOOL` for `other`), and on the right the elapsed time (`1.4s`, then `2m 03s` past 60 s).
- Line 1, by category: file name (`read`, `edit`, `write`), `$ command` (`command`), the pattern (`search`), the sub-agent name or `sub-agent` (`agent`), the raw tool name (`other`).
- Line 2: the folder of the file, `shell · <repo>`, `grep · <repo>`, `spawn sub-agent`, or empty.
- Lines 3–4: for an open or ended command, the last 2 non-empty output lines, prefixed `> `. For other categories: `started HH:MM:SS`.
- Line 5 (footer):
  - Open: an **elapsed bar** — one filled cell per second, 12 cells, then it restarts — plus a spinner (`| / - \`) and `running`. It shows time passed, never a percent.
  - Ended OK: `[ OK ]`.
  - Failed: `[FAIL]` (inverse red) and `exit N` when there is an exit code.
  - Ended without a result (`ok: null`, for example the collector's 10-minute call timeout): `[ ?? ] no result`, dim.
- Files with more than one path in `files`: the first path, and `+N` after the name.
- **Several open calls** (Claude Code runs tool calls in parallel; an `Agent`/`Task` call stays open for the whole sub-agent run):
  - The card shows the newest open call. The title adds `+N open` for the other open calls.
  - An open call never goes into history. Only ended calls do.
  - When the shown call ends and another call is still open, the ended call moves to history as usual, and the card shows the newest remaining open call without the print animation.
  - When no call is open, the card keeps the last ended call (dim, with its result) until the next call starts.

### History card

- 14 × 4, light border, dim. Title: the category label. Line 1: the target (file name, command, pattern). Line 2: `ok`, `fail`, or `??`, and the duration.

### Event log

- One line per event, newest at the bottom:
  - `tool.end`: `HH:MM:SS  agent  LABEL  target  ok|FAIL|??  duration`.
  - A new sub-agent (a new agent with a parent): `BOOT  sub-agent of <parent>`.
  - Status `interrupted` or `finished`: `STOP` or `DONE`.
  - Host state changes: `HOST  <host> up|down|N sources degraded`.
  - **Unattributed file changes:** `FS  <n> change(s) in <folder>/` in dim style, with no agent name. They are grouped per folder for 2 seconds, and they keep the existing flicker-suppression rule: a change within 2 seconds of a confirmed write to the same path is not shown.
- The log holds the last 500 lines in memory and shows what fits.

### Startup history

The owner chose to show the recent past when the TUI starts while agents already run.

- The collector's first scan reads logs changed in the last 24 hours (the last 4 MiB of each, at most 120 logs). The TUI reads the collector's stream from its first event (phase 1: from the child's first line; phase 2: it subscribes before the collectors start), so it receives the scan events. Each remote sends its own scan on every SSH connect and reconnect.
- **History event:** an event whose skew-corrected time is more than 10 seconds older than the moment it arrives. A history event updates the store, the cards, and the counters. It starts no animation and writes no log line. Every other event is live.
  - Trade-off: a live event that arrives more than 10 seconds late (possible with the Codex log fallback) also does not animate. It still shows on the cards.
- After the first scan of a host, the log writes one line: `HOST  <host> loaded <N> agents, <M> calls from logs`.
- Scan events from different logs arrive out of time order. History cards and the current call card use call start and end times, not arrival order.
- **No double counting:** calls are identified by `(agentId, callId)`. A replayed `tool.start` or `tool.end` for a known call changes nothing. This covers remote reconnects.
- **Counters** (agent card):
  - `calls N`: distinct calls of this agent that the TUI has seen (scan plus live).
  - `fault N`: those of them with `ok` false.
  - `seen <age>`: time since the oldest record of this agent the TUI has seen. It is not the session's true start, because the scan reads only the tail of each log, so the label says `seen`, not `up`.
  - The counters stay correct when the store trims old calls (the per-host call cap), so they are kept apart from the call list.
- No event schema change.

### Coverage panel and help

- `c` opens a centered box over the board with the current coverage content (log roots, open and discovered logs, watched roots, malformed records, dropped file-system events, degraded sources) and the diagnostics buffer. `c` or `Esc` closes it.
- `?` opens a box with all keys.

## Interaction

| Key | Action |
|---|---|
| `j` / `↓`, `k` / `↑` | Move the selection between rows. The board scrolls to keep it visible. |
| `g` / `G` | First / last row. |
| `Enter`, or a mouse click on an agent card | Focus the selected (or clicked) agent. Enter again clears it. |
| `Esc` | Clear the focus, close a panel. |
| `Space` | Pause: the board and log stop changing. The store keeps applying events. Resume shows the live state. |
| `l` | Toggle the tall log. |
| `c` | Coverage panel. |
| `?` | Help. |
| `q`, `Ctrl-C` | Quit. |

- **Focus:** the other rows are drawn dim. The focused agent's call card grows to show up to 10 output lines for a command (this replaces the drawer's live command list). The event log shows only that agent's lines (host lines stay).
- With no selection, the board does not scroll by itself.

## Animation

All animation moves in whole cells. A frame draws at most every 50 ms (20 fps), and only while something animates. With nothing animating and no open call, the TUI redraws only on a change. The spinner and the elapsed bar of open calls tick at 8 frames per second.

| Change | Animation |
|---|---|
| A call starts | The new call card prints top to bottom, one line per frame (7 frames). |
| The previous call card leaves | It collapses bottom to top (4 frames) while a history card prints at the head of the history. |
| History cards move | They move right in steps of 3 columns per frame until they reach their place. The oldest card past the limit disappears. |
| A call fails | The call card flashes inverse red 3 times (6 frames). |
| A sub-agent appears | The gutter draws downward, then its row prints. |
| The "where" line changes | It types out left to right over 6 frames. |
| A row collapses or expands | Its height changes one line per frame. |

- `--reduced-motion`: every change is instant. Spinners still turn.
- Bursts: events that arrive between two frames are applied together. If more than 3 calls of one agent start and end between two frames, only the newest call animates; the others go straight into history and the log.

## Color

- Palette from the prototype (One Dark style): text `#c5c8c6`, dim `#5c6370`, faint `#3a3f4b`, bright `#ffffff`; categories: `read` blue `#61afef`, `edit`/`write` magenta `#c678dd`, `command` green `#98c379`, `search` yellow `#e5c07b`, `agent` cyan `#56b6c2`, `other` text; providers: Claude orange `#e5915a`, Codex cyan `#56b6c2`; OK green; FAIL red `#e06c75`.
- Color depth follows the terminal (Lip Gloss v2 detects truecolor, 256, and 16 colors and degrades). `NO_COLOR` turns color off.
- The background is the terminal's own background. The TUI does not paint it.

## Text safety

- Every string from agent data (names, paths, commands, patterns, output, host labels, diagnostics) goes through one sanitizer before it reaches the cell grid:
  - Remove ANSI escape sequences, C0 controls (a tab becomes one space), DEL, C1 controls (`U+0080`–`U+009F`), and bidirectional override and isolate characters (`U+202A`–`U+202E`, `U+2066`–`U+2069`).
  - Replace newlines inside one-line fields with `⏎`.
  - Replace invalid UTF-8 with `U+FFFD`.
- Cell width comes from one width function (the one Lip Gloss uses). A wide character (CJK, emoji) takes 2 cells. A wide character that does not fit at the end of a field becomes `…`.
- Only text from the cell grid and the library's own control sequences reach the terminal.

## Performance targets

- Binary: under 20 MB for each target (`-trimpath`, `-ldflags "-s -w"`).
- With 50 agents and 200 events per second in bursts: building the board and the grid takes under 4 ms per frame.
- With agents idle: the whole process (collector, hub, TUI) stays under 2% of one core. Phase 1 measures the Go process apart from the TypeScript child.
- Event-to-screen latency under 1 second, as before.
- Memory: under 150 MB with the 24-hour scan of 120 logs.

## Testing

- **Cell model:** golden-file tests for each card function (grid as text plus a style mask; an `-update` flag rewrites them). Cases: every category, open / OK / failed / no result, several open calls, long names (truncation with `…`), wide characters, multi-file calls.
- **Layout:** history card count for widths 80, 120, 200; collapsed vs full rows; tree gutters for 1, 2, and 3 sub-agents, and for a `session` link; scrolling keeps the selection visible.
- **Sanitizer:** escape sequences, C1, bidi overrides, newlines, invalid UTF-8.
- **Animation:** a fake clock steps frames; tests check the grid after N frames for print, collapse, shift, and flash; `--reduced-motion` is instant.
- **Program:** Bubble Tea model tests that send key, mouse, resize, and event messages and check the view; a fixed-size run on the demo stream with snapshots of chosen frames; quit restores the terminal.
- **Store:** the 421 lines of `test/viewer/store.test.ts` ported, plus startup history, double counting, and counters past the call cap.
- **Phase 1 integration:** the Go binary with `AGENT_VIEW_COLLECTOR` pointing at a fake collector that writes recorded NDJSON; child exit and restart with backoff.
- **Phase 2 parity:** every TypeScript test for the collector, hub, remote, schema, lifecycle, and NDJSON is ported. A parity test runs the TypeScript collector and the Go collector on the same fixture logs and demo seed and compares the normalized envelope streams (times and sequence numbers removed). It must match before the TypeScript code is deleted. The golden fixtures in `test/fixtures/` move to `testdata/`.
- **File watching (phase 2 spike):** the M0 scenarios from the removed M0 results (`git show b553ff5:docs/superpowers/spikes/2026-11-17-m0-results.md`) against FSEvents and kqueue on macOS and inotify on Linux.
- **CLI:** argument parsing, `--port` only with `serve`, `--remote` rejected in phase 1, non-TTY exit code 2.

## Milestones

- **P1 — Go TUI.** Go module, schema types, NDJSON decoder, store port, cell model and cards, board and layout, animation, Bubble Tea program, child collector with backoff, CLI, tests. The owner uses it daily against real data. The TypeScript code does not change.
- **P2a — Spike.** File watching on macOS (FSEvents vs kqueue) and Linux (inotify). Short written result.
- **P2b — Port.** Collector, sources, discovery, tail, state, demo, hub, remote, `collect --stdio`, `serve`, `audit`. Parity test green.
- **P2c — Switch.** In-process collector in the TUI, `--remote` on, build script for the 4 targets, size check, README rewrite, delete the TypeScript, mark superseded sections in the 2026-11-17 spec.

## Decisions made by Claude (owner to check)

1. Phase 1 runs the TypeScript collector as a child process (stdout NDJSON); `AGENT_VIEW_COLLECTOR` overrides the command.
2. Phase 1 is local only; `--remote` comes in phase 2.
3. `agent-view serve` keeps only `GET /api/snapshot`; `/ws` is dropped; the live audit becomes `agent-view audit`.
4. Non-TTY start exits with code 2 and names `serve`.
5. Idle agents collapse to one line; the rule for "full" is in "Rows".
6. State words add `THINKING` and `WAITING`, with a fixed order against `STALE`.
7. An elapsed bar (one cell per second) replaces the progress bar.
8. Unattributed file changes appear only as grouped `FS` lines in the log.
9. Focus shows up to 10 output lines in the call card (replaces the drawer).
10. Colors degrade with the terminal; `NO_COLOR` respected; green and amber themes deferred.
11. Several open calls: the card shows the newest, with `+N open`; open calls never go into history.
12. `ok: null` shows as `??` and is not a fault.
13. Startup history: an event more than 10 seconds old on arrival is history (no animation, no log line); the age counter is called `seen`.
14. Sprites come from the agent ID. Minimum terminal 80 × 24.
15. Targets: binary under 20 MB, memory under 150 MB.

## Left to the builder

- Package layout (`cmd/agent-view`, `internal/...`) and the module path.
- How the cell grid becomes a Bubble Tea frame (Lip Gloss layers or one styled string).
- Frame scheduling details within the stated limits.
- Exact text of the help and "too small" screens.
- The golden-file format.
- The build script form (Makefile, `go run` script, or GoReleaser).
