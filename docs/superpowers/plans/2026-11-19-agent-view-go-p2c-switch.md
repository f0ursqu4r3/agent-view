# Agent View Go Switch (P2c) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Go program the whole product: the TUI runs the collector and the hub in-process and accepts `--remote`, a build script makes the four binaries and checks their size, the README is rewritten, and the TypeScript code is deleted after its behavior is frozen as golden files.

**Architecture:** A new `internal/bridge` subscribes to the hub and feeds the Bubble Tea program (converting envelopes through the existing wire encoder and `internal/schema` decoder, so the P1 store and TUI do not change). `cmd/agent-view` builds one hub stack (local collector, remotes) that `serve` and the TUI share. A quiet-terminal layer routes every in-process diagnostic and the process's stderr into the TUI's diagnostics buffer. The parity tests are frozen into goldens recorded from the TypeScript code, then the TypeScript code is deleted.

**Tech Stack:** Go 1.27.3, Bubble Tea v2, Lip Gloss v2, fsnotify, fsevents (cgo, darwin), `golang.org/x/sys/unix` (already direct), tmux 3.x for the end-to-end tests (tests skip without it). Bun 1.4.4 is needed only by Task 8, once.

**Spec:** `docs/superpowers/specs/2026-11-18-agent-view-tui-design.md` (Phase 2, milestone P2c). Builds on P1, P2a, P2b-1 and P2b-2 and their follow-ups in `docs/superpowers/followups/` (the "For P2c" lists).

## Global Constraints

- Go module `agent-view` at the repository root, Go 1.27.3. No new dependency (spec: "Anything else needs a reason in the plan").
- Targets (spec, verbatim): `darwin-arm64`, `darwin-x64`, `linux-x64`, `linux-arm64`. "Linux builds use `CGO_ENABLED=0`. Darwin builds are made on a Mac." Build flags: `-trimpath`, `-ldflags "-s -w"`.
- Performance targets (spec, verbatim): "Binary: under 20 MB for each target"; "With 50 agents and 200 events per second in bursts: building the board and the grid takes under 4 ms per frame"; "With agents idle: the whole process (collector, hub, TUI) stays under 2% of one core"; "Event-to-screen latency under 1 second"; "Memory: under 150 MB with the 24-hour scan of 120 logs". A missed target is reported with its number (DONE_WITH_CONCERNS), never hidden.
- Quiet terminal (spec, verbatim): "while the TUI runs, nothing writes to stdout or stderr. All library and collector logging goes to the diagnostics buffer."
- Exit (spec, verbatim): "`q`, `Ctrl-C`, `SIGTERM`, `SIGHUP`, or a panic restore the terminal (Bubble Tea does this; a panic handler must too), stop the collectors and children, then print held diagnostics to stderr. `Ctrl-Z` suspends and resumes cleanly."
- CLI (spec, verbatim): "`agent-view` — start the collector (… in-process, plus `--remote` hosts) and the TUI. If stdin or stdout is not a TTY, exit with code 2 and a message that names `agent-view serve`"; flags `--demo`, `--demo-scale normal|stress`, `--watch <dir>`, `--remote <ssh-host>`, `--linger <minutes>`, `--reduced-motion`; "`--port` only with `serve`" (and `audit`); exit codes 0 normal, 2 usage error, 1 other failure; `NO_COLOR` set to any value turns color off.
- Deletions (spec, verbatim): "the TypeScript code, `package.json`, `bun.lock`, `tsconfig.json`, `scripts/compile.ts`, the browser viewer, and `spikes/pixi-stress/`".
- Read-only access to agent data. Tests write only inside their own temporary directories. Real logs are read only in Task 11 and only as counts and numbers.
- **Process safety (hard rule, after the P1 incident):** never stop a process by name (`pkill`, `killall`, `pkill -f`). Stop only a PID you started, or run `tmux -L <private socket> kill-server` on a socket you created. Every tmux test uses its own socket named `av-<test>-<pid>`.
- Known differences from TypeScript (P2b-1 and P2b-2 follow-ups) stay as they are.
- The TypeScript test suite is not run (6 file-watch tests failed before P1).
- After every task: `go test -count=1 ./...`, `go vet ./...`, `GOOS=linux CGO_ENABLED=0 go vet ./...` and `gofmt -l .` are clean. Commits: Conventional Commits, no AI attribution.

## Review Focus

1. **Screen corruption by in-process code:** a collector diagnostic, the fsevents library's `log.Printf`, a direct write to fd 2, or a Go panic message must never reach the terminal while the TUI runs, and a panic message must print on the real stderr after the terminal is restored. Tests in Tasks 1 and 4.
2. **A stalled TUI event loop** (paused, slow terminal): the hub and collector never block, the bridge queue is bounded, and after an overflow the store equals the hub's state. Test in Task 2.
3. **Quit while a remote `ssh` child ignores SIGTERM, or a log scan is mid-read:** the program exits within 2 s, restores the terminal, and leaves no process. Test in Task 4.
4. **A remote that sends garbage, a half line, or drops:** its host shows `down` with a reason and reconnects; the other hosts are unaffected; no crash. Test in Task 4.
5. **Three or more hosts, and hosts with equal agent IDs or labels (`--remote box --remote box` gives `box` and `box#2`):** rows stay separate, the status bar fits 80 columns, and each host's coverage is shown. Tests in Task 5.

---

### Task 1: Collector diagnostics hook (`internal/collect`)

**Files:** Modify `internal/collect/collector.go`, `internal/collect/collector_test.go`.

**Interfaces:**
- Produces: `collect.Options.Diag func(msg string)`. Nil keeps today's behavior: the line `agent-view: <msg>` plus a newline goes to `os.Stderr` (`collect --stdio` and `serve` rely on it). Messages: `collector step failed: <v>`, `cannot update watch roots: <message>`, `snapshot failed: <v>`.

- [ ] **Step 1: Write the failing tests.** `TestDiagReceivesStepFailure` (a hook that panics once, as in `TestStepPanicIsRecovered`, with `Diag` set: the message arrives through `Diag`, and nothing is written to `os.Stderr`). `TestGreetSnapshotPanicSendsEmptySnapshot` (a state hook makes `Snapshot` panic during `Subscribe`: the subscriber receives `hello` and then an empty `snapshot` envelope, and `Diag` gets `snapshot failed: …`). The existing stderr test keeps passing with `Diag` nil.
- [ ] **Step 2:** Run `go test ./internal/collect/`. Expected: FAIL.
- [ ] **Step 3:** Implement. Replace both `fmt.Fprintf(os.Stderr, …)` calls with one private `c.diag(msg string)`; `greet` recovers a `Snapshot` panic and delivers `wire.Snapshot{}`.
- [ ] **Step 4:** Run `go test -race -count=3 ./internal/collect/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): route collector diagnostics through an injectable hook`.

### Task 2: Hub-to-TUI bridge (`internal/bridge`)

**Files:** Create `internal/bridge/bridge.go`, `internal/bridge/bridge_test.go`.

**Interfaces:**
- Consumes: `hub.Hub` (`Subscribe`, `Hosts`, `OnHostsChange`), `wire.EncodeLine`, `schema.DecodeLine`, `feed.EnvelopesMsg`, `feed.StateMsg`.
- Produces: `type Config struct{ Hub *hub.Hub; Send func(tea.Msg); Batch time.Duration /* 0 = 50 ms */; MaxQueue int /* 0 = 50,000 */; Diag func(string) }`; `func Start(c Config) *Bridge`; `func (b *Bridge) Stop()` (idempotent).

Rules:
- The hub subscriber runs under the hub lock (hub contract): it only calls `wire.EncodeLine`, appends the line to a mutex-guarded queue, and wakes the run goroutine without blocking. It never calls `Send` or a hub method.
- The queue holds lines and host-state changes in order. The run goroutine decodes each line with `schema.DecodeLine` (a line that fails to decode is skipped, counted, and reported once through `Diag`) and sends at most one `feed.EnvelopesMsg` per `Batch` interval; `Stop` flushes the pending batch.
- `Start` first sends one `feed.StateMsg{Host, State, Detail}` per `Hub.Hosts()` entry, then every change from `OnHostsChange` (diffed against the last state sent per label), in order with the envelopes.
- Overflow: past `MaxQueue` entries the subscriber drops the whole queue and raises a resync flag. The run goroutine then unsubscribes, calls `Hub.Subscribe` again (the replay is hello, snapshot and coverage per host), sends the current host states, and reports `bridge: the display fell behind; resynced from the hub`.
- A panic in the run goroutine is recovered and reported through `Diag`; the bridge keeps running.

- [ ] **Step 1: Write the failing tests.**
  - `TestBridgeOrderAndBatching`: a fake source through a real hub; 500 envelopes arrive as `EnvelopesMsg` batches in order, never more than one batch per interval (fake clock or a 20 ms interval with timestamps), after one `StateMsg{Host, "up"}`.
  - `TestBridgeHostStates`: connecting then up then down (with detail) produce `StateMsg` in that order, once each.
  - `TestBridgeNeverBlocksHub` (Review Focus 2): `Send` blocks on a gate; 60,000 envelopes go through the hub within 5 s; `Stop` returns after the gate opens.
  - `TestBridgeResyncAfterOverflow`: with `MaxQueue` 1,000 and a blocked `Send`, push 3,000 envelopes, open the gate: a `bridge: …resynced…` diagnostic appears and a P1 `store.Store` fed with the received messages holds the same agents and open calls as `Hub.SnapshotAll()`.
  - `TestBridgeRecoversPanic`: a `Send` that panics once: diagnostic appears, later batches still arrive.
  - `TestBridgeStopIsClean`: `Stop` twice; no goroutine remains (`runtime.NumGoroutine` back to its start value within 2 s); no message after `Stop` returns.
- [ ] **Step 2:** Run `go test ./internal/bridge/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race -count=3 ./internal/bridge/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): bridge the hub to the TUI`.

### Task 3: One hub stack for `serve` and the TUI (`cmd/agent-view`, `internal/cli`)

**Files:** Create `cmd/agent-view/stack.go`; modify `cmd/agent-view/serve.go`, `internal/cli/args.go`, `internal/cli/args_test.go`.

**Interfaces:**
- Produces: `type stack struct{ hub *hub.Hub; label string; local *hub.LocalSource; remotes []*remote.Remote }`; `func newStack(a cli.Args, remoteCmd []string, diag func(string)) (*stack, error)` (hub with the lifecycle from `--linger`; local source over `collect.New` with `Diag: diag`; one remote per `--remote`, labels from `hub.UniqueLabel`; `remoteCmd` is the test override or nil); `func (s *stack) start(onErr func(string))` (remotes, then the local source in a goroutine; failures go to `onErr`, serving continues); `func (s *stack) stop(bound time.Duration)` (the bounded stop and the SIGKILL of remote children that `serve` has today).
- `cli.Args`: the TUI mode accepts `--remote` (repeatable, same validation and message as `serve`). `--port` outside `serve` and `audit` stays a usage error with the message `--port is only valid with serve or audit`.

- [ ] **Step 1: Write the failing tests.** `TestParseTUIRemote` (`--remote a --remote b` gives two remotes in the TUI mode; an invalid host gives the serve message; `--port 1` gives the new message and exit 2). `TestStackStopBound` (a stack with a stubborn fake remote: `stop(1.5 s)` returns within 2 s and the marked child is gone). All `serve_test.go` tests stay unchanged and pass.
- [ ] **Step 2:** Run `go test ./internal/cli/ ./cmd/agent-view/`. Expected: FAIL.
- [ ] **Step 3:** Implement; refactor `runServe` onto the stack without changing its order, messages or exit codes.
- [ ] **Step 4:** Run `go test -race ./internal/cli/ ./cmd/agent-view/`. Expected: PASS.
- [ ] **Step 5:** Commit: `refactor(go): share one hub stack between serve and the TUI`.

### Task 4: The TUI runs on the in-process hub (`cmd/agent-view`, `internal/quiet`, `internal/feed`)

**Files:** Create `internal/quiet/stderr_unix.go`, `internal/quiet/stderr_test.go`, `cmd/agent-view/tui_e2e_test.go`; modify `cmd/agent-view/main.go`, `internal/cli/args.go` (usage text); delete `internal/feed/child.go`, `internal/feed/child_test.go`, `internal/feed/testdata/fakecollector/`, `Args.CollectorFlags` and its tests. `internal/feed/msg.go` stays (message types).

**Interfaces:**
- Consumes: `newStack`, `bridge.Start`, `diagWriter` (exists).
- Produces: `func quiet.CaptureStderr(onLine func(string)) (restore func(), err error)`. It redirects fd 2 to a pipe with `unix.Dup2`; a goroutine splits the pipe into lines (each cut to 4,096 bytes) for `onLine`; `restore` puts fd 2 back, closes the pipe, waits for the reader, and is idempotent.

TUI path order: `newStack` → `bridge.Start` (subscribed before any source starts: spec "Startup history") → `stack.start` → `p.Run()` → `bridge.Stop()` → `stack.stop(1.5 s)` → print the held diagnostics to stderr. `CaptureStderr` starts before the program and `restore` is deferred, so a panic prints on the real stderr after the terminal is restored. `log.SetOutput`, the collector `Diag`, the bridge `Diag` and the captured stderr all feed the program's diagnostics buffer. `AGENT_VIEW_REMOTE_COMMAND` (test only) applies to the TUI; `AGENT_VIEW_COLLECTOR` is gone from code and usage text.

- [ ] **Step 1: Write the failing tests.**
  - `internal/quiet` (helper-process pattern): `TestCaptureStderrLines` (writes through `os.Stderr` and through `syscall.Write(2, …)` reach `onLine`; after `restore` a write reaches the real stderr), `TestCaptureStderrLongLine` (a 100 KB line yields a line of at most 4,096 bytes and the stream continues), `TestCapturePanicPrintsAfterRestore` (Review Focus 1: a panic after capture, with a deferred `restore`, shows the panic text on the real stderr of the helper process).
  - `cmd/agent-view/tui_e2e_test.go`, tmux on a private socket, 100 × 30, the binary built by `TestMain`, `HOME` a temp directory, tests skip without tmux:
    - `TestTUIDemoAndRemote`: args `--demo --remote fakehost`, `AGENT_VIEW_REMOTE_COMMAND=["sh","-c","cat <file>; exec sleep 600.<marker>"]` where `<file>` holds a well-formed remote stream (hello, snapshot, one agent named `remote-agent-e2e`, one open call). Within 15 s the pane shows both host labels with `up` and `remote-agent-e2e`. `q` ends the program with exit status 0 within 3 s; no process with the marker remains.
    - `TestTUIStartupHistory`: no `--demo`; `HOME` holds one Claude log from one hour ago with a `custom-title` of `startup-history-agent` and two tool calls. Within 15 s the pane shows that agent row and a log line matching `loaded \d+ agent`. The agent is history: no `BOOT` log line for it.
    - `TestTUISignals`: for `SIGTERM` and `SIGHUP`, sent with `kill -<sig> <pane pid>` to the program's own PID (never by name), the program exits within 3 s, the marked remote child is gone, and the pane is back at the shell. If Bubble Tea does not already handle `SIGTERM`, handle it like `SIGHUP` (`p.Quit()`).
    - `TestTUIQuitWithStubbornRemote` (Review Focus 3): the remote ignores SIGTERM (`trap '' TERM`); after `q` the program exits within 3 s with status 0 and the marked process is gone.
    - `TestTUIBrokenRemote` (Review Focus 4): the remote prints garbage, one valid hello, half a line, and exits 1; the host shows `down` with a reason, then returns to `connecting`; the local host is still `up`; the program stays alive until `q`.
    - `TestTUIQuietTerminal` (Review Focus 1): a remote that exits 255 after printing `QUIET-TEST-MARKER` on stderr gives the host detail `ssh failed: QUIET-TEST-MARKER`. While the program runs, the board (`capture-pane -p`) never shows the marker; after `c` opens the coverage panel it does; after `q` the marker is printed once on the real stderr with the held diagnostics.
- [ ] **Step 2:** Run `go test ./internal/quiet/ ./cmd/agent-view/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race ./internal/quiet/ ./cmd/agent-view/`. Expected: PASS; no `tmux` server of this test remains (`tmux -L <socket> ls` fails).
- [ ] **Step 5:** Commit: `feat(go): run the collector and hub inside the TUI`.

### Task 5: Several hosts in the TUI (`internal/tui`)

**Files:** Modify `internal/tui/batch.go`, `internal/tui/panels.go`, `internal/tui/model.go`, their tests and goldens.

**Interfaces:**
- Changes: host state is keyed by the raw host label, the same string the store uses (cleaning is only for drawing). `CoveragePanel(covs []HostCoverage, diag []string, w, h int) *cells.Grid` with `type HostCoverage struct{ Label string; Coverage schema.Coverage }` replaces the first-host-only call; the model builds the slice in the store's host order.

Rules: one block per host, each starting with `host <label>` when more than one host exists (a single host draws byte-identically to today); when the content is taller than the box, the last visible line reads `… <n> more lines`. The status bar keeps working at 80 columns with 3 hosts: segments that do not fit are cut with `…`, never over the clock.

- [ ] **Step 1: Write the failing tests.** `TestHostStateKeyedByRawLabel` (a host label with an ESC byte: the state word is applied to that host's segment; the drawn label is cleaned). `TestCoveragePanelTwoHosts` (golden, 2 hosts, with and without diagnostics) and `TestCoveragePanelOneHostUnchanged` (the existing golden passes unchanged). `TestCoveragePanelTruncates` (5 hosts in a 12-line box: last line `… N more lines`). `TestStatusBarThreeHostsAt80` (golden). `TestEqualAgentIDsOnTwoHosts` (Review Focus 5: two hosts with the same agent ID give two separate rows; `box` and `box#2` both appear).
- [ ] **Step 2:** Run `go test ./internal/tui/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test ./internal/tui/`. Expected: PASS (`-update` is used only for the new goldens).
- [ ] **Step 5:** Commit: `feat(go): show every host in the status bar and coverage panel`.

### Task 6: Store speed with several hosts (`internal/store`, `internal/tui`)

**Files:** Modify `internal/store/store.go`, `internal/store/apply.go`, `internal/store/changes.go` (as needed), `internal/tui/bench_test.go`.

P1 follow-up: `RecentEnded` and `OpenCalls` sort all host calls per call and run per row per frame; the frame was 2.1 ms alone and 3.8–3.97 ms with 200 envelopes (target 4 ms). Remotes make it worse.

**Interfaces:** `RecentEnded` and `OpenCalls` keep their signatures and results. The store keeps a per-agent list of open calls (by start time) and a per-agent list of ended calls (newest end first), updated in `Apply` and trimmed with the call cap.

- [ ] **Step 1:** Add `BenchmarkFrame50Agents3Hosts200Envelopes` (50 agents over 3 hosts, 200 envelopes per frame, as the existing benchmark builds its frames) and run it on the current code: record the number.
- [ ] **Step 2:** Implement the index. All existing store, board, compose and model tests pass unchanged (they are the proof that results did not change).
- [ ] **Step 3:** Run the benchmark five times. Expected: the median is under 4 ms and lower than step 1. Record both numbers in the commit message.
- [ ] **Step 4:** Run `go test -race ./internal/store/ ./internal/tui/`. Expected: PASS.
- [ ] **Step 5:** Commit: `perf(go): index calls per agent in the store`.

### Task 7: Cleanups from the P2b-2 follow-ups

**Files:** Create `internal/sshhost/sshhost.go`, `internal/safe/safe.go`; modify `internal/text/`, `internal/remote/`, `internal/hub/`, `internal/wire/`, `internal/server/`, `internal/cli/`, with their tests.

**Interfaces:**
- Produces: `text.JSTrim(s string) string` (moved from `remote.jsTrim`, same behavior and tests); `wire.MarshalNoEscape(v any) ([]byte, error)` (exported, `server` uses it and its copy is deleted); `safe.Call(fn func()) (recovered any)` (one helper for the `safely` closures of `hub` and `remote`); `sshhost.Valid(s string) bool` (moved from `remote.ValidSSHHost`; `cli` imports `sshhost`, not `remote`).

- [ ] **Step 1:** Move the existing tests with the code (no new behavior). Add `TestSafeCall` (returns the panic value, nil without a panic) and `TestMarshalNoEscape` (`<&>` stays literal).
- [ ] **Step 2:** Run `go test ./...`. Expected: FAIL (missing packages).
- [ ] **Step 3:** Implement the moves; delete the duplicates.
- [ ] **Step 4:** Run `go test -race ./internal/...`. Expected: PASS; `go list -deps ./internal/cli | grep internal/remote` prints nothing.
- [ ] **Step 5:** Commit: `refactor(go): consolidate small helpers`.

### Task 8: Freeze the parity tests as goldens (`internal/parity`, `testdata/golden/parity/`)

The spec requires that the Go and TypeScript streams match before the TypeScript is deleted. This task records the TypeScript side once, so the guarantee outlives the TypeScript code.

**Files:** Create `internal/parity/golden_test.go`, `testdata/golden/parity/{fixtures.json,demo-normal-60000.ndjson,demo-stress-10000.ndjson,hub.json,audit.json}`; modify `internal/parity/*_test.go`.

**Interfaces:**
- Consumes: the P2b-1 and P2b-2 parity helpers and normalizer, the real bun path resolution.
- Produces: with `PARITY_WRITE_GOLDEN=1` the live parity tests (which still need bun) write the normalized TypeScript results into the golden files; new tests `TestGoldenFixtures`, `TestGoldenDemoTrace`, `TestGoldenHubSnapshot`, `TestGoldenAudit` compare the Go side with the files and need no bun.

Rules: the stored fixtures result is the normalized form of `TestParityFixtures` (ordered envelopes, the status and timeout multisets, the last coverage). Demo traces store the TypeScript virtual-clock lines in full, `ts` included. Hub and audit goldens come from the Go-hub test inputs; their times are stored as `T+<ms>` offsets from the test's start time `t0` (the test writes its logs at `t0` plus fixed deltas), and every path under the temporary `HOME` is stored as `<HOME>/…` (both the `/var` and `/private/var` spellings). Golden JSON is written with sorted keys and two-space indent so diffs are readable.

- [ ] **Step 1: Write the golden tests and the writer.** `TestGoldenNonVacuous`: the fixtures golden holds at least 100 ordered envelopes, the normal demo golden at least 400 lines, the stress demo golden at least 2,000, the hub golden two hosts and at least 4 agents, the audit golden a Claude agent count above 0. `TestGoldenDetectsChange`: a copy of a golden with one value changed makes the comparison fail.
- [ ] **Step 2:** Run `PARITY_WRITE_GOLDEN=1 go test -count=1 -run 'TestParity' ./internal/parity/` once (needs bun). The live parity tests pass first (the Go side still matches the TypeScript side), then the goldens are written.
- [ ] **Step 3:** Run `go test -count=1 -run 'TestGolden' ./internal/parity/` with `bun` removed from `PATH` (`env PATH=/usr/bin:/bin go test …`, using the absolute path of `go`). Expected: PASS.
- [ ] **Step 4:** Run the whole parity package with bun on `PATH` once more. Expected: PASS (live and golden agree).
- [ ] **Step 5:** Commit: `test(go): freeze the TypeScript parity results as goldens`.

### Task 9: Delete the TypeScript

**Files:** Delete `src/`, `viewer/`, `test/` (after moving the fixtures), `scripts/`, `spikes/pixi-stress/`, `package.json`, `bun.lock`, `tsconfig.json`; move `test/fixtures/` to `testdata/fixtures/`; modify `internal/parity/`, `.gitignore`, Go comments.

Rules:
- `git mv test/fixtures testdata/fixtures`, then update the five Go readers (`internal/collect/state_test.go`, `internal/schema/decode_test.go`, `internal/logs/claude_test.go`, `internal/logs/codex_test.go`, `internal/parity/`).
- `internal/parity`: delete everything that needs bun or TypeScript (the live `TestParity*`, `TestParityLocal`, the `AGENT_VIEW_PARITY_LOCAL` mode, bun path resolution, `test/parity/`). Keep the normalizer, its tests, and the golden tests. `testdata/gitignore-cases.json` stays and `internal/fschange` keeps reading it.
- `.gitignore`: remove `node_modules/` and `test/.tmp/`; keep the rest.
- Go comments that name TypeScript files (`src/cli.ts`, `src/collector/…`, `test/…`) become "the former TypeScript <name> (removed in P2c; see git history)". The fake demo command texts in `internal/demo/demo.go` stay (they are data).
- Do not delete untracked or ignored local items: `node_modules/`, `work/`, `.claude/`, `dist/` contents. `spikes/*.ts` probes and `spikes/fswatch/` stay (the spec lists only `spikes/pixi-stress/`; the probes are evidence for `docs/superpowers/spikes/2026-11-17-m0-results.md`).

- [ ] **Step 1:** Make the changes. Run `git ls-files | grep -E '^(src|viewer|test|scripts)/|package.json|bun.lock|tsconfig.json|spikes/pixi-stress'`. Expected: no output.
- [ ] **Step 2:** Run `go build ./... && go test -count=1 ./... && go vet ./... && GOOS=linux CGO_ENABLED=0 go vet ./... && gofmt -l .`. Expected: all clean, with no skipped parity tests that mention bun.
- [ ] **Step 3:** Run `git grep -n -E 'src/cli\.ts|src/collector|src/hub|src/shared|src/viewer/(store|tree)|test/fixtures|test/parity|bun src' -- '*.go'`. Expected: no output (demo and test data strings such as `bun test` are not matched).
- [ ] **Step 4:** Commit: `chore: delete the TypeScript collector, hub and viewer`.

### Task 10: Build script and size check (`tools/dist`)

**Files:** Create `tools/dist/main.go`, `tools/dist/main_test.go`.

**Interfaces:**
- Produces: `go run ./tools/dist [-out dist] [-targets darwin-arm64,darwin-x64,linux-x64,linux-arm64] [-max-bytes 20971520]`. Per target: `GOOS`/`GOARCH` (`x64` is `amd64`), `CGO_ENABLED=1` for darwin and `0` for linux, `go build -trimpath -ldflags "-s -w" -o <out>/agent-view-<target> ./cmd/agent-view`. Darwin targets need a darwin host: on another host they fail with `darwin targets need a Mac (cgo)` and exit 1. It prints a table of target and size in MB, exits 1 when any binary is at or above `-max-bytes`, and smoke-tests the binary that matches the host: `collect --stdio --demo --label dist-check` must print a first line that decodes as a `hello`, within 10 s.
- Pure helpers for the tests: `type target struct{ name, goos, goarch string; cgo bool }`, `func targets() []target`, `func (t target) env() []string`, `func checkSizes(sizes map[string]int64, max int64) error`.

- [ ] **Step 1: Write the failing tests.** `TestTargetsEnv` (the four names, `x64` maps to `amd64`, cgo on for darwin only, `GOOS` and `GOARCH` set). `TestCheckSizes` (19.9 MiB passes, exactly 20 MiB fails, the error names the target). `TestBuildLinuxArm64` (builds `linux-arm64` into a temp directory: the file exists, starts with the ELF magic, is under the limit, and `go version -m <file>` lists `-trimpath=true`). `TestDarwinNeedsMac` (on a non-darwin host the darwin target returns the message; on darwin the test skips).
- [ ] **Step 2:** Run `go test ./tools/dist/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test ./tools/dist/` and `go run ./tools/dist -out /tmp/av-dist-check` on this Mac (all four targets). Expected: PASS; four binaries, each under 20 MiB; the smoke test passes; record the four sizes in the commit message. Remove the temporary output directory afterwards.
- [ ] **Step 5:** Commit: `feat(go): add the four-target build script with a size check`.

### Task 11: Performance gate on the whole process (`tools/perf`, a results doc)

**Files:** Create `tools/perf/measure.sh`, `cmd/agent-view/latency_test.go`, `docs/superpowers/spikes/2026-11-19-p2c-performance.md`.

**Interfaces:**
- Produces: `tools/perf/measure.sh <binary> <seconds> [args…]`. It runs the binary in a tmux session on a private socket (`av-perf-<pid>`), samples `ps -o %cpu=,rss=` of that process every second, and prints `cpu_avg_pct`, `cpu_max_pct`, `rss_peak_mb`. It stops only through that socket's `kill-server`. It reads agent data read-only and prints numbers only.
- `TestLatencyAppendToModel` (package `main`, no terminal): a stack on a temp `HOME` with one Claude session log; after the first scan, a tool call is appended to the log; the test measures the time until the bridge delivers an `EnvelopesMsg` that holds the call, over 20 appends. It logs p50 and p95, and fails above 10 s (a dead watcher or poll); the spec number (1 s) is recorded in the doc, not asserted, because a busy `fseventsd` delays events.

- [ ] **Step 1:** Write the latency test and the script; run `go test -run TestLatencyAppendToModel -v ./cmd/agent-view/` three times and note p50 and p95.
- [ ] **Step 2:** Build with `go run ./tools/dist -out <temp dir> -targets darwin-arm64` (never into `dist/`, which holds the owner's old binaries). Measure, each for 60 s: (a) the real `HOME` as it is, noting how many agents were working (the idle target applies at 0); (b) an idle scan: a temporary `HOME` holding copies of the newest 120 real logs with their modification times set to one hour ago (read-only on the real logs; the copy is removed afterwards; report counts only), which gives the memory number for "the 24-hour scan of 120 logs" and the idle CPU; (c) `--demo --demo-scale stress`. Also run the Task 6 benchmark once more.
- [ ] **Step 3:** Write `docs/superpowers/spikes/2026-11-19-p2c-performance.md`: environment (machine, load of `fseventsd` at the time), the five spec targets, each measured number, and whether each target is met. Counts and numbers only: no path, name, command or output from real logs.
- [ ] **Step 4:** Run the gate. Expected: all clean; no `tmux` server of this task remains; if a target is missed, the report says which and by how much.
- [ ] **Step 5:** Commit: `test(go): measure the whole process against the spec targets`.

### Task 12: README rewrite and superseded-section marks

**Files:** Modify `README.md`, `docs/superpowers/specs/2026-11-17-agent-view-design.md`, `docs/superpowers/spikes/2026-11-17-m0-results.md`.

Rules:
- `README.md` describes only the Go program, in sections: what it shows; install (build with `go run ./tools/dist`, copy one binary onto the `PATH`); run (flags, keys from the help screen, `NO_COLOR`, the non-terminal message); hosts (`--remote`, `BatchMode=yes` needs non-interactive SSH authentication, the remote needs `agent-view` on the `PATH` of a non-login shell, for example `/usr/local/bin`); headless (`serve`, `audit`, `collect --stdio`); build (cgo on macOS: Xcode Command Line Tools; Linux `CGO_ENABLED=0`; sizes); tests (`go test ./...`, the golden `-update` flag, tmux for the end-to-end tests, `tools/perf`); performance (numbers and a link to the Task 11 doc); package layout; security and privacy (read-only, `Host`/`Origin` checks, text sanitizing). It is at most 220 lines and has no Bun, npm, browser, viewer or `AGENT_VIEW_COLLECTOR` text.
- `2026-11-17-agent-view-design.md`: a status line at the top (`Superseded in part by 2026-11-18-agent-view-tui-design.md`) and one blockquote `> Superseded by 2026-11-18-agent-view-tui-design.md` under each of: the runtime constraint (in "Constraints"), "Architecture", "Command-line interface", "Viewer" (with its three subsections), "Packaging", "Performance targets", "Milestones", and the viewer parts of "Testing". Nothing else changes.
- `2026-11-17-m0-results.md`: one line at the top of section 3: the PixiJS probe code was removed in P2c.

- [ ] **Step 1:** Write the three changes.
- [ ] **Step 2:** Run `grep -n -i -E '\bbun\b|npm|browser|viewer|AGENT_VIEW_COLLECTOR' README.md; wc -l README.md`. Expected: no matches; at most 220 lines.
- [ ] **Step 3:** Run `grep -c 'Superseded' docs/superpowers/specs/2026-11-17-agent-view-design.md`. Expected: 9 or more (status line plus the sections above).
- [ ] **Step 4:** Run `go test -count=1 ./...` and `go vet ./...`. Expected: clean.
- [ ] **Step 5:** Commit: `docs: rewrite the README for the Go program and mark superseded sections`.
