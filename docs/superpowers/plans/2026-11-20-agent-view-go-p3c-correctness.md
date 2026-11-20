# Agent View P3c Correctness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two hosts can never merge their agents into one row, the coverage panel never hides the newest diagnostics, and the status bar and the rows agree on what "working" means.

**Architecture:** `internal/store` gets a typed key `AgentKey{Host, ID}`. Its four ID-only queries and `LaneOrder` use it. The board, the animation engine and the TUI use the same key for rows, selection, focus, hits and the log filter. `CoveragePanel` reserves the newest diagnostic lines and cuts host blocks. A diagnosis task names the cause of "120 working while the rows say STALE" first. A later task fixes the status bar count.

**Tech Stack:** Go 1.27.3, Bubble Tea v2, Lip Gloss v2.

**Spec:** `docs/superpowers/specs/2026-11-19-agent-view-p3-design.md`, section "P3c Correctness". Source of the items: `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`, "Accepted gaps" (equal agent IDs, coverage panel) and "Observations" (120 working).

## Global Constraints

- Goldens: only `testdata/compose/coverage_one_host.golden` and `testdata/compose/coverage_two_hosts.golden` may change, and only in Task 4. After any other task `git status --short testdata` shows nothing. Run `-update` only in Task 4 step 5, only for the two panel tests, and read the diff before the commit.
- Task 4's commit message explains each changed golden case one by one: the case name, the old lines, the new lines, the reason.
- No change to the event schema, the wire format, the CLI, or any drawn text except the new panel marker `… N more hosts` (and its singular) and the status bar count of Task 5.
- Never join a host label and an agent ID into one string to make a key. Use the struct.
- The frame benchmark `BenchmarkFrame50Agents3Hosts200Envelopes` (`internal/tui/bench_test.go`) stays within 10% of the median measured on the base commit in the same session (spec: "slows the frame by more than 10%"). Task 6 runs the gate.
- The TUI writes nothing to the terminal while it runs. No test or script stops a process by name. tmux only on a private `-L` socket stopped by `kill-server` first.
- Read no real log. Task 1 uses generated logs in a temp directory. Documents hold counts only. No local path, user name or host name in a tracked file.
- Each contract has a test that fails without the change. Each implementer shows a mutation proof (command and failing output) in the report, in a temp copy outside the repo or fully reverted.
- No AI attribution in a commit message. Conventional Commits.
- Before each commit: `go build ./...`, `go vet ./...`, `go vet -tags avtest ./cmd/agent-view`, `GOOS=linux CGO_ENABLED=0 go vet ./...`, `gofmt -l .` (empty), the focused tests. Run `go test -count=1 ./...` once at the end of each multi-package task. The tests of `internal/collect`, `fschange`, `logs` and `watch` time out when `fseventsd` is busy; that is environmental.
- Do not touch `.claude/`, `node_modules/`, `work/`, `dist/`, `test/`.

## Review Focus

1. **Equal agent IDs on two hosts: select, focus and the log filter follow the host.** `j` moves between the two rows, `enter` focuses one, and the log then shows only that host's lines (Task 3).
2. **A parent ID that exists only on another host is no parent.** A row must not become a sub-agent because a different host has an agent with the parent's ID (Task 3).
3. **An agent leaves one host and the twin on the other host stays.** Animation state, `known` and the focus of the twin are kept (Task 3).
4. **80 × 24 terminal, three or more hosts, many diagnostics.** The box is 18 inner lines. The newest four diagnostic lines show, and a host that is down before its first envelope has no block but its reason shows (Task 4).
5. **Old agents: the bar says N working and the rows say STALE.** This includes a parent that waits on a child and an ancestor kept on the board only by a present child (Tasks 1 and 5).

---

### Task 1: Diagnose "120 working while the rows say STALE"

This task fixes nothing. It names the cause with measured counts and pins today's behavior.

**Files:**
- Create: `docs/superpowers/followups/2026-11-20-agent-view-go-p3c-correctness.md`
- Create: `cmd/agent-view/working_stale_test.go` (package `main` already imports `collect`, `store`, `tui`, `bridge`, `hub`; use another package only if it needs no import cycle)
- Read: `internal/tui/model.go` (`status`), `internal/cards/state.go`, `internal/lifecycle/lifecycle.go` (`PresentIDs`, `IsStale`), `internal/store/store.go` (`Agents`), the collector's status rules in `internal/collect` and `internal/logs`

**Interfaces:**
- Produces: the follow-ups file with the section `## 120 working while the rows say STALE`: the cause in plain words, a table of measured counts, and the spec sentence that the status bar follows (`docs/superpowers/specs/2026-11-18-agent-view-tui-design.md`, "Status bar (line 1)").
- Produces: `TestWorkingCountIncludesStaleRows`, which pins the behavior as found. Task 5 replaces it.

Three causes are likely. Measure all three. Do not stop at the first one that explains some rows.
1. The bar counts `Agent.Status == "working"`, and an agent with no event for 2 to 10 min is still present (linger 10 min) while its row says STALE.
2. An agent with no event for hours stays present only because a present descendant keeps it (`PresentIDs` adds every ancestor). Its status stays `working`. Its row says STALE.
3. A log that ends with no completion record leaves the collector status at `working` until the linger ends.

- [ ] **Step 1: Generate the logs.** In a temp dir (`t.TempDir()`, resolved with `EvalSymlinks`), write Claude-format logs as `internal/collect/collector_test.go` does (`toolUseLine`, `writeLog` show the record shape; copy what you need into the new test). Build four groups with exact counts: (a) 40 logs, last record 5 min old, an open tool call; (b) 40 logs, last record 5 min old, no open call, status working; (c) 20 parent logs, last record 1 h old, each with a present child whose last record is fresh; (d) 20 logs, last record fresh, open call. Set the record times and `os.Chtimes` so the collector reads them. Read `internal/logs` first to learn how the collector makes a parent link (a sub-agent log file or a session link) and which records set the status `working`; build group (c) from that. Run a real `collect.Collector` on those roots.
- [ ] **Step 2: Feed and count.** Feed the collector's envelopes to a `store.Store` and to a `tui.Model` the way production does (the bridge re-encodes hub envelopes; reuse that path, do not invent one). Read `Status.Working` from the model and the state word of every agent from `cards.State`. Record per group: agents present, status `working`, counted by the bar, state word WORKING, THINKING, WAITING and STALE.
- [ ] **Step 3: Write `TestWorkingCountIncludesStaleRows`.** It asserts the measured numbers of a smaller fixed fixture (2 or 3 agents per group), so the test is fast. Today the bar counts every agent with status working, including the ones whose row says STALE.
- [ ] **Step 4: Write the follow-ups file.** Header as in the P3b file (date 2026-11-20, branch `go-p3c-correctness`, plan, spec). The section names the cause (which of the three, in what share) with the table. Add a line "Decision for Task 5" that says which behavior the bar should have, based on what you measured. If the measurement shows a cause that none of the three explains, say so first, report DONE_WITH_CONCERNS, and name what the controller must rule.
- [ ] **Step 5: Run** `go test -count=1 -run TestWorkingCountIncludesStaleRows ./cmd/agent-view/` — expected: PASS. No generated file is left outside the temp dir. No real log is read.
- [ ] **Step 6: Commit** `docs(p3c): name the cause of the working count that disagrees with the rows`

### Task 2: AgentKey in the store

**Files:**
- Modify: `internal/store/store.go`, `internal/store/changes.go`
- Modify (call sites and tests so the build stays green): `internal/store/store_test.go`, `changes_test.go`, `index_test.go`, `internal/board/layout.go`, `internal/board/log.go`, `internal/tui/batch.go`, `model.go`, `rows.go`, `internal/tui/model_test.go`, `compose_test.go`, `bench_test.go`, `internal/bridge/bridge_test.go`, any other caller `go build ./... && go vet ./...` finds

**Interfaces:**
- Produces in `internal/store`:
  - `type AgentKey struct{ Host, ID string }`
  - `func (a ViewAgent) Key() AgentKey`, `func (c Change) Key() AgentKey` (`Host`, `AgentID`), `func (c *ViewCall) Key() AgentKey` (`Host`, `AgentID`)
  - `func (s *Store) Agent(k AgentKey) (ViewAgent, bool)`, `Counters(k AgentKey) Counters`, `OpenCalls(k AgentKey) []*ViewCall`, `RecentEnded(k AgentKey, n int) []*ViewCall`
  - `func (s *Store) LaneOrder(now int64) []AgentKey` (same order as before)
  - `holder(id)` goes. A lookup is `s.hosts[k.Host]`, then the agent. An unknown host or ID gives the zero value, as an unknown ID does today.
- Stays: `board.UI.Selected`, `board.UI.Focus`, `board.Row.AgentID`, `tui.Hit.AgentID` and the `anim` methods keep their string IDs in this task. Task 3 changes them. Callers outside the store build a key from what they hold: `Row.Host` with `Row.AgentID`, `Change.Key()`, `ViewAgent.Key()`. In `board.Rows` the `present` set and the parent lookup use `AgentKey{a.Host, *a.ParentID}`.

- [ ] **Step 1: Write the failing tests** in `internal/store/store_test.go`, using the file's existing helpers for envelopes and hosts:
  - `TestAgentLookupByHostAndID`: hosts `box` and `box#2` each hold an agent `s1` with a different name, an open call and an ended call of their own. `Agent`, `OpenCalls`, `RecentEnded` and `Counters` return each host's own data for `AgentKey{"box#2","s1"}`, and the zero value for `AgentKey{"nohost","s1"}` and `AgentKey{"box","nope"}`.
  - `TestAgentKeysDoNotJoinStrings`: host `a` with ID `b/c` and host `a/b` with ID `c` are two agents, and `LaneOrder` lists both.
  - `TestLaneOrderHasKeys`: two hosts, equal IDs: `[]AgentKey{{"box","s1"},{"box#2","s1"}}`.
  - In `internal/tui/model_test.go`, `TestEqualAgentIDsOnTwoHosts`: `LaneOrder` is compared with the two keys, and the subtest `rows show their own host` runs (delete its `t.Skip`). The names, calls (`box2.go`) and the host header `box#2 ─` show for the second row.
- [ ] **Step 2: Run** `go test -run 'TestAgentLookupByHostAndID|TestAgentKeysDoNotJoinStrings|TestLaneOrderHasKeys' ./internal/store/` — expected: FAIL to compile (no `AgentKey`).
- [ ] **Step 3: Implement** the interface above. Migrate the existing store tests with one helper (for example `k(id string) AgentKey` for the file's usual host) and migrate every caller outside the store. Do not change behavior for one host.
- [ ] **Step 4: Run** `go build ./... && go vet ./... && go test -count=1 ./internal/store/ ./internal/board/ ./internal/tui/ ./internal/bridge/ ./internal/anim/` — expected: PASS, including the un-skipped subtest. `git status --short testdata` empty. Mutation proof: make `Store.Agent` answer from the first host that holds the ID; `TestAgentLookupByHostAndID` and the subtest fail.
- [ ] **Step 5: Commit** `refactor(store): identify an agent by host and ID`

### Task 3: row identity in the board, the animation and the TUI

**Files:**
- Modify: `internal/board/layout.go`, `internal/board/log.go`, `internal/anim/anim.go`, `internal/tui/batch.go`, `compose.go`, `keys.go`, `model.go`, `rows.go`, and their tests (`internal/board/*_test.go`, `internal/anim/anim_test.go`, `internal/tui/*_test.go`)

**Interfaces:**
- Consumes: Task 2's `store.AgentKey` and the keyed store queries.
- Produces:
  - `board.UI.Selected, Focus store.AgentKey`; the zero key means none. `func (r Row) Key() store.AgentKey`. `board.Scroll(rows []Row, height, top int, selected store.AgentKey) int`.
  - `board.LogLine.Host string`, set where `AgentID` is set. `func (l *Log) Render(w, h int, focus store.AgentKey) *cells.Grid` keeps a line when the focus is the zero key, when its key equals the focus, or when its label is `HOST`.
  - `anim.Engine`: `OnAgentAdded(k store.AgentKey, hasParent bool)`, `OnFail(k)`, `OnStopped(k)`, `Settle(k)`, `Forget(k)`, `Frame(k store.AgentKey, targetRowH, histCount int) AgentFrame`; `OnChange(c store.Change, shown bool)` uses `c.Key()`.
  - `tui.Hit.Key store.AgentKey` replaces `AgentID`. `Model.known` is `map[store.AgentKey]bool`. `callAgents(envs)` returns `[]store.AgentKey` built from `e.Host` and the call or agent ID of each envelope. `shownCalls` takes and returns keys.

- [ ] **Step 1: Write the failing tests**
  - `internal/board/layout_test.go` `TestRowsParentOnAnotherHostIsNoParent`: host `a` has agent `p`; host `b` has agent `c` with `ParentID` `p` and no `p`. The row of `c` has `Sub` false. A second case: both hosts have `p`, and `c`'s parent on `b` makes `c` a sub-row of `b`'s `p` only. With `UI.Selected` set to one twin's key, only that row is `Full`.
  - `internal/board/log_test.go` `TestLogFocusFiltersByHostAndID`: two hosts, equal agent IDs, a tool line each plus a `HOST` line. Focus on the key of the second host keeps its line and the `HOST` line and drops the first host's line.
  - `internal/anim/anim_test.go` `TestEqualIDsOnTwoHostsAnimateApart`: `OnAgentAdded` for `{box,s1}` leaves `Frame({box#2,s1}, …)` with no appearance state; `Forget({box,s1})` keeps the state of `{box#2,s1}`.
  - `internal/tui/model_test.go` (use `boxHosts` with equal IDs): `TestSelectionAndFocusFollowTheHost`: `j` selects `{box,session-1}`, `j` again `{box#2,session-1}`; `enter` focuses the second; the screen shows `box2.go` and not `box.go` in the log; `esc` clears it. A click on the second card focuses it (read the hit rectangles of the model). `TestPruneForgetsOnlyTheHostThatLeft`: the agent of `box` ages out past the linger while `box#2` keeps its twin alive; `known` loses `{box,session-1}` only, and a focus on the twin stays.
- [ ] **Step 2: Run** the new tests — expected: FAIL (compile error for the new key fields, then the behavior).
- [ ] **Step 3: Implement** the interface above in the board, the animation and the TUI. Replace every string ID that stands for an agent in these packages by the key. Keep `Row.AgentID` and `Row.Host` as fields.
- [ ] **Step 4: Run** `go test -count=1 ./internal/board/ ./internal/anim/ ./internal/tui/` and `go test -race -count=1 ./internal/tui/` — expected: PASS. `git status --short testdata` empty (no golden changes). Mutation proofs: (1) compare `Selected` by `.ID` only; the selection test fails. (2) drop `Host` from the log filter; the log test fails. (3) key `known` by ID; the prune test fails.
- [ ] **Step 5: Commit** `fix(tui): select, focus and animate a row by host and agent ID`

### Task 4: coverage panel keeps the newest diagnostics

**Files:**
- Modify: `internal/tui/panels.go` (`CoveragePanel`), `internal/tui/panels_test.go`, `testdata/compose/coverage_one_host.golden`, `testdata/compose/coverage_two_hosts.golden`, `README.md` (the coverage panel limitation, near the line that says "cuts the newest diagnostic lines first")

**Interfaces:**
- Produces: `CoveragePanel(covs []HostCoverage, diag []string, w, h int) *cells.Grid` (signature unchanged) with one layout rule for any number of hosts. Let `inner = h-2`.
  1. The diagnostics section is a blank line, the line `diagnostics`, then diagnostic lines (`none` when `diag` is empty). Reserve `2 + max(1, min(4, len(diag)))` lines for it.
  2. A block is one host's coverage lines. With two or more hosts a block starts with the line `host <label>`, and blocks are separated by one blank line. With zero or one host there is one block and no host line.
  3. The blocks get `body = inner - reserve` lines. With two or more hosts, show the largest number `k` of whole blocks that fit in `body`, counting the blank separators and, when `k < n`, one marker line directly after the blocks: `… N more hosts` with `N = n - k` (`… 1 more host` when `N` is 1). When `k = 0` (the first block plus its marker does not fit) show the first block cut by lines: its first `body-1` lines (host line included), then one marker `… M more lines, N more hosts`, `M` being the lines of that block left out and `N = n - 1` (`… 1 more line` and `… 1 more host` when 1). With zero or one host this is the marker `… M more lines`. With `body = 0` draw no block and no marker; with `body = 1` draw only the marker. (Amended during Task 4: a real host block often has 12 or more lines, so showing no block at 80 × 24 hid all coverage.)
  4. The diagnostics section follows the body directly. Its lines are the newest ones that fit: `room = inner - bodyUsed - 2`, so at least `min(4, len(diag))` lines show, and older lines drop without a marker.
  5. When `inner` is smaller than the reserve, draw the last `inner` lines of the diagnostics section and nothing else.
  - When everything fits in `inner` the output equals today's. A box with `w < 6` or `h < 3` stays as it is.

- [ ] **Step 1: Write the failing tests** in `internal/tui/panels_test.go` (helpers `hostCovs`, `numbered`, `panelLines`, `oneHost` exist):
  - `TestCoveragePanelKeepsNewestFourDiagnostics`: `hostCovs("h1".."h5")`, `numbered("diag", 9)`, `w` 72, `h` 20 (the 80 × 24 box). The lines contain `diag 6` to `diag 9` in order, the last line is `diag 9`, the line `… 4 more hosts` follows the first block, and the first block is whole.
  - `TestCoveragePanelOneHostCutsLinesNotDiagnostics`: one host with 30 log roots, 3 diagnostics, `h` 12. The block ends with `… N more lines` (N computed from the full content), and the three diagnostics show after `diagnostics`.
  - `TestCoveragePanelOneMoreHostIsSingular`: two hosts where only one block fits: the marker is `… 1 more host`.
  - `TestCoveragePanelDownHostReasonShows`: two blocks and a diagnostics list whose newest line is `ghost collector down: exit status 255` (host `ghost` has no block); `h` 20; the line shows.
  - `TestCoveragePanelTinyBox`: `h` 6 (inner 4) and 6 diagnostics: the lines are `diag 3` to `diag 6`.
  - `TestCoveragePanelFitsUnchanged`: the box of `contentLines` height shows the same lines as before (reuse its checks: no marker, all lines). Replace `TestCoveragePanelTruncates` with these; keep its edge idea (one line short of the content).
- [ ] **Step 2: Run** `go test -run 'TestCoveragePanel' ./internal/tui/` — expected: FAIL for the new tests; the two golden tests still pass.
- [ ] **Step 3: Implement** the rule in one code path for any number of hosts (the single-host branch and the multi-host branch go away). Keep `coverageLines`. A host line and a diagnostic line are cleaned as today.
- [ ] **Step 4: Run** the panel tests. The two golden tests now fail only in their `short box` cases: the one-host box (72 × 12) and the two-host box (72 × 14). Every other case must stay byte-identical. Mutation proof: set the reserve to 0; `TestCoveragePanelKeepsNewestFourDiagnostics` fails.
- [ ] **Step 5: Update the two goldens** with `go test ./internal/tui -run 'TestCoveragePanel(OneHostUnchanged|TwoHosts)' -update`. `git diff --stat testdata` must list exactly the two files. `git diff testdata` must show changes in the two `short box` cases only. Update the comment of `TestCoveragePanelOneHostUnchanged` (the golden is no longer "recorded before the change" for that case).
- [ ] **Step 6: Update the README** limitation to the new rule: the panel keeps the newest four diagnostic lines and cuts hosts first, with `… N more hosts`.
- [ ] **Step 7: Commit** `fix(tui): keep the newest diagnostics in the coverage panel` with a body that explains each changed case: `coverage_one_host` case `short box` (old: coverage fills the box and no diagnostic shows; new: coverage cut with `… 5 more lines`, then `diagnostics` and 3 lines) and `coverage_two_hosts` case `short box` (old: blocks fill the box, the diagnostics are cut; new: `… 2 more hosts`, then the diagnostics). Check the numbers against the real diff and correct them.

### Task 5: the status bar counts only agents that are not STALE

**Files:**
- Modify: `internal/tui/model.go` (`status`), `internal/tui/model_test.go`, `cmd/agent-view/working_stale_test.go`, `README.md` (the status bar paragraph), `docs/superpowers/followups/2026-11-20-agent-view-go-p3c-correctness.md`

**Interfaces:**
- Consumes: Task 2's keys, `store.ViewAgent.Stale`, `cards.Waiting(a, open, hasPresentChild)`.
- Produces: `Model.status` counts in `Working` each present agent whose `Status` is `working` and whose row does not say STALE: that is `!a.Stale`, or `cards.Waiting(a, m.store.OpenCalls(a.Key()), hasPresentChild)` is true. `hasPresentChild` is true when another agent of the same host in `Agents(now)` has this agent as `ParentID`. `Agents` still counts every present agent. Query `OpenCalls` only for an agent that is `Stale`.
- Rule (my call, from the spec text "the STALE word is a state, not working"): the bar and the rows say the same thing. If Task 1 recorded a different decision, follow Task 1 and say why in the report.

- [ ] **Step 1: Write the failing tests**
  - `internal/tui/model_test.go` `TestStatusWorkingMatchesTheRows`: one host, seven agents on a fake clock: W1 working with a fresh open call (WORKING); W2 working, fresh, no call (THINKING); P working with an open `agent` call, no event for 3 min, and a present child C (WAITING); C, the child of P, working with a fresh open call (WORKING); S1 working with an open call and no event for 3 min (STALE); S2 working, no call, no event for 3 min (STALE); I idle. The status bar reads `7 agents` and `4 working`. `Working` equals the number of agents whose `cards.State` word is WORKING, THINKING or WAITING. The screen shows `STALE` twice.
  - `cmd/agent-view/working_stale_test.go`: replace `TestWorkingCountIncludesStaleRows` with `TestWorkingCountMatchesTheRows`: the same generated fixture, now `Working` equals the count of agents with status working whose `cards.State` word is not STALE. The ancestors of group (c) are not counted.
- [ ] **Step 2: Run** both tests — expected: FAIL (the bar counts the STALE agents).
- [ ] **Step 3: Implement** `status` as in the interface. Keep the other counts as they are.
- [ ] **Step 4: Run** `go test -count=1 ./internal/tui/ ./cmd/agent-view/ -run 'Status|Working'` — expected: PASS. Mutation proof: count every `working` agent again; both tests fail.
- [ ] **Step 5: Update the README** status bar paragraph: `working` counts agents that are working and not STALE. Add the cause and the new rule to the follow-ups file under the Task 1 section.
- [ ] **Step 6: Commit** `fix(tui): count only agents that are not STALE as working`

### Task 6: follow-ups, README and the frame gate

**Files:**
- Modify: `docs/superpowers/followups/2026-11-20-agent-view-go-p3c-correctness.md`, `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`, `README.md` (only if a statement is now wrong)

**Interfaces:**
- Produces: the P3c follow-ups file lists what stays open and the deferred minors (the controller fills the minors from its ledger). The P2c follow-ups file moves the three items to a section `## Done in P3c`, in the style of `## Done in P3b`: equal agent IDs on two hosts, the coverage panel cuts, and the working count (observation). It adds one line near the top that points to the P3c file.

- [ ] **Step 1: Run the full checks.** `go build ./...`, `go vet ./...`, `go vet -tags avtest ./cmd/agent-view`, `GOOS=linux CGO_ENABLED=0 go vet ./...`, `gofmt -l .` (empty), `go test -count=1 ./...`, `go test -race -count=1 ./internal/store/ ./internal/board/ ./internal/anim/ ./internal/tui/`. `git diff main --stat -- testdata` lists only the two panel goldens.
- [ ] **Step 2: Frame gate.** Make a temporary worktree of the base commit outside the repo (`git worktree add <scratchpad dir> <base sha>`). Run `go test -run '^$' -bench BenchmarkFrame50Agents3Hosts200Envelopes -count=5 ./internal/tui` there and in the repo, alternating, in one session. Take the median of each. Expected: the repo median is at most 1.10 times the base median. Remove the worktree (`git worktree remove`). Put both medians in the follow-ups file. If the ratio is above 1.10, find the cost (likely the key hashing in `rows.go` or the extra `OpenCalls` in `status`) and fix it before the commit.
- [ ] **Step 3: Write the documents.** Check every claim in them against the code (line numbers cost nothing to leave out; name functions instead).
- [ ] **Step 4: Commit** `docs(p3c): record what P3c finished and what stays open`
