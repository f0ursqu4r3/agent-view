# Agent View Go P3c correctness — follow-ups

Date: 2026-11-20. Branch: go-p3c-correctness. Plan: `docs/superpowers/plans/2026-11-20-agent-view-go-p3c-correctness.md`. Spec: `docs/superpowers/specs/2026-11-19-agent-view-p3-design.md`, section "P3c Correctness".

P3c fixes three correctness defects: equal agent IDs on two hosts, the coverage panel cuts, and the working count. This file holds the diagnosis of the working count. The tasks that follow add what they fix, and what stays open.

## 120 working while the rows say STALE

**Cause.** The status bar and the rows use two different rules for "working".

- The status bar counts each present agent whose `Status` is `working` (`Model.status`, `internal/tui/model.go`). It does not look at the time of the last event.
- A row says STALE when the status is `working` and the agent has had no event for more than 2 min (`cards.State`, `internal/cards/state.go`). A WAITING row is never STALE.
- Time never clears the status `working`. In a Claude log, a tool call sets it, and only a turn-end record sets `idle` (`ClaudeAdapter.Ingest`, `internal/logs/claude.go`). A log that ends with no turn-end record keeps the status `working`.
- The board keeps an agent for 10 min after its last event, and for any time while a present agent descends from it (`lifecycle.PresentIDs`, `internal/lifecycle/lifecycle.go`).

So an agent can be present, have the status `working`, and have a STALE row. The bar counts it. The row does not.

**Measurement.** The test fixture generates Claude logs in a temporary directory. A real collector reads them, and the messages go the same way as in `runTUI`: collector, hub, bridge, model. No real log is read. `Status.Working` comes from the first line of the model's screen. The state word of each agent comes from `cards.State`.

| Group | The log | Logs | Present | Status `working` (the bar counts these) | WORKING | THINKING | WAITING | STALE |
|---|---|---|---|---|---|---|---|---|
| a | last record 5 min old, a call open | 40 | 40 | 40 | 0 | 0 | 0 | 40 |
| b | last record 5 min old, no open call, no turn end | 40 | 40 | 40 | 0 | 0 | 0 | 40 |
| c | parent, last record 1 h old, no open call, no turn end | 20 | 20 | 20 | 0 | 0 | 0 | 20 |
| c-child | the sub-agent of a "c" agent: fresh record, a call open | 20 | 20 | 20 | 20 | 0 | 0 | 0 |
| d | last record fresh, a call open | 20 | 20 | 20 | 20 | 0 | 0 | 0 |
| Total | | 140 | 140 | 140 | 40 | 0 | 0 | 100 |

The status bar reads `140 agents` and `140 working`. The rows say WORKING for 40 agents and STALE for 100. The bar counts 100 agents that the rows call STALE (71% of its count).

Which cause keeps the 100 STALE agents in the count:

| Cause | Groups | Agents | Share of the 100 |
|---|---|---|---|
| 1. Present for 2 to 10 min after the last event (the linger) | a, b | 80 | 80% |
| 2. Present for hours, only as the ancestor of a present agent | c | 20 | 20% |
| 3. A log with no turn-end record keeps the status `working` | b, c | 60 | 60% |

The causes overlap: groups b and c are in two rows. Cause 3 is why the status is `working` in b and c. In group a, the open call sets the status (and the collector ends an open call only after 10 min without an event). Cause 1 and cause 2 are why the agent is still present.

`TestWorkingCountIncludesStaleRows` (`cmd/agent-view/working_stale_test.go`) pins these numbers at 2 agents for each group, and again at the sizes of the table.

**Controls.** Each condition above is needed. A throwaway run of the same fixture (not kept as a test) showed:

- A log with a turn-end record and a last record 5 min old: present, status `idle`, row IDLE. The bar counts 0 of the 3 agents.
- A log with no turn-end record and a last record 11 min old, with no child: not present.
- A parent log with a last record 1 h old, with no child: not present.

**The number 120.** A collector reads at most 120 logs, the newest by file time (`defaultMaxLogs`, `internal/logs/source.go`). `collect.Options` has no field that changes it. In a throwaway run with 10,000 generated logs (200 logs of group b with a last record 3 to 6.3 min old, and 9,800 older logs), the log scan found 10,000 logs and kept 120 (`logs.SelectLogs`). The bar read `120 agents` and `120 working`, and all 120 rows said STALE. That is the number the owner saw: the cap, not a count of working agents. P3c does not change the cap.

**A parent that waits.** A throwaway run with 5 parents (an open `Task` call, last record 5 min old) and 5 fresh children gave 5 WAITING rows and 5 WORKING rows. `ViewAgent.Stale` is true for the 5 WAITING parents, because it only tests the status and the age. The rule `!a.Stale || cards.Waiting(...)` counted 10, the same as the rows. The plain `!a.Stale` would count 5. In the table fixture the rule counted 40 (the 40 WORKING rows), and in the 10,000-log run it counted 0, again the same as the rows.

**What the spec says.** "Status bar (line 1)" in `docs/superpowers/specs/2026-11-18-agent-view-tui-design.md` says: "counts of agents, working agents, calls per minute, and faults in the linger window". It does not say whether a STALE agent is a working agent. The state-word table in the same spec says that status `working` with no event for 2 min is `◌ STALE`, and that "A parent that waits for a sub-agent is not shown as stale."

**Fixture for Task 5.** `measureWorking(t, workingSizes{...})` in `cmd/agent-view/working_stale_test.go` generates the logs, runs the pipeline, and returns the bar numbers and the counts of each group. Two facts matter when you reuse it:

- A collector reads at most 120 logs, and the table fixture has 140. The fixture therefore puts at most 100 logs on one host and uses two hosts, so the status bar shows two host segments. A parent log and its child log always share a host.
- `countWorking` calls `Store.OpenCalls(a.ID)`. Task 2 changes that call to take a key.

**Decision for Task 5.** The status bar counts an agent as working only when its status is `working` and its row does not say STALE. That is: `Working` equals the agents whose `cards.State` word is WORKING, THINKING or WAITING. The measurements support this. 100 of the 140 agents that the bar counts have a STALE row, and no measured case needs another rule. Count a WAITING parent as working even when its last event is older than 2 min: use `cards.Waiting`, and do not use `ViewAgent.Stale` alone. `Agents` keeps counting every present agent (140). The ancestors of group c are not counted (they are STALE), and their fresh children are counted.
