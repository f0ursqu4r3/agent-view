# Agent View Go P3a consolidation — follow-ups

Date: 2026-11-19. Branch: go-p3a-consolidation. Plan: `docs/superpowers/plans/2026-11-19-agent-view-go-p3a-consolidation.md`. Spec: `docs/superpowers/specs/2026-11-19-agent-view-p3-design.md`.

P3a is a refactor. It keeps every behavior and every golden file, with the one accepted change below. The finished items of the older lists are in `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`, under "Done in P3a". The review ledger of the run is not tracked, so this file is the only record of the findings that the task reviews deferred.

At the end of P3a, the full test suite and the race tests of the changed packages pass. The frame benchmark `BenchmarkFrame50Agents3Hosts200Envelopes` has a median of 1,540,997 ns/op. The base commit has 1,555,864 ns/op in the same session (ratio 0.99; five alternating runs of each).

## Accepted change in behavior

- **A far-future event time stays on the board.** The store no longer formats `lastEventMs` as text and parses it back for the presence check. A UTC event time outside the years 0 to 9999 (for example `9999-12-31T23:00:00-14:00`, or an extreme hello skew) now counts as its real far-future value. Such an agent stays present and is never stale until the sweep drops it. Before P3a, such a time counted as the epoch. No real event is in this range.

## Deferred during the P3a task reviews

- **Ordered (Task 1):**
  - `TestLRUNegativeLimitHoldsNothing` (`internal/ordered/lru_test.go`, line 92) hangs until the 10 min test timeout if the guard in `LRU.Set` goes (the `First()` check that ends the eviction loop). Fix: run `Set` in a goroutine with a deadline.
  - No `testing.AllocsPerRun` test shows that `LRU.Get` allocates nothing. A reviewer measured 0 allocations for a `Get` hit, a `Set` of a present key and `Has`.
  - `LRU.Get` does two map lookups for each hit (`Map.Get`, then `moveToBack`). The old `pathCache.get` did one.
  - The doc comment of `Map.All` (`internal/ordered/map.go`) says "deleting the next key ends the loop early". The loop yields the deleted entry once, and then it stops. The old `omap.all` did the same. Fix: change the comment.
- **Collect and logs (Task 3):** No test pins the choice of `Has` or `Get` at the call sites. Nothing fills `endedLimit` (20,000, `internal/collect/state.go`) or `MaxPending` (5,000, `internal/logs/output.go`). This gap exists since before P3a. A reviewer swapped every `Has` for `Get`, and all tests still passed. Fix: add eviction-order tests for `State.Apply` (`internal/collect`) and for `CodexAdapter` (`internal/logs`).
- **Fanout (Task 4):**
  - `TestRemovedIsSafeUnderRace` (`internal/fanout/fanout_test.go`, line 60) loops `for !s.Removed() {}` with no bound. A broken `Remove` makes it spin until the test timeout. Fix: bound the loop with a deadline. The last `Removed()` check after `wg.Wait()` adds nothing.
  - No change that a reviewer made to the code made `TestSendCallsFnInOrder` fail.
- **Lifecycle (Task 5):**
  - No store test pins the stale boundary. Only the lifecycle tests fail when `>` becomes `>=` in `IsStale`.
  - A broken cycle guard in `lifecycle.PresentIDs` hangs the tests until the timeout. It does not fail them at once.
  - `PresentIDs` and `IsStale` call `WithDefaults()` on options that the store already resolved (`internal/store/store.go`, line 145). This is cheap and harmless.
- **TUI (Task 6):**
  - `TestEmptyHostStateIsTheLocalLabel` (`internal/tui/model_test.go`, line 496) cannot catch a first-store-host label that someone adds again, because the store is empty in that test. Fix: send one envelope batch from host `box` first.
  - `hostState` (`internal/tui/status.go`, lines 64 and 65) still uses the literal `"connecting"`. The constant `stateConnecting` exists in `internal/tui/batch.go`.
  - `TestModelFeedDown` now expects the log line `HOST  devbox down`. The old line, `HOST  local down`, came from the fallback for a state without a host. The test now sends the label `devbox`.
