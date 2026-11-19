# Agent View Go hub P2b-2 — follow-ups

Date: 2026-11-19. Branch: go-p2b2-hub. Plan: `docs/superpowers/plans/2026-11-18-agent-view-go-p2b2-hub.md`.

These are the findings that the per-task reviews and the final review deferred. The final fix wave fixed these, so they are left out: a remote child that ignored SIGTERM outlived `serve`; the server matched the decoded path (`/api/%73napshot` reached the route); the `TestServePortInUse` comment overstated what it guards; the README did not name SIGHUP, the stubborn-remote kill, or `BatchMode=yes`. Items resolved during the tasks are also left out (the oversize-line test, the coverage comments, the Task 8 report output).

## For P2c

- Delete `internal/feed` together with the TUI path that runs a child collector. Its process code is duplicated in `internal/remote`.
- Consolidate `safely` (in `hub` and `remote`) and the three subscriber types.
- Move `jsTrim` to `internal/text`.
- Export one no-escape marshal from `wire`. The server copies `wire.marshalValue`.
- Give the collector an injectable stderr writer. A test swaps `os.Stderr` now.
- A `Snapshot` panic in `greet` leaves a subscriber that got `hello` but no `snapshot` (Task 1). This matters once the TUI subscribes in-process.
- Hub subscribers run under the hub lock and can stall the collector loop. The TUI subscriber must not block.
- `cli` imports `remote` only for `ValidSSHHost`.

## Known differences from TypeScript (allowed)

The P2b-1 list still holds. Also:

- The decoder rejects a known kind with a field of the wrong type, or with a null or missing `agent` (`agent.upsert`), `call` (`tool.start`) or `coverage` (`coverage`). TypeScript forwards these lines. For a null `coverage`, TypeScript replays it, and that breaks the audit; Go keeps the last valid coverage.
- A known kind loses the fields it does not define.
- Struct field names match case-insensitively: `"AgentID"` sets `agentId`. TypeScript ignores it.
- `Raw` (an unknown kind) keeps invalid UTF-8 and duplicate members as received.
- `audit --port 0` or a port above 65535 exits with code 2. The TypeScript assert exits with code 1.
- The Go audit fails on any envelope it cannot decode. TypeScript reads only `snapshot` and `coverage`.
- `serve` also stops on SIGHUP.
- `AddSource` after `Stop` returns an error. TypeScript leaks a subscription.
- The hub does not mirror `sourceStatus` (TypeScript never reads it), and it has no `lingerMs` getter (only the dropped viewer used it).
- After a remote child is reaped, the read ends of its pipes close after a 2 s grace. A process outside the child's group that holds the pipes cannot block the respawn or `Stop`.
- A remote child leads its own process group. `Stop` sends SIGTERM and SIGKILL to the group, and each time the child exits, the rest of its group gets SIGKILL. TypeScript signals the child only.
- `serve` exits within 2 s. When the hub has not stopped after 1.5 s, `serve` sends SIGKILL to the process group of each remote child at once. TypeScript waits for the hub without a bound, and its remote sends SIGKILL to the child 2 s after SIGTERM.
- Bun folds dot segments in the request path: `/api/./snapshot` and `/api/x/../snapshot` get the snapshot in TypeScript. Go replies 404.

## Open questions

- A remote child stays when `serve` itself gets SIGKILL or crashes. TypeScript is the same.
- The interplay with ssh `ControlPersist` was reasoned about, not run.
- Hub CPU and memory were not measured. P2c sets the targets.
- `LineSplitter` (P2b-1 code) copies quadratically on a very long line.
- When `waitExit` cannot tell that the child has exited (a `waitid` or `kqueue` error), the remote ends the child's group after the reap, as before the fix wave. A signal can then reach a reused pid in a very small window.

## Deferred during task reviews

- **Collector hardening (Task 1):** no test that a panicking await or `Subscribe` item still closes its done channel; `TestTickRunsUnderSustainedLoad` can pass without the fix if the queue empties for a moment (gate the hook), and it fails the old build in most runs, not in all; a dead `d >= load` check; the recovered panic message has no stack.
- **Wire decoder (Task 2):** a known kind with a null or missing identifying field (`agentId`, `callId`, `agent.id`) decodes with zero values and is forwarded, as in TypeScript, so a faulty remote can add empty-ID entries to a mirror; `intField` applies the 2^53 bound only to float literals; each line is parsed about four times (lines go up to 8 MiB); test gaps: null and missing members, an escaped envelope key in `Raw`, duplicate members, the case-insensitive match.
- **Hub (Task 3):** host listeners can run concurrently and must not call `Stop` (documented); no test for `AddSource`'s late path (`Stop` during `Subscribe`) or the "hub is stopped" error; no test for a panicking host listener, an invalid `hello.now` (offset 0), `LocalSource` `Stop` racing `Start`, or `LocalSource` going down when `Start` fails (the collector's `Start` cannot fail in a test); `TestTickAgesMirrors` and `TestStopWhileCollectorDelivers` lack `t.Cleanup(h.Stop)`; the recover in `record` has no test.
- **Remote (Task 4):** `Stop` racing a respawn can start one more child after `Stop` (it gets SIGTERM at once; check `stopCh` before the spawn); `Stop` can take about 4 s (2 s to SIGKILL, then the 2 s grace) when an outside process holds the pipes (close the read ends at once after `Stop`); no tests for the 200-unit detail cut, the 4,096-unit stderr tail, or the `jsTrim` edge characters.
- **Server (Task 5):** the stuck-client test does not assert that the handler is mid-write, and its 1 s limit can flake under `-race`; no test that 404 comes before 403; the `Serve` error is ignored; no `IdleTimeout`; `net/http` protocol errors lack the security headers; the 500 path has no test; `Hosts` and `SnapshotAll` read in two lock steps (add a comment).
- **Audit (Task 6):** a wrapped JSON error can repeat a numeric literal (for example `number 1.5`), so make the decode error fixed text; the agent loops run in map order, so with two faults the message can change (the verdict does not); `Fetch` reads the body without a bound; test gaps: two snapshots (overwrite or accumulate), the tail cycle a→b→c→b, a confirmed write with an empty `AgentID`.
- **CLI and serve (Task 7):** `audit` ignores a stdout write error; a one-element loop in `args_test`; `collect serve` and `serve collect` give different usage messages.
- **Hub parity test (Task 8):** a hub that crashes mid-test orphans its `sh` and `sleep` child, which `pgrep -P` in `stop` cannot find (record the child PIDs after `waitStable`); a reused PID can get SIGKILL in `stop`; the URL regexp `$` can match half a line (use `\n`); `sessionLinks`, `writesByLapsedAgents` and the clock offset are not exercised.
