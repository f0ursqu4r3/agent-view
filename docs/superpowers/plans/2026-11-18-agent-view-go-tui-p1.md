# Agent View Go TUI — P1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go `agent-view` binary that runs the existing TypeScript collector as a child process and shows its events as the animated terminal card table.

**Architecture:** A feed goroutine runs `bun src/cli.ts collect --stdio`, decodes NDJSON envelopes, and sends them in batches to a Bubble Tea v2 program. A Go port of the viewer store holds agent and call state and reports changes. Pure functions build cards into a cell grid; an animation engine steps frames; a composer joins the board, log, status bar, and key bar into one grid, which the view encodes as styled text.

**Tech Stack:** Go 1.27, `charm.land/bubbletea/v2` v2.1.2, `charm.land/lipgloss/v2` v2.1.1 (width only), `github.com/charmbracelet/x/ansi` (escape stripping), `github.com/rivo/uniseg` (graphemes). All already in Bubble Tea's dependency graph.

**Spec:** `docs/superpowers/specs/2026-11-18-agent-view-tui-design.md` (approved 2026-11-18). Read it first; this plan points to its sections instead of repeating them.

## Global Constraints

- Module path `agent-view`, `go 1.27`, one module at the repository root. TypeScript files are not changed in P1.
- No dependency beyond those in Tech Stack.
- Card sizes: agent card 24 × 7, call card 28 × 7, history card 14 × 4, sub-agent indent 5, full row 9 lines, collapsed row 1 line, history cards 0–6.
- Minimum terminal 80 × 24; smaller shows the "too small" screen.
- Frames at most every 50 ms and only while something animates; spinner and elapsed bar tick at 8 per second (125 ms) only while a call is open; the clock ticks once per second.
- History event: skew-corrected event time more than 10 s older than its arrival.
- Store constants (from `viewer/store.ts`): flicker hold 2 s, output kept 16 KiB, 2,000 calls per host, 16 recent writes per path, 4,000 recent paths, 10,000 held unattributed touches, sweep at most once per second, drop after 2 lingers. New: dormant agent cap 10,000.
- Lifecycle defaults (from `src/shared/lifecycle.ts`): linger 10 min, stale 2 min, finished fade 1 min, file TTL 30 min.
- Palette and category labels: spec "Color" and "Call card". Labels: `read` READ, `edit` EDIT, `write` WRITE, `command` RUN, `search` GREP, `agent` SPAWN, `other` TOOL.
- Nothing writes to stdout or stderr while the program runs. `log` output goes to the diagnostics buffer.
- Every string from agent data passes `text.Clean` before it reaches a grid.
- Commit messages: Conventional Commits, no AI attribution lines. Work on a branch, not `main`.

## Review Focus

1. **The collector command is missing or dies** (no `bun`, not run from the repo root, a crash): the TUI must start, show the host `down` with the reason in the coverage panel, retry with backoff, and quit cleanly. Test in Task 12.
2. **A huge NDJSON line** (a snapshot of hundreds of agents, over 64 KiB): it must decode, not stall the reader. Test in Task 12.
3. **Hostile or odd text** (escape codes, bidi overrides, emoji, CJK, invalid UTF-8 in file names and output): no escape reaches the terminal, and card borders stay aligned. Tests in Tasks 2 and 6.
4. **A small or resized terminal** (79 × 23, resize during an animation, 0 × 0 before the first size message): no panic, the "too small" screen, then a correct full redraw. Test in Task 11.
5. **A stress burst** (`--demo-scale stress`, hundreds of events per batch): one frame stays under 4 ms for 50 agents, and animations do not pile up (the burst rule). Tests in Tasks 9 and 14.

---

### Task 1: Module, schema types, and NDJSON decoding

**Files:**
- Create: `go.mod`, `internal/schema/schema.go`, `internal/schema/decode.go`
- Test: `internal/schema/decode_test.go`

**Interfaces:**
- Produces:
  - `type Agent struct { ID, Provider, Name, Role string; ParentID *string; Link string; Cwd, ProjectRoot *string; Model string; Status, LastEventAt string }` with the JSON names of `src/shared/schema.ts` (`id`, `parentId`, `projectRoot`, `lastEventAt`, …).
  - `type Call struct { AgentID, CallID, StartedAt, Category, RawName string; Files []string; Command, Pattern string }`.
  - `type FileTouch struct { Path, LastTouchAt, Attribution, AgentID string }`, `type Write struct { Path, Op string }`, `type SourceState struct { State, Detail string }`, `type Coverage struct { LogRoots []string; LogsOpen, LogsDiscovered int; WatchRoots []string; MalformedRecords, FSDropped int; Sources map[string]SourceState }`.
  - `type Envelope struct` with `V int; Host string; Seq int64; Ts, Kind string` and every kind's fields: `Agent *Agent; AgentID, Status string; Call *Call; CallID, Chunk string; Truncated bool; Ok *bool; ExitCode *int; Writes []Write; Path, Op string; Agents []Agent; OpenCalls []Call; Coverage *Coverage; Source, State, Detail, Now, CollectorVersion, Platform string; Demo bool; RawFiles json.RawMessage` (`json:"files"`).
  - `func (e Envelope) UpdateFiles() []string` (for `tool.update`) and `func (e Envelope) SnapshotFiles() []FileTouch` (for `snapshot`); both return nil on a shape mismatch.
  - `func DecodeLine(line []byte) (Envelope, bool)` — false for invalid JSON, a non-object, or `v != 1`.
  - `func Label(category string) string` — the labels in Global Constraints; unknown → `TOOL`.

- [ ] **Step 1: Create the branch and module.** `git switch -c go-tui-p1`; `go mod init agent-view`; set `go 1.27`.
- [ ] **Step 2: Write the failing tests**

```go
func TestDecodeLineAcceptsEveryKind(t *testing.T)        // one line per kind from test/fixtures/golden/session.ndjson, wrapped with "v":1,"host":"h","seq":n; Kind and key fields decoded
func TestDecodeLineRejects(t *testing.T)                 // `not json`, `[]`, `{"v":2,"kind":"hello"}`, `{"kind":"hello"}` → ok == false
func TestToolEndOkTriState(t *testing.T)                 // "ok":true → *true, "ok":false → *false, "ok":null → nil
func TestFilesDependsOnKind(t *testing.T)                // tool.update files → UpdateFiles() == ["/a"]; snapshot files → SnapshotFiles()[0].Attribution == "confirmed"
func TestLabel(t *testing.T)                             // Label("search") == "GREP"; Label("x") == "TOOL"
```

- [ ] **Step 3: Run** `go test ./internal/schema/` — expect FAIL (undefined).
- [ ] **Step 4: Implement** the types and functions. Unknown kinds decode without error (the store ignores them).
- [ ] **Step 5: Run** `go test ./internal/schema/` — expect PASS.
- [ ] **Step 6: Commit** `feat(go): add schema types and NDJSON decoding`

---

### Task 2: Text cleaning and the cell grid

**Files:**
- Create: `internal/text/clean.go`, `internal/cells/grid.go`, `internal/cells/encode.go`, `internal/cells/palette.go`
- Test: `internal/text/clean_test.go`, `internal/cells/grid_test.go`, `internal/cells/encode_test.go`

**Interfaces:**
- Produces:
  - `func text.Clean(s string) string` — one-line field: `ansi.Strip`, then remove C0 (tab → one space), DEL, C1 `U+0080`–`U+009F`, `U+202A`–`U+202E`, `U+2066`–`U+2069`; newline → `⏎`; `strings.ToValidUTF8(s, "�")` first.
  - `func text.CleanLines(s string) []string` — the same per line; empty lines removed (used for command output).
  - `type Role uint8` with `RText, RDim, RFaint, RBright, RRead, REdit, RCommand, RSearch, RAgent, RClaude, RCodex, ROK, RFail`; `func RoleOf(category string) Role`.
  - `type Style struct { Role Role; Bold, Inverse bool }`; `type Cell struct { G string; W uint8; S Style }` (`W` 0 marks the right half of a wide grapheme).
  - `type Grid struct { W, H int; C []Cell }`; `func New(w, h int) *Grid` (spaces, `RText`).
  - `func (g *Grid) Text(x, y, max int, s string, st Style) int` — writes graphemes (`uniseg`) up to `max` cells; if `s` is wider, the last written cell is `…`; a wide grapheme that does not fit becomes `…`; returns cells written. Out-of-bounds writes are clipped, never panic.
  - `func (g *Grid) Box(x, y, w, h int, kind BoxKind, st Style)` with `BoxKind` `Heavy` (`┏━┓┃┗┛`), `Round` (`╭─╮│╰╯`), `Square` (`┌─┐│└┘`).
  - `func (g *Grid) Blit(src *Grid, x, y int)`, `func (g *Grid) ClipRows(top, n int)` (keep only rows `top..top+n-1` of a card, for print and collapse), `func (g *Grid) Dim()` (every role → `RDim`/`RFaint`, keeps `RFail`).
  - `func Plain(g *Grid) string` and `func Mask(g *Grid) string` — one line per row; mask letters `t d f b r e c s a o x k F` for the roles in order, uppercase when bold, `#` when inverse.
  - `func Encode(g *Grid) string` — rows joined by `\n`, SGR runs: `38;2;R;G;B` for the role's color from `palette.go` (spec "Color"), `1` bold, `7` inverse, reset `0` at style change and line end. Bubble Tea downsamples the colors to the terminal profile and honors `NO_COLOR`.

- [ ] **Step 1: Write the failing tests**

```go
func TestCleanRemovesEscapesAndControls(t *testing.T) // "a\x1b[31mb\x1b]0;title\x07c" → "abc"; "x‮y" → "xy"; "\u009b2J" → "2J"; "a\tb" → "a b"; "l1\nl2" → "l1⏎l2"; "\xff" → "�"
func TestCleanLinesDropsEmpty(t *testing.T)           // "one\n\n\x1b[2Ktwo\n" → ["one","two"]
func TestTextTruncatesWithEllipsis(t *testing.T)      // max 5, "abcdefgh" → Plain row "abcd…"
func TestTextWideGraphemes(t *testing.T)              // max 5, "日本語" → "日本…" (2+2+1 cells); max 4, "a日本" → "a日…" (1+2+1 cells); the right half of "日" has W == 0
func TestTextClipsOutOfBounds(t *testing.T)           // x = -3 and x = W+2 do not panic
func TestBoxKinds(t *testing.T)                       // 4×3 Heavy → "┏━━┓","┃  ┃","┗━━┛"
func TestMaskLetters(t *testing.T)                    // RRead bold → "R"; RFail inverse → "#"
func TestEncodeRunsAndResets(t *testing.T)            // two cells of one style emit one SGR; output contains no byte < 0x20 except ESC and '\n'
```

- [ ] **Step 2: Run** `go test ./internal/text/ ./internal/cells/` — expect FAIL.
- [ ] **Step 3: Implement** the functions. `go get charm.land/lipgloss/v2@v2.1.1 github.com/charmbracelet/x/ansi github.com/rivo/uniseg`.
- [ ] **Step 4: Run** the tests — expect PASS.
- [ ] **Step 5: Commit** `feat(go): add text cleaning and cell grid`

---

### Task 3: Store port — core

**Files:**
- Create: `internal/store/lifecycle.go`, `internal/store/store.go`
- Test: `internal/store/store_test.go`

Port `viewer/store.ts` and the parts of `src/shared/lifecycle.ts` it uses, with times in Unix milliseconds (`int64`). Read both TypeScript files; keep their behavior except where this task says otherwise.

**Interfaces:**
- Consumes: `schema.Envelope`, `schema.Agent`, `schema.Call`, `schema.Coverage`.
- Produces:
  - `type Options struct { Now func() int64; LingerMs, StaleMs, FinishedFadeMs, FileTTLMs int64 }` (zero → lifecycle default).
  - `type ViewAgent struct { schema.Agent; Host string; LastEventMs int64; Stale bool }`.
  - `type ViewCall struct { schema.Call; Host string; StartMs, EndMs int64; Ended bool; Ok *bool; ExitCode *int; Output string; OutputTruncated bool }`.
  - `func New(o Options) *Store`; `func (s *Store) Apply(e schema.Envelope) []Change` (Task 4 fills `Change`; return nil here).
  - `func (s *Store) Agents(now int64) []ViewAgent`, `func (s *Store) LaneOrder(now int64) []string`, `func (s *Store) Agent(id string) (ViewAgent, bool)`, `func (s *Store) Hosts() []string` (first-appearance order), `func (s *Store) Coverage(host string) (schema.Coverage, bool)`, `func (s *Store) IsDemo() bool`.
- Not ported: `calls(window)`, `commands()`, the confirmed-touch queue, `takeResnapshotRequest`, `setHosts` (linger comes from `Options`).
- **Changed — dormant agents:** the sweep moves a dropped agent's record (agent, order rank, counters) into a dormant map (cap 10,000, oldest out) instead of forgetting it. An event for a dormant agent revives it with its record. An event for an agent never seen is dropped and counted in `s.UnknownEvents` (the stdio feed has no resnapshot path).

- [ ] **Step 1: Write the failing tests** — port these tests from `test/viewer/store.test.ts` with the same names and assertions: "snapshot replaces that host state…", "tool lifecycle…", "agents() excludes agents past linger…", "Review Focus 2: a host whose hello.now is 5 minutes behind…" (presence part), "laneOrder nests sub-agents…", "hosts, coverage, demo flag…" (no focus), "keeps at most 2000 calls per host…", "ignores unknown and malformed envelopes…", "I1: agents idle for over 2x linger are dropped…", "I1: repeated reads with the same now…", "tool.end keeps the exit code…". Replace "R25" with:

```go
func TestDormantAgentRevives(t *testing.T)   // agent idle 2×linger+1 s → not in Agents(); then tool.start for it → back in Agents() with its old order rank
func TestUnknownAgentEventDropped(t *testing.T) // tool.start for never-seen "zz" → no agent, UnknownEvents == 1
```

- [ ] **Step 2: Run** `go test ./internal/store/` — expect FAIL.
- [ ] **Step 3: Implement** the port.
- [ ] **Step 4: Run** `go test ./internal/store/` — expect PASS.
- [ ] **Step 5: Commit** `feat(go): port viewer store core`

---

### Task 4: Store — changes, history, counters, calls, unattributed touches

**Files:**
- Modify: `internal/store/store.go`
- Create: `internal/store/changes.go`
- Test: `internal/store/changes_test.go`

**Interfaces:**
- Produces:
  - `type ChangeKind uint8` with `CallStarted, CallEnded, AgentAdded, AgentStatus, CallOutput`.
  - `type Change struct { Kind ChangeKind; Host, AgentID, CallID, Status string; AtMs int64; History bool }` — `History` per Global Constraints, using `Options.Now` as arrival time.
  - `Apply` returns one `Change` per effect: a new call key → `CallStarted`; first end of a call → `CallEnded`; a new agent (not a revive) → `AgentAdded`; a status change → `AgentStatus`; output → `CallOutput`. A replayed `tool.start` or `tool.end` for a known `(agentId, callId)` returns nothing and changes nothing (this replaces the TypeScript "restarted call" behavior).
  - `type Counters struct { Calls, Faults int; FirstSeenMs int64 }`; `func (s *Store) Counters(agentID string) Counters` — distinct calls; faults are ends with `ok` false; first seen is the oldest record time of the agent; kept apart from the call list so trimming does not lower them; carried into dormant records.
  - `func (s *Store) OpenCalls(agentID string) []*ViewCall` (newest start first), `func (s *Store) RecentEnded(agentID string, n int) []*ViewCall` (newest end first, by `EndMs`, not arrival).
  - `func (s *Store) HostStats(host string) (agents, calls int)`.
  - `type Touch struct { Host, Path string; AtMs int64 }`; `func (s *Store) TakeUnattributed(now int64) []Touch` — the TypeScript `takeTouches` rules for unattributed touches only: held 2 s, dropped if a confirmed **write** of the same path is within 2 s before or after, dropped when older than the file TTL.

- [ ] **Step 1: Write the failing tests**

```go
func TestChangesForLifecycle(t *testing.T)        // upsert → AgentAdded; tool.start → CallStarted; tool.end ok:false → CallEnded; agent.status → AgentStatus
func TestHistoryFlag(t *testing.T)                // now = T; event ts T-11s → History true; ts T-9s → false; host offset from hello applied
func TestReplayIsIgnored(t *testing.T)            // same tool.start twice and same tool.end twice → second Apply returns no changes; Counters.Calls == 1
func TestCountersSurviveTrim(t *testing.T)        // 2,100 ended calls → Counters.Calls == 2100 while RecentEnded holds ≤ 2000
func TestFaultsCountOnlyFalse(t *testing.T)       // ends ok:false, ok:null, ok:true → Faults == 1
func TestRecentEndedOrdersByEndTime(t *testing.T) // ends arrive c2 (end 5s) then c1 (end 9s) → RecentEnded = [c1, c2]
func TestOpenCallsNewestFirst(t *testing.T)       // three open calls → newest StartMs first
func TestUnattributedRules(t *testing.T)          // port the three TS tests: held 2 s; suppressed by a write up to 2 s before or after; reads, call starts, and snapshot seeds do not suppress; older than TTL dropped
```

- [ ] **Step 2: Run** `go test ./internal/store/` — expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/store/` — expect PASS.
- [ ] **Step 5: Commit** `feat(go): add store changes, history, and counters`

---

### Task 5: Agent card, sprite, and state words

**Files:**
- Create: `internal/cards/sprite.go`, `internal/cards/state.go`, `internal/cards/agent.go`, `internal/cards/golden_test.go` (shared `-update` helper), `testdata/cards/agent_*.golden`
- Test: `internal/cards/agent_test.go`, `internal/cards/state_test.go`

**Interfaces:**
- Consumes: `store.ViewAgent`, `store.ViewCall`, `store.Counters`, `cells.*`.
- Produces:
  - `func Sprite(agentID string) [4]string` — FNV-1a 32 of the ID seeds a mulberry32 generator; an 8 × 8 mirrored bitmap with the prototype's rules (`term-cards.html`, `sprite()`); rows pair into half blocks.
  - `type StateWord struct { Text string; Style cells.Style; Flash bool }`; `func State(a store.ViewAgent, open []*store.ViewCall, hasPresentChild bool, now, staleMs int64) StateWord` — the table in spec "Agent card", first match wins.
  - `type AgentIn struct { Agent store.ViewAgent; State StateWord; Counters store.Counters; Now int64; HasSubs, Selected, Focused bool }`; `func AgentCard(in AgentIn) *cells.Grid` (24 × 7) per spec "Agent card"; the tee at column 2 when `HasSubs`; `seen` age formatted `42s`, `31m`, `2.5h`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSpriteStable(t *testing.T)            // Sprite("x") == Sprite("x"); Sprite("x") != Sprite("y"); each row 8 cells of " ▀▄█"
func TestStateOrder(t *testing.T)              // table-driven, one case per spec row, plus: interrupted with open call → STOPPED; open agent call + present child + no event 5 min → WAITING
func TestAgentCardGolden(t *testing.T)         // cases: main working, sub idle, has subs (tee), selected, long name "Data-load review of the API" (…); compares Plain+Mask with testdata/cards/agent_<case>.golden
```

- [ ] **Step 2: Run** `go test ./internal/cards/` — expect FAIL.
- [ ] **Step 3: Implement**; generate goldens with `go test ./internal/cards/ -update`, then read every golden file and check it against the spec layout by eye.
- [ ] **Step 4: Run** `go test ./internal/cards/` — expect PASS.
- [ ] **Step 5: Commit** `feat(go): add agent card, sprite, and state words`

---

### Task 6: Call card, history card, collapsed row

**Files:**
- Create: `internal/cards/call.go`, `internal/cards/history.go`, `internal/cards/collapsed.go`, `testdata/cards/call_*.golden`, `testdata/cards/history_*.golden`
- Test: `internal/cards/call_test.go`

**Interfaces:**
- Produces:
  - `func Where(projectRoot string, c *store.ViewCall) (repo, folder string)` — `repo` is the base name of the project root (or of the agent's `cwd` when there is no root); `folder` is the first file's folder relative to the project root with a trailing `/` (`./` at the root), `(shell)` for commands, `*` for searches, empty for others. Used for call card line 2 and the "where" line.
  - `type CallIn struct { Call *store.ViewCall; ProjectRoot string; OtherOpen int; Now int64; Spin int; Stale bool; LastEventMs int64; OutputLines int }`; `func CallCard(in CallIn) *cells.Grid` — 28 wide; height 7, or `5 + OutputLines` when `OutputLines` > 2 (focus shows up to 10). Content per spec "Call card". Elapsed: `1.4s` under 60 s, then `2m 03s`. Elapsed bar: 12 cells, `(elapsed seconds % 12) + 1` filled. Spinner frames `| / - \` by `Spin % 4`. `ok` nil and ended → `[ ?? ] no result`. `Stale` → spinner stops, footer `no events <age>`. Command output: `text.CleanLines(Output)`, last N lines, prefixed `> `.
  - `func HistoryCard(c *store.ViewCall) *cells.Grid` — 14 × 4 per spec.
  - `func CollapsedRow(width int, a store.ViewAgent, st StateWord, last *store.ViewCall, now int64) *cells.Grid` — 1 line per spec "Rows".
  - `func Target(c *store.ViewCall) string` — the line-1 text without decoration (used by history cards and the log): file base name (+`+N`), `$ command`, pattern, sub-agent name, raw name.

- [ ] **Step 1: Write the failing tests**

```go
func TestCallCardGolden(t *testing.T) // cases: read open 1.4s spin 0; edit ended ok; command open with 3 output lines (shows last 2); command failed exit 2; ok nil ended; stale open; agent call with OtherOpen 2 ("+2 open"); search; other; files ["/a/x.ts","/a/y.ts"] ("x.ts +1"); name "\x1b[31m日本語-file-name-that-is-long.ts" (clean + wide + …); focused with OutputLines 10
func TestElapsedFormat(t *testing.T)  // 1400 ms → "1.4s"; 123000 ms → "2m 03s"
func TestHistoryCardGolden(t *testing.T) // ok, fail, ??
func TestCollapsedRow(t *testing.T)   // Plain == "▸ eos-api · codex · - IDLE 4m · last READ users.ts ok" padded to width
```

- [ ] **Step 2: Run** `go test ./internal/cards/` — expect FAIL.
- [ ] **Step 3: Implement**; generate and review goldens as in Task 5.
- [ ] **Step 4: Run** `go test ./internal/cards/` — expect PASS.
- [ ] **Step 5: Commit** `feat(go): add call, history, and collapsed cards`

---

### Task 7: Board layout

**Files:**
- Create: `internal/board/layout.go`
- Test: `internal/board/layout_test.go`

**Interfaces:**
- Consumes: `store.Store` (`LaneOrder`, `Agent`, `OpenCalls`, `RecentEnded`).
- Produces:
  - `type UI struct { Selected, Focus string; Paused, TallLog, Reduced bool; Panel Panel; Top int }` with `Panel` `PanelNone, PanelCoverage, PanelHelp`.
  - `type Row struct { AgentID, Host, HostHeader string; Sub, LastSib, SessionLink, Full bool; Y, H int }` — `HostHeader` non-empty on the first row of a host group when there is more than one host; `H` is 9 or 1 (plus 1 for a host header).
  - `func Rows(s *store.Store, ui UI, now int64) []Row` — order and the full/collapsed rule per spec "Rows" (open call, a call ended in the last 60 s, selected, or focused).
  - `func HistoryCount(width int, sub bool) int` — `min(6, max(0, (width - x + 1) / 15))` with `x = indent + 54`.
  - `func Scroll(rows []Row, height, top int, selected string) int` — the new top that keeps the selected row fully visible; unchanged when nothing is selected.
  - `func Gutter(rows []Row, i int) []string` — the gutter strings for row `i` (spec "Tree lines"; `┆` for a session link).

- [ ] **Step 1: Write the failing tests**

```go
func TestRowsFullRule(t *testing.T)       // open call → Full; ended 30 s ago → Full; ended 90 s ago → collapsed; selected → Full
func TestRowsHostHeaders(t *testing.T)    // two hosts → two headers; one host → none
func TestHistoryCount(t *testing.T)       // (80,false) → 1; (120,false) → 4; (200,false) → 6; (80,true) → 1; (58,false) → 0
func TestScrollKeepsSelection(t *testing.T) // 10 full rows, height 30, select row 7 → top shows rows 5–7 fully; no selection → top unchanged
func TestGutters(t *testing.T)            // 1, 2, 3 subs: middle line "├──", last "└──", nothing below the last; session link uses "┆"
```

- [ ] **Step 2: Run** `go test ./internal/board/` — expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/board/` — expect PASS.
- [ ] **Step 5: Commit** `feat(go): add board layout`

---

### Task 8: Event log model

**Files:**
- Create: `internal/board/log.go`
- Test: `internal/board/log_test.go`

**Interfaces:**
- Consumes: `store.Change`, `store.Touch`, `store.Store`, `cards.Target`.
- Produces:
  - `type LogLine struct { AtMs int64; AgentID, Who string; WhoRole cells.Role; Label string; LabelRole cells.Role; Target, Result string; DurMs int64; Dim bool }`.
  - `type Log struct` (ring of 500); `func (l *Log) Add(ln LogLine)`; `func (l *Log) FromChange(s *store.Store, c store.Change)` — skips `History` changes; `CallEnded` → tool line (`ok`, `FAIL`, `??`); `AgentAdded` with a parent → `BOOT  sub-agent of <parent name>`; `AgentStatus` interrupted/finished → `STOP`/`DONE`.
  - `func (l *Log) Host(atMs int64, host, text string)` — `HOST` lines (feed up/down, sources degraded, and `loaded N agents, M calls from logs`).
  - `func (l *Log) AddTouches(t []store.Touch)` — groups by folder per 2-second bucket into dim `FS  <n> change(s) in <folder>/` lines.
  - `func (l *Log) Render(w, h int, focus string) *cells.Grid` — the box from spec "Event log"; columns time 8, who 16, label 6, target (flexible), result 4, duration 6; the prompt line `$ ▌`; with `focus` set, only that agent's lines and `HOST` lines.

- [ ] **Step 1: Write the failing tests**

```go
func TestLogSkipsHistory(t *testing.T)     // History CallEnded → no line
func TestLogToolLine(t *testing.T)         // failed command → Plain contains "RUN" and "FAIL"; ok nil → "??"
func TestLogBootLine(t *testing.T)         // sub-agent AgentAdded → "BOOT" and "sub-agent of agent-view"
func TestLogGroupsTouches(t *testing.T)    // 3 touches in /p/src within 2 s + 1 in /p/test → "FS  3 change(s) in /p/src/" and "FS  1 change(s) in /p/test/"
func TestLogRingAndFocus(t *testing.T)     // 600 adds keep 500; focus "a" hides "b" lines but keeps HOST lines
```

- [ ] **Step 2: Run** `go test ./internal/board/` — expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/board/` — expect PASS.
- [ ] **Step 5: Commit** `feat(go): add event log model`

---

### Task 9: Animation engine

**Files:**
- Create: `internal/anim/anim.go`
- Test: `internal/anim/anim_test.go`

**Interfaces:**
- Consumes: `store.Change`.
- Produces:
  - `type Engine struct`; `func New(reduced bool) *Engine`.
  - `func (e *Engine) OnChange(c store.Change, shownCallChanged bool)` — ignores `History`; starts the spec "Animation" effects: call print (7 frames), leaving card collapse (4 frames) + history print, history shift (3 columns per frame), fail flash (6 frames), sub-agent gutter then row print, "where" type-out (6 frames), row height change (1 line per frame). Burst rule: more than 3 `CallStarted` for one agent since the last `Step` → only the newest animates.
  - `func (e *Engine) Step()` (one 50 ms frame), `func (e *Engine) Active() bool`.
  - `type AgentFrame struct { CallRows, LeavingRows, HistPrintRows, WhereChars, GutterRows, RowH int; Flash bool; HistOffset []int }`; `func (e *Engine) Frame(agentID string, targetRowH, histCount int) AgentFrame` — values the composer applies; "done" values (full rows, zero offsets) when nothing animates. With `reduced`, every `Frame` is done and `Active` is false.

- [ ] **Step 1: Write the failing tests**

```go
func TestCallPrintsOneLinePerFrame(t *testing.T)  // CallStarted → CallRows 0; Step×3 → 3; Step×4 more → 7; Active false
func TestFailFlashes3Times(t *testing.T)          // CallEnded with fail → Flash pattern over 6 frames = on,off,on,off,on,off
func TestHistoryShiftsBy3(t *testing.T)           // new history card → HistOffset[1] starts at -15, then -12, -9, …, 0
func TestHistoryChangesDoNotAnimate(t *testing.T) // History change → Active false
func TestBurstAnimatesOnlyNewest(t *testing.T)    // 5 CallStarted for one agent before Step → one print sequence
func TestReducedMotionIsInstant(t *testing.T)     // New(true): any change → Frame done, Active false
```

- [ ] **Step 2: Run** `go test ./internal/anim/` — expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/anim/` — expect PASS.
- [ ] **Step 5: Commit** `feat(go): add animation engine`

---

### Task 10: Frame composer

**Files:**
- Create: `internal/tui/compose.go`, `internal/tui/panels.go`
- Test: `internal/tui/compose_test.go`

**Interfaces:**
- Consumes: Tasks 5–9.
- Produces:
  - `type Status struct { Hosts []HostStatus; Agents, Working, CallsPerMin, Faults int; Paused bool; Clock string }`, `type HostStatus struct { Label, State string; Degraded int }`.
  - `type Input struct { Store *store.Store; UI board.UI; Anim *anim.Engine; Log *board.Log; Status Status; Now int64; Spin int; W, H int; Diagnostics []string }`.
  - `func Compose(in Input) (*cells.Grid, []Hit)` — line 1 status bar (spec "Status bar"), the board (rows from `board.Rows`, scrolled by `UI.Top`, cards from Tasks 5–6 with `AgentFrame` applied via `ClipRows`, offsets, flash inverse, dimming when focus is set), the log (8 lines + prompt, or half the screen with `TallLog`), the key bar. Below 80 × 24: only the "too small" screen. No agents: one dim line `waiting for agents…` on the board.
  - "Where" line per row: `<repo>:<folder> $ ▌` from `cards.Where`, cut to `AgentFrame.WhereChars` while it types out.
  - Call card choice per row: `OpenCalls(id)[0]` if any (with `OtherOpen = len - 1`), else `RecentEnded(id, 1)[0]`, else an empty dim card. History cards: `RecentEnded` minus the call on the card, `HistoryCount` of them. `hasPresentChild` for `cards.State`: any row whose agent's `ParentID` is this agent. The focused agent's card gets `OutputLines: 10`.
  - `type Hit struct { AgentID string; X, Y, W, H int }` — agent card rectangles for mouse clicks.
  - `panels.go`: `CoveragePanel(cov schema.Coverage, diag []string, w, h int) *cells.Grid`, `HelpPanel(w, h int) *cells.Grid`, blitted centered when `UI.Panel` is set.

- [ ] **Step 1: Write the failing tests**

```go
func TestComposeTooSmall(t *testing.T)        // 79×23 → contains "too small"; 0×0 → empty grid, no panic
func TestComposeStatusAndKeys(t *testing.T)   // first line has "AGENT VIEW" and the host label; last line has "q quit"
func TestComposeDemoSnapshot(t *testing.T)    // store loaded from testdata/feed/demo.ndjson at a fixed now, 120×40 → Plain matches testdata/compose/demo.golden
func TestComposeFocusDims(t *testing.T)       // focus "a" → Mask of b's row has only d/f letters
func TestComposeHitsMatchCards(t *testing.T)  // Hit for each visible agent card at its drawn position
func TestComposePanels(t *testing.T)          // PanelCoverage → "malformed" visible; PanelHelp → "space" and "pause" visible
```

- [ ] **Step 2: Create** `testdata/feed/demo.ndjson`: run `bun src/cli.ts collect --stdio --demo` for 20 s and keep the output; replace host labels with `demo`.
- [ ] **Step 3: Run** `go test ./internal/tui/` — expect FAIL.
- [ ] **Step 4: Implement**; generate and review goldens.
- [ ] **Step 5: Run** `go test ./internal/tui/` — expect PASS.
- [ ] **Step 6: Commit** `feat(go): add frame composer and panels`

---

### Task 11: Bubble Tea model

**Files:**
- Create: `internal/tui/model.go`, `internal/tui/keys.go`
- Test: `internal/tui/model_test.go`

**Interfaces:**
- Consumes: Task 10, `feed` message types from Task 12 (define them in `internal/feed/msg.go` in this task: `type EnvelopesMsg []schema.Envelope`, `type StateMsg struct { Host, State, Detail string }`, `type DiagMsg string`).
- Produces:
  - `type Config struct { Store *store.Store; Reduced bool; Now func() time.Time; LingerMs int64 }`; `func NewModel(c Config) *Model`; `Model` implements `tea.Model` (`Init() tea.Cmd`, `Update(tea.Msg) (tea.Model, tea.Cmd)`, `View() tea.View`).
  - `View` returns `tea.View{Content: cells.Encode(grid), AltScreen: true, MouseMode: tea.MouseModeCellMotion}`.
  - `func (m *Model) Diagnostics() []string` (printed by `main` after exit; ring of 200).
- Behavior:
  - `EnvelopesMsg`: apply each envelope (unless paused: still apply to the store, but do not feed the log or animations; on resume, nothing replays); for each `Change`, `Log.FromChange` and `Anim.OnChange`; then `Log.AddTouches(Store.TakeUnattributed(now))`. The `HOST … loaded N agents, M calls from logs` line goes out once per host, at its first live change or 3 s after its `hello`, whichever is first.
  - Ticks: `frameMsg` every 50 ms while `Anim.Active()`; `spinMsg` every 125 ms while any open call exists; `clockMsg` every second. Each tick re-arms itself only while its condition holds; the event batch restarts stopped ticks.
  - Keys per spec "Interaction" (`tea.KeyPressMsg.String()`: `"j"`, `"down"`, `"k"`, `"up"`, `"g"`, `"G"`, `"enter"`, `"esc"`, `"space"`, `"l"`, `"c"`, `"?"`, `"q"`, `"ctrl+c"`, `"ctrl+z"` → `tea.Suspend`). `tea.MouseClickMsg` inside a `Hit` → toggle focus.
  - `tea.WindowSizeMsg` → store size, recompute `UI.Top`.
  - `Status`: host label and state from `StateMsg` (`connecting` until the first one), degraded = coverage sources not `ok`; agents and working from the store; calls per minute = live `CallStarted` in the last 60 s; faults = live failed `CallEnded` within the linger window. `DiagMsg` lines and host labels pass `text.Clean`.

- [ ] **Step 1: Write the failing tests**

```go
func TestModelAppliesBatches(t *testing.T)    // EnvelopesMsg from demo.ndjson → View content shows the demo agent names
func TestModelKeys(t *testing.T)              // j/k move Selected; enter sets Focus; enter again clears; esc clears; space toggles Paused ("PAUSED" in status); q returns tea.Quit
func TestModelMouseFocus(t *testing.T)        // MouseClickMsg at an agent card → Focus == that agent
func TestModelResizeMidAnimation(t *testing.T) // CallStarted, then WindowSizeMsg 79×23 → "too small"; then 120×40 → full board, no panic
func TestModelTicksStop(t *testing.T)         // after animations end and no call is open, Update(frameMsg) returns no frame command
func TestModelPauseKeepsStore(t *testing.T)   // paused + batch → store has the call; log has no new line; unpause → board shows it
func TestModelFeedDown(t *testing.T)          // StateMsg{State:"down", Detail:"exec: bun: not found"} → status shows "down"; coverage panel shows the detail; q still quits
```

- [ ] **Step 2: Run** `go test ./internal/tui/` — expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/tui/` — expect PASS.
- [ ] **Step 5: Commit** `feat(go): add Bubble Tea model and keys`

---

### Task 12: Child collector feed

**Files:**
- Create: `internal/feed/child.go`
- Test: `internal/feed/child_test.go`, `internal/feed/testdata/fakecollector/main.go`

**Interfaces:**
- Consumes: `schema.DecodeLine`, the message types from Task 11.
- Produces:
  - `type Config struct { Argv []string; Dir string; Send func(tea.Msg); Batch time.Duration; MinBackoff, MaxBackoff time.Duration }` (defaults 50 ms, 1 s, 30 s; `Send` is `(*tea.Program).Send` in `main`).
  - `func Run(ctx context.Context, c Config)` — starts the child in its own process group (`Setpgid`), reads stdout with `bufio.Reader.ReadBytes('\n')` (no line limit), sends `EnvelopesMsg` at most every `Batch`, sends stderr lines as `DiagMsg`, sends `StateMsg{State:"up"}` on the first envelope and `StateMsg{State:"down", Detail: <exit or start error>}` on exit, then restarts after backoff (doubling, reset after 60 s up). On `ctx` done, sends `SIGTERM` to the process group, waits 2 s, then `SIGKILL`.
  - `func DefaultArgv(flags []string) ([]string, error)` — `AGENT_VIEW_COLLECTOR` (JSON array of strings) if set, else `["bun", "src/cli.ts", "collect", "--stdio", flags…]`; error if `src/cli.ts` is not in the working directory and the variable is unset: `run from the agent-view repository root, or set AGENT_VIEW_COLLECTOR`.

- [ ] **Step 1: Write the fake collector**: a Go program that writes the lines of a file given as argument, with an optional `--exit-after N` and `--stderr text`, and one 200 KiB snapshot line when `--big` is set.
- [ ] **Step 2: Write the failing tests**

```go
func TestRunBatchesEnvelopes(t *testing.T)   // 300 lines → several EnvelopesMsg, total 300, order kept
func TestRunBigLine(t *testing.T)            // --big → one snapshot envelope decoded
func TestRunRestartsWithBackoff(t *testing.T) // --exit-after 5 → StateMsg down, then up again; second start ≥ MinBackoff later
func TestRunMissingBinary(t *testing.T)      // Argv ["/no/such/bin"] → StateMsg down with Detail containing "no such file", retries, no panic
func TestRunStderrToDiag(t *testing.T)       // --stderr boom → DiagMsg "boom"
func TestRunStopsChildOnCancel(t *testing.T) // cancel ctx → child process gone within 3 s
func TestDefaultArgv(t *testing.T)           // env override parsed; bad JSON → error; no src/cli.ts and no env → the error text above
```

- [ ] **Step 3: Run** `go test ./internal/feed/` — expect FAIL.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run** `go test ./internal/feed/` — expect PASS.
- [ ] **Step 6: Commit** `feat(go): add child collector feed`

---

### Task 13: CLI and main

**Files:**
- Create: `internal/cli/args.go`, `cmd/agent-view/main.go`
- Modify: `README.md` (a "Go TUI (phase 1)" section: build, run, keys, `AGENT_VIEW_COLLECTOR`), `.gitignore` (`/agent-view` binary)
- Test: `internal/cli/args_test.go`

**Interfaces:**
- Produces:
  - `type Args struct { Demo bool; DemoScale string; Watch []string; LingerMin int; Reduced bool }`; `func Parse(argv []string) (Args, error)`; `func (a Args) CollectorFlags() []string` (the flags passed to the child).
  - `type UsageError struct{ Msg string }`.
- Behavior of `main`:
  - Parse; on `UsageError` print it and the usage to stderr, exit 2.
  - stdin or stdout not a character device → print `agent-view needs a terminal; agent-view serve comes in phase 2`, exit 2.
  - `log.SetOutput` to the model's diagnostics; build the store (`LingerMs` from `--linger`), the model, `tea.NewProgram(model)` (keep Bubble Tea's default panic catching and its SIGINT/SIGTERM handling); start `feed.Run` with `Send: p.Send`; on `SIGHUP` call `p.Quit()`; `p.Run()`; cancel the feed (in a `defer`, so a panic also stops the child); print `model.Diagnostics()` to stderr; exit 0 (1 on a program error).

- [ ] **Step 1: Write the failing tests**

```go
func TestParseFlags(t *testing.T)          // --demo --demo-scale stress --watch /a --watch /b --linger 5 --reduced-motion → fields set; CollectorFlags() == ["--demo","--demo-scale","stress","--watch","/a","--watch","/b","--linger","5"]
func TestParseRejects(t *testing.T)        // --remote h, --port 1, serve, collect, --no-open, --demo-scale x, --linger 0 → UsageError naming the token ("--remote comes in phase 2")
```

- [ ] **Step 2: Run** `go test ./internal/cli/` — expect FAIL.
- [ ] **Step 3: Implement** `args.go` and `main.go`.
- [ ] **Step 4: Run** `go test ./...` — expect PASS.
- [ ] **Step 5: Manual check** in a real terminal from the repo root: `go run ./cmd/agent-view --demo`. Expect: cards print and move, history fills, a failed command flashes, `?` and `c` panels open, `q` restores the terminal. Then `NO_COLOR=1 go run ./cmd/agent-view --demo`: no color, layout intact. Then without `--demo` against real sessions.
- [ ] **Step 6: Commit** `feat(go): add CLI entry point for the phase 1 TUI`

---

### Task 14: Performance and size checks

**Files:**
- Create: `internal/tui/bench_test.go`

- [ ] **Step 1: Write the benchmark**: `BenchmarkComposeStress` — a store with 50 agents (10 main, 40 sub), 200 envelopes applied per iteration from a generated stress batch, then `Compose` at 200 × 60.
- [ ] **Step 2: Run** `go test ./internal/tui/ -bench ComposeStress -benchtime 200x` — expect under 4 ms/op. If over, profile (`-cpuprofile`) and fix before continuing.
- [ ] **Step 3: Size:** `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o /tmp/av ./cmd/agent-view && ls -l /tmp/av` — expect under 20 MB. Repeat for `darwin/arm64`.
- [ ] **Step 4: Idle CPU:** run `go run ./cmd/agent-view` with no agents working for 60 s; measure the Go process with `ps -o time` before and after — expect under 1% of one core (the child is measured apart).
- [ ] **Step 5: Memory:** run against real logs (the 24-hour scan) for 60 s; read the Go process RSS with `ps -o rss` — expect under 150 MB.
- [ ] **Step 6: Record** the four numbers in the README phase 1 section.
- [ ] **Step 7: Commit** `test(go): add compose benchmark and record size, CPU, and memory`
