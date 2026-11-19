# Agent View Go collector P2b-1 — follow-ups

Date: 2026-11-18. Branch: go-p2b1-collector. Plan: `docs/superpowers/plans/2026-11-18-agent-view-go-p2b1-collector.md`.

These are the findings that the per-task reviews and the final review deferred. The final fix wave fixed these, so they are left out: a partial watch root restarted every other tick (the directory limit, ENOSPC, EMFILE, ENFILE, and an unreadable subdirectory); a dropped watcher hint for a new log; the `TestRealScheduler` clock flake; the README notes on cgo and exit time. Items resolved during the tasks are also left out (the Task 5 log shapes, now in the parity fixtures; the test helper names; the start of the fsnotify error loop; the fsnotify EINVAL error; the local parity guard).

## For P2c

- Three ordered or LRU implementations (final review M3): `internal/collect/ordered.go`, `internal/logs/ordered.go`, and `pathCache` in `internal/collect/paths.go`. Extract one generic package.
- The store and the lifecycle package keep two copies of the presence logic (`schema.Agent` and `wire.Agent`). Unify them.

## Known differences from TypeScript (allowed)

The plan's list still holds (U+2028 and U+2029 escapes, the `hello` fields). Also:

- An empty-string `command` or `pattern` is `""` in TypeScript and absent in Go (`wire.Call` uses `omitempty`). The parity normalizer removes it.
- A lone UTF-16 surrogate becomes U+FFFD.
- A time that is not RFC 3339 parses as invalid.
- An exit code beyond 2^53 prints differently. An exit code that does not fit an `int` is absent. The comment in `codex.go` overstates the parity.
- `isInteger` is false beyond ±2^63. JSON nested more than 10,000 levels deep is malformed.
- Go sorts the keys of `coverage.sources`.
- Go sorts strings with `strings.Compare` (byte order). JavaScript compares UTF-16 code units.
- A deleted root gives two degraded statuses in Go (the watcher error, then "root not found"). TypeScript gives one.
- The fsnotify backend does not degrade when a subdirectory is gone before it is watched (ENOENT or ENOTDIR). The TypeScript per-dir mode does.
- A partial watch root (a capacity or permission limit) is kept, not restarted. TypeScript restarts every degraded root, but it uses the recursive mode in production and does not hit these limits.
- After a dropped watcher hint, the log source rescans on the next poll. TypeScript has no hint buffer.
- `--watch ""` is rejected. TypeScript uses the current directory.
- `--linger` parses with `ParseFloat`. It accepts `1_0` and `0x1p3`, and it rejects `0x10`, which JavaScript `Number` accepts.

## Open questions

- macOS letter case: `EvalSymlinks` keeps the given case, but FSEvents reports the on-disk case. A `--watch` path typed in a different case can get no events. Not checked against Bun.
- The queue has no memory bound when the stdout reader is slow. TypeScript stdout buffering is the same.
- Linux uses one inotify instance per root. The default `max_user_instances` is 128.
- The fsevents library can print "failed to retrieve registry" to stderr on a late callback. It is harmless.
- A partial watcher is not restarted, so it does not add a directory that becomes readable later. It still adds new directories while the limits allow.
- After a partial limit, a later failure is not reported: `Degraded` keeps its first reason. The restarted watcher stays quiet when it reports the same reason again.
- While `fseventsd` is busy (100% CPU on the development Mac during the fix wave), the FSEvents tests time out after 10 s. The base commit fails the same way.

## Deferred during task reviews

- **Wire format (Task 1):** the no-HTML-escape test has no nested `Marshaler` case; `TestPlatform` mirrors the implementation; `EncodeLine` panics on a nil `Ev`.
- **Lifecycle (Task 2):** `WithDefaults` treats only 0 as unset; the store treats `<= 0` as unset.
- **Collector state (Task 3):** `Apply` forwards a pointer event unchanged (the collector passes values); a duplicate returns `(e, false)`, not a zero event; no wire-level check that an unattributed touch omits `agentId`; the file merge in `tool.update` is O(n²).
- **Log discovery and tailing (Task 4):** a relative `CODEX_HOME` or `CLAUDE_CONFIG_DIR` is not made absolute before `EvalSymlinks` (Node `realpath` returns an absolute path); `ErrCode` returns the `PathError` text for an errno it does not list; `TestScanLogsFirstErrorWins` has only one failing directory; `Get` cannot tell absent from `null`; no test for `skipFirst` with an oversized record that warns once.
- **Claude adapter (Task 5):** the second loop of `TestClaudeWrongFieldTypes` asserts only that there is no `ToolOutput`.
- **Codex adapter (Task 6):** the last loop of `TestCodexWrongFieldTypes` asserts only that nothing panics; `claude.go` keeps thin flush wrappers; a `newBoundedSet` parameter shadows the `max` builtin.
- **Tree watcher (Task 7):** no committed test for subtree tracking and `forget`; `TestTreeCloseStopsCallbacks` closes before events flow; `TestFsnotifyNewDirectories` does not prove that the read of a new directory reports its entries; the path where the root `Add` fails has no test; `fsutil.ErrCode` does not list `EINVAL`.
- **File-change source (Task 8):** a stale `ok` can follow a `degraded` in the window between `start` and `onChange`; the doc comment on `mu` overstates the lock rule (`Degraded` is called under `mu`, which is safe because it is atomic); the birth time and the mtime come from two `lstat` calls; a regexp compile error silently never matches (add a test that compiles every pattern); `TestDefaultHome` skips where the code falls back to `user.Current()`.
- **Log source (Task 9):** no test for the `Emit` panic recovery; `TestLogSourceStopDuringStart` stops at a different point than TypeScript (the stop check after `ScanLogs` has no test); the `OnWarning` panic swallow is not documented.
- **Demo source (Task 10):** a stale real-scheduler callback can run after a quick `Stop` and `Start` (check `timers[id]` under the lock); the runner goroutine never exits; `Start` rebuilds the normal projects, so the cursor resets on a restart; no test that a pending real callback is not emitted after `Stop`.
- **Collector (Task 11):** `TestStopDuringStart` rarely overlaps `Start`; the `TestStopIdempotent` comment overstates; the `Collector` doc says the loop is the only caller of subscribers, which is not true after `Stop`; the stderr output of a `SetRoots` panic has no test.
- **CLI (Task 12):** a `--linger` value that rounds to 0 ms becomes 1 ms; the shared `started` variable name; a test reads the exec stderr buffer without a lock on its failure path; no SIGHUP test.
- **Parity test (Task 13):** in local mode, `fs.change` events and `fs:` statuses from real projects reset the quiet timer; an unterminated last line is dropped silently; an interrupted run leaves the local copy in `TMPDIR`, and the command has no `WaitDelay`; the fixture test does not assert that the known `testdata/parity` IDs appear; local mode copies 120 logs, but each collector reads `floor(120 / providers)` per provider; `logsDiscovered == n` fails when two real roots collapse to one local directory; the `agent.upsert` and `tool.start` requirement fails when the newest 120 logs have no tool calls (a loud failure, not a false pass).
