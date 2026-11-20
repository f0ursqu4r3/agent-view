# Agent View Go P3c correctness — follow-ups

Date: 2026-11-20. Branch: go-p3c-correctness. Plan: `docs/superpowers/plans/2026-11-20-agent-view-go-p3c-correctness.md`. Spec: `docs/superpowers/specs/2026-11-19-agent-view-p3-design.md`, section "P3c Correctness".

P3c fixed three correctness defects: equal agent IDs on two hosts, the coverage panel cuts, and the working count. The finished items of the older lists are in `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`, under "Done in P3c".

The review ledger of the run is not tracked. This file is the only record of what P3c finished, of what stays open, of the findings that the task reviews deferred, and of the diagnosis of the working count.

P3c changes two golden files: `testdata/compose/coverage_one_host.golden` and `testdata/compose/coverage_two_hosts.golden`, in their `short box` cases only. It changes two kinds of drawn text: the markers of the coverage panel (`… N more hosts` and `… M more lines, N more hosts`) and the `working` count of the status bar. It changes no event schema, no wire format and no CLI.

At the end of P3c, the full test suite and the race tests of `internal/store`, `internal/board`, `internal/anim` and `internal/tui` pass. The frame benchmark `BenchmarkFrame50Agents3Hosts200Envelopes` has a median of 1,607,564 ns/op. The base commit (eb0067e, the code before P3c) has 1,544,839 ns/op in the same session. The gate is the ratio of the two medians of five alternating pairs (one run at a time, base first), at most 1.10. The ratio is 1.04. The runs of the branch were 1,663,175, 1,561,178, 1,607,564, 1,531,693 and 1,705,208 ns/op (a spread of 11.3% from the lowest to the highest). The runs of the base were 1,549,939, 1,544,839, 1,608,162, 1,508,185 and 1,499,021 ns/op (a spread of 7.3%). Pair 5 alone had a ratio of 1.138, so one pair is not a measure; the median is.

## What P3c finished

- **Row identity (host, agent ID).** `store.AgentKey{Host, ID}` (`internal/store/store.go`) identifies an agent. `Store.Agent`, `Counters`, `OpenCalls` and `RecentEnded` take a key, and `LaneOrder` returns keys. `ViewAgent`, `ViewCall` and `Change` have a `Key()` method. The board (`UI.Selected`, `UI.Focus`, `Row.Key()` and the focus filter of the log in `internal/board`), the animation (`internal/anim`) and the TUI (`Hit.Key`, `known`, `prune` and the key handlers in `internal/tui`) use the key. Two hosts with an agent of the same ID give two rows. The selection, the focus, the calls, the counters and the animation of a row follow the host and the ID. No key is a joined string. Tests: `TestAgentLookupByHostAndID` and `TestAgentKeysDoNotJoinStrings` (store), `TestEqualAgentIDsOnTwoHosts` (its subtest `rows show their own host` runs now), `TestSelectionAndFocusFollowTheHost` and `TestPruneForgetsOnlyTheHostThatLeft` (tui), `TestRowsParentOnAnotherHostIsNoParent` and `TestLogFocusFiltersByHostAndID` (board), `TestEqualIDsOnTwoHostsAnimateApart` (anim).
- **The coverage panel cuts by one rule.** `CoveragePanel` (`internal/tui/panels.go`) has one layout for any number of hosts. It reserves the newest four diagnostic lines (`diagKept`), so the reason that a host is down stays visible. It cuts the coverage instead, in whole host blocks, and shows `… N more hosts` (`… 1 more host`). When the first host does not fit whole together with the `… N more hosts` line, it shows the first lines of its block and `… M more lines, N more hosts`. With one host the marker is `… M more lines`. Two goldens changed, in their `short box` cases:
  - `coverage_one_host` (72 × 12). The old panel showed the 9 lines of the coverage and no diagnostic. The new panel shows the first 4 lines, `… 5 more lines`, and 3 diagnostic lines.
  - `coverage_two_hosts` (72 × 14). The old panel showed `host box` with its 9 lines and `… 15 more lines`, and no diagnostic. The new panel shows the first 6 lines of the first block, `… 4 more lines, 1 more host`, and 3 diagnostic lines.
  - Every other case of both files is byte-identical to the base. Tests: `TestCoveragePanelKeepsNewestFourDiagnostics`, `TestCoveragePanelCutsFirstBlockWhenNoneFits` and the other `TestCoveragePanel*` tests in `internal/tui/panels_test.go`.
- **The status bar counts the agents that work.** `Model.status` (`internal/tui/model.go`) counts in `working` each agent whose status is `working` and whose row does not say STALE. A WAITING parent counts, although the store marks it stale. `agents` still counts every present agent. The README states the rule. Tests: `TestStatusWorkingMatchesTheRows` (`internal/tui/model_test.go`) and `TestWorkingCountMatchesTheRows` (`cmd/agent-view/working_stale_test.go`). The diagnosis is in the last section of this file.

## What stays open by design

- **The collector reads at most 120 logs.** The limit is `defaultMaxLogs` (`internal/logs/source.go`), and `collect.Options` has no field for it. A setup with more logs shows at most 120 agents, and all of them can be STALE. P3c leaves the cap. The "120 working" that the owner saw most likely comes from this cap (see the last section).
- **At 80 × 24 with four or more diagnostics, two or more hosts show only the first block.** The body of the panel has 12 lines (18 lines inside the panel, 6 for the reserve). With fewer diagnostics the body is taller (15 lines with none), and more blocks can fit. A host block has 10 lines in the test fixture (the `host` line and 9 lines of coverage), and more for a host with several log or watch roots. The first block shows whole, or cut by lines, and the coverage of the other hosts stays hidden until the terminal is taller. The reasons of failing hosts still show in the diagnostics, as long as they are among the newest lines.
- **A host that never sent an envelope has no block in the panel.** The store adds a host with the first envelope that it accepts (`Store.Apply`), and the panel lists the hosts of the store. The state of such a host shows in the status bar, and the reason of a failure shows in the diagnostics.
- **A box smaller than the reserve shows the diagnostics only.** When the inside of the panel has fewer lines than the reserve (6, or fewer with fewer than four diagnostics), the panel shows the last lines of the diagnostics section. With a body of 0 or 1 line it shows no coverage or only the marker. The smallest screen (80 × 24) gives the panel 20 lines, so a user cannot reach these cases.
- **The bar and the rows use the same 2 min rule, from two places.** `Store.Agents` and `Store.Agent` set `Stale` with `Options.StaleMs`. The rows use `store.DefaultStaleMs` (`cards.State` in `internal/tui/rows.go`). `runTUI` sets only `LingerMs` and no other production code sets `StaleMs`, so the two agree today. A store with another `StaleMs` could show a bar that disagrees with the rows. The inline stale test in `callCard` (`internal/tui/rows.go`) is a third copy of the rule. It is open since P3a (see "TUI rows (final review)" in `docs/superpowers/followups/2026-11-19-agent-view-go-p3a-consolidation.md`).
- **`agents` counts every present agent, STALE agents included.** The fixture of the diagnosis shows `140 agents` and `40 working`. The spec sentence for the status bar does not say whether a STALE agent is a working agent. P3c chose to follow the state words of the rows.
- **Only one test pins the WAITING rule.** The generated logs of `TestWorkingCountMatchesTheRows` hold no WAITING parent (the ancestors of group c have no open call, so they are STALE). `TestStatusWorkingMatchesTheRows` is the only test that fails when `status` drops the WAITING exception.
- **The measurements of the diagnosis are not all tests.** The run with 10,000 logs, the three controls and the run with a WAITING parent were throwaway runs. Only the table of the fixture is a test. `TestWorkingCountMatchesTheRows` runs a real collector on temporary roots. It waits for no file event, but a busy `fseventsd` can slow the start of the file watcher.
- **The frame allocates 6.5% more bytes than before P3c, because `board.LogLine` grew.** In the ten runs of the gate, the base commit gave 6,414,815 to 6,418,445 B/op and 4,822 to 4,824 allocs/op. The branch gave 6,833,089 to 6,833,404 B/op and 4,825 allocs/op (the same count in all five runs). That is about 415 KB more for each op. P3c added the field `Host string` (16 bytes) to `LogLine` (`internal/board/log.go`). The final reviewer measured the struct with `unsafe.Sizeof` on both commits: 184 bytes before and 200 bytes after. A second measurement gave the same numbers. The fields add up the same way: 7 strings of 16 bytes (6 before P3c), `fsKey` of 40 bytes, 3 integers of 8 bytes, and the two `cells.Role` bytes and the `Dim` bool in 3 padded slots of 8 bytes. `Log.Add` copies all 500 kept lines into a new slice on every call once the log is full. One op of the benchmark ends about 50 calls (10 that the previous batch left open and 40 of its own), so it makes about 50 `Add` calls. The extra bytes are 50 × 500 × 16 B = 400 KB for each op, which matches the rise. The same arithmetic puts the copy of the log at 50 × 500 × 184 B = 4.6 MB for each op before P3c and 50 × 500 × 200 B = 5.0 MB after, of the 6.4 and 6.8 MB for each op. That copy existed before P3c. The time is inside the gate. The README performance table keeps the numbers of P2c (1.52 ms for a batch of 200 envelopes and one frame). In the session of P3c the same benchmark gave 1.54 ms on the base commit and 1.61 ms on the branch, so the table is not changed.
- **`Log.Add` copies the whole log on every call once it holds 500 lines.** The code is in `internal/board/log.go`. The comment above `type Log` said "a ring", which it is not (the comment now says what the type does). A fix would trim in chunks (copy only when the length passes twice the cap) or use a real ring. It is outside P3c.
- **`internal/audit` identifies agents by ID alone.** `Check` keeps `agents := map[string]wire.Agent{}` in `internal/audit/audit.go`. The problem is latent: an agent ID holds the host label of its collector, and the hub makes the labels unique. If two hosts sent equal IDs, one agent would replace the other. The counts for each provider could be lower, the parent check could report a false `parent cycle`, and a confirmed write could count for the provider of the wrong host. It is outside the P3c spec.
- **A cut coverage block keeps the first lines of the block.** These are the log roots and the watch roots. The `sources` lines, which carry `down` and `degraded`, are the last lines of a block and can be hidden. In the `short box` cases of the two goldens, the hidden lines include a source that is `down`. A later phase could list `sources` first.

## Deferred during the P3c task reviews

Task 5 fixed T1-M1, T1-M2 and T1-M5. Task 2 fixed T1-M4. Task 6 fixed T5-M1 and T5-M2. This list leaves them out. The items below stay open.

- **Diagnosis (Task 1):**
  - **T1-M3.** `TestWorkingCountMatchesTheRows` (`cmd/agent-view/working_stale_test.go`) reads the wall clock (`countWorking`). Group d has about 115 s of margin before its rows read STALE. `tui.Config.Now` could pin the clock.
- **Store key (Task 2):**
  - **T2-M1.** `refOpen` and `refEnded` in `internal/store/index_test.go` find the host with `s.stored`, the helper that the code under test uses. They would repeat a bug of that helper.
  - **T2-M2.** Loop variables in `internal/store/index_test.go` shadow the test helper `k` (`internal/store/store_test.go`). Rename the helper, for example `key`.
  - **T2-M3.** The `Errorf` for `Agent` in `TestAgentLookupByHostAndID` tests `a.ID` but does not print it.
  - **T2-M4.** The doc comments of `Store.OpenCalls` and `Store.RecentEnded` do not say that an unknown host gives nil.
  - **T2-M5.** `TestEqualAgentIDsOnTwoHosts` (`internal/tui/model_test.go`) gives both agents the same name, so it cannot tell the two cards apart by name.
- **Row identity in the board, the animation and the TUI (Task 3):**
  - **T3-M1.** `Render` in `internal/board/log.go` builds the key of the log filter inline. A `LogLine.Key()` method would match `Row.Key()`.
  - **T3-M2.** Some migrated test messages print an `AgentKey` with `%q` (for example `Selected = %q` in `internal/tui/model_test.go`). Use `%v`.
  - **T3-M3.** The test helpers `k` (anim), `hk` (board) and `dk` (tui) have different names. Accepted.
- **Coverage panel (Task 4):**
  - **T4-M1.** The README says that the panel "always keeps the newest four" diagnostic lines. It does not when the box is smaller than the reserve. The doc comment of `CoveragePanel` is not exact for a body of 0 or 1 line. Drop "always" and add a sentence.
  - **T4-M2.** When the box is smaller than the reserve, `CoveragePanel` takes the last `inner` lines of `section(diagKept)`. The tests stop at an inside height of 4. Add a box of height 7 with 6 diagnostics (it must show `diagnostics` and `diag 3` to `diag 6`), and a comment that the section has exactly `reserve` lines there.
  - **T4-M3.** `CoveragePanel` is about 70 lines. Extract the choice of the blocks (`fitBlocks`) and make `section` a function of the package.
  - **T4-M4.** `diag[max(0, len(diag)-room):]` in `section` panics when `room` is below 0. The invariant (`room` is at least 1 when `inner >= reserve`) has no comment, and no clamp protects it.
  - **T4-M5.** `trimEnd` in `internal/tui/panels_test.go` repeats a loop of `contentLines`. The comment "(20 × 18 inside)" of `TestCoveragePanelKeepsNewestFourDiagnostics` is muddled. No test covers zero hosts with a cut coverage and 9 diagnostics (height 12 gives `… 3 more lines`, then `diag 6` to `diag 9`).
  - **T4-M6.** The commit messages of `efb1923` and `2aac2cf` write `...` for `…`. The two-host paragraph of `efb1923` is superseded by `2aac2cf`.
  - **Mutation proof.** The proof of Task 4 sets the reserve to 0. In a copy outside the repo, a constant reserve of 1, 2, 5 or 7 also made the `TestCoveragePanel*` tests fail. The constants 3, 4 and 6 were not tried.
- **Status count (Task 5):**
  - **T5-M3.** `Model.status` builds the slice of `OpenCalls` before it looks at the set of parents. Test `parents[a.Key()]` first, so the clone is skipped for an agent with no sub-agent (the result is the same).
  - **T5-M4.** The doc comment of `Model.status` says "a parent with an open agent call". `cards.Waiting` looks at the newest open call only (`open[0]`).
  - **T5-M5.** In `TestStatusWorkingMatchesTheRows` the check of `len(agents)` sits after the loop over the agents. It belongs before it. No test runs the status path with equal agent IDs on two hosts. No test pins that the bar and the rows agree when `store.Options.StaleMs` is not the default.

## 120 working while the rows say STALE

**Cause.** Before Task 5, the status bar and the rows used two different rules for "working".

- The status bar counted each present agent whose `Status` was `working` (`Model.status`, `internal/tui/model.go`). It did not look at the time of the last event.
- A row says STALE when the status is `working` and the agent has had no event for more than 2 min (`cards.State`, `internal/cards/state.go`). A WAITING row is never STALE.
- Time never clears the status `working`. In a Claude log, a tool call sets it, and only a turn-end record sets `idle` (`ClaudeAdapter.Ingest`, `internal/logs/claude.go`). A log that ends with no turn-end record keeps the status `working`.
- The board keeps an agent for 10 min after its last event, and for any time while a present agent descends from it (`lifecycle.PresentIDs`, `internal/lifecycle/lifecycle.go`).

So an agent could be present, have the status `working`, and have a STALE row. The bar counted it. The row did not.

**Measurement.** The test fixture generates Claude logs in a temporary directory. A real collector reads them, and the messages go the same way as in `runTUI`: collector, hub, bridge, model. No real log is read. `Status.Working` comes from the first line of the model's screen. The state word of each agent comes from `cards.State`.

| Group | The log | Logs | Present | Status `working` (the bar counted these before Task 5) | WORKING | THINKING | WAITING | STALE |
|---|---|---|---|---|---|---|---|---|
| a | last record 5 min old, a call open | 40 | 40 | 40 | 0 | 0 | 0 | 40 |
| b | last record 5 min old, no open call, no turn end | 40 | 40 | 40 | 0 | 0 | 0 | 40 |
| c | parent, last record 1 h old, no open call, no turn end | 20 | 20 | 20 | 0 | 0 | 0 | 20 |
| c-child | the sub-agent of a "c" agent: fresh record, a call open | 20 | 20 | 20 | 20 | 0 | 0 | 0 |
| d | last record fresh, a call open | 20 | 20 | 20 | 20 | 0 | 0 | 0 |
| Total | | 140 | 140 | 140 | 40 | 0 | 0 | 100 |

Before Task 5, the status bar read `140 agents` and `140 working`. The rows say WORKING for 40 agents and STALE for 100. The bar counted 100 agents that the rows call STALE (71% of its count).

Which cause keeps the 100 STALE agents in the count. The shares come from the sizes of the fixture, not from field data. Cause 3 is a mechanism that overlaps causes 1 and 2, not a disjoint share:

| Cause | Groups | Agents | Share of the 100 |
|---|---|---|---|
| 1. Present for 2 to 10 min after the last event (the linger) | a, b | 80 | 80% |
| 2. Present for hours, only as the ancestor of a present agent | c | 20 | 20% |
| 3. A log with no turn-end record keeps the status `working` | b, c | 60 | 60% |

The causes overlap: groups b and c are in two rows. Cause 3 is why the status is `working` in b and c. In group a, the open call sets the status (and the collector ends an open call only after 10 min without an event). Cause 1 and cause 2 are why the agent is still present.

`TestWorkingCountMatchesTheRows` (`cmd/agent-view/working_stale_test.go`) builds this fixture at 2 agents for each group, and again at the sizes of the table. Task 1 wrote it as `TestWorkingCountIncludesStaleRows`, which pinned `140 working`.

**Controls.** Each condition above is needed. A throwaway run of the same fixture (not kept as a test) showed:

- A log with a turn-end record and a last record 5 min old: present, status `idle`, row IDLE. The bar counted 0 of the 3 agents.
- A log with no turn-end record and a last record 11 min old, with no child: not present.
- A parent log with a last record 1 h old, with no child: not present.

**The number 120.** A collector reads at most 120 logs, the newest by file time (`defaultMaxLogs`, `internal/logs/source.go`). `collect.Options` has no field that changes it. In a throwaway run with 10,000 generated logs (200 logs of group b with a last record 3 to 6.3 min old, and 9,800 older logs), the log scan found 10,000 logs and kept 120 (`logs.SelectLogs`). The bar read `120 agents` and `120 working`, and all 120 rows said STALE. That matches the number the owner saw, so the cap is the likely source, not a count of working agents. P3c does not change the cap.

**A parent that waits.** A throwaway run with 5 parents (an open `Task` call, last record 5 min old) and 5 fresh children gave 5 WAITING rows and 5 WORKING rows. `ViewAgent.Stale` is true for the 5 WAITING parents, because it only tests the status and the age. The rule `!a.Stale || cards.Waiting(...)` counted 10, the same as the rows. The plain `!a.Stale` would count 5. In the table fixture the rule counted 40 (the 40 WORKING rows), and in the 10,000-log run it counted 0, again the same as the rows.

**What the spec says.** "Status bar (line 1)" in `docs/superpowers/specs/2026-11-18-agent-view-tui-design.md` says: "counts of agents, working agents, calls per minute, and faults in the linger window". It does not say whether a STALE agent is a working agent. The state-word table in the same spec says that status `working` with no event for 2 min is `◌ STALE`, and that "A parent that waits for a sub-agent is not shown as stale."

**Fixture for Task 5.** `measureWorking(t, workingSizes{...})` in `cmd/agent-view/working_stale_test.go` generates the logs, runs the pipeline, and returns the bar numbers and the counts of each group. Two facts matter when you reuse it:

- A collector reads at most 120 logs, and the table fixture has 140. The fixture therefore puts at most 100 logs on one host and uses two hosts, so the status bar shows two host segments. A parent log and its child log always share a host.
- `countWorking` looks up the open calls with the key of the agent (`Store.OpenCalls(a.Key())`). Task 1 wrote it with `Store.OpenCalls(a.ID)`, and Task 2 changed it when the store took a key.

**Decision for Task 5.** The status bar counts an agent as working only when its status is `working` and its row does not say STALE. That is: `Working` equals the agents whose `cards.State` word is WORKING, THINKING or WAITING. The measurements support this. 100 of the 140 agents that the bar counted had a STALE row, and no measured case needs another rule. Count a WAITING parent as working even when its last event is older than 2 min: use `cards.Waiting`, and do not use `ViewAgent.Stale` alone. `Agents` keeps counting every present agent (140). The ancestors of group c are not counted (they are STALE), and their fresh children are counted.

**Fix (Task 5).** `Model.status` (`internal/tui/model.go`) now counts in `Working` each present agent whose status is `working` and whose row does not say STALE. That is `!a.Stale`, or `cards.Waiting(a, OpenCalls(key), hasPresentChild)` for a stale agent. `hasPresentChild` is true when another agent of the same host on the board has the agent as its parent. `OpenCalls` is queried only for a stale agent. `Agents` still counts every present agent. On the fixture above the bar now reads `140 agents` and `40 working`, the same as the rows. The README states the rule in its status bar paragraph.

Tests: `TestStatusWorkingMatchesTheRows` (`internal/tui/model_test.go`) has seven agents (WORKING, THINKING, WAITING with a fresh WORKING child, two STALE, one IDLE). The bar reads `7 agents` and `4 working`, and `Working` equals the number of WORKING, THINKING and WAITING words. `TestWorkingCountMatchesTheRows` checks the bar against the rows on the generated logs, at both fixture sizes. A plain `!a.Stale` fails the first test (the WAITING parent is not counted). The old count of every `working` agent fails both.
