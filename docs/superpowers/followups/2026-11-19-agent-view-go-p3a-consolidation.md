# Agent View Go P3a consolidation — follow-ups

Date: 2026-11-19. Branch: go-p3a-consolidation. Plan: `docs/superpowers/plans/2026-11-19-agent-view-go-p3a-consolidation.md`. Spec: `docs/superpowers/specs/2026-11-19-agent-view-p3-design.md`.

P3a is a refactor. It keeps every behavior and every golden file, with the three accepted changes below. The finished items of the older lists are in `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`, under "Done in P3a". The review ledger of the run is not tracked, so this file is the only record of the findings that the task reviews deferred.

At the end of P3a, the full test suite and the race tests of the changed packages pass. The frame benchmark `BenchmarkFrame50Agents3Hosts200Envelopes` has a median of 1,540,997 ns/op. The base commit has 1,555,864 ns/op in the same session (ratio 0.99; five alternating runs of each).

## Accepted changes in behavior

- **A far-future event time stays on the board.** The store no longer formats `lastEventMs` as text and parses it back for the presence check. A UTC event time outside the years 0 to 9999 (for example `9999-12-31T23:00:00-14:00`, or an extreme hello skew) now counts as its real far-future value. Such an agent stays present and is never stale until the sweep drops it. Before P3a, such a time counted as the epoch. No real event is in this range.
- **A negative lifecycle duration takes the default in the collector path too.** `lifecycle.Options.WithDefaults` treats a zero or negative field as unset. Before P3a, the collector kept a negative value, and only the store replaced it. The CLI cannot produce a negative value: `--linger` rejects values below 1, and the CLI sets only `LingerMs`.
- **A `StateMsg` with an empty `Host` counts as `local`.** The status bar then shows a permanent `local` segment next to the real hosts. The bridge never sends an empty `Host`, so no user sees this.

## Deferred during the P3a task reviews

- **Ordered (Task 1):**
  - `TestLRUNegativeLimitHoldsNothing` (`internal/ordered/lru_test.go`, line 92) hangs until the 10 min test timeout if the guard in `LRU.Set` goes (the `First()` check that ends the eviction loop). Fix: run `Set` in a goroutine with a deadline.
  - No `testing.AllocsPerRun` test shows that `LRU.Get` allocates nothing. A reviewer measured 0 allocations for a `Get` hit, a `Set` of a present key and `Has`.
  - `LRU.Get` does two map lookups for each hit (`Map.Get`, then `moveToBack`). The old `pathCache.get` did one. The frame benchmark does not show the cost, because it runs the store and the TUI, not the collector path cache. A microbenchmark gave about 20 ns and 0 allocations for each hit. That is small next to the `realpath` call that a miss costs.
  - **Done (e42c3b2):** The doc comment of `Map.All` (`internal/ordered/map.go`) said "deleting the next key ends the loop early". The loop yields the deleted entry once, and then it stops. The old `omap.all` did the same. The comment now says so.
- **Collect and logs (Task 3):** No test pins the choice of `Has` or `Get` at the call sites. Nothing fills `endedLimit` (20,000, `internal/collect/state.go`) or `MaxPending` (5,000, `internal/logs/output.go`). This gap exists since before P3a. A reviewer swapped every `Has` for `Get`, and all tests still passed. Fix: add eviction-order tests for `State.Apply` (`internal/collect`) and for `CodexAdapter` (`internal/logs`).
- **Fanout (Task 4):**
  - **Done (08e5d55):** `TestRemovedIsSafeUnderRace` (`internal/fanout/fanout_test.go`) looped `for !s.Removed() {}` with no bound. A broken `Remove` made it spin until the test timeout. The loop now stops after 5 s and fails the test. The last `Removed()` check after `wg.Wait()` is gone, because `TestRemoveSetsRemoved` covers it.
  - No change that a reviewer made to the code made `TestSendCallsFnInOrder` fail.
  - `TestPanickingSubscriberAndUnsubscribe` (`internal/collect/collector_test.go`, line 509) still passes when `fanout.Sub.Send` loses its `recover`. The panicking subscriber panics in `greet`, so the collector never adds it to `c.subs`. The recovery of the loop (`handle`) hides the loss.
  - `internal/remote` has no test for a panicking subscriber. Only the fanout unit test and `TestPanickingSubscriber` in `internal/hub` pin the `recover` in `Send`. Fix: let a subscriber panic only after N envelopes, so that it joins the list first. Add the same test to `internal/remote`.
- **Lifecycle (Task 5):**
  - No store test pins the stale boundary. Only the lifecycle tests fail when `>` becomes `>=` in `IsStale`.
  - A broken cycle guard in `lifecycle.PresentIDs` hangs the tests until the timeout. It does not fail them at once.
  - `PresentIDs` and `IsStale` call `WithDefaults()` on options that the store already resolved (`internal/store/store.go`, line 145). This is cheap and harmless.
- **TUI (Task 6):**
  - **Done (8cc6e69):** `TestEmptyHostStateIsTheLocalLabel` (`internal/tui/model_test.go`) could not catch a first-store-host label that someone adds again, because the store was empty in that test. The test now sends one envelope batch from host `box` first.
  - **Done (66d4291):** `hostState` (`internal/tui/status.go`) used the literal `"connecting"`. It now uses the constant `stateConnecting` from `internal/tui/batch.go`.
  - `TestModelFeedDown` now expects the log line `HOST  devbox down`. The old line, `HOST  local down`, came from the fallback for a state without a host. The test now sends the label `devbox`.
- **TUI rows (final review):** `callCard` in `internal/tui/rows.go` (line 290) repeats the stale rule inline: `in.Now-p.agent.LastEventMs > store.DefaultStaleMs`. The rule exists in `lifecycle.IsStale`. The inline copy was there before P3a.
