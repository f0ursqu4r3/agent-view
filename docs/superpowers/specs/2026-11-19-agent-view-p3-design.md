# Agent View P3 — hardening, correctness, consolidation, Linux check

Date: 2026-11-19. Status: draft for owner approval.
Builds on: `2026-11-18-agent-view-tui-design.md` (P1 to P2c are merged) and `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`. Nothing in the TUI spec changes. P3 pays off the known defects of the Go program before any new feature.

## Intent

The Go program works and meets its targets on the owner's Mac. The follow-ups list holds the defects that the reviews found and the owner has not yet decided. P3 closes that list and checks that the program also runs on Linux. The owner chose all four themes.

## Goals

- A remote host or a signal can no longer leave the TUI in a bad state, and a failing host always shows a reason.
- Two hosts can never merge their agents into one row, and the coverage panel never hides the newest diagnostics.
- One implementation of each duplicated piece of code, with no change in output.
- The tests, the build and the performance gate run on Linux, and the numbers are published with their limits.

## Non-goals

- New features, new themes (green and amber stay deferred), Windows, changes to the event schema or the wire format.
- Linux `amd64` runtime measurements. This check runs on `arm64` only; `amd64` stays cross-built and size-checked.

## Anti-goals

- A refactor that changes any golden file or slows the frame by more than 10% (1.73 ms today).
- Anything that makes the TUI write to the terminal while it runs.
- A test or script that stops a process by name.

## Order and why

Four sub-phases, one plan each, in this order:

1. **P3a Consolidation.** The other three touch the same files (`remote`, `hub`, `collect`, `store`, `tui`). Doing it first means each later fix lands once.
2. **P3b Hardening.**
3. **P3c Correctness.**
4. **P3d Linux check.** Last, so it measures the final code.

Alternative considered: hardening first, because it fixes real (if rare) failures. Rejected: P3a rewrites the code that P3b edits, so P3b would be done twice.

## P3a Consolidation

- One generic ordered map with LRU eviction replaces the three copies in `internal/collect/ordered.go`, `internal/logs/ordered.go` and `pathCache` in `internal/collect/paths.go`. `internal/store/omap.go` looks like a fourth copy; the plan checks it.
- One shared core for the three `subscriber` types (`internal/collect`, `internal/hub`, `internal/remote`): the removed flag and the send that survives a panic. Each package keeps its own greeting and ordering rules.
- One presence implementation for `internal/store/lifecycle.go` and `internal/lifecycle`.
- Remove the dead `host == ""` branch of `setState` in `internal/tui/batch.go`, with its tests, and fix the comments that mention it.
- **Done when:** every golden is byte-identical, `go test ./...` passes, and `BenchmarkFrame50Agents3Hosts200Envelopes` is within 10% of 1.73 ms.

## P3b Hardening

Each item is a contract. The builder chooses the mechanism.

- **Remote child on its own session.** The child has no controlling terminal. A `/dev/tty` write or read by a wrapper fails at once with a reason in the host's diagnostics. The group kill still works. *My call: `Setsid`.*
- **Timeouts.** The default `ssh` command has `-o ConnectTimeout=10`. A host that connects but sends no hello in 15 s goes `down` with the detail "no hello after 15 s" and restarts with the normal backoff. *My call: 10 s and 15 s.*
- **One signal handler.** SIGINT, SIGTERM and SIGHUP are handled from before the sources start until after the held diagnostics print. The first signal starts a normal quit (also before `p.Run` starts). Repeated signals during shutdown are ignored; the shutdown is still bounded by `stopWait`. No remote child is orphaned.
- **Bridge panic.** A panic in the bridge loop or in `Send` sets the resync flag and wakes the loop. No queued work waits for the next envelope.
- **Resync diagnostics.** The "collector down" line of a host appears once per change of state. A resync does not repeat it.
- **Done when:** each contract has a test that fails without the change (a mutation proof, as in P2c), and the end-to-end tests still pass 5 times in a row.

## P3c Correctness

- **Row identity (host, agent ID).** `Store.Agent`, `OpenCalls`, `RecentEnded` and `Counters` take a typed key `AgentKey{Host, ID}` and the board, animation and TUI use it, so equal IDs on two hosts give two rows. `TestEqualAgentIDsOnTwoHosts` is no longer skipped. *My call: a typed key everywhere, not a hidden rewrite of the ID inside the store, because a hidden rewrite shows up later as a wrong ID on screen.*
- **Coverage panel.** The panel always shows the newest 4 diagnostic lines when any exist. When the box is too small it cuts host blocks first and says "… N more hosts". The single-host and multi-host paths follow the same rule.
- **"120 working" while the rows say STALE.** A diagnosis comes first: reproduce with generated logs, name the cause, then decide. The doc does not prejudge the fix. *Done when:* the cause is written in the follow-ups doc and a test pins the behavior that the owner keeps.
- **Done when (all):** goldens that change are explained one by one in the commit message.

## P3d Linux check

- **Target (owner's choice):** the Linux VM that is already running on this Mac (Colima, `aarch64`, 12 CPUs, 32 GiB). The numbers come from a VM, not bare metal. Every document says so.
- **Files must live on the VM's own disk.** A copy of the repo is made inside the VM, not the macOS mount. File events from the other side of a shared mount do not reach `inotify`, so a test on the mount would measure the mount.
- **Tools.** The VM needs Go 1.27.3 and `tmux`. Both are downloads, so the owner is asked before the first one.
- **Work:** `go test ./...` on `linux/arm64` (fix Linux-only failures), a native build with `go run ./tools/dist -targets linux-arm64` and its smoke test, the tmux end-to-end tests, the latency test, and a Linux path in `tools/perf/measure.sh` (its `ps -o time=` parsing assumes the macOS format with hundredths of a second; the plan checks and reads `/proc/<pid>/stat` if needed).
- **Output:** a spike doc with CPU, memory, latency and frame time under the same method as `2026-11-19-p2c-performance.md`; the README performance section gets a Linux line.
- **Done when:** the suite is green on Linux or each failure is a recorded, owner-visible ruling.

## Constraints that carry over

- The SDD process of P2c: one plan per sub-phase, a task review after each task, a final review, one fix wave, merge only on the owner's word.
- Process safety: never stop a process by name, on the Mac or in the VM; tmux only on a private `-L` socket stopped by `kill-server` first. Real logs are read only, with counts only in any document. No host name, user name or local path in a tracked file.
- No AI attribution in commits.

## Left to the builder

Package names and file layout, the generic type's API, how the signal handler works with Bubble Tea v2, the exact diagnostic texts (except the two named above), and how the repo copy gets into the VM.
