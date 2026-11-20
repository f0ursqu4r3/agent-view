# P2c performance results (2026-11-19)

Spec: `docs/superpowers/specs/2026-11-18-agent-view-tui-design.md`. This file measures the five performance targets on the whole process: the collector, the hub and the TUI in one program. Tooling: `tools/perf/measure.sh`, `TestLatencyAppendToModel` in `cmd/agent-view/latency_test.go`, `tools/dist` and `BenchmarkFrame50Agents3Hosts200Envelopes`.

Result: four targets are met outright (size, frame time, latency and memory). The idle CPU target is met in steady state (0.2–0.3%). Averaged over the first 60 s from launch, it is 2.0% (83 logs) and 2.8% (120 logs). The latency row has one condition: the writer closes the log file after each append. See "Open caveats".

## Environment

- Mac: Apple M4 Max, 16 cores, 128 GB. macOS 27.0.1 (Darwin 27.0.0). Go 1.27.3 darwin/arm64. tmux 3.6b.
- `fseventsd` load (instant CPU from `top`, before and after every measurement and before every latency run): 0.2% to 0.8% of one core. It never rose above 1%, so no run here is under a busy `fseventsd`. Other software was busy: the three busiest other processes used about 30% to 170% of one core each. This load is the same in all runs.
- Binaries: built with `go run ./tools/dist -out <temp dir>`, so `-trimpath` and `-ldflags "-s -w"`. The CPU, memory and demo runs use `darwin-arm64`.
- Real logs: read only. Every number about them is a count or a measurement. No path, name or content is in this file.

## The five targets

| # | Target (spec) | Measured | Met? |
|---|---|---|---|
| 1 | Binary under 20 MB for each target | darwin-arm64 8.7 MB, darwin-x64 9.4 MB, linux-x64 9.3 MB, linux-arm64 8.6 MB (9,144,802 / 9,836,552 / 9,789,600 / 9,044,128 bytes) | met |
| 2 | 50 agents and 200 events per second: board and grid under 4 ms per frame | median of 5 runs: 1.52 ms for one batch of 200 envelopes plus one frame; 0.89 ms for the frame alone | met |
| 3 | Agents idle: whole process under 2% of one core | after the first scan: 0.2% average, 1.0% busiest second (83 logs); 0.3% average, 1.0% busiest second (120 logs). First 60 s from launch: 2.0% (83 logs, two runs) and 2.8% (120 logs) | met after the first scan; at the limit (83 logs) or over it (120 logs) when the first scan counts |
| 4 | Event-to-screen latency under 1 second | append to the TUI model: p50 64 ms, p95 65 to 66 ms, slowest 66 to 74 ms (6 runs of 20 appends). The screen adds at most one frame, 50 ms at 20 FPS. Condition: the writer closes the log file after each append. A writer that holds the file open is seen only by the 2 s poll, up to 2 s | met for a writer that closes the file after each append; over the target (up to 2 s) for a writer that holds it open |
| 5 | Memory under 150 MB with the 24-hour scan of 120 logs | peak RSS 71.6 MB and 71.1 MB with 120 logs; 64.3 to 70.2 MB with the 83 logs that the 24-hour rule gives on this machine | met |

MB in row 1 is 1,048,576 bytes, as in `tools/dist`. The limit is 20 MB.

## Measurements

CPU and memory, each run through `tools/perf/measure.sh`. "From launch" is a run of 60 s. "After the first scan" is a run of 70 s with `--skip 10`: the first 10 s do not count for the two CPU lines. Peak RSS always covers the whole run.

| Run | Logs read | Agents working | CPU avg | CPU busiest second | Peak RSS |
|---|---|---|---|---|---|
| (a) real HOME, from launch | the real roots, 24-hour rule | 1 | 3.5% | 101.4% | 71.2 MB |
| (a) real HOME, after the first scan | same | 1 | 1.6% | 1.9% | 73.5 MB |
| (b) idle scan, 83 logs, from launch | 60 Claude + 23 Codex, 148.2 MB | 0 | 2.0% | 101.8% | 69.0 MB |
| (b) idle scan, 83 logs, from launch (second run) | same | 0 | 2.0% | 103.4% | 70.2 MB |
| (b) idle scan, 83 logs, after the first scan | same | 0 | 0.2% | 1.0% | 64.3 MB |
| (b) idle scan, 120 logs, from launch | 60 Claude + 60 Codex, 492.6 MB | 0 | 2.8% | 146.7% | 71.6 MB |
| (b) idle scan, 120 logs, after the first scan | same | 0 | 0.3% | 1.0% | 71.1 MB |
| (c) `--demo --demo-scale stress`, from launch | none (demo source) | 45 to 46 of 50 | 10.1% | 11.5% | 33.9 MB |
| (c) `--demo --demo-scale stress`, second run | none | 45 to 46 of 50 | 10.0% | 11.5% | 34.2 MB |

- The real HOME had 2 agents on the board and 1 of them working in every run. The idle target applies at 0 working agents, so rows (a) are not the idle target. They show the cost with one live agent.
- The first scan costs about 1.1 s of CPU time with 83 logs and about 1.5 s with 120 logs. It makes the busiest second in the "from launch" rows.

Benchmark (d), `go test -run xxx -bench BenchmarkFrame50Agents3Hosts200Envelopes -count 5 ./internal/tui/`:

| Run | ns/op (batch + frame) | compose-ns/op (frame) | B/op | allocs/op |
|---|---|---|---|---|
| 1 | 1,523,284 | 886,637 | 6,423,300 | 4,823 |
| 2 | 1,579,921 | 901,065 | 6,423,221 | 4,823 |
| 3 | 1,520,494 | 880,286 | 6,423,149 | 4,824 |
| 4 | 1,519,605 | 875,739 | 6,423,349 | 4,824 |
| 5 | 1,612,016 | 920,606 | 6,423,181 | 4,824 |
| median | 1,523,284 | 886,637 | | |

Latency, `go test -run TestLatencyAppendToModel -v ./cmd/agent-view/`, 20 appends per run, 1.5 s apart:

| Run | p50 | p95 | slowest |
|---|---|---|---|
| 1 | 64 ms | 66 ms | 67 ms |
| 2 | 64 ms | 65 ms | 66 ms |
| 3 | 64 ms | 65 ms | 72 ms |
| 4 | 64 ms | 66 ms | 67 ms |
| 5 | 64 ms | 66 ms | 69 ms |
| 6 | 64 ms | 66 ms | 74 ms |

About 50 ms of the 64 ms is the bridge batch (50 ms). The rest is FSEvents (about 10 ms in a watcher-only probe) and the collector.

## Method

- **CPU.** `tools/perf/measure.sh <binary> <seconds> [args…]` starts the program as the only process of a pane in its own tmux server, on a private socket (`av-perf-<pid>`), 100 x 30. It samples that one PID every second. CPU comes from the cumulative CPU time of the PID (`ps -o time=`), not from `ps -o %cpu`. The average is the CPU time between the first and the last sample over the wall time between them. The busiest second is the highest value between two samples. Peak RSS is the largest `ps -o rss=` value. The script prints these three numbers and nothing else. It stops the program with `kill-server` on its own socket, then removes the socket file.
- **(a) Real HOME.** The program runs on the real HOME as the owner runs it. It reads the real logs read only. The number of agents working comes from the status bar of the pane (`capture-pane -p`), read at 15 s, 30 s and 45 s; only the counts were kept.
- **(b) Idle scan.** A temporary HOME holds copies of real logs, laid out under the same kinds of root as the originals: `.claude/projects`, and `.codex/sessions` or `.codex-accounts/<account>/sessions` for Codex. Every copy has its modification time set to one hour ago. The program found no agent working. The copies and the HOME are deleted.
  - 83 logs is what the collector's rule gives on this machine: modified in the last 24 hours, newest first, at most floor(120 / providers) per provider that has logs. Codex had 23 logs in 24 hours, and its unused quota does not pass to Claude.
  - 120 logs is the most the rule allows: 60 Claude and 60 Codex, taken from the last 30 days, with the modification time set to one hour ago. It is the worst case for memory.
  - Logs of sessions that were still writing at the time of the copy were left out (modified in the last 30 minutes). A tool call that is open at the copy shows as working.
- **(c) Busy.** `--demo --demo-scale stress`: 50 agents and about 200 events per second. The spec has no CPU target for this case; the number is recorded. The frame target is row 2.
- **(d) Frame benchmark.** The Task 6 benchmark, 5 runs, median. ns/op covers applying one batch of 200 envelopes and composing one frame. compose-ns/op is the frame alone. Both are below 4 ms.
- **Latency.** `TestLatencyAppendToModel` builds the stack as `runTUI` does (`newStack`, `bridge.Start`, a fake `Send`) on a temporary HOME with one Claude log. After the first scan, it appends a tool-call pair (`tool_use`, `tool_result`) 20 times, 1.5 s apart, each with a new call ID. An append opens the file, writes and closes it. The latency of an append is the time from just before the open to the arrival of the `EnvelopesMsg` that holds a `tool.start` for that call ID. The test logs p50 and p95. It fails only when an append takes more than 10 s or never arrives. It also fails when a goroutine of the stack survives the stop.
- **Sizes.** The table of `go run ./tools/dist -out <temp dir>`; all four targets build on this Mac and the smoke test passes. The temporary directories and binaries are deleted.

## Open caveats

- **Idle CPU and the first scan.** The first scan reads every selected log once. It uses more than one core for one to two seconds (busiest second 101.8% to 146.7%). After that the process uses 0.2% to 0.3% of one core. Averaged over the first 60 s from launch, the cost is 2.0% (83 logs) and 2.8% (120 logs). The spec says "with agents idle"; this file reads that as the steady state, and reports the first 60 s too.
- **Latency depends on the writer closing the file.** FSEvents posts the modified event of a file when its writer closes it. A watcher-only probe showed 5 to 14 ms when the writer closes after each append, and no event within 3 s when the writer keeps the file open. A writer that keeps the log open is seen only by the 2 s poll: up to 2 s, which is over the 1 s target. An early version of the test that kept the file open measured 0.50 s p50 and 2.03 s for the slowest append (the 1.5 s gap and the 2 s poll aligned). At the time of the runs, `lsof` listed 17 Claude Code processes and 9 Codex processes: none held a log open when sampled.
- **Latency is measured to the TUI model**, at the `Send` of the bridge. The screen adds the next frame: at most 50 ms at 20 FPS, and 0.9 ms to compose it.
- **Linux was not measured.** The Linux binaries are built and sized here. CPU, memory and latency on Linux (inotify) are not measured. `measure.sh` runs on Linux, where `ps -o time=` has a resolution of one second, so the busiest-second value is coarse there.
- **One machine, single runs.** The numbers come from one Mac. Repeated runs agree within 0.1% for CPU and 6 MB for memory. Memory depends on the content of the logs, not only on their count.
- **No busy `fseventsd`.** `fseventsd` stayed below 1% in all runs. A busy `fseventsd` delays events by seconds, and this file does not measure that case. The test does not assert the 1 s target for that reason: it fails only above 10 s.
