# Agent View Go P3b hardening — follow-ups

Date: 2026-11-19. Branch: go-p3b-hardening. Plan: `docs/superpowers/plans/2026-11-19-agent-view-go-p3b-hardening.md`. Spec: `docs/superpowers/specs/2026-11-19-agent-view-p3-design.md`.

P3b hardens the remote child, the timeouts, the signals, the bridge and the repeated host state. The finished items of the older lists are in `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`, under "Done in P3b".

The review ledger of the run is not tracked. This file is the only record of what stays open and of the findings that the task reviews deferred.

P3b changes no golden file. It changes no text that the TUI draws, except one new host detail: `no hello after 15 s`.

## What stays open by design

- **The signal watcher covers the TUI only.** `serve` and `collect` keep their own handler (`collectSignals` in `cmd/agent-view/collect.go`). P3b did not change it.
- **A hello timeout does not catch a child that sends its hello and then goes silent.** `ServerAliveInterval=15` ends a dead connection. It does not end a live collector that sends nothing.
- **`helloTimeout` and `PanicResyncGap` are not configurable.** `helloTimeout` is 15 s (`internal/remote/remote.go`). `PanicResyncGap` is 5 s (`internal/bridge/bridge.go`). Only tests change them. They have no flag and no environment variable.
- **A host whose login takes more than 15 s now loops.** It shows `down` with `no hello after 15 s`, waits with backoff, and starts again. Slow `sshd` name lookups (`UseDNS`, GSSAPI) and a heavy shell start file cause this. Before P3b, such a host waited as `connecting` until its hello came.
- **The hello timeout has two small windows.**
  - The timer can end while the reader delivers the hello, before the state is `up`. The timer then ends a child whose hello is on its way.
  - The reader can read a hello after the timer sent SIGTERM. The host then shows `up` for a short time and goes `down` after.
  - `TestHelloAtTheTimerEdge` and `TestHelloCountedWhenTheTimerEndsKeepsTheChild` pin the rest: a hello that the Remote counted before the signal is never ended.
- **A batch lost inside the panic gap is not recovered.** After a panic, the bridge resyncs at most once for each `PanicResyncGap`. A panic in `Send` inside the gap loses its batch. The TUI misses it until the next resync (a later panic outside the gap, or a queue overflow). This is the cost of the rate limit (T3-M2).
- **The resync after a panic reuses the overflow text.** The diagnostic is `bridge: the display fell behind; resynced from the hub`. The panic line before it names the real cause. A distinct text needs a reason field and changes three tests (T3-M4).
- **Real `ssh` is not tested.** All tests use stub commands. Nobody checked how `ConnectTimeout=10` acts on the banner and the key exchange. Nobody checked `ssh` setups with FIDO keys, an askpass program or a `ProxyCommand` in a child that has no controlling terminal.
- **Some signal paths are not tested.** No end-to-end test sends INT while `p.Run` runs: `TestTUISignals` sends TERM and HUP. The tests send every signal with `kill`. A real terminal close, and a SIGHUP after a `ctrl+z` suspend, are checked by reading the code only.
- **Linux is not run.** `GOOS=linux CGO_ENABLED=0 go vet ./...` passes. No P3b test ran on Linux.
- **A panic stops the signal watcher before the crash text prints.** The deferred calls run first, so the three signals have their default action again. The program exits with a code other than 0. The crash limitation in the README is unchanged.

## Deferred during the P3b task reviews

- **Remote child (Task 1):**
  - **T1-M1.** `TestStopKillsTheGroupOfASessionLeader` (`internal/remote/remote_test.go`, line 558) kills the grandchild in a cleanup, after `waitGone` showed it gone. A reused PID could get the signal. This is theoretical. `TestRemoteEscapedPipeHolder` (line 696) has the same cleanup kill.
  - **T1-M2.** The helper process in `TestMain` (`internal/remote/remote_test.go`, line 36) calls `os.Exit(2)` with no message when `Getsid` or the file write fails. The test then reports a 15 s timeout, not the cause.
  - **T1-M3.** In `TestChildLeadsItsOwnSession` (line 526) the check `len(data) > 0` is redundant. The helper writes the file with an atomic rename.
- **Hello timeout (Task 2):**
  - **T2-M1.** `run` (`internal/remote/remote.go`, line 361) keeps a `sawHello` flag and an `exitedC != nil` check in the timer case. `signalSilentChild` makes both redundant, because it checks the child and the state under the lock. `readStdout` has its own `sawHello` with another meaning. Remove the two checks.
  - **T2-M2.** `TestSilentChildTimesOutWithADetail`, `TestHelloBeforeTheTimeoutKeepsTheChild` and `TestHelloCountedWhenTheTimerEndsKeepsTheChild` use a 200 ms hello timeout. They assume that `sh` writes its PID or its hello in that time. A slow start fails them. Fix: wait with `until`, or use about 500 ms.
  - **T2-M3.** `helloDetail` (line 409) gives `no hello after 0 s` for zero and `no hello after -5 s` for a negative time. No production path sets such a value.
  - **T2-M4.** When the timer sends SIGTERM and then `Stop` begins, the child gets a second SIGTERM. The `stopC` case does not check `timedOut`. This is harmless and not tested.
- **Bridge (Task 3):**
  - **T3-M1.** The comment in `recovered` (`internal/bridge/bridge.go`, lines 221 to 224) says "so the diagnostics stay bounded". This is true only for identical lines. Lines that differ each time give one diagnostic for each batch tick (about 20 each second). They can push useful lines out of the 200-line buffer of the TUI. Change the comment to say "identical lines".
  - **T3-M2.** A batch lost inside the gap is not recovered. See "What stays open by design".
  - **T3-M3.** `TestPanicWhileStoppingEnds` (`internal/bridge/bridge_test.go`, line 592) says that the panic is in the stop path. The panic is in the flush of the batch timer while `Stop` waits. A panic in the stop path itself has no test. Only an experiment checked it.
  - **T3-M4.** The resync after a panic reuses the overflow text. See "What stays open by design".
  - **T3-M5.** A `PanicResyncGap` at or below `Batch` would repeat the panic and the replay at the batch rate. The CLI cannot set the gap, so this is not reachable.
  - **T3-M6.** A panic that repeats inside `Hub.Subscribe` leaves the bridge with no hub subscription. A resync follows a panic at most once for each gap, so the retries end. The TUI then gets no more envelopes. This case existed before P3b.
- **Repeated host state (Task 4):**
  - **T4-M1.** Two mutations of `setState` (`internal/tui/batch.go`) survive the tests. The first stores `hostDetail` only when it is not empty. The second compares the raw detail, not the cleaned one. Add a test with `down(x)`, `down("")`, `down(x)` that expects two lines. Add a test with an escape sequence in the detail.
  - **T4-M2.** No test sends an empty `Host` twice, or a repeated message while the board is paused. Both work as intended.
- **Signals (Task 5):**
  - **T5-M3.** `TestTUIRepeatedSignalsDuringShutdownKeepDiagnostics` (`cmd/agent-view/tui_e2e_test.go`, line 635) only logs a failed first `kill`. If the shutdown ended first, the test does not test anything. Make the failure fatal.
  - **T5-M4.** `TestTUISignalBeforeRunExitsCleanly` (line 617) only logs how many stub PIDs it recorded. It does not require one.
  - **T5-M5.** `waitExit` (line 175) checks `alternate_on` before `pane_dead_signal`. A death by signal during `p.Run` then does not name the signal. Swap the two checks.
  - **T5-M6.** In `runTUI` (`cmd/agent-view/main.go`, line 199) `watchShutdownSignals()` comes after the `RemoteCommandOverride` check (line 191). Move it above that check.
  - **T5-M7.** The counter `shutdownSignals.read` (`cmd/agent-view/signals.go`, line 22) serves mostly the tests. The loop uses it only to find the first signal.
  - **T5-M8.** The cleanup in `startTUI` (`cmd/agent-view/tui_e2e_test.go`, line 72) has a window of about 10 ms. It checks `pane_dead`, reads `pane_pid` and sends the kill in three steps, so the PID could be reused. One `list-panes -F '#{pane_dead} #{pane_pid}'` call would narrow it.
  - **T5-M9.** `TestTUISignalBeforeRunExitsCleanly` relies on a fixed 100 ms sleep in the test hook (`cmd/agent-view/avtest_hook.go`, line 47). A heavily loaded machine could let the old `p.Quit()` bug pass. The production code needs no timing.
  - **T5-M10.** The cleanup of `newStubPIDs` (`cmd/agent-view/serve_test.go`, line 179) kills only the PIDs in the file. A stub that has started but not yet written its PID is not killed. The window is a few ms.
