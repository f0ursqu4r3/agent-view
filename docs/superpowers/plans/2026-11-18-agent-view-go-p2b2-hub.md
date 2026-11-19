# Agent View Go Hub (P2b-2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port the TypeScript hub (per-host mirrors, late-subscriber snapshot), remote collectors over SSH, the headless `agent-view serve` with `GET /api/snapshot`, and the live audit (`agent-view audit`) to Go, and prove the hub with a parity test against the TypeScript hub.

**Architecture:** New packages `hub` (the merge and the per-host `collect.State` mirrors, plus `LocalSource` around the P2b-1 collector), `remote` (an SSH child per remote host, with backoff), `server` (standard-library HTTP, one data route), and `audit` (pure checks and counts over a snapshot). `wire` gains a decoder, because the hub now reads envelopes from remote collectors. `cmd/agent-view` gains the `serve` and `audit` modes. The TUI is unchanged: it still runs the TypeScript collector child until P2c.

**Tech Stack:** Go 1.27.3, standard library `net/http`, `golang.org/x/sys/unix` (already a direct dependency; `SignalName`). Bun 1.4.4 runs the TypeScript side of the parity test.

**Spec:** `docs/superpowers/specs/2026-11-18-agent-view-tui-design.md` (Phase 2, milestone P2b). Builds on P2b-1 (`docs/superpowers/plans/2026-11-18-agent-view-go-p2b1-collector.md`) and its follow-ups `docs/superpowers/followups/2026-11-18-agent-view-go-p2b1-collector.md` ("For P2b-2"). P2c (the TUI on the in-process hub, `--remote` in the TUI, the build script, deleting TypeScript) is a separate plan.

## Global Constraints

- Go module `agent-view`, Go 1.27.3. No new dependencies.
- Behavior: each Go file does what the TypeScript file named in its task does, with the same envelopes, field presence and order. Read the TypeScript file before writing the Go file. Known differences from P2b-1 still apply; new ones are named in the task that causes them.
- Hub contract (spec, verbatim): "The hub's `subscribe` keeps its contract: hello, snapshot, and coverage for each host first, then live envelopes."
- `agent-view serve [--port <n>]` (spec, verbatim): "the headless hub with `GET /api/snapshot` on `127.0.0.1` (standard library HTTP; same `Host` checks as today). `/ws` is dropped, because no client uses it." The browser viewer at `/` is dropped too: no route but `/api/snapshot` exists.
- Audit (spec): "The live audit becomes `agent-view audit [--port <n>]` … and keeps printing counts only." It never prints paths, names, commands or output.
- Values, verbatim from the TypeScript: hub tick 1,000 ms; remote backoff starts at 1,000 ms, doubles, at most 30,000 ms; a connection up for 60,000 ms resets the backoff; SIGKILL 2,000 ms after SIGTERM; status detail at most 200 characters; kept stderr tail 4,096 characters; stdout line at most 8 MiB; default port 4317; SSH host pattern `^[A-Za-z0-9_][A-Za-z0-9._@:-]*$`; default remote argv `ssh -T -o BatchMode=yes -o ServerAliveInterval=15 <host> agent-view collect --stdio --label <shell-quoted label>`.
- HTTP, verbatim from `src/hub/server.ts`: bind `127.0.0.1` only; a data request needs `Host` equal to `127.0.0.1:<port>` or `localhost:<port>`, and an `Origin` that is absent or equal to `http://127.0.0.1:<port>` or `http://localhost:<port>` (else 403 `forbidden`); a method other than GET gets 405 `method not allowed` with `Allow: GET`; any other path gets 404 `not found`; every reply has `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`; `/api/snapshot` replies `Content-Type: application/json` with `{"hosts":[…],"envelopes":[…]}`.
- JSON output of envelopes always goes through the `wire` encoder (no HTML escaping), never `json.Marshal` on an `Envelope`.
- The collector, hub and server only read files; tests write only in their own temporary directories.
- Tests: port every test of the TypeScript test files each task names, one Go test per TypeScript test, in the same order, with the TypeScript title as the Go test's doc comment, except the tests a task lists as dropped (with the reason). The task states the expected count.
- The TypeScript code is not changed. New TypeScript files are allowed only under `test/parity/`.
- The TUI mode is unchanged except its non-terminal message. It still rejects `--remote` and `--port`.
- After every task: `go test -count=1 ./...`, `go vet ./...`, `GOOS=linux CGO_ENABLED=0 go vet ./...` and `gofmt -l .` are clean. The TypeScript test suite is not run.
- Commits: Conventional Commits. No AI attribution in any commit message.

## Review Focus

1. **A stuck HTTP client** (it connects to `/api/snapshot` and never reads the reply): the hub keeps ingesting and other requests are served. The snapshot is built under the hub lock; the reply is written without it. Test in Task 5.
2. **A remote child that ignores SIGTERM:** `Stop` sends SIGKILL after 2 s and returns within 3 s, and no process of the child's group is left. Test in Task 4.
3. **A remote connection that drops mid-line:** the partial last line is never delivered; the host goes `down` with a detail, then reconnects with backoff. Test in Task 4.
4. **A remote line that is a valid envelope with a field of the wrong type** (for example `"seq":"7"`, or a `tool.start` whose `call` is a string): the line is skipped and counted as malformed, and the stream continues. Test in Task 4 (and the decoder rule in Task 2).
5. **`serve` on a port that is in use:** stderr `agent-view: cannot start the server on port <n>: <message>`, exit code 1, and no collector or remote child is left running. Test in Task 7.

---

### Task 1: Collector hardening (P2b-1 follow-ups)

Closes the three "For P2b-2" items of the P2b-1 follow-ups doc before the hub builds on the collector.

**Files:**
- Modify: `internal/collect/collector.go`, `internal/wire/encode.go`, `docs/superpowers/followups/2026-11-18-agent-view-go-p2b1-collector.md` (strike the three items)
- Test: `internal/collect/collector_test.go`

**Interfaces:**
- Consumes and keeps: everything in `internal/collect` as it is.
- Produces: no new names.

Rules: one deferred `recover` in the loop's per-item handler, which still closes the item's done channel and writes `agent-view: collector step failed: <value>` to stderr; ticks and coverage are polled without blocking between queued batches, so a queue that never empties still runs them at their interval (as TypeScript timers join the queue tail); the `Envelope.MarshalJSON` doc says to use `EncodeLine`, because `json.Marshal` escapes `<`, `>` and `&` again.

- [ ] **Step 1:** Write `TestStepPanicIsRecovered`: an unexported test hook, called at the start of every queued step, panics once; the collector keeps delivering later envelopes, the stderr line appears, and `Stop` returns. Write `TestTickRunsUnderSustainedLoad`: with `Lifecycle.CallTimeoutMs` 100 and one open call, the test pushes a batch through the collector's internal push every millisecond for 3 s; the timed-out `tool.end` for that call arrives before the load ends.
- [ ] **Step 2:** Run `go test ./internal/collect/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race -count=3 ./internal/collect/`. Expected: PASS.
- [ ] **Step 5:** Commit: `fix(go): recover collector steps and keep ticks fair under load`.

### Task 2: Envelope decoding (`internal/wire`)

The hub reads envelopes from remote collectors and feeds them to `collect.State`, so it needs them as `wire` events.

**Files:**
- Create: `internal/wire/decode.go`
- Modify: `internal/wire/event.go` (add `Raw`, `HostStatus`), `internal/collect/state_test.go` (replace the test-only golden decoder with `wire.DecodeSourceEvent`, ruling R1 of P2b-1)
- Test: `internal/wire/decode_test.go`

**Interfaces:**
- Consumes: the `wire` types of P2b-1.
- Produces: `func DecodeSourceEvent(line []byte) (SourceEvent, error)` (an event object with `kind` and `ts`, no envelope fields); `func DecodeEnvelope(line []byte) (Envelope, error)`; `type Raw struct{ KindName string; Fields []byte }` (an event of a kind this version does not know: `Kind()` returns `KindName`, and it encodes back to its original fields, so the hub forwards it); `type HostStatus struct{ Label, Kind, State, Detail string }` (JSON `label`, `kind`, `state`, `detail` omitted when empty).

Rules: `DecodeEnvelope` accepts a line only when it is a JSON object with `v` equal to 1 and with `kind` (string), `host` (string), `seq` (number) and `ts` (string) — `parseLine` plus `isEnvelope` of `src/hub/remote.ts`. A known kind whose fields do not decode into its struct is an error (a new known difference: TypeScript forwards such a line unchecked). Fields a known kind does not define are dropped (a new known difference). `seq` that is a number but not an integer is an error.

- [ ] **Step 1: Write the failing tests**
  - `TestDecodeRoundTrip`: for one envelope of every kind (reuse the rows of Task 1 of P2b-1's `TestEncodeShapes`), `EncodeLine(DecodeEnvelope(EncodeLine(e)))` equals `EncodeLine(e)` byte for byte.
  - `TestDecodeUnknownKind`: `{"v":1,"host":"h","seq":1,"kind":"future.thing","x":[1,2],"ts":"T"}` decodes to a `Raw` with kind `future.thing`, and encodes back with the same `x` and the new `host` when the envelope's host changes.
  - `TestDecodeRejects` (Review Focus 4): `v` 2; a missing `kind`, `host`, `seq` or `ts`; `"seq":"7"`; `"seq":1.5`; a `tool.start` whose `call` is a string; non-JSON; a JSON array — each returns an error.
  - The golden test of `internal/collect` passes with `DecodeSourceEvent`.
- [ ] **Step 2:** Run `go test ./internal/wire/ ./internal/collect/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test ./internal/wire/ ./internal/collect/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): decode wire envelopes`.

### Task 3: Hub and local source (`internal/hub`)

Ports `src/hub/hub.ts`.

**Files:**
- Create: `internal/hub/hub.go`, `internal/hub/local.go`
- Test: `internal/hub/hub_test.go`

**Interfaces:**
- Consumes: `collect.NewState`, `State.Apply`, `State.Load`, `State.Tick`, `State.Snapshot`, `State.FileCount`; `collect.Collector` (`Start`, `Stop`, `Subscribe`); `wire` (Task 2); `lifecycle.Options`, `lifecycle.ParseISO`.
- Produces: `type Source interface{ Label() string; Kind() string; Subscribe(fn func(wire.Envelope)) (unsubscribe func()); Status() wire.HostStatus; Start() error; Stop(); OnStatusChange(fn func()) (remove func()) }`; `type Options struct{ Lifecycle lifecycle.Options; TickMs int64 /* 0 = 1000 */ }`; `func New(o Options) *Hub`; `func (h *Hub) UniqueLabel(wanted string) string`; `func (h *Hub) AddSource(s Source) error` (error `duplicate host label: <label>`); `func (h *Hub) Subscribe(fn func(wire.Envelope)) (unsubscribe func())`; `func (h *Hub) SnapshotAll() []wire.Envelope`; `func (h *Hub) Hosts() []wire.HostStatus`; `func (h *Hub) OnHostsChange(fn func([]wire.HostStatus)) (remove func())`; `func (h *Hub) StoredFileCount() int`; `func (h *Hub) Stop()`; `type LocalSource struct`; `func NewLocalSource(c *collect.Collector, label string) *LocalSource`.

Rules: one hub mutex guards the mirrors, the subscribers and the tick; `ingest` (called on source goroutines) and `Subscribe` take it, so a subscriber gets `SnapshotAll` and then every later envelope with no gap and no duplicate. Mirrors keep the order in which sources were added. A subscriber runs while the hub mutex is held: it must not call `Subscribe`, `Stop` or `AddSource` (document it), and it should not block for long. A panicking subscriber or listener is recovered. The tick runs on its own goroutine and stops in `Stop`; `Stop` unsubscribes every source, stops them all (concurrently, waiting for all), then notifies host listeners once more. The hello offset is hub time minus `ParseISO(hello.now)` (0 when invalid); a mirror is aged and snapshotted at hub time minus its offset. `LocalSource` is `connecting` until `Start` returns, `up` after (unless a `Stop` raced it), `down` with the error message when `Start` fails, and `down` after `Stop`.

- [ ] **Step 1:** Port `test/hub/hub.test.ts` (12 tests). Tests that need a source use a fake `Source`; the LocalSource test uses a demo collector on a `demo.FakeScheduler`.
- [ ] **Step 2:** Run `go test ./internal/hub/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race -count=3 ./internal/hub/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port the hub`.

### Task 4: Remote collector (`internal/remote`)

Ports `src/hub/remote.ts`.

**Files:**
- Create: `internal/remote/remote.go`
- Modify: `internal/text/` (add `SliceUTF16`, moved from `internal/logs/text.go`; `internal/logs` calls it)
- Test: `internal/remote/remote_test.go`, `internal/text/` tests for `SliceUTF16`

**Interfaces:**
- Consumes: `hub.Source` (Task 3); `wire.DecodeEnvelope`, `wire.HostStatus` (Task 2); `logs.LineSplitter` (P2b-1).
- Produces: `func ValidSSHHost(s string) bool`; `func DefaultCommand(sshHost, label string) []string`; `type Options struct{ SSHHost, Label string; Command func(sshHost, label string) []string; BackoffMs [2]int64; MaxLineBytes int }`; `func New(o Options) (*Remote, error)` (error `invalid ssh host: use a host name or user@host`); `*Remote` implements `hub.Source`; `func (r *Remote) MalformedLines() int`.

Rules: the child runs in its own process group with stdin closed; `Stop` sends SIGTERM to the group, SIGKILL to the group after 2,000 ms, waits for the child, and never respawns. The first `hello` of a child sets `up` and arms the 60,000 ms stable timer. On exit: code 127 → `agent-view not found on <host>`; 255 → `ssh failed: <last non-empty stderr line>`; else `collector exited <code>`, or the signal name from `unix.SignalName` (for example `SIGTERM`), or `unknown`; details are cut to 200 UTF-16 units with `text.SliceUTF16`. A spawn that fails sets `down` with `spawn failed: <message>` and retries. An oversize line (more than `MaxLineBytes` pending without a newline) is counted once and dropped up to and including its newline. Status changes only notify listeners when state or detail changes.

- [ ] **Step 1: Write the failing tests**
  - Port `test/hub/remote.test.ts` (11 tests). The fake child is a real short process: `sh -c '<script>'` through `Options.Command`.
  - `TestRemoteKillsStubbornChild` (Review Focus 2): a child that runs `trap '' TERM; sleep 30` (and a grandchild in the same group): `Stop` returns within 3 s, and neither process exists afterwards.
  - `TestRemoteMidLineDrop` (Review Focus 3): a child prints one full envelope line and then half of a second line, then exits 1: exactly one envelope is delivered, the host goes `down` with `collector exited 1`, and a retry is scheduled.
  - `TestRemoteBadFieldTypes` (Review Focus 4): lines with `"seq":"7"` and a `tool.start` whose `call` is a string are counted in `MalformedLines`, and the next valid envelope is delivered.
- [ ] **Step 2:** Run `go test ./internal/remote/ ./internal/text/ ./internal/logs/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race -count=3 ./internal/remote/` and `go test ./internal/text/ ./internal/logs/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port the remote collector`.

### Task 5: Snapshot server (`internal/server`)

Ports the `/api/snapshot` route of `src/hub/server.ts`. The viewer route and `/ws` are dropped (spec).

**Files:**
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `hub.Hub` (`Hosts`, `SnapshotAll`); `wire` encoding.
- Produces: `type Server struct{ Port int; URL string }`; `func Start(h *hub.Hub, port int) (*Server, error)` (binds `127.0.0.1:<port>`; port 0 picks a free port); `func (s *Server) Stop()`.

Rules: the Global Constraints' HTTP rules. The body is built from `Hosts()` and `SnapshotAll()` (which take the hub lock briefly) and encoded with the `wire` encoder; the reply is written after the hub lock is released. `URL` is `http://127.0.0.1:<port>`.

- [ ] **Step 1: Write the failing tests**
  - Port 4 tests of `test/hub/server.test.ts`: "/api/snapshot returns hosts and envelopes with no-store and nosniff", "server rejects /ws and /api/snapshot with a foreign Host or Origin (403)" (only `/api/snapshot` exists now), "non-GET requests to data routes get 405", "unknown paths get 404 and the server binds 127.0.0.1 only". Dropped, because the spec drops the viewer and `/ws`: "server serves the bundled viewer at / and streams envelopes over /ws", "server sends hosts again when host status changes", "resnapshot message resends hello + snapshot; other messages are ignored", "stop closes sockets and releases the hub subscription", "I2: every hosts message carries the hub linger".
  - `TestRootAndWSAre404`: `GET /` and `GET /ws` with a good `Host` get 404 `not found`.
  - `TestStuckClientDoesNotBlockHub` (Review Focus 1): a hub with 2,000 agents; a raw TCP client sends a valid request and never reads; meanwhile 200 envelopes are ingested and a second client's request completes within 1 s.
- [ ] **Step 2:** Run `go test ./internal/server/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test -race ./internal/server/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): serve the hub snapshot over HTTP`.

### Task 6: Live audit (`internal/audit`)

Ports `test/live-audit.ts` as a library and the `agent-view audit [--port <n>]` command body.

**Files:**
- Create: `internal/audit/audit.go`
- Test: `internal/audit/audit_test.go`

**Interfaces:**
- Consumes: `wire.DecodeEnvelope`-compatible decoding of the snapshot body (the body's envelopes are envelope objects).
- Produces: `type Report struct{ Counts map[string]ProviderCounts; LoadedLogs, DiscoveredLogs, MalformedRecords, DegradedSources, WritesByLapsedAgents int }` with JSON keys `counts`, `loadedLogs`, `discoveredLogs`, `malformedRecords`, `degradedSources`, `writesByLapsedAgents`; `type ProviderCounts struct{ Agents, ConfirmedParents, SessionLinks, ConfirmedWrites int }` with JSON keys `agents`, `confirmedParents`, `sessionLinks`, `confirmedWrites`; `func Check(body []byte, nowMs int64) (Report, error)`; `func Fetch(port int) ([]byte, error)` (`GET http://127.0.0.1:<port>/api/snapshot`, error unless status 200).

Rules: the same checks, in the same order, with the same messages as the TypeScript asserts: `at least one agent`, `agent is its own parent`, `parent cycle`, `confirmed write has no agent ID`, `recent confirmed write has an unknown agent` (recent: under 9 minutes); counts for `claude` and `codex` in that order. The report prints as JSON with two-space indent, the same key order as the TypeScript, and nothing else.

- [ ] **Step 1:** Write `TestAuditCounts` (a body with two Claude agents — one with a confirmed parent, one with a session link — one Codex agent, one confirmed write each, two coverage envelopes: exact report), `TestAuditRejects` (one body per failing check: the error text equals the TypeScript message), and `TestAuditLapsedWrite` (a confirmed write 10 minutes old by an unknown agent counts in `writesByLapsedAgents`; 8 minutes old fails).
- [ ] **Step 2:** Run `go test ./internal/audit/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test ./internal/audit/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): port the live audit`.

### Task 7: `agent-view serve` and `agent-view audit` (`internal/cli`, `cmd/agent-view`)

Ports the hub mode of `src/cli.ts` (`runHub`, `remoteCommandOverride`, the `--remote` and `--port` rules) as `serve`, and adds `audit`.

**Files:**
- Modify: `internal/cli/args.go`, `internal/cli/args_test.go`, `cmd/agent-view/main.go`, `README.md` (the Go section: `serve`, `audit`, the dropped browser viewer)
- Create: `cmd/agent-view/serve.go`, `cmd/agent-view/audit.go`
- Test: `cmd/agent-view/serve_test.go`, `internal/cli/args_test.go`

**Interfaces:**
- Consumes: `hub.New`, `hub.NewLocalSource`, `remote.New`, `remote.ValidSSHHost`, `server.Start`, `audit.Fetch`, `audit.Check`, `collect.New`.
- Produces: `cli.Args.Mode` gains `"serve"` and `"audit"`; `cli.Args` gains `Port int` and `Remotes []string`; `func cli.RemoteCommandOverride(value string) ([]string, error)`.

Rules: `serve` and `audit` must be the first argument. `serve` accepts `--port` (digits only, at most 65535, default 4317; `Invalid port: <value>`), `--remote` (repeatable; `Invalid --remote value (use a host name or user@host, no spaces or shell characters)`), `--demo`, `--demo-scale`, `--watch`, `--linger`. `audit` accepts only `--port`. `--port` outside `serve` and `audit`, and `--remote` outside `serve`, are usage errors (the TUI keeps its P1 messages until P2c). `serve`: the local label is `hub.UniqueLabel(<hostname up to the first dot, or "local">)`; each remote gets `UniqueLabel(<ssh host>)`; `AGENT_VIEW_REMOTE_COMMAND` (test only) replaces every remote's argv when it is a non-empty JSON array of strings (`AGENT_VIEW_REMOTE_COMMAND must be a JSON array of strings (test only)` and exit 2 otherwise); prints `Agent View: <url>` and a newline to stdout; a port in use prints `agent-view: cannot start the server on port <n>: <message>`, stops the hub, exits 1; remote failures print `agent-view: remote <label> failed: <message>` and never stop the hub; a local collector failure prints `agent-view: local collector failed: <message>` and keeps serving; SIGINT and SIGTERM stop the server and the hub and exit 0 (within 2 s, as `collect`). `audit`: prints the report and exits 0, or prints `agent-view: audit: <message>` and exits 1. The TUI's non-terminal message becomes `agent-view needs a terminal; use agent-view serve for a headless hub`.

- [ ] **Step 1: Write the failing tests**
  - Port the hub-mode tests of `test/cli.test.ts` as `serve` tests: "parseArgs: defaults, --remote repeatable, …" (the `--remote` and `--port` parts), "parseArgs rejects --remote values that could inject ssh options or shell text", "remoteCommandOverride: unset is null; a JSON array of strings is the argv; anything else is an error", "hub mode prints the URL, serves /api/snapshot with the local host up, and exits 0 on SIGTERM", "hub mode: --remote adds one remote host per flag; an unreachable remote never stops the hub" (5 tests; the binary tests run `serve --port 0`).
  - `TestServePortInUse` (Review Focus 5): a listener holds a port; `serve --port <it> --demo` exits 1 within 3 s with the message, and no child process remains.
  - `TestAuditCommand`: `audit --port <port of a running serve --demo>` prints a report with `counts.claude.agents` above 0 and exits 0; `audit --port <a closed port>` exits 1.
- [ ] **Step 2:** Run `go test ./internal/cli/ ./cmd/agent-view/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `go test ./internal/cli/ ./cmd/agent-view/`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(go): add agent-view serve and agent-view audit`.

### Task 8: Hub parity with TypeScript (`internal/parity`)

Extends the P2b-1 parity test to the hub: the TypeScript hub (`bun src/cli.ts --no-open --port 0 …`) and the Go hub (`agent-view serve --port 0 …`) run on the same inputs, and their `/api/snapshot` bodies and audit reports must match.

**Files:**
- Create: `internal/parity/hub_test.go`
- Modify: `README.md` (the "Parity" paragraph)

**Interfaces:**
- Consumes: the P2b-1 parity helpers (temporary `HOME`, fixture copying, real bun path, normalizer); the Go binary built in `TestMain`.

Rules:
- Inputs: a temporary `HOME` with one Claude session log and one Codex rollout that the test writes at start with timestamps from the last minute (the P2b-1 fixtures are dated 2026-01-01, so their agents are outside the linger window and a snapshot would hold none), plus one fake remote `--remote fakehost`. `AGENT_VIEW_REMOTE_COMMAND` is `["sh","-c","cat <file>; exec sleep 600"]` for both hubs. The test writes `<file>` at start: a `hello`, an empty `snapshot`, three `agent.upsert` (one sub-agent), two `tool.start`, one `tool.end`, a `coverage`, all with timestamps from the last minute and `host` set to `elsewhere` (the hubs must rewrite it to `fakehost`).
- Wait until both report the local host and `fakehost` `up` and the bodies stop changing for 3 s (60 s at most). Fetch both bodies.
- Normalize: drop every envelope's `seq` and `ts`, `hello.now`, `hello.collectorVersion`, `hello.platform`; compare `hosts` exactly, and `envelopes` in order as parsed JSON values, with the P2b-1 rules for empty `command`/`pattern` and timed-out calls.
- Audit parity: run `bun test/live-audit.ts <port>` and `agent-view audit --port <port>` against the Go hub; the printed JSON must be equal as parsed values.
- Non-vacuity: the Go body must hold at least one agent per host and both hosts `up`; the audit must report at least one Claude agent.
- Skip when bun is not on PATH. Both hubs and their children are stopped on every path.

- [ ] **Step 1:** Write `TestParityHubSnapshot` and `TestParityAudit`.
- [ ] **Step 2:** Run `go test -count=1 -run 'TestParityHub|TestParityAudit' ./internal/parity/`. Expected: PASS (fix the owning Go package if a difference appears; if TypeScript has a bug, Go matches TypeScript and the bug goes into a new `docs/superpowers/followups/2026-11-18-agent-view-go-p2b2-hub.md`).
- [ ] **Step 3:** Run `go test -count=1 ./...`, `go vet ./...`, `GOOS=linux CGO_ENABLED=0 go vet ./...`, `gofmt -l .`. Expected: all clean.
- [ ] **Step 4:** Commit: `test(go): add hub parity against TypeScript`.
