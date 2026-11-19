# Agent View Go Collector (P2b-1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port the TypeScript collector core to Go (log discovery and tailing, the Claude and Codex adapters, file-change watching, collector state, the demo, and `agent-view collect --stdio`) and prove it with a parity test against the TypeScript collector.

**Architecture:** New packages `wire` (events and exact NDJSON), `lifecycle`, `fsutil`, `watch` (FSEvents on macOS, fsnotify elsewhere), `logs`, `fschange`, `demo`, and `collect`. One collector goroutine owns the state; sources send batches to it through a channel, so events keep their order as in the TypeScript promise queue. `agent-view collect --stdio` writes the envelopes to stdout. The parity test runs the TypeScript and Go collectors on the same temporary home directory and compares the normalized streams; the demo is compared with a virtual-time trace.

**Tech Stack:** Go 1.27.3, `github.com/fsnotify/fsnotify` v1.10.1, `github.com/fsnotify/fsevents` v0.2.0 (cgo, darwin only), `golang.org/x/sys/unix` (already required by fsnotify; used for the file birth time on Linux). Bun 1.4.4 runs the TypeScript side of the parity test.

**Spec:** `docs/superpowers/specs/2026-11-18-agent-view-tui-design.md` (Phase 2, milestone P2b). File-watching decisions: `docs/superpowers/spikes/2026-11-18-p2a-file-watch-results.md`. P2b-2 (hub, remote, `serve`, `audit`) and P2c (switch-over, delete TypeScript) are separate plans.

## Global Constraints

- Go module `agent-view`, Go 1.27.3. Dependencies: only those in Tech Stack. Anything else needs a ruling with a reason.
- Behavior: each Go file does what the TypeScript file named in its task does, with the same events, the same field presence (absent vs `null` vs empty), and the same order. Read the TypeScript file before writing the Go file. Where Go cannot match, the task names the difference.
- Known differences (allowed, never fixed by changing TypeScript): a lone UTF-16 surrogate in a log string becomes U+FFFD in Go; times that are not RFC 3339 parse as invalid (epoch) in Go, where `Date.parse` accepts more forms; `hello.collectorVersion` and `hello.platform` are normalized away by the parity test; Go escapes U+2028 and U+2029 in JSON output (the parsed values are equal).
- NDJSON: schema version 1. Envelope fields `v`, `host`, `seq`, `kind`, the event's fields, `ts`. JSON output never escapes `<`, `>`, `&`.
- The collector only reads: it never writes to agent logs, watched projects, or any path outside the test's own temporary directory.
- Lifecycle values (`src/shared/lifecycle.ts`): linger 600,000 ms; stale 120,000 ms; finished fade 60,000 ms; call timeout 600,000 ms; file TTL 1,800,000 ms.
- Limits, verbatim from the TypeScript: tail limit 4 MiB; 120 logs, 24 hours; poll 2,000 ms; safety rescan 10,000 ms (a watcher missing or degraded) and 120,000 ms (all healthy); rescan debounce 50 ms; `MAX_PENDING` 5,000; command 4,096 characters; output: the last 16 KiB in chunks of at most 4 KiB; ended-call memory 20,000 keys; watch roots at most 24; path caches 5,000 entries; collector tick 1,000 ms; coverage every 5,000 ms; file changes 50 per second per root; at most 12,000 directories per root (fsnotify backend).
- P2a decisions, verbatim: `FS_WATCH_DARWIN=fsevents (flags FileEvents|NoDefer; latency 50 ms)`; `FS_WATCH_LINUX=inotify (one watch per directory, found by a walk; a new directory is added on Create and then read)`; `FS_WATCH_KQUEUE=no-go`; `FS_WATCH_ROOTS=resolve each root with filepath.EvalSymlinks before watching`; `FS_WATCH_DROPS=report the root as degraded (fs:<root>); no per-file rescan`; `FS_WATCH_IGNORED=filter ignored directories in the event loop`; `BUILD_DARWIN=CGO_ENABLED=1`.
- Tests: port every test of the TypeScript test files each task names. One Go test function per TypeScript test, in the same order, with the TypeScript title as the Go function's doc comment. The task states the expected count.
- Fixtures: read in place from `test/fixtures/` (P2c moves them to `testdata/`). New Go-only fixtures go in `testdata/`.
- The TypeScript code is not changed. New TypeScript files are allowed only under `test/parity/`.
- The P1 TUI keeps running the TypeScript collector as its child. Only Tasks 2 and 12 touch P1 code.
- After every task: `go test -count=1 ./...`, `go vet ./...` and `gofmt -l .` are clean. The TypeScript test suite is not run (6 file-watch tests failed before P1).
- Commits: Conventional Commits, `feat(go): …`, `test(go): …`. No AI attribution in any commit message.

## Review Focus

1. **Log fields of an unexpected JSON type** (a number `timestamp`, a string `message`, an object `content`, a string `exit_code`): the adapter treats the field as absent, as `str()` and `isObject()` do, and never drops the line or panics. Tests in Tasks 5 and 6.
2. **Codex `FileChange.changes` key order:** writes follow JavaScript `Object.entries` order (array-index keys first in ascending order, then the other keys in insertion order). A Go map would shuffle them. Tests in Tasks 4 and 6.
3. **The reader of `collect --stdio` goes away** (the hub exits, the SSH link drops): the next write fails, and the process stops with exit code 0 within 2 s, without a panic and without dying from SIGPIPE. Test in Task 12.
4. **A log file that is deleted between the scan and the read, or that ends without a newline:** a missing file is silent (no warning); a partial last line waits until its newline arrives. Tests in Tasks 4 and 9.
5. **Multi-byte characters and invalid UTF-8 in tool output:** output chunks never split a character; invalid bytes become U+FFFD; nothing panics. Tests in Tasks 4 and 5.

---

### Task 1: Wire format (`internal/wire`)

Ports `src/shared/schema.ts` (types, `categoryFor`) and `encodeLine` from `src/shared/ndjson.ts`. Decoding stays in the P1 package `internal/schema`; P2c decides whether the TUI store reads `wire` types directly.

**Files:**
- Create: `internal/wire/event.go`, `internal/wire/encode.go`, `internal/wire/category.go`, `internal/wire/clock.go`
- Test: `internal/wire/wire_test.go`

**Interfaces:**
- Produces:
  - `const SchemaVersion = 1`; `const CollectorVersion = "0.2.0"` (the `package.json` version).
  - `type Agent struct { ID, Provider, Name, Role string; ParentID *string; Link string; Cwd, ProjectRoot *string; Model string; Status, LastEventAt string }`. JSON: `parentId`, `cwd`, `projectRoot` encode `null` when nil; `model` is omitted when empty.
  - `type Call struct { AgentID, CallID, StartedAt, Category, RawName string; Files []string; Command, Pattern string }`. `files`, `command`, `pattern` are omitted when empty.
  - `type FileTouch struct { Path, LastTouchAt, Attribution, AgentID string }` (`agentId` omitted when empty); `type Write struct { Path, Op string }`; `type SourceState struct { State, Detail string }` (`detail` omitted when empty).
  - `type Coverage struct { LogRoots []string; LogsOpen, LogsDiscovered int; WatchRoots []string; MalformedRecords, FSDropped int; Sources map[string]SourceState }`. Nil slices and maps encode as `[]` and `{}`.
  - `type Event interface{ Kind() string }` with one struct per kind: `Hello{CollectorVersion, Platform, Now string; Demo bool}` (`demo` only when true), `Snapshot{Agents []Agent; OpenCalls []Call; Files []FileTouch}` (empty lists encode `[]`), `AgentUpsert{Agent Agent}`, `AgentStatusChange{AgentID, Status string}` (kind `agent.status`), `ToolStart{Call Call}`, `ToolUpdate{AgentID, CallID string; Files []string}`, `ToolOutput{AgentID, CallID, Chunk string; Truncated bool}`, `ToolEnd{AgentID, CallID string; Ok *bool; ExitCode *int; Writes []Write}` (`ok` encodes `null` when nil; `exitCode` and `writes` omitted when nil), `FsChange{Path, Op string}`, `SourceStatus{Source string; SourceState}` (state fields inline), `CoverageReport{Coverage Coverage}` (kind `coverage`).
  - `type SourceEvent struct { Ts string; Ev Event }`; `type Envelope struct { V int; Host string; Seq int64; Ts string; Ev Event }`; `func (e Envelope) MarshalJSON() ([]byte, error)` writes one flat object, keys in the order `v`, `host`, `seq`, `kind`, the event's fields in `schema.ts` order, `ts`; `func EncodeLine(e Envelope) []byte` (JSON plus `\n`, no HTML escaping).
  - `func CategoryFor(provider, rawName string) string` (the tables of `schema.ts`; unknown → `other`).
  - `func FormatISO(ms int64) string` (`new Date(ms).toISOString()` for years 0–9999); `func NowMs() int64`; `func Platform() string` (`runtime.GOOS + "-" + arch`, with `amd64` → `x64` and `386` → `ia32`, as Node's `process.arch`).

- [ ] **Step 1: Write the failing tests**
  - Port `test/shared/schema.test.ts` (2 tests): `TestCategoryForClaude`, `TestCategoryForCodex`.
  - `TestEncodeShapes`: a table, one row per kind, comparing `EncodeLine` output with an exact string. Rows must include:
    - `ToolEnd` with nil `Ok` gives `{"v":1,"host":"h","seq":3,"kind":"tool.end","agentId":"a","callId":"c","ok":null,"ts":"T"}` + `\n`.
    - `AgentUpsert` with nil `ParentID`/`Cwd`/`ProjectRoot` and empty `Model` contains `"parentId":null,"link":"root","cwd":null,"projectRoot":null` and no `model` key.
    - An empty `Snapshot` contains `"agents":[],"openCalls":[],"files":[]`.
    - `Hello` without demo has no `demo` key; with demo it has `"demo":true`.
    - `ToolOutput` with `Truncated:false` keeps `"truncated":false`.
  - `TestEncodeNoHTMLEscape`: a chunk `<&>` appears as `<&>` in the output.
  - `TestFormatISO`: `1767225600123` → `2026-01-01T00:00:00.123Z`; `-1` → `1969-12-31T23:59:59.999Z`.
  - `TestPlatform`: `Platform()` equals `runtime.GOOS + "-" + want`, where `want` is `x64` for `amd64` and `GOARCH` otherwise.
- [ ] **Step 2:** Run `go test ./internal/wire/`. Expected: FAIL (package missing).
- [ ] **Step 3:** Implement the files. `MarshalJSON` builds the object with an ordered writer (for example, `bytes.Buffer` and `json.Encoder` with `SetEscapeHTML(false)` per value). It does not use a map.
- [ ] **Step 4:** Run `go test ./internal/wire/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): add the collector wire format`.

### Task 2: Lifecycle (`internal/lifecycle`)

Ports `src/shared/lifecycle.ts` and the `laterIso`/`msOf` helpers of `src/collector/state.ts`.

**Files:**
- Create: `internal/lifecycle/lifecycle.go`
- Modify: `internal/store/lifecycle.go` (`parseISO` and `msOf` call the new package; the store keeps its own `Options`)
- Test: `internal/lifecycle/lifecycle_test.go`

**Interfaces:**
- Consumes: `wire.Agent`.
- Produces: `type Options struct { LingerMs, StaleMs, FinishedFadeMs, CallTimeoutMs, FileTTLMs int64 }`; `func Defaults() Options`; `func (o Options) WithDefaults() Options` (a zero field takes its default); `func ParseISO(s string) (int64, bool)` (RFC 3339 only); `func MsOf(s string) int64` (invalid → 0); `func LaterISO(a, b string) string` (`b` only when its time is greater; an empty time loses); `func PresentAgentIDs(agents []wire.Agent, now int64, o Options) map[string]bool`; `func IsStale(a wire.Agent, now int64, o Options) bool`.

- [ ] **Step 1:** Port `test/shared/lifecycle.test.ts` (4 tests). Add `TestLaterISO`: `LaterISO("", "2026-01-01T00:00:00Z")` is the second argument; `LaterISO("2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z")` is the first; equal times keep the first.
- [ ] **Step 2:** Run `go test ./internal/lifecycle/`. Expected: FAIL.
- [ ] **Step 3:** Implement. Change `internal/store/lifecycle.go` to call `lifecycle.ParseISO` and `lifecycle.MsOf` and delete its copies.
- [ ] **Step 4:** Run `go test ./internal/lifecycle/ ./internal/store/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): share lifecycle rules between the collector and the store`.

### Task 3: Collector state (`internal/collect/state.go`)

Ports `src/collector/state.ts`.

**Files:**
- Create: `internal/collect/state.go`, `internal/collect/ordered.go` (insertion-ordered map and LRU set)
- Test: `internal/collect/state_test.go`

**Interfaces:**
- Consumes: `wire`, `lifecycle`.
- Produces: `type State struct{ Home string /* default os.UserHomeDir() */ }`; `func NewState(o lifecycle.Options) *State`; `func (s *State) Apply(e wire.SourceEvent) (wire.SourceEvent, bool)` (false: duplicate, not forwarded); `func (s *State) Tick(now int64) []wire.SourceEvent`; `func (s *State) Snapshot(now int64) wire.Snapshot`; `func (s *State) Load(snap wire.Snapshot)`; `func (s *State) WatchRoots(now int64, max int) []string`; `func (s *State) Agent(id string) (wire.Agent, bool)`; `func (s *State) FileCount() int`.

Order rules that a Go map would break: open calls keep insertion order (`Tick` ends them in that order); the ended set is an LRU in which a repeated key moves to the end and the oldest key leaves past 20,000. `Snapshot` sorts agents by ID (byte order), open calls by start time, then by the `agentId|callId` key, and files by path.

- [ ] **Step 1:** Port `test/collector/state.test.ts` (15 tests). The golden-file test reads `../../test/fixtures/golden/session.ndjson`.
- [ ] **Step 2:** Run `go test ./internal/collect/`. Expected: FAIL.
- [ ] **Step 3:** Implement. Deep-copy calls and slices that cross the boundary, as the TypeScript does with spreads.
- [ ] **Step 4:** Run `go test ./internal/collect/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port the collector state`.

### Task 4: Tailing, discovery and record decoding (`internal/logs`, `internal/fsutil`)

Ports `src/collector/tail.ts`, `src/collector/discovery.ts` and `LineSplitter` from `src/shared/ndjson.ts`. Adds a JSON decoder that keeps the key order.

**Files:**
- Create: `internal/fsutil/fsutil.go`, `internal/logs/json.go`, `internal/logs/tail.go`, `internal/logs/discovery.go`
- Test: `internal/fsutil/fsutil_test.go`, `internal/logs/json_test.go`, `internal/logs/tail_test.go`, `internal/logs/discovery_test.go`

**Interfaces:**
- Produces:
  - `fsutil.ErrCode(err error) string`: `ENOENT`, `EACCES`, `ENOTDIR`, `EMFILE`, `ENFILE`, `ENOSPC`, `EPERM`, `ELOOP` for those errno values (also when wrapped), else `err.Error()`.
  - `fsutil.FindProjectRoot(cwd string) string` (`findProjectRoot`: nearest ancestor with a `.git` entry, by `Lstat`; else `cwd` unchanged).
  - `logs.Obj` (an ordered JSON object), `func Decode(line string) (any, error)`: values are `*Obj`, `[]any`, `string`, `float64` (`±Inf` when out of range, as `JSON.parse`), `bool`, `nil`. A duplicate key keeps its first position and its last value. Non-whitespace after the value is an error. `func (o *Obj) Get(key string) any`; `func (o *Obj) Entries() []Entry` (`Object.entries` order: keys that are canonical array indexes, `0` to `4294967294`, first in ascending numeric order, then the other keys in insertion order); `type Entry struct{ Key string; Val any }`.
  - Helpers that the adapters share: `func str(v any) (string, bool)` (a non-empty string only), `func obj(v any) *Obj` (nil unless an object), `func isInteger(v any) (int, bool)`.
  - `type LineSplitter struct`; `func (l *LineSplitter) Push(chunk []byte) []string` (complete lines only; each line decoded as UTF-8 with invalid bytes replaced by U+FFFD, as `TextDecoder`); `Reset()`; `PendingBytes() int`.
  - `const TailLimit = 4 << 20`; `type Tailer struct`; `func NewTailer(onRecord func(record any, file string), limit int, onWarning func(string)) *Tailer`; `func (t *Tailer) Read(file string) error`; `Forget(file string)`; `OpenFiles() int`; `Malformed() int`. A cursor holds offset, inode, splitter and the skip-first flag.
  - `type Root struct{ Provider, Path, Scope string }`; `type RecentLog struct{ Root; File string; MtimeMs float64 }`; `func DiscoverRoots(getenv func(string) string, home string) []Root`; `func ScanLogs(roots []Root, sinceMs int64, onRoot func(r Root, code string)) []RecentLog` (code `""` when the scan worked; newest first, ties by `File` ascending); `func SelectLogs(all []RecentLog, maxLogs int) []RecentLog`; `func ListRecentLogs(roots []Root, sinceMs int64, maxLogs int, onRoot func(Root, string)) []RecentLog`.

Warnings, verbatim: `Large logs load only their header and last 4 MiB. Earlier tool calls may be missing.` and `A log record exceeded 4 MiB and was skipped.` (`describeLimit`: whole MiB as `N MiB`, else `N bytes`).

- [ ] **Step 1: Write the failing tests**
  - Port `test/collector/tail.test.ts` (5), `test/collector/discovery.test.ts` (7, the two `findProjectRoot` tests go to `fsutil_test.go`), and the `LineSplitter` test of `test/shared/ndjson.test.ts` (1). The `parseLine` test is already covered by `internal/schema` (P1).
  - `TestDecodeEntriesOrder` (Review Focus 2): `{"b.ts":1,"a.ts":2,"10":3,"2":4}` gives the keys `2`, `10`, `b.ts`, `a.ts`.
  - `TestDecodeDuplicateKey`: `{"a":1,"b":2,"a":3}` gives the keys `a`, `b` and `a` = 3.
  - `TestDecodeTrailingGarbage`: `{} x` is an error; `{}  ` is not.
  - `TestTailPartialLineWaits` (Review Focus 4): a file `{"a":1}\n{"b":` gives one record; after `2}\n` is appended, a second read gives the second record.
  - `TestTailInvalidUTF8` (Review Focus 5): a line with bytes `0xff 0xfe` inside a string gives a record whose string holds U+FFFD, and no error.
- [ ] **Step 2:** Run `go test ./internal/fsutil/ ./internal/logs/`. Expected: FAIL.
- [ ] **Step 3:** Implement. `Decode` walks `json.Decoder.Token()` with `UseNumber`. `ScanLogs` walks with `os.ReadDir` to depth 8 and uses `ENOENT` on the root as "missing" (no callback).
- [ ] **Step 4:** Run `go test ./internal/fsutil/ ./internal/logs/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port log discovery and tailing`.

### Task 5: Output chunks and the Claude adapter (`internal/logs`)

Ports `src/collector/sources/common.ts` and `src/collector/sources/claude-logs.ts`.

**Files:**
- Create: `internal/logs/output.go`, `internal/logs/claude.go`, `internal/logs/text.go`
- Test: `internal/logs/output_test.go`, `internal/logs/claude_test.go`

**Interfaces:**
- Consumes: `Decode`, `Obj`, `str`, `obj` (Task 4); `wire`.
- Produces: `const MaxPending = 5000; MaxCommandChars = 4096; MaxChunkBytes = 4096; MaxOutputBytes = 16 * 1024`; `func OutputEvents(ts, agentID, callID, text string) []wire.SourceEvent`; `func sliceUTF16(s string, n int) string` (the first `n` UTF-16 code units, as `String.prototype.slice(0, n)`, but a surrogate pair is never cut: its first half is dropped); `func resolveFile(cwd *string, file any) (string, bool)` (`path.resolve`: an absolute path is cleaned; a relative path needs a `cwd` and joins it, made absolute against the process directory when `cwd` is relative); `type ClaudeAdapter struct`; `func NewClaudeAdapter(host string) *ClaudeAdapter`; `func (a *ClaudeAdapter) Ingest(record any, file, scope string) []wire.SourceEvent`.

Keep the TypeScript's insertion-ordered `pending` map (the oldest entry leaves past 5,000). The sub-agent file pattern is `[/\\]subagents[/\\]agent-(.+)\.jsonl$`.

- [ ] **Step 1: Write the failing tests**
  - Port `test/collector/claude-logs.test.ts` (18).
  - `TestOutputEventsKeepsCharacters` (Review Focus 5): 20,000 bytes made of `é` (2 bytes) and `😀` (4 bytes) give chunks of at most 4,096 bytes, each valid UTF-8; their concatenation equals the last 16 KiB of the input from a character boundary; only the first chunk has `Truncated` true. Empty text gives no events.
  - `TestClaudeWrongFieldTypes` (Review Focus 1): records with `"timestamp":123`, `"message":"text"`, and `"message":{"content":{}}` give no events and no panic; a later record of the same session with a valid `timestamp` gives the `agent.upsert`.
  - `TestSliceUTF16`: `sliceUTF16("ab😀c", 3)` is `ab`; `sliceUTF16("ab😀c", 4)` is `ab😀`.
- [ ] **Step 2:** Run `go test ./internal/logs/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test ./internal/logs/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port the Claude log adapter`.

### Task 6: The Codex adapter (`internal/logs`)

Ports `src/collector/sources/codex-logs.ts`.

**Files:**
- Create: `internal/logs/codex.go`
- Test: `internal/logs/codex_test.go`

**Interfaces:**
- Consumes: Tasks 4 and 5 helpers.
- Produces: `type CodexAdapter struct`; `func NewCodexAdapter(host string) *CodexAdapter`; `func (a *CodexAdapter) Ingest(record any, file, scope string) []wire.SourceEvent`.

Exact values: patch line pattern `^\*\*\* (Add|Update|Delete|Move to)(?: File)?: (.+)$`; success `Success\. Updated the following files:`; failure `(?i)Failed to|Error:|patch rejected`; exit line `Process exited with code (-?\d+)`; `FileChange` types `add`→`create`, `update`→`edit`, `delete`→`delete`, `move_path`→`move`; `completed_at_ms` is used only when it is a finite number with absolute value below 8.64e15, truncated toward zero, then `FormatISO`. `FileChange.changes` uses `Obj.Entries()`.

- [ ] **Step 1: Write the failing tests**
  - Port `test/collector/codex-logs.test.ts` (28).
  - `TestCodexFileChangeOrder` (Review Focus 2): an `item_completed` `FileChange` whose `changes` keys are `b.ts`, `a.ts`, `10`, `2` (all `update`, `cwd` `/p`) gives `call.files` and `writes` in the order `/p/2`, `/p/10`, `/p/b.ts`, `/p/a.ts`.
  - `TestCodexWrongFieldTypes` (Review Focus 1): `"exit_code":"1"` on a `CommandExecution` gives a `tool.end` without `exitCode` and `ok` true; `"payload":"x"` gives no events and no panic.
- [ ] **Step 2:** Run `go test ./internal/logs/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test ./internal/logs/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port the Codex log adapter`.

### Task 7: Tree watcher (`internal/watch`)

Replaces `src/collector/watch.ts` with the P2a decisions. The TypeScript per-dir mode becomes the fsnotify backend; the recursive mode becomes FSEvents on macOS.

**Files:**
- Create: `internal/watch/watch.go`, `internal/watch/fsnotify.go`, `internal/watch/fsevents_darwin.go`, `internal/watch/fsevents_other.go`
- Modify: `go.mod`, `go.sum` (add fsnotify v1.10.1 and fsevents v0.2.0)
- Test: `internal/watch/watch_test.go`, `internal/watch/fsevents_darwin_test.go`

**Interfaces:**
- Consumes: `fsutil.ErrCode`.
- Produces: `type Op int` with `Rename` and `Change` (Node's `fs.watch` event names); `type Options struct{ Ignore func(rel string) bool; MaxDirs int /* 0 = 12000 */ }`; `type Watcher interface{ Close(); Degraded() string /* "" when healthy */ }`; `func Tree(root string, onChange func(rel string, op Op), o Options) Watcher` (FSEvents on darwin, fsnotify elsewhere); unexported `treeFsnotify` with the same signature, which the tests run on every platform.

Rules:
- `root` is resolved with `filepath.EvalSymlinks`; `rel` is relative to the resolved root. An event for the root itself is not reported. `Ignore(rel)` filters every event (both backends) and stops the walk into an ignored directory (fsnotify). Symlinked directories are not followed.
- FSEvents: flags `FileEvents|NoDefer|WatchRoot`, latency 50 ms. Every event reports `Rename` (Bun's macOS behavior, which `fs-changes` relies on). `MustScanSubDirs`, `UserDropped` or `KernelDropped` set `Degraded()` to `events dropped under load`. `RootChanged` with the root gone sets `watcher error on <root>: ENOENT`.
- fsnotify: `Write` and `Chmod` report `Change`; all others report `Rename`. A new directory is added on `Create` and then read, and its entries are reported as `Rename`. `ErrEventOverflow` sets `events dropped under load`. Removal of the root sets `watcher error on <root>: ENOENT`. Past `MaxDirs`: `more than <n> directories; later directories are not watched`.
- A root that cannot be watched: `cannot watch <root>: <code>`, and no panic. `Degraded()` keeps its first reason. After `Close` no callback runs.

- [ ] **Step 1: Write the failing tests**
  - Port `test/collector/watch.test.ts` (6): the recursive-mode test runs on `Tree`; the per-dir tests run on `treeFsnotify`.
  - `TestTreeSymlinkRoot`: a root given through a symlink reports `sub/f.txt` for a write to `<real>/sub/f.txt`.
  - `TestTreeRootRemoved`: removing the root sets `Degraded()` to `watcher error on <root>: ENOENT` within 2 s (both backends on darwin, fsnotify elsewhere).
  - `TestFSEventsDropFlags` (darwin only): the unexported flag handler, given a batch with `MustScanSubDirs|UserDropped` on the root path, sets `events dropped under load`.
- [ ] **Step 2:** Run `go test ./internal/watch/`. Expected: FAIL.
- [ ] **Step 3:** Implement. The `spikes/fswatch` harness shows both backends.
- [ ] **Step 4:** Run `go test ./internal/watch/` on macOS, and `GOOS=linux CGO_ENABLED=0 go vet ./internal/watch/`. Expected: PASS and clean.
- [ ] **Step 5:** Commit: `feat(go): add the tree watcher (FSEvents on macOS, fsnotify elsewhere)`.

### Task 8: File-change source (`internal/fschange`)

Ports `src/collector/sources/fs-changes.ts`, including a gitignore matcher equal to the `ignore` package 7.0.16 that the TypeScript uses.

**Files:**
- Create: `internal/fschange/gitignore.go`, `internal/fschange/ratelimit.go`, `internal/fschange/source.go`, `internal/fsutil/birth_darwin.go`, `internal/fsutil/birth_linux.go`, `internal/fsutil/birth_other.go`
- Test: `internal/fschange/gitignore_test.go`, `internal/fschange/source_test.go`

**Interfaces:**
- Consumes: `watch.Tree`, `watch.Options`, `watch.Op`; `wire`; `fsutil.ErrCode`.
- Produces: `var IgnoredDirs map[string]bool` (the TypeScript list, 17 names); `type RateLimiter struct`; `func NewRateLimiter(perSecond int, now func() int64) *RateLimiter`; `func (r *RateLimiter) Allow(key string) bool`; `type GitignoreMatcher struct`; `func NewGitignoreMatcher(root string) *GitignoreMatcher`; `func (m *GitignoreMatcher) IsIgnored(abs string) bool`; `func (m *GitignoreMatcher) Invalidate(gitignorePath string)`; `type Options struct{ Emit func([]wire.SourceEvent); OnStatus func(source string, s wire.SourceState); MaxPerSecond int; Home string; Now func() int64; Watch func(root string, onChange func(string, watch.Op), o watch.Options) watch.Watcher }`; `func New(o Options) *Source`; `func (s *Source) SetRoots(roots []string)`; `Stop()`; `Dropped() int`; `Roots() []string`; `fsutil.BirthMs(path string) float64` (darwin `Birthtimespec`; Linux `unix.Statx` with `STATX_BTIME`; 0 when unknown).

Rules: the matcher follows `node_modules/ignore/index.js` (7.0.16). Read that file: its pattern-to-regex rules and its `test()` check of parent directories (a path whose parent directory is ignored stays ignored). Status details, verbatim: `cannot watch: root not found`, `cannot watch: not a directory`, `refused: too broad`. The `op` rule: `Change` → `change`; a missing file → `delete`; a regular file with a birth time whose modification time is more than 1,000 ms after it → `change`; else `create`. `SetRoots` may be called from any goroutine; events and statuses go out through `Emit` and `OnStatus`.

- [ ] **Step 1:** Port `test/collector/fs-changes.test.ts` (15). Add `TestGitignoreCases`: a table of at least 30 pattern/path cases that covers negation, trailing `/`, leading and middle `/`, `*`, `?`, `[a-z]`, `**/`, `/**`, `a/**/b`, escaped `\#` and `\!`, and trailing spaces. Each expected value comes from running `ignore` 7.0.16 (Task 13 checks the same table against it).
- [ ] **Step 2:** Run `go test ./internal/fschange/ ./internal/fsutil/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test ./internal/fschange/ ./internal/fsutil/` and `GOOS=linux CGO_ENABLED=0 go vet ./internal/fsutil/ ./internal/fschange/`. Expected: PASS and clean.
- [ ] **Step 5:** Commit: `feat(go): port the file-change source`.

### Task 9: Log source (`internal/logs/source.go`)

Ports `src/collector/sources/log-source.ts`.

**Files:**
- Create: `internal/logs/source.go`
- Test: `internal/logs/source_test.go`

**Interfaces:**
- Consumes: Tasks 4–7.
- Produces: `type SourceOptions struct{ Host string; Roots []Root; Emit func([]wire.SourceEvent); OnStatus func(string, wire.SourceState); MaxLogs int; SinceMs, PollMs, RescanMs, HealthyRescanMs int64; OnWarning func(string); Watch func(root string, onChange func(string, watch.Op), o watch.Options) watch.Watcher }` (zero values take the Global Constraints defaults); `func NewSource(o SourceOptions) *Source`; `func (s *Source) Start() error` (returns after the first scan, and after the reads of the files it selected, have emitted their events); `Stop()`; `func (s *Source) Coverage() LogCoverage`; `type LogCoverage struct{ LogRoots []string; LogsOpen, LogsDiscovered, MalformedRecords int }`.

Design: one goroutine owns the selection, the tailer and the timers, so a file is never read twice at once and a trigger during a read gives one follow-up read. Watcher hints reach it through a buffered channel; a full channel drops the hint (the 2 s poll covers it). Each read of a file emits one batch. New logs are read oldest first. The status source key is `logs:<root>`. Warnings, verbatim: `Cannot read <file>: <code>` (never for `ENOENT`), `Log scan failed: <message>`, `Event consumer failed: <message>`.

- [ ] **Step 1:** Port `test/collector/log-source.test.ts` (10). Add `TestLogSourceDeletedBeforeRead` (Review Focus 4): a selected file deleted before its read gives no warning and no events, and the source keeps running.
- [ ] **Step 2:** Run `go test ./internal/logs/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race ./internal/logs/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port the log source`.

### Task 10: Demo (`internal/demo`)

Ports `src/collector/demo.ts` on a scheduler that tests and the parity trace can drive in virtual time.

**Files:**
- Create: `internal/demo/rand.go`, `internal/demo/sched.go`, `internal/demo/demo.go`
- Test: `internal/demo/demo_test.go`

**Interfaces:**
- Consumes: `wire`.
- Produces: `func Mulberry32(seed uint32) func() float64`; `type Scheduler interface{ NowMs() float64; After(ms float64, fn func()) (cancel func()) }`; `func NewRealScheduler() Scheduler` (one goroutine runs every callback; `NowMs` is the Unix time in whole milliseconds); `type FakeScheduler struct`; `func NewFakeScheduler(startMs float64) *FakeScheduler`; `func (f *FakeScheduler) Advance(ms float64)` (fires due callbacks in order of fire time, then insertion order; a callback scheduled during `Advance` fires in the same call when due); `type Options struct{ Host, Scale string; Emit func([]wire.SourceEvent); Sched Scheduler }`; `func New(o Options) *Source`; `Start()`; `Stop()`; `func FilePaths(scale string) []string`.

Rules: every place where the TypeScript reads `Date.now()` or `new Date()` reads `Sched.NowMs()`; a time becomes `FormatISO(int64(ms))`. A timer fires at `now + ms` with the fractional part kept. `Mulberry32` uses `uint32` arithmetic. The seed is `20261117`.

- [ ] **Step 1: Write the failing tests**
  - `TestMulberry32`: the first three values of `Mulberry32(20261117)` are `0.876015507383272`, `0.0866415249183774`, `0.9978529647924006` (exact `float64` equality).
  - Port the three `DemoSource` tests of `test/collector/collector.test.ts` (normal, stress, stop), driven by `FakeScheduler`. Stress: 50 agents, `FilePaths("stress")` has 3,000 distinct paths, and 10 virtual seconds give 1,600–2,400 events.
- [ ] **Step 2:** Run `go test ./internal/demo/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race ./internal/demo/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port the demo source`.

### Task 11: Collector (`internal/collect/collector.go`)

Ports `src/collector/collector.ts`.

**Files:**
- Create: `internal/collect/collector.go`, `internal/collect/paths.go` (LRU path caches, `realPath`, `projectRootFor`)
- Test: `internal/collect/collector_test.go`

**Interfaces:**
- Consumes: `State` (Task 3); `logs.DiscoverRoots`, `logs.NewSource`, `logs.Root`; `fschange.New`; `demo.New`, `demo.Scheduler`; `fsutil.FindProjectRoot`; `wire`; `lifecycle.Options`.
- Produces: `type Options struct{ Host string; Demo bool; DemoScale string; Watch []string; Lifecycle lifecycle.Options; Roots []logs.Root /* nil: DiscoverRoots */; Sched demo.Scheduler /* nil: real */ }`; `func New(o Options) *Collector`; `func (c *Collector) Start() error` (returns once the first scan's events are in the state); `func (c *Collector) Stop()` (idempotent; nothing is delivered after it returns); `func (c *Collector) Subscribe(fn func(wire.Envelope)) (unsubscribe func())`; `func (c *Collector) Coverage() wire.Coverage` (safe from any goroutine).

Design: one loop goroutine owns the state, the sequence number and the subscriber list. It receives source batches, status changes, ticks (1,000 ms), coverage ticks (5,000 ms), subscribe requests and a barrier for `Start`. It enriches each event (not in demo mode), applies it, wraps it, and delivers it to every subscriber in order. A subscriber that panics is recovered and does not affect the others. `Subscribe` delivers `hello` and `snapshot` with the current sequence number (not incremented) before any later envelope, on the loop goroutine when it runs, else on the caller's; it returns after both are delivered. Timed-out `tool.end` events from `Tick` go out directly and are not applied again. A failure to set the watch roots writes `agent-view: cannot update watch roots: <message>` to stderr.

- [ ] **Step 1:** Port the 11 remaining tests of `test/collector/collector.test.ts` (Task 10 owns the three `DemoSource` tests). The demo-collector tests use a `FakeScheduler`.
- [ ] **Step 2:** Run `go test ./internal/collect/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race ./internal/collect/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port the collector`.

### Task 12: `agent-view collect --stdio` (`internal/cli`, `cmd/agent-view`)

Ports `collectStdio` and the collect rules of `parseArgs` from `src/cli.ts`.

**Files:**
- Modify: `internal/cli/args.go`, `cmd/agent-view/main.go`, `README.md` (the Go section: `collect --stdio`, and how to run the TUI on the Go collector with `AGENT_VIEW_COLLECTOR`)
- Create: `cmd/agent-view/collect.go`
- Test: `internal/cli/args_test.go`, `cmd/agent-view/collect_test.go`

**Interfaces:**
- Consumes: `collect.New`, `collect.Options`, `wire.EncodeLine`.
- Produces: `cli.Args` gains `Mode string` (`"tui"` or `"collect"`) and `Label string`; `func runCollect(args cli.Args, stdout io.Writer, stderr io.Writer, signals <-chan os.Signal) int`.

Rules (from `parseArgs`): `collect` must be the first argument; `collect` without `--stdio` is a usage error (`collect needs --stdio`); `--label` and `--stdio` are valid only with `collect`; `--label` needs a non-empty name (`--label needs a name`); `--remote`, `--port` and `--reduced-motion` are usage errors with `collect`. `--linger` takes decimal minutes above 0 (`Math.round(minutes * 60000)` ms) in both modes; this replaces the P1 whole-minutes rule. `--watch` values become absolute paths. The default label is `os.Hostname()`. Usage errors print `agent-view: <message>` and the usage text to stderr and exit 2. The TTY check of P1 does not apply to `collect`. SIGINT, SIGTERM and SIGHUP stop the collector and exit 0. `signal.Notify` for SIGPIPE, so a write to a closed stdout returns an error; the first write error stops the collector and exits 0.

- [ ] **Step 1: Write the failing tests**
  - Port the collect parts of `test/cli.test.ts`: the collect assertions of "parseArgs: defaults, …, collect --stdio --label" and of "parseArgs rejects unknown flags, bad values, and collect without --stdio", "collect --stdio without --label uses the host name", and "usage errors print to stderr and exit with code 2" (4 tests). Add `TestParseLingerDecimal`: `--linger 1.5` gives 90,000 ms; `--linger 0` and `--linger x` are usage errors.
  - `TestCollectStdioDemo`: the built binary with `collect --stdio --demo --label t` writes a `hello` line with `"demo":true` and `host` `t` first, then a `snapshot`, then `agent.upsert` lines within 1 s.
  - `TestCollectStdioClosedStdout` (Review Focus 3): the binary with `--demo`; the test reads three lines, then closes its read end of the pipe. The process exits with code 0 within 2 s.
  - `TestCollectStdioSIGTERM`: SIGTERM gives exit code 0 within 2 s.
- [ ] **Step 2:** Run `go test ./internal/cli/ ./cmd/agent-view/`. Expected: FAIL.
- [ ] **Step 3:** Implement. Build the test binary once in `TestMain` with `go build -o <tmp>/agent-view .`.
- [ ] **Step 4:** Run `go test ./internal/cli/ ./cmd/agent-view/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): add agent-view collect --stdio`.

### Task 13: Parity with the TypeScript collector (`internal/parity`)

The spec's exit test for the port: "A parity test runs the TypeScript collector and the Go collector on the same fixture logs and demo seed and compares the normalized envelope streams (times and sequence numbers removed). It must match before the TypeScript code is deleted."

**Files:**
- Create: `internal/parity/parity_test.go`, `internal/parity/normalize_test.go`, `test/parity/demo-trace.ts`, `test/parity/ignore-cases.ts`, `testdata/parity/claude/…`, `testdata/parity/codex/…`
- Modify: `README.md` (a "Parity" paragraph: how to run it, and the opt-in local mode)

**Interfaces:**
- Consumes: the Go binary (built in `TestMain`), `bun src/cli.ts collect --stdio`, `demo.New` with `FakeScheduler`, `fschange.NewGitignoreMatcher`.

Rules:
- Every test skips with `t.Skip("bun not on PATH")` when `bun` is missing.
- **Stream runs:** a temporary `HOME` holds `.claude/projects/…` and `.codex/sessions/…`. Files get distinct mtimes inside the last hour, in a fixed order. Both programs run at the same time from the repository root (`bun src/cli.ts collect --stdio --label parity` and `<go binary> collect --stdio --label parity`), with the environment cleared of `CODEX_HOME` and `CLAUDE_CONFIG_DIR`, and `HOME` set. Each is read until 3 s pass with no line other than `coverage` (60 s at most), then sent SIGTERM.
- **Normalize:** drop `seq` and the envelope `ts`; drop `now`, `collectorVersion` and `platform` from `hello`. Compare these as sets: `source.status` envelopes, and `tool.end` envelopes with `ok: null` (timeouts land at a tick, whose position depends on timing). Compare the last `coverage` envelope on its own. Every other envelope is compared in order, as parsed JSON values (key order is ignored).
- **Corpus `TestParityFixtures`:** `test/fixtures/claude`, `test/fixtures/codex` and the new `testdata/parity` logs. The new logs must contain at least one of each: a tool output above 16 KiB with multi-byte characters at the 4 KiB boundaries; a command above 4,096 characters; `is_error`; `custom-title` and `agent-name`; a sub-agent spawn with `toolUseResult.agentId`; `Write`, `MultiEdit`, `NotebookEdit` with a relative path; `Grep` with `path`; a record without `timestamp`; a record with a number `timestamp`; a malformed line; model `<synthetic>`; `end_turn`; `turn_duration`; Codex `session_meta` with a parent; `turn_context`; `exec_command` with an array command; `function_call_output` with JSON metadata `exit_code`; `Process exited with code 2`; `apply_patch` success and failure; a namespaced `functions.apply_patch`; `item_completed` `CommandExecution` with `aggregated_output` and `exit_code`; `FileChange` with `move_path` and array-index keys; `completed_at_ms`; `task_started`, `task_complete` and `turn_aborted`; an item of an unknown thread. Every `cwd` is under `/p/` so that no project root exists and no file watcher starts.
- **Corpus `TestParityLocal`:** runs only with `AGENT_VIEW_PARITY_LOCAL=1`. It copies the newest 120 logs of the real roots (read-only) into the temporary home, keeping their mtimes (a tie moves the later file by 1 ms), and compares as above, but also drops `fs.change`, `fs:` statuses and `watchRoots`, because the copied `cwd` values name real projects. On a mismatch it prints only the index, the kind, the agent ID and the call ID, never log content. The temporary copy is removed at the end.
- **`TestParityDemoTrace`:** `bun test/parity/demo-trace.ts <scale> <ms>` replaces `setTimeout`, `clearTimeout` and `Date` (both `Date.now()` and `new Date()` without arguments) with a virtual clock that starts at `1767225600000`, fires timers at `now + ms` in order of fire time, then creation order, runs `DemoSource` with host `parity`, and prints every emitted source event as one JSON line. The Go side runs `demo.New` on `NewFakeScheduler(1767225600000)`. Cases: `normal` for 60,000 ms and `stress` for 10,000 ms. The lists must be equal, field for field, including `ts`.
- **`TestParityGitignore`:** `bun test/parity/ignore-cases.ts` prints the `ignore` 7.0.16 result for every row of the Task 8 table; each must equal `GitignoreMatcher`'s result.
- When TypeScript and Go differ because of a TypeScript bug, Go matches TypeScript, and the bug goes into `docs/superpowers/followups/` for after P2c.

- [ ] **Step 1:** Write the tests, the two TypeScript scripts and the fixture logs.
- [ ] **Step 2:** Run `go test -count=1 ./internal/parity/`. Expected: PASS. If a stream differs, fix the Go package that owns the field and re-run that package's tests too.
- [ ] **Step 3:** Run `AGENT_VIEW_PARITY_LOCAL=1 go test -count=1 -run TestParityLocal ./internal/parity/` once. Expected: PASS. Report the number of logs and compared envelopes in the task report (counts only).
- [ ] **Step 4:** Run `go test -count=1 ./...`, `go vet ./...`, `gofmt -l .`. Expected: all clean.
- [ ] **Step 5:** Commit: `test(go): add the collector parity test against TypeScript`.
