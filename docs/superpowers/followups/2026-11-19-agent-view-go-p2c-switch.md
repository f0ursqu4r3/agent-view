# Agent View Go P2c switch — follow-ups

Date: 2026-11-19. Branch: go-p2c-switch. Plan: removed from the tree; read it with `git show b553ff5:docs/superpowers/plans/2026-11-19-agent-view-go-p2c-switch.md`.

These are the findings that the per-task reviews and the final review deferred. The review ledger of the run is not tracked, so this file is the only record after the merge. The final fix wave fixed these, so they are left out: no test pinned the stderr capture and `log.SetOutput` in `runTUI` (now `TestTUIProcessOwnWrites`); tests and a script that stopped processes by name; the `--skip 08` base-8 error in `tools/perf/measure.sh`; the untested environment override and the 500 ms deadlines in the `tools/dist` tests; the `.gitignore` comment position; the stale `feed.StateMsg` comment; the headline of the performance file and the README wording. Items resolved during the tasks are also left out.

P3a (branch `go-p3a-consolidation`) finished four items of this file. They are under "Done in P3a" below. The findings that the P3a task reviews deferred are in `docs/superpowers/followups/2026-11-19-agent-view-go-p3a-consolidation.md`.

P3b (branch `go-p3b-hardening`) finished the three hardening items, one bridge item, one test gap and one test-helper item of this file. They are under "Done in P3b" below. What stays open after P3b, and the findings that the P3b task reviews deferred, are in `docs/superpowers/followups/2026-11-19-agent-view-go-p3b-hardening.md`.

## Accepted gaps

- **Equal agent IDs on two hosts share one row.** The store keeps the agents of two hosts apart, but `Store.Agent`, `OpenCalls`, `RecentEnded` and `Counters` take an agent ID alone and answer from the first host that has it. The subtest `TestEqualAgentIDsOnTwoHosts` in `internal/tui/model_test.go` (about line 578) is skipped for this reason. The collector builds every agent ID as `<host label>/<provider>:…`, and the hub makes the labels unique, so IDs do not collide in practice. A collision needs a source that sends an ID without the label. Fix: identify a row by (host, ID) across store, board, anim and tui. It is a large refactor, so it was not done.
- **The coverage panel cuts the diagnostics in two ways.** With two or more hosts that have data, the panel needs more lines than a normal 80 × 24 box. The multi-host path (`internal/tui/panels.go`, from line 167) puts the diagnostics last, so a short box cuts the newest diagnostic lines first and shows `… N more lines`. The single-host path (line 216) keeps the newest lines and drops the oldest. The two paths are inconsistent. A host that is down before its first envelope has no block, so its reason can be hidden in a setup with three hosts. The common cases (local alone, local and one failing remote) show the reason. Fix idea: reserve the newest four diagnostic lines and cut host blocks instead.
- **A crash outside `main` and Bubble Tea cannot restore the terminal.** A panic or fatal error on a goroutine of a library (collector, hub, `ssh` readers) prints its text on the real terminal, because `debug.SetCrashOutput` points at the original fd 2. The terminal state (alternate screen, mouse mode) stays as it was. The user must run `reset`. This cannot be fixed in the process. It is in the `CaptureStderr` doc (`internal/quiet/stderr_unix.go`) and in the README.
- **A host state can be resent after the start.** The bridge subscribes before the sources start. A stale host state can reach the TUI once, and the next change corrects it. Fixing it needs hub changes (T2).
- **A snapshot that lists an open call for an agent it does not list.** The sweep deleted the call from the call table, but the old `openByAgent` index kept it. After the agent revived, `OpenCalls` returned a ghost open call that no `tool.end` could close. The new index follows the call table, so the call is not shown. This differs from the former TypeScript viewer store (T6).

## Test gaps

- **Resync side effects.** `sendHosts` (`internal/bridge/bridge.go`, line 304) sends every host state again after a resync: `resyncFromHub` clears the record of sent states first. No bridge test pins this. The bridge resend is accepted. The TUI half is done: `setState` shows a repeated state once (see "Done in P3b").
- **`exitWait` is 3 s, the requirement is 2 s.** The end-to-end helper in `cmd/agent-view/tui_e2e_test.go` allows 3 s. Only `TestStackStopBound` pins the 2 s bound. 3 s is also tight under `-race` and with a busy `fseventsd`.
- **Quit during a log scan.** No test quits while a log scan is mid-read.
- **An empty snapshot after a resync.** An empty snapshot does not clear the store (the P1 store rule), so "store equals hub" after a resync holds only for non-empty agents and open calls.

## Caveats of the final fix wave

These limits belong to the final fix wave. The earlier text of this file did not state them.

- **`log.SetOutput(diag)` is pinned only through the 4,096-byte cut.** `runTUI` calls `log.SetOutput(diag)` (`cmd/agent-view/main.go`, line 216). The `log` package writes to fd 2 by default, so the stderr capture also takes a short log line. A short log line therefore does not prove the call. `TestTUIProcessOwnWrites` fails without the call only because of its long log line. The capture cuts a line to 4,096 bytes (`maxLine` in `internal/quiet/stderr_unix.go`). The tail of the long line arrives only through `log.SetOutput`.
- **`go vet ./...` does not compile the `avtest` hook.** `cmd/agent-view/avtest_hook.go` has the build tag `avtest`. Also run `go vet -tags avtest ./cmd/agent-view`.
- **`TestServePortInUse` cannot detect a changed start order.** The test says that no remote child may start before the server binds. A change that calls `st.start` before `server.Start` in `runServe` (`cmd/agent-view/serve.go`) still passes: the test passed in 25 of 25 runs with that change.

## Deferred during task reviews

- **Diag hook (Task 1):** `TestPanickingDiagIsIgnored` does not assert that the hook ran; the `Diag` doc should say "safe for concurrent use"; the `captureStderr` cleanup order on a failing run; the greeting tests run at seq 0 only; test stubs rely on incidental nil panics. Out of scope: `snapshotAll` in `internal/hub/hub.go` (line 199) calls `m.state.Snapshot(now)` without a recover (see "Carried over").
- **Bridge (Task 2):** ended calls in a dropped window are not replayed; a host entry flushes the pending batch early (add a comment); test gaps: `Stop` flushes the pending batch, host states sent again after a resync, a replay that races live envelopes, the undecodable-line diagnostic appears once; `b.work[1:]` keeps the backing array. The recovered-panic part of this item is done (see "Done in P3b").
- **Stack (Task 3):** the comment of `TestNewStackInvalidRemote` says that the hub stops, but nothing observes it; `stack_test.go` lacks `t.Cleanup(stop)` on an early failure; `stack.start` panics on a nil `onErr` and has no guard against a second call; `localLabel` should move to `stack.go`.
- **TUI start (Task 4):** `restore` ignores the `Dup2` error; `diagWriter` details (the `sent` trim copies 256 entries per line, late lines beyond 256 are lost, an empty captured line becomes an empty diagnostic); `stack.stop` drops a hub-stop panic when `onErr` is nil (no production path); recovered panics report `%v` without a stack; a plain swap of `bridge.Start` and `st.start` is unguarded except by a comment (a race of microseconds); `unix.Dup` then `CloseOnExec` is not atomic (no child exists yet); the `g` branch of the demo-remote wait is unexercised.
- **Panels (Task 5):** no test for tiny boxes (height 3 to 4) or for CJK and long labels in the multi-host panel; the multi-host path cleans up to 200 diagnostic lines per frame while the panel is open (clean only the visible ones); comment why the single-host and multi-host paths are split (byte identity); name this file in the `t.Skip` of `TestEqualAgentIDsOnTwoHosts`; the left label overdraws the right border below about 22 columns (inherited, masked by `tooSmall`); the "something is cut" ellipsis check in the status sweep is weak.
- **Store index (Task 6):** `refOpen` is the new definition, not the old algorithm (add a comment); no dedicated regression test for the ghost-call fix; `unindex` returns silently on a missing call (panic instead, `Apply` recovers); inserting at the front of a newest-first slice is O(list length); the `OpenCalls` half of `TestRecentEndedReturnsACopy` checks nothing; the P1 follow-ups doc still describes the old slow spot; `board.findEnded` still asks for 2,000 ended calls (now a clone of one agent's list).
- **Helpers (Task 7):** no test for a panicking subscriber or listener in `internal/remote`; the "never `json.Marshal` an Envelope" warning is duplicated in two doc comments; other swallow-only defers remain (`collect.subscriber.send`, demo, logs source, watch) for a later pass. `safe.Call(nil)` returns early, where the old remote copy swallowed the nil-call panic (same visible result). The deleted `server.marshalNoEscape` had an error prefix `server: ` that nobody saw.
- **Goldens (Task 8):** the hub golden takes the local label to replace from the Go body itself, so a change in `localLabel()` would still pass (compute the expected label in the test); the hub time rule turns any RFC 3339 string into T+ms (accept only when `wire.FormatISO` round-trips it); the audit golden skips the freezer; a failed check can panic at `internal/parity/golden_test.go:498` (`d[0]` when `len(d)` is 0); `/var` and `/private/var` look the same after freezing.
- **TypeScript removal (Task 9):** ragged re-wrapped comment lines in `cmd/agent-view/collect.go`, `serve.go`, `internal/collect/collector.go` and `internal/fschange/source.go`; stale two-hub wording and a variadic `waitStable`/`startHub` name argument in `internal/parity/hub_test.go`; the golden README names commit `37a1695`. That is valid while `main` moves to this branch by fast-forward. A squash or a rebase orphans it: tag the commit first.
- **Build tool (Task 10):** the size display rounds 20,971,519 bytes to `20.0 MB ok` (floor to one decimal, or print two); `TestDarwinNeedsMac` skips on a Mac and leaves the `checkHost` wiring unexercised there (pass `hostGOOS` into `buildTarget`); the smoke failure subtests assert no duration; `chmod 0755` and `failed = true` after a smoke failure in `run` are unguarded; nits: the name `info2`, `os.ReadFile` of 9 MB to read 4 bytes.
- **Measurement (Task 11):**
  - M2: the performance file does not state the measurement resolution (10 ms `ps` tick, windows of about 1.04 s, peak RSS is the largest 1 s sample).
  - M4: in `tools/perf/measure.sh`, check `is_program` again before SIGKILL, and exit with 143 for SIGTERM (optional).
  - M5: polish `cmd/agent-view/latency_test.go`: the shadowed `before`, a dead final check, the `Cleanup` order, the 1 MiB goroutine dump buffer (let it grow), an integer percentile, one shared list of environment names with `isolatedEnv`.
- **README review (Task 12):** O1, the single-host coverage path keeps the newest diagnostics and the multi-host path cuts the newest first (see "Accepted gaps").

## Carried over from the earlier follow-ups

The P2b-1 and P2b-2 lists named these for P2c. P2c did not do them. P3a did three of them: the ordered copies, the presence rules and the subscriber types (see "Done in P3a"). The two items below are open.

- A `Snapshot` panic in `snapshotAll` (`internal/hub/hub.go`, line 199). `Subscribe` and `SnapshotAll` call it under the hub lock, and it has no recover. The collector is safe: `greet` recovers a `Snapshot` panic and sends an empty snapshot (0ab0320). In the hub, `snapshotAll` builds the whole list before `Subscribe` sends the first envelope. A panic there reaches the caller of `Subscribe` or `SnapshotAll`, and no subscriber gets a `hello` without a `snapshot`.
- Hub subscribers run under the hub lock and can stall the collector loop. The bridge subscriber never blocks, but another subscriber could.

## Observations

- The status bar shows "120 working" while the rows say STALE in a run with 10,000 logs. The status logic comes from earlier phases (the collector's presence rules). Nobody investigated it in P2c.
- The idle CPU target is met in steady state. The first scan after launch costs 1.1 to 1.5 CPU seconds, so the 60 s average from launch is 2.0% (83 logs, at the limit) and 2.8% (120 logs, over it). The README and `docs/superpowers/spikes/2026-11-19-p2c-performance.md` say so. If the owner reads the target as "the first minute too", the target is missed.
- Linux CPU, memory and latency are not measured. Only the sizes are.

## Done in P2c

These items of the P2b-1, P2b-2 and P1 lists are finished. The older files mark them.

- P2b-2, delete the child-collector path of `internal/feed`: `internal/feed/child.go`, its tests and its fake collector are gone (92197a6). The package keeps only the message types.
- P2b-2, consolidate `safely`: the copies in `hub`, `remote` and the bridge are `safe.Call` now (733a231). The three `subscriber` types are not consolidated. (Done in P3a, bd7a2ab.)
- P2b-2, `jsTrim` is `text.JSTrim` (733a231).
- P2b-2, one no-escape marshal: `wire.MarshalNoEscape`; `server` uses it (733a231).
- P2b-2, `cli` no longer imports `remote`: `sshhost.Valid` (733a231).
- P2b-2, an injectable collector diagnostics writer: `collect.Options.Diag` (0ab0320).
- P2b-2, the TUI subscriber must not block: the bridge queues each envelope under the hub lock and sends from its own goroutine (31cd951).
- P2b-2, hub CPU and memory: measured for the whole process (28e65b2).
- P1, the per-agent index in the store (a4de2d9): the frame costs 1.73 ms where it cost 3.86 ms.
- P1, the coverage panel shows every host (9c9d965).
- P1, diagnostics that arrive after the program exits: the program prints the held and late lines after it restores the terminal (92197a6). `diagWriter` has a test now.
- P1, the child-feed items (the `host` field comment, `upFor`, the stderr line bound, the environment override, empty `Argv`, a child that leaves the group) went with `internal/feed/child.go` (92197a6).
- P1, the README: `ctrl+z` and `--port` are listed, and the Bun flags table is gone (7d20411).

## Done in P3a

P3a is a refactor. It changed no golden file. These items of the P2b-1, P2b-2 and P2c lists are finished. The older files mark them.

- The ordered and LRU copies: four, not three. The lists named `internal/collect/ordered.go` (a map and an LRU set), `internal/logs/ordered.go` (a map and a bounded set) and `pathCache` in `internal/collect/paths.go`. The store had a fourth, `internal/store/omap.go`. All four are gone. `internal/ordered` holds one generic `Map` and one `LRU` (baf60dd). The store uses them (9dfc4f8). `collect` and `logs` use them (22fc4bf).
- The three `subscriber` types in `internal/collect`, `internal/hub` and `internal/remote`. `internal/fanout` holds the shared core: a callback that recovers a panic, and a removed flag. Each package keeps its own list of `*fanout.Sub`, its own lock and its own `Removed` check (bd7a2ab).
- The two copies of the presence rules. The store deleted `internal/store/lifecycle.go`. It builds `lifecycle.Row` values from its millisecond times and calls `lifecycle.PresentIDs` and `lifecycle.IsStale`. The default durations live in `internal/lifecycle` (a3abce4). One change in behavior is accepted: see the P3a file.
- The dead `host == ""` path in `setState` (`internal/tui/batch.go`). The old code replaced every per-host state and took the first store host as the label. Now an empty `Host` is the label `local` and touches no other host. The `feedState` field is gone, and `stateConnecting` replaces it (cc18e44). The bridge always sets `Host`, so the frame does not change for any input the bridge sends.

## Done in P3b

P3b changed no golden file. These items of this file are finished. The P3b file lists what stays open.

- (a) The remote child kept the controlling terminal. `spawn` in `internal/remote/remote.go` now starts the child with `Setsid: true` (62dda32). The child has no controlling terminal, so a read or write on `/dev/tty` fails at once. A session leader leads its own process group, so the group kill is unchanged (`TestStopKillsTheGroupOfASessionLeader`).
- (b) No connect timeout and no hello timeout. `DefaultCommand` now has `-o ConnectTimeout=10` (62dda32). A child that sends no hello in 15 s gets SIGTERM, and its host shows `down` with `no hello after 15 s` (dbe3926). The retry uses the normal backoff.
- (c) Signal gaps in `runTUI`. One watcher (`cmd/agent-view/signals.go`) takes INT, TERM and HUP from before the sources start until after the diagnostics print. The first signal quits the program with exit code 0, also before `p.Run`. Later signals are ignored (59bda29, 1b67cab).
- Bridge (Task 2), the recovered panic. `recovered` in `internal/bridge/bridge.go` now sets the resync flag and wakes the run goroutine. The replay from the hub brings the TUI back after a panicking `Send` (78c208d). A resync follows at most once per `PanicResyncGap` (5 s).
- Test gap, resync side effects, the TUI half. `setState` in `internal/tui/batch.go` adds the "collector" diagnostic only when the state or the detail differs from the last message of the host (a6c9558). The bridge still sends every host state again after a resync.
- TUI start (Task 4), the `startTUI` cleanup that left a hung program running. The cleanup now sends SIGKILL to the pane PID that `tmux` reports, before `kill-server`, and waits until the pane is dead (1b67cab). The guard is the pane state, not the command name.
