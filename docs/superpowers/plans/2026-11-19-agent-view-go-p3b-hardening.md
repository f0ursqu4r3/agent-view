# Agent View P3b Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A remote host or a signal can no longer leave the TUI in a bad state, and a failing host always shows a reason.

**Architecture:** `internal/remote` starts the child in its own session and times out a child that connects but never sends hello. `internal/bridge` resyncs after a panic, with a rate limit. The TUI model applies a repeated host state once. One signal watcher in `cmd/agent-view` covers the whole TUI run, from before the sources start until after the held diagnostics print.

**Tech Stack:** Go 1.27.3, Bubble Tea v2 (`tea.WithoutSignalHandler`), tmux for the end-to-end tests.

**Spec:** `docs/superpowers/specs/2026-11-19-agent-view-p3-design.md`, section "P3b Hardening". Source of the items: `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`, section "Hardening the owner must decide", and the "Deferred during task reviews" bullet for the bridge (Task 2).

## Global Constraints

- Every golden file stays byte-identical. Never run a test with `-update`. `git status --short testdata` must show nothing after any test run.
- No change to the event schema, the wire format, the CLI or any text the TUI draws, except the new host detail `no hello after 15 s`.
- The TUI writes nothing to the terminal while it runs. No test or script stops a process by name; stop only a PID the test started; tmux only on a private `-L` socket named `av-<test>-<pid>`, stopped by `kill-server` first.
- Each contract below has a test that fails without the change. Each implementer shows a mutation proof (command and failing output) in the report, in a temp copy outside the repo or fully reverted.
- No local path, user name or host name in a tracked file. No AI attribution in a commit message. Conventional Commits.
- Before each commit: `go build ./...`, `go vet ./...`, `go vet -tags avtest ./cmd/agent-view`, `GOOS=linux CGO_ENABLED=0 go vet ./...`, `gofmt -l .` (empty), the focused tests. Run `go test -count=1 ./...` once at the end of each multi-package task.
- Do not touch `.claude/`, `node_modules/`, `work/`, `dist/`, `test/`.

## Review Focus

1. **A deterministic panic must not make the bridge spin.** Without a limit, panic, resync and replay would repeat at full speed (Task 3 test).
2. **A child that exits inside the hello window keeps its own detail.** Exit 127 gives `agent-view not found on <host>` and exit 255 gives `ssh failed: …`, not the timeout text (Task 2 test).
3. **A hello that arrives as the timer fires must not be killed.** `-race -count=20` on the hello tests (Task 2).
4. **A second Ctrl-C or SIGTERM during shutdown must not lose diagnostics or orphan a child** (Task 5 end-to-end test, with a remote that ignores SIGTERM).
5. **`Setsid` must not break the group kill.** SIGTERM then SIGKILL still reach a grandchild of the child (Task 1 test).

---

### Task 1: child session and connect timeout

**Files:**
- Modify: `internal/remote/remote.go` (`spawn`, `DefaultCommand`), `internal/remote/remote_test.go`

**Interfaces:**
- Produces: `spawn` starts the child with `SysProcAttr{Setsid: true}` instead of `Setpgid: true` (a session leader leads its own group, so `killGroup(pid)` is unchanged; never set both). `DefaultCommand(sshHost, label)` returns exactly `ssh -T -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 <sshHost> agent-view collect --stdio --label '<label>'`.

- [ ] **Step 1: Write the failing tests**
  - `TestChildLeadsItsOwnSession`: run a helper child (the test binary as helper process, as `TestMain` does for `escapeEnv`) that prints its pid and `unix.Getsid(0)`; assert the sid equals its pid and differs from the test process's sid.
  - `TestStopKillsTheGroupOfASessionLeader`: the child is `sh -c 'sleep 600 & echo $!; wait'`; read the grandchild pid from stdout; after `Stop` the grandchild is gone (poll with `kill -0` through `syscall.Kill(pid, 0)`, deadline 5 s). It must pass with the old code too: it pins that `Setsid` keeps the group kill.
  - Update `TestDefaultCommand`: `cmd[:9]` is `ssh -T -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 user@host.example`; the rest as before.
- [ ] **Step 2: Run** `go test -run 'TestChildLeadsItsOwnSession|TestDefaultCommand' ./internal/remote/` — expected: FAIL.
- [ ] **Step 3: Implement** the two changes.
- [ ] **Step 4: Run** `go test -race -count=1 ./internal/remote/ ./internal/hub/ ./internal/parity/` — expected: PASS; `git status --short testdata` empty. Mutation proof: put `Setpgid: true` back; `TestChildLeadsItsOwnSession` fails.
- [ ] **Step 5: Commit** `fix(remote): start the child in its own session and cap the ssh connect time`

### Task 2: hello timeout

**Files:**
- Modify: `internal/remote/remote.go` (`run`, a new field and constant), `internal/remote/remote_test.go`

**Interfaces:**
- Produces: `const defaultHelloTimeout = 15 * time.Second`; `Remote.helloTimeout time.Duration` (tests shorten it before `Start`, like `stableUp`). `run` starts the timer when the child starts. If the timer fires before the first hello that sets the state `up`: `signalChild(SIGTERM)`, `SIGKILL` after `killAfter` (the existing `killC` path), and `run` returns the detail `no hello after 15 s` (whole seconds as `N s`, otherwise `N ms`), not `collector exited …`. The loop sets `down` with that detail and backs off as usual. A child that exits first keeps its own detail. A child that sent hello is never timed out. A stop or kill keeps its current behavior. Decide the hello-versus-timer race with a variable set in the hello case, not with `select` order.

- [ ] **Step 1: Write the failing tests** (stub children as the existing tests build them; `helloTimeout` 200 ms)
  - `TestSilentChildTimesOutWithADetail`: a child that sleeps and sends nothing; the status becomes `down` with a detail that starts `no hello after`, within 3 s; the child is gone.
  - `TestHelloBeforeTheTimeoutKeepsTheChild`: the child sends a hello line, then sleeps; after 600 ms the state is still `up`.
  - `TestChildThatExitsFirstKeepsItsOwnDetail`: exit 127 inside the window gives `agent-view not found on <host>`; exit 255 with a stderr line gives `ssh failed: <line>`.
  - `TestTimedOutChildIsStartedAgainWithBackoff`: after the timeout a second child starts after about `firstDelay` (use a small `BackoffMs`).
  - `TestHelloAtTheTimerEdge`: a child that sends hello after 190 ms with `helloTimeout` 200 ms, repeated 20 times in a loop with fresh remotes, is never reported down (flaky by design only if the race is wrong; run with `-race`).
  - `TestHelloDetailText`: `helloDetail(d time.Duration) string` (the formatting helper that `run` uses) gives `no hello after 15 s` for `defaultHelloTimeout` and `no hello after 200 ms` for 200 ms.
- [ ] **Step 2: Run** the new tests — expected: FAIL (first one waits for its deadline).
- [ ] **Step 3: Implement.** Add the timer through the existing `arm` helper and a `timedOut` flag read after the loop.
- [ ] **Step 4: Run** `go test -race -count=20 -run 'Hello|TimesOut|TimedOut' ./internal/remote/` and `go test -race -count=1 ./internal/remote/ ./internal/hub/ ./internal/parity/ ./cmd/agent-view/` — expected: PASS. Mutation proof: delete the timer case; `TestSilentChildTimesOutWithADetail` fails.
- [ ] **Step 5: Commit** `fix(remote): time out a child that never sends hello`

### Task 3: bridge recovers from a panic with a resync

**Files:**
- Modify: `internal/bridge/bridge.go` (`Config`, `loop`), `internal/bridge/bridge_test.go`

**Interfaces:**
- Produces: `Config.PanicResyncGap time.Duration` (zero means 5 s). The recover handler in `loop` (after the diagnostic line) sets the resync flag under `b.mu` and calls `poke()`, so the next `drain` resyncs from the hub (`resyncFromHub` drops `work` and `pending`, subscribes again, and the hub replays). It does this at most once per `PanicResyncGap`; a panic inside the gap is only reported. It never resyncs when the stop channel is closed (the stop path must still end). `run` and the stop path are otherwise unchanged.

- [ ] **Step 1: Write the failing tests** (read the existing bridge tests for the hub fixture and the `Send` stub)
  - `TestPanicInSendResyncsFromTheHub`: `Send` panics on the first `EnvelopesMsg` and works after that; assert the diagnostics hold `recovered a panic` and the resync line, and that the replayed `hello` and `snapshot` arrive in a later `EnvelopesMsg` within 2 s.
  - `TestRepeatedPanicsResyncOncePerGap`: `Send` panics on every `EnvelopesMsg`, `PanicResyncGap` is one hour; push 20 envelopes through the hub; the resync line appears exactly once.
  - `TestPanicWhileStoppingEnds`: `Send` panics on every `EnvelopesMsg`; `Stop` returns within 2 s and sends nothing after it returns.
- [ ] **Step 2: Run** the new tests — expected: FAIL (the first one never sees the replay).
- [ ] **Step 3: Implement** in `loop`'s recover handler and `Config`.
- [ ] **Step 4: Run** `go test -race -count=5 ./internal/bridge/` and `go test -race -count=1 ./internal/tui/ ./cmd/agent-view/` — expected: PASS. Mutation proofs: remove the flag set (first test fails); remove the gap check (second test fails).
- [ ] **Step 5: Commit** `fix(bridge): resync from the hub after a recovered panic`

### Task 4: a repeated host state adds no second diagnostic

**Files:**
- Modify: `internal/tui/batch.go` (`setState`), `internal/tui/model.go` (a `hostDetail map[string]string` field), `internal/tui/model_test.go`

**Interfaces:**
- Produces: `setState` adds the `<host> collector <state>: <detail>` diagnostic only when the host's state or detail differs from the last `StateMsg` of that host (a host without an earlier message counts as `connecting` with no detail). The HOST log line keeps its existing rule (state change only). A resync of the bridge sends every host state again; the model applies it once.

- [ ] **Step 1: Write the failing tests**
  - `TestRepeatedHostStateAddsNoSecondDiagnostic`: send `StateMsg{Host: "devbox", State: "down", Detail: "ssh failed: x"}` twice; the diagnostics hold one such line and the log one `HOST` line.
  - `TestChangedDetailAddsADiagnostic`: same state, a different detail, gives two lines.
  - `TestDownAgainAfterUpAddsADiagnostic`: down (x), up, down (x) gives two lines.
- [ ] **Step 2: Run** — expected: FAIL (first test shows two lines).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race -count=1 ./internal/tui/ ./internal/bridge/` — expected: PASS. Mutation proof: remove the comparison; the first test fails.
- [ ] **Step 5: Commit** `fix(tui): show a repeated host state once`

### Task 5: one signal watcher for the whole run

**Files:**
- Create: `cmd/agent-view/signals.go`, `cmd/agent-view/signals_test.go`
- Modify: `cmd/agent-view/main.go` (`runTUI`, a `beforeStart` hook beside `beforeRun`), `cmd/agent-view/avtest_hook.go`, `cmd/agent-view/tui_e2e_test.go`

**Interfaces:**
- Produces in `signals.go`: `type shutdownSignals struct{ … }`; `func watchShutdownSignals() *shutdownSignals` (registers SIGINT, SIGTERM and SIGHUP on one channel); `func (s *shutdownSignals) Requested() <-chan struct{}` (closed on the first signal); `func (s *shutdownSignals) Stop()` (unregisters, idempotent). After the first signal the watcher keeps reading and ignoring signals until `Stop`.
- `runTUI` uses it: call `watchShutdownSignals()` before `newStack` and `defer Stop`; add `var beforeStart = func() {}` and call it just before `st.start`; build the program with `tea.WithoutSignalHandler()`; replace the SIGHUP-only code by a goroutine that calls `p.Quit()` on `Requested()`; call `Stop` only after the held diagnostics have printed. A signal before `st.start` or before `p.Run` ends the run with exit code 0 and leaves no child (whether one frame is drawn first is the builder's choice). Exit code 0 for each of the three signals, as today. The default action must never apply between the start of `runTUI` and its return.
- `avtest_hook.go` (build tag `avtest`): `beforeStart` and `beforeRun` read `<HOME>/avtest-hook/signal-before-start` and `signal-before-run` (content `TERM`, `INT` or `HUP`) and send that signal to the process itself.

- [ ] **Step 1: Write the failing tests**
  - Unit (`signals_test.go`): `TestShutdownSignalsFirstSignalClosesRequested` for each of INT, TERM, HUP (`syscall.Kill(os.Getpid(), sig)`); `TestShutdownSignalsRepeatsAreIgnored` (three more signals: no panic, `Requested` stays closed, `Stop` returns); `TestShutdownSignalsStopTwice`.
  - End-to-end (`tui_e2e_test.go`, built with `-tags avtest` like `TestTUIProcessOwnWrites`; read it first; signal only the PID the test started):
    - `TestTUISignalBeforeStartExitsCleanly` (subtests TERM, INT, HUP): exit code 0, the stub remote never started, the terminal restored.
    - `TestTUISignalBeforeRunExitsCleanly` (same subtests): exit code 0, the stub remote that did start is gone.
    - `TestTUIRepeatedSignalsDuringShutdownKeepDiagnostics`: a remote that ignores SIGTERM (as `TestTUIQuitWithStubbornRemote` builds it), the own-write markers of `TestTUIProcessOwnWrites`; press `q`, then send SIGTERM twice 200 ms apart; the process exits 0, each marker prints exactly once, no stub is left.
- [ ] **Step 2: Run** the new tests — expected: FAIL (build failure counts as RED).
- [ ] **Step 3: Implement** `signals.go` and the `runTUI` changes.
- [ ] **Step 4: Run** `go test -race -count=1 ./cmd/agent-view/` and `go test -count=5 -run 'TestTUI|TestRunTUI' ./cmd/agent-view/` — expected: PASS 5 of 5. Mutation proofs: call `Stop` right after `p.Run()` returns (the repeated-signal test fails); register after `beforeStart` (the before-start test fails with a nonzero exit code).
- [ ] **Step 5: Commit** `fix: handle INT, TERM and HUP in one place for the whole TUI run`

### Task 6: gate and docs

**Files:**
- Modify: `README.md`, `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`, `docs/superpowers/followups/2026-11-19-agent-view-go-p3a-consolidation.md` (only where it names these items), the P3b follow-ups file below
- Create: `docs/superpowers/followups/2026-11-19-agent-view-go-p3b-hardening.md`

- [ ] **Step 1: Goldens and suite.** `go test -count=1 ./...`, then `git status --short testdata` and `git diff --stat main -- testdata` — expected: both empty.
- [ ] **Step 2: Stability.** `go test -count=5 -run 'TestTUI|TestRunTUI|TestServe' ./cmd/agent-view/` and `go test -race -count=20 -run 'Hello|TimesOut|TimedOut' ./internal/remote/` — expected: PASS every run.
- [ ] **Step 3: Race.** `go test -race -count=1 ./internal/remote/ ./internal/bridge/ ./internal/tui/ ./internal/hub/ ./cmd/agent-view/` — expected: PASS.
- [ ] **Step 4: README.** Line ~100: the ssh command gains `-o ConnectTimeout=10` in the exact order of Task 1. The hosts section says that a host that sends no hello in 15 s shows `down` with `no hello after 15 s` and starts again with the normal backoff, and that the remote child has no controlling terminal. Remove or fix any sentence that these changes make false (check "Known limitations" and the paragraph at line ~70). README at most 220 lines; `grep -n -i -E '\bbun\b|npm|browser|viewer|AGENT_VIEW_COLLECTOR' README.md` has no match.
- [ ] **Step 5: Follow-ups.** In the P2c file move hardening items (a), (b), (c), the bridge panic bullet, and the "Resync side effects" test-gap bullet to a "Done in P3b" list with the commits (verify each against the code first). In the new P3b file record what stays open (the signal watcher does not cover `serve`; a hello timeout does not catch a child that sends hello and then goes silent; anything the task reviews defer).
- [ ] **Step 6: Verify** `git grep -n -I -E '/U[s]ers/|k[y]le|d[o]ugan'` (no hit).
- [ ] **Step 7: Commit** `docs: record the P3b hardening`
