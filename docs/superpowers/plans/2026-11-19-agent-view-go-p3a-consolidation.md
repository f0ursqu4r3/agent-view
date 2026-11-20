# Agent View P3a Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace each duplicated piece of the Go program (ordered maps, subscriber type, presence rules, the dead P1 host-state path) with one implementation, and change no output.

**Architecture:** A new `internal/ordered` package (generic `Map` and `LRU`) replaces five hand-written copies. A new `internal/fanout` package holds the one subscriber core. `internal/lifecycle` owns the presence rules and the lifecycle defaults, and the store calls it. The TUI loses its empty-host `StateMsg` path.

**Tech Stack:** Go 1.27.3 (generics, `iter.Seq2`), Bubble Tea v2 (unchanged), `go test`.

**Spec:** `docs/superpowers/specs/2026-11-19-agent-view-p3-design.md`, section "P3a Consolidation". Follow-ups: `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`.

## Global Constraints

- Every golden file stays byte-identical. Never run a test with `-update` in this plan. `git status --short testdata` must show nothing after any test run.
- The frame benchmark `BenchmarkFrame50Agents3Hosts200Envelopes` (`internal/tui/bench_test.go`) stays within 10% of the baseline measured on the base commit in the same session (the spec quotes 1.73 ms from an earlier day).
- No change to the event schema, the wire format, the CLI or any text the TUI draws.
- The TUI writes nothing to the terminal while it runs. No test or script stops a process by name; tmux only on a private `-L` socket stopped by `kill-server` first.
- No local path, user name or host name in a tracked file. No AI attribution in a commit message (no `Co-Authored-By`, no "Generated with"). Conventional Commits.
- Before each commit: `go build ./...`, `go vet ./...`, `go vet -tags avtest ./cmd/agent-view`, `GOOS=linux CGO_ENABLED=0 go vet ./...`, `gofmt -l .` (empty), and the focused tests. Run `go test -count=1 ./...` once at the end of each task that touches more than one package.
- Do not touch `.claude/`, `node_modules/`, `work/`, `dist/`, `test/`.

## Review Focus

1. **The LRU types do not share one recency rule.** `pathCache.get` makes a hit the newest. `lruSet.has` does not touch the order. `boundedSet.add` and `lruSet.add` of a present key move it to the end. The new `LRU` must keep each rule (Task 1 tests).
2. **Deleting the current entry while iterating.** `collect.omap.each` and `store.omap.all` both allow it. `All` must too (Task 1 test), and callers must not delete another key in the loop.
3. **A subscriber that panics must not stop the others, and a removed subscriber gets nothing** once its caller checks `Removed` (Task 4 tests, and `-race` on `collect`, `hub`, `remote`).
4. **Presence at the edges.** `now - last == window` is absent (`>=`); stale is strict (`>`); a finished agent uses the fade window; a parent of a present agent is present; a cycle ends; a missing parent is not an error; a zero or negative duration takes the default (Task 5 tests).
5. **An empty-`Host` `StateMsg` must not panic or erase another host's state** after the dead path goes (Task 6 test).

---

### Task 1: `internal/ordered`

**Files:**
- Create: `internal/ordered/map.go`, `internal/ordered/lru.go`, `internal/ordered/map_test.go`, `internal/ordered/lru_test.go`

**Interfaces:**
- Produces (exact):
  - `type Map[K comparable, V any] struct{ … }`; `func NewMap[K comparable, V any]() *Map[K, V]`
  - `func (m *Map[K, V]) Len() int`, `Has(k K) bool`, `Get(k K) (V, bool)`, `Set(k K, v V)` (an existing key keeps its position), `Delete(k K)`, `First() (K, V, bool)`, `All() iter.Seq2[K, V]` (insertion order; the loop body may delete the current key only), and an unexported `moveToBack(k K) bool`.
  - `type LRU[K comparable, V any] struct{ … }`; `func NewLRU[K comparable, V any](limit int) *LRU[K, V]`
  - `func (c *LRU[K, V]) Get(k K) (V, bool)` (a hit becomes the newest), `Set(k K, v V)` (sets or replaces, becomes the newest, then the oldest leaves while `Len() > limit`), `Has(k K) bool` (does not change the order), `Len() int`.
  - A set is `LRU[K, struct{}]`; there is no separate set type.

- [ ] **Step 1: Write the failing tests** (names and assertions)
  - `TestMapReplaceKeepsPosition`: set a, b, c, set b again with a new value; `All` yields a, b, c with b's new value.
  - `TestMapAllMayDeleteTheCurrentKey`: delete each key inside the loop; the loop visits all keys once and the map ends empty.
  - `TestMapFirstOnEmpty`: `First` returns `(zero, zero, false)`; after `Delete` of the only key it is empty again.
  - `TestLRUGetMakesTheNewest`: limit 2, set a, b, `Get(a)`, set c; b is gone, a and c remain.
  - `TestLRUHasDoesNotTouch`: limit 2, set a, b, `Has(a)`, set c; a is gone.
  - `TestLRUSetOfPresentKeyReplacesAndMoves`: limit 2, set a=1, b, set a=2, set c; b is gone, `Get(a)` is 2.
  - `TestLRUSetPastLimitDropsTheOldest`: limit 3, set five keys; `Len()` is 3 and only the newest three remain.
  - `TestLRULimitZeroHoldsNothing`: limit 0, `Set` leaves `Len()` 0 and does not panic.
  - `TestLRUAsSet`: `LRU[string, struct{}]` with `Set(k, struct{}{})` and `Has`.
- [ ] **Step 2: Run** `go test ./internal/ordered/` — expected: FAIL (package does not build).
- [ ] **Step 3: Implement** `Map` with `container/list` and a `map[K]*list.Element`, as `internal/store/omap.go` does today, plus `moveToBack` via `list.MoveToBack`. Implement `LRU` on a `Map`: `Get` and `Set` use `moveToBack`, so a hit allocates nothing.
- [ ] **Step 4: Run** `go test -race ./internal/ordered/` — expected: PASS.
- [ ] **Step 5: Commit** `feat(ordered): add one generic ordered map and LRU`

### Task 2: the store uses `ordered.Map`

**Files:**
- Modify: `internal/store/apply.go`, `calls.go`, `store.go`, and any other file that names `omap` or `newOmap`
- Delete: `internal/store/omap.go`

**Interfaces:** Consumes `ordered.Map` from Task 1. Produces: no change to any exported store identifier.

- [ ] **Step 1:** Replace `*omap[K, V]` with `*ordered.Map[K, V]`, `newOmap[K, V]()` with `ordered.NewMap[K, V]()`, and the lower-case methods with the exported ones (`len`→`Len`, `has`→`Has`, `get`→`Get`, `set`→`Set`, `delete`→`Delete`, `first`→`First`, `all`→`All`). Delete `omap.go`.
- [ ] **Step 2: Run** `go test -race -count=1 ./internal/store/ ./internal/tui/ ./internal/parity/` — expected: PASS, and `git status --short testdata` empty.
- [ ] **Step 3: Commit** `refactor(store): use the shared ordered map`

### Task 3: `collect` and `logs` use `ordered`

**Files:**
- Modify: `internal/collect/state.go`, `paths.go`, `collector_test.go` (line ~673), `internal/logs/claude.go`, `codex.go`
- Delete: `internal/collect/ordered.go`, `internal/logs/ordered.go`

**Interfaces:** Consumes `ordered.Map` and `ordered.LRU`. Mapping (exact):
- `collect.omap[V]` → `*ordered.Map[string, V]`; `each(f)` → `for k, v := range m.All()`.
- `collect.lruSet` → `*ordered.LRU[string, struct{}]`; `add(k)` → `Set(k, struct{}{})`; `has(k)` → `Has(k)`.
- `collect.pathCache` → `*ordered.LRU[string, string]`; `get` → `Get`; `set` → `Set`. Misses stay unstored (that rule lives in `paths.go`).
- `logs.orderedMap[V]` → `*ordered.Map[string, V]`; `len()` → `Len()`; `deleteOldest()` → `if k, _, ok := m.First(); ok { m.Delete(k) }`.
- `logs.boundedSet` → `*ordered.LRU[string, struct{}]`; `add` → `Set`; `has` → `Has`.

- [ ] **Step 1:** Make the replacements. Port each assertion of the `newPathCache(2)` test in `collector_test.go` that `internal/ordered` tests do not already cover into `internal/ordered/lru_test.go`; then delete that test. Delete both `ordered.go` files.
- [ ] **Step 2: Run** `go test -race -count=1 ./internal/collect/ ./internal/logs/ ./internal/hub/ ./internal/parity/` — expected: PASS; `git status --short testdata` empty.
- [ ] **Step 3: Commit** `refactor: use the shared ordered map in collect and logs`

### Task 4: `internal/fanout` (one subscriber core)

**Files:**
- Create: `internal/fanout/fanout.go`, `internal/fanout/fanout_test.go`
- Modify: `internal/collect/collector.go`, `internal/hub/hub.go`, `internal/remote/remote.go` (and their users of `subscriber`)

**Interfaces:**
- Produces (exact): `type Sub struct{ … }`; `func NewSub(fn func(wire.Envelope)) *Sub`; `func (s *Sub) Send(env wire.Envelope)` (calls `fn`, recovers a panic with `safe.Call`); `func (s *Sub) Remove()`; `func (s *Sub) Removed() bool`. `Send` does not check `Removed`; each caller keeps its own check, as today.
- Consumes: `safe.Call`, `wire.Envelope`.

- [ ] **Step 1: Write the failing tests**
  - `TestSendSurvivesAPanic`: `Send` on a `Sub` whose `fn` panics returns normally.
  - `TestSendCallsFnInOrder`: three `Send` calls reach `fn` in order.
  - `TestRemoveSetsRemoved`: `Removed()` is false, then true after `Remove()`; `Remove` twice is safe.
  - `TestRemovedIsSafeUnderRace`: one goroutine calls `Remove` while another calls `Removed` in a loop (run with `-race`).
- [ ] **Step 2: Run** `go test ./internal/fanout/` — expected: FAIL.
- [ ] **Step 3: Implement** `fanout.go`. Then replace the three local `subscriber` types: `collect` (`subs []*fanout.Sub`, the `item.sub` field, `unsubscribe` → `Remove`, `s.removed.Load()` → `Removed()`), `hub` (`isRemoved` → `Removed`, `(*subscriber).isRemoved` → `(*fanout.Sub).Removed`), `remote` (`removed.Store(true)` → `Remove()`, `safe.Call(func() { s.fn(env) })` → `s.Send(env)`). Each package keeps its own greeting, ordering and locking.
- [ ] **Step 4: Run** `go test -race -count=1 ./internal/fanout/ ./internal/collect/ ./internal/hub/ ./internal/remote/ ./internal/bridge/ ./internal/parity/ ./cmd/agent-view/` — expected: PASS; `git status --short testdata` empty.
- [ ] **Step 5: Commit** `refactor: share one subscriber core in collect, hub and remote`

### Task 5: one presence implementation

**Files:**
- Modify: `internal/lifecycle/lifecycle.go`, `internal/lifecycle/lifecycle_test.go`, `internal/store/store.go`, `internal/collect/state.go` (only if the signature below changes a call), `internal/tui/model.go`, `internal/tui/rows.go` (only if a constant moves)
- Delete: `internal/store/lifecycle.go` (move what remains into `store.go`)

**Interfaces:**
- Produces in `lifecycle` (exact):
  - constants `DefaultLingerMs int64 = 600_000`, `DefaultStaleMs = 120_000`, `DefaultFinishedFadeMs = 60_000`, `DefaultCallTimeoutMs = 600_000`, `DefaultFileTTLMs = 1_800_000`; `Defaults()` builds `Options` from them.
  - `Options.WithDefaults()`: a zero **or negative** field takes its default (the store's rule; `--linger` already rejects values below 1, so the collector path cannot see a negative).
  - `type Row struct { ID string; ParentID *string; Status string; LastEventMs int64 }`
  - `func PresentIDs(rows []Row, now int64, o Options) map[string]bool`
  - `func IsStale(status string, lastEventMs, now int64, o Options) bool` (replaces the `wire.Agent` version)
  - `PresentAgentIDs(agents []wire.Agent, now int64, o Options) map[string]bool` stays as a thin adapter that builds rows (the collector's state calls it).
- Store: `Store.lc` becomes a `lifecycle.Options` (built from `store.Options` and `WithDefaults()`); the type `windows`, `resolve`, `presentAgentIDs`, `isStale` and `isoOf` go; `store.DefaultLingerMs`, `DefaultStaleMs`, `DefaultFinishedFadeMs`, `DefaultFileTTLMs` stay as named constants equal to the lifecycle ones (the TUI and the store tests use them); `isoOf(ms)` becomes `wire.FormatISO(ms)` (same layout string).

- [ ] **Step 1: Write the failing tests** in `lifecycle_test.go`
  - `TestPresentIDsBoundaryIsExclusive`: `now - last == LingerMs` is absent; `== LingerMs-1` is present.
  - `TestPresentIDsFinishedUsesTheFadeWindow`: a finished agent at `FinishedFadeMs-1` present, at `FinishedFadeMs` absent.
  - `TestPresentIDsParentOfPresentIsPresent`, `TestPresentIDsMissingParentIsNotAnError`, `TestPresentIDsCycleEnds` (keep the existing cycle test if it already covers it).
  - `TestWithDefaultsTakesZeroAndNegative`: `Options{LingerMs: -5}.WithDefaults().LingerMs == DefaultLingerMs`.
  - `TestIsStaleBoundaryIsStrict`: `now - last == StaleMs` is false, `+1` is true, a non-`working` status is never stale.
  - `TestAdapterMatchesRows`: `PresentAgentIDs` over `[]wire.Agent` equals `PresentIDs` over the same agents as rows.
- [ ] **Step 2: Run** `go test ./internal/lifecycle/` — expected: FAIL.
- [ ] **Step 3: Implement** the lifecycle changes, then make the store call them: in `presentAgents` build `[]lifecycle.Row` from `lastEventMs` (no ISO format and re-parse); keep `ag.LastEventAt = wire.FormatISO(a.lastEventMs)` in the returned entries, because `ViewAgent.LastEventAt` shows it. In `Agents` and `Agent` use `lifecycle.IsStale(a.agent.Status, a.lastEventMs, now, s.lc)`. Replace the `s.lc.lingerMs`-style field uses with the `lifecycle.Options` fields. Delete `internal/store/lifecycle.go`.
- [ ] **Step 4: Run** `go test -race -count=1 ./internal/lifecycle/ ./internal/store/ ./internal/collect/ ./internal/tui/ ./internal/parity/` and `go test -count=1 ./...` — expected: PASS; `git status --short testdata` empty.
- [ ] **Step 5: Commit** `refactor(lifecycle): keep one copy of the presence rules`

### Task 6: remove the dead empty-host path

**Files:**
- Modify: `internal/tui/batch.go` (`setState`, `segments`), `internal/tui/model.go` (`feedState` field and comment), `internal/tui/model_test.go`, `internal/feed/msg.go` (comment)

**Interfaces:** Produces: `setState` treats an empty `Host` as `localHost`; there is no replace-all branch and no first-store-host label rule. A host without its own state shows `"connecting"` (a package constant replaces the `feedState` field).

- [ ] **Step 1: Write the failing test** `TestEmptyHostStateIsTheLocalLabel` in `model_test.go`: send `StateMsg{Host: "box", State: "up"}`, then `StateMsg{State: "down", Detail: "x"}`; assert the `box` segment is still `up`, a `local` segment is `down`, and the diagnostics hold one line that starts with `local collector down`.
- [ ] **Step 2: Run** the test — expected: FAIL (the old branch clears every host state).
- [ ] **Step 3: Implement.** Delete the `host == ""` branch, the `feedState` field and its use in `segments`; use the constant. Rewrite the tests that send `StateMsg{State: …}` without a `Host` (lines ~431, 681, 846, 939, 951) to send an explicit label (`devbox`) and keep what they assert. Delete a test only if its sole purpose is the replace-all or first-store-host rule, and name it in the commit body. Fix the comments of `setState`, `model.go` and `feed/msg.go` to say that the bridge always sets `Host` and an empty one means `local`.
- [ ] **Step 4: Run** `go test -race -count=1 ./internal/tui/ ./internal/bridge/ ./cmd/agent-view/` — expected: PASS (`TestTUIQuietTerminal` and `TestTUIProcessOwnWrites` included).
- [ ] **Step 5: Commit** `refactor(tui): drop the empty-host state path`

### Task 7: gate and docs

**Files:**
- Modify: `docs/superpowers/followups/2026-11-19-agent-view-go-p2c-switch.md`, `docs/superpowers/followups/2026-11-18-agent-view-go-p2b2-hub.md`, `README.md`, `docs/superpowers/spikes/2026-11-19-p2c-performance.md`

- [ ] **Step 1: Goldens.** Run `go test -count=1 ./...`; then `git status --short testdata` and `git diff --stat main -- testdata` — expected: both empty.
- [ ] **Step 2: Benchmark.** Make a temporary worktree of the base commit outside the repo (`git worktree add <scratchpad dir> <base sha>`), run `go test -run '^$' -bench BenchmarkFrame50Agents3Hosts200Envelopes -count=5 ./internal/tui` there and in the repo, alternating, in one session. Take the median of each. Expected: the repo median is at most 1.10 times the base median. Remove the worktree (`git worktree remove`). Put both medians in the report file.
- [ ] **Step 3: Race.** `go test -race -count=1 ./internal/ordered/ ./internal/fanout/ ./internal/lifecycle/ ./internal/store/ ./internal/collect/ ./internal/hub/ ./internal/remote/ ./internal/bridge/ ./internal/tui/` — expected: PASS.
- [ ] **Step 4: Follow-ups docs.** In the P2c follow-ups doc move these to "Done in P3a" with the commit: three ordered or LRU copies (and the fourth, `internal/store/omap.go`), the three `subscriber` types, the two presence copies, the dead `host == ""` path (and delete its "Test gaps" bullet). Fix the "Carried over" bullet about a `Snapshot` panic in `greet`: `collect` already recovers it (commit 0ab0320); the open spot is `m.state.Snapshot(now)` in `internal/hub/hub.go` (about line 208), called under the hub lock with no recover. Add the three fix-wave caveats that the doc lacks: `log.SetOutput(diag)` is pinned only through the 4,096-byte cut (a short log line also reaches fd 2); `go vet ./...` does not compile the `avtest` hook, so run `go vet -tags avtest ./cmd/agent-view`; `TestServePortInUse` cannot detect a changed start order. Update the P2b-2 doc the same way for the items P3a finished.
- [ ] **Step 5: Two wording fixes.** README: replace "agent-view itself writes nothing to the terminal" with "agent-view writes nothing to the terminal except the board". Performance doc: make the headline and the latency row agree (the latency target is met only for writers that close the file; a writer that holds a log open is seen by the 2 s poll).
- [ ] **Step 6: Verify** `grep -n -i -E '\bbun\b|npm|browser|viewer|AGENT_VIEW_COLLECTOR' README.md; wc -l README.md` (no match, at most 220 lines) and `git grep -n -I -E '/U[s]ers/|k[y]le|d[o]ugan'` (no hit; the brackets keep the pattern from matching its own text).
- [ ] **Step 7: Commit** `docs: record the P3a consolidation and fix two wording slips`
