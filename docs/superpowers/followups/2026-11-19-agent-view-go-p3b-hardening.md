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

The final fix wave fixed T1-M1, T1-M2, T1-M3, T2-M1, T2-M2, T3-M1, T3-M3, T4-M1, T5-M3, T5-M5, T5-M6 and T5-M8. This list leaves them out. The items below stay open.

- **Hello timeout (Task 2):**
  - **T2-M3.** `helloDetail` (`internal/remote/remote.go`, line 408) gives `no hello after 0 s` for zero and `no hello after -5 s` for a negative time. No production path sets such a value.
  - **T2-M4.** When the timer sends SIGTERM and then `Stop` begins, the child gets a second SIGTERM. The `stopC` case does not check `timedOut`. This is harmless and not tested.
- **Bridge (Task 3):**
  - **T3-M2.** A batch lost inside the gap is not recovered. See "What stays open by design". A panic inside the gap leaves half-handled work in `b.work` and `pending` until the next wake or tick. `recovered` returns before `poke` inside the gap.
  - **T3-M4.** The resync after a panic reuses the overflow text. See "What stays open by design".
  - **T3-M5.** A `PanicResyncGap` at or below `Batch` would repeat the panic and the replay at the batch rate. The CLI cannot set the gap, so this is not reachable.
  - **T3-M6.** A panic that repeats inside `Hub.Subscribe` leaves the bridge with no hub subscription. A resync follows a panic at most once for each gap, so the retries end. The TUI then gets no more envelopes. This case existed before P3b.
- **Repeated host state (Task 4):**
  - **T4-M2.** No test sends an empty `Host` twice, or a repeated message while the board is paused. Both work as intended.
- **Signals (Task 5):**
  - **T5-M4.** `TestTUISignalBeforeRunExitsCleanly` (`cmd/agent-view/tui_e2e_test.go`, line 620) only logs how many stub PIDs it recorded. It does not require one.
  - **T5-M7.** The counter `shutdownSignals.read` (`cmd/agent-view/signals.go`, line 22) serves mostly the tests. The loop uses it only to find the first signal.
  - **T5-M9.** `TestTUISignalBeforeRunExitsCleanly` relies on a fixed 100 ms sleep in the test hook (`cmd/agent-view/avtest_hook.go`, line 47). A heavily loaded machine could let the old `p.Quit()` bug pass. The production code needs no timing.
  - **T5-M10.** The cleanup of `newStubPIDs` (`cmd/agent-view/serve_test.go`, line 179) kills only the PIDs in the file. A stub that has started but not yet written its PID is not killed. The window is a few ms.

## Found by the final review

- **A SIGTERM to a suspended TUI leaves one frame on the normal screen.** In bash, `kill %1` on a suspended TUI sends TERM and CONT. Bubble Tea restores the terminal from the background process group, and the process stops again on SIGTTOU. After `fg`, the pending quit runs: the exit code is 0 and the alternate screen is off. But one full frame stays on the normal screen and in the scrollback. Before P3b, a TERM during the suspend was lost, because Bubble Tea sets `ignoreSignals` to 1 while the program is suspended. No code change is planned.
- **The 15 s hello timer starts with `ssh`, not with the connect.** The timer in `run` (`internal/remote/remote.go`, at `helloTimerC`) starts after the spawn. The connect time, up to 10 s, is part of the 15 s. A host with a connect time near 10 s has about 5 s for its hello. The spec says "connects but sends no hello in 15 s". The README says what the code does.
- **No test opens `/dev/tty` in the child.** The spec says that a `/dev/tty` use by a wrapper fails at once, with a reason in the diagnostics of the host. `TestChildLeadsItsOwnSession` pins the session, so the child has no controlling terminal. No test opens `/dev/tty`. The reason reaches the diagnostics only when `ssh` exits with 255 (`ssh failed: <stderr>`). A wrapper that exits with 1 shows `collector exited 1`, with no stderr text.
