# P2a spike results: file watching in Go (2026-11-18)

Spec: `docs/superpowers/specs/2026-11-18-agent-view-tui-design.md` (milestone P2a). Harness: `spikes/fswatch/` (its own Go module, not part of `./...`). Later tasks read the decision lines below. One decision per line.

FS_WATCH_DARWIN=fsevents (github.com/fsnotify/fsevents v0.2.0, cgo; flags FileEvents|NoDefer; latency 50 ms)
FS_WATCH_LINUX=inotify (github.com/fsnotify/fsnotify v1.10.1; one watch per directory, found by a walk; a new directory is added on Create and then read)
FS_WATCH_KQUEUE=no-go (one descriptor per file and per directory; the largest local repo needs more descriptors than the per-process limit)
FS_WATCH_ROOTS=resolve each root with filepath.EvalSymlinks before watching; FSEvents reports real paths (/private/tmp, not /tmp)
FS_WATCH_DROPS=on FSEvents MustScanSubDirs, UserDropped or KernelDropped, and on fsnotify ErrEventOverflow, report the root as degraded (fs:<root>) with the count of drops; no per-file rescan
FS_WATCH_IGNORED=FSEvents cannot skip node_modules and the other ignored directories; filter them in the event loop (about 40 ms of CPU per 20,000 events)
BUILD_DARWIN=CGO_ENABLED=1 on macOS for both darwin/arm64 and darwin/amd64; a darwin build with CGO_ENABLED=0 fails
BUILD_LINUX=CGO_ENABLED=0 (fsnotify only; pure Go)

The spike passes the spec's rule for FSEvents: no missed events in the M0 scenarios, 0 descriptors, 0% idle CPU. One condition: under heavy churn FSEvents can drop events. It always signals the drop with a flag, so the collector must handle the flag (see "Drops under churn").

## Environment

- Mac: Apple M4 Max, Darwin 27.0.0. Go 1.27.3. Xcode clang 21.0.0. `kern.maxfilesperproc` 245,760.
- Linux: OrbStack container `alpine:3.20`, kernel 7.2.12-orbstack, linux/arm64, 8 CPUs. `max_user_watches` 1,048,576, `max_queued_events` 1,048,576. The harness ran as a static binary (`CGO_ENABLED=0`); test files were written inside the container (overlay and tmpfs), not through a bind mount.

## Method

Same as M0 (`docs/superpowers/spikes/2026-11-17-m0-results.md`, section 2): 1,000 files, 10 sub-directories x 100 files, written over 2,000 ms. Watcher armed 300 ms before the first write. Latency is measured from just before the write to the first event for that file. Settle time 1,500 ms. 3 runs per class and backend.

- `fsevents`: one stream on the root, flags `FileEvents`, latency 50 ms (`NoDefer` tested as a variant).
- `fsnotify` (kqueue on macOS, inotify on Linux): one watch per directory, found by a walk that skips the ignored directory names of `fs-changes.ts`. A new directory is added on its Create event and then read, so files written before the add are reported. This is the TypeScript per-dir mode.
- Descriptors: the count in `/dev/fd` (macOS) or `/proc/self/fd` (Linux), minus the count before arming.

Scale mode: a tree of 20,000 or 100,000 files (10 files per directory), then 20 s idle, a burst of 200 files (one per directory over 1 s), and churn: a separate process writes 20,000 files as fast as it can into `node_modules` (ignored) or into `gen` (not ignored). The CPU numbers are the watcher process only.

## macOS: M0 scenarios

Every class, every backend, every run: 1000/1000 files seen, 0 missed, 0 unknown events.

| Class | FSEvents: p95 (3 runs) | kqueue: p95 (3 runs) | kqueue descriptors at end |
|---|---|---|---|
| home (`test/.tmp`) | 54.8–55.7 ms | 0.5–0.8 ms | 1,013 |
| tmpdir (`/private/var/folders/…/T`) | 55.3–55.9 ms | 0.6–0.8 ms | 1,013 |
| private-tmp | 55.1–55.5 ms | 0.8–1.0 ms | 1,013 |
| private-tmp + late sub-directories | 55.0–55.5 ms | 0.8–1.1 ms | 1,018–1,020 |
| private-tmp ROOT watch (watcher on `/private/tmp`) | 53.6–56.9 ms | 0.6–0.8 ms | 134,695–134,803 |
| private-tmp + external writer | 53.7–57.4 ms | 0.5–0.8 ms | 1,013 |
| `/tmp` (symlinked spelling) | 54.2–56.0 ms | 0.7–1.2 ms | 1,013 |
| home + external writer | 54.7–56.1 ms | 0.5–0.7 ms | 1,013 |
| symlink root | 54.4–56.7 ms | 0.8–0.9 ms | 1,013 |

- FSEvents: 0 descriptors in every run. Arming took 0.4–1.4 ms. 1,000–1,017 events per 1,000 files.
- kqueue: one descriptor per directory and per file (11 directories, 1,000 files, and 2 for the kqueue itself). The ROOT watch walked 10,329–10,432 directories under `/private/tmp`, opened 133,694–133,789 descriptors, and took 2.2–4.7 s to arm.

Sparse writes (20 files, 500 ms apart, private-tmp, 3 runs). This is the usual case: one agent edits one file.

| Setting | p95 |
|---|---|
| FSEvents, latency 50 ms | 62.4–67.2 ms |
| FSEvents, latency 50 ms, `NoDefer` | 13.2–19.3 ms |
| FSEvents, latency 10 ms | 24.2–25.2 ms |
| kqueue | 1.2–3.5 ms |

`NoDefer` sends the first event after a quiet period at once, then batches for the latency window. In the 1,000-file burst it made no difference (p95 54.4–65.0 ms). Both settings are far inside the 1-second event-to-screen budget.

## macOS: scale

| Tree | Backend | Arm (wall / CPU) | Descriptors | Peak RSS | Idle CPU (20 s) | Burst 200 files |
|---|---|---|---|---|---|---|
| 20,000 files, 2,201 dirs | FSEvents | 1.0 / 0.5 ms | 0 | 14.5 MB | 0.000% | 200/200, p95 57.8 ms |
| 20,000 files, 2,201 dirs | kqueue | 289 / 299 ms | 22,204 | 26.5 MB | 0.000% | 200/200, p95 0.2 ms |
| 100,000 files, 11,001 dirs | FSEvents | 1.3 / 0.6 ms | 0 | 15.6 MB | 0.000% | 200/200, p95 55.9 ms |
| 100,000 files, 11,001 dirs | kqueue | 1,775 / 1,824 ms | 111,004 | 86.0 MB (121.5 MB after churn) | 0.000% | 200/200, p95 0.4 ms |

The spec's memory target is 150 MB for the whole process. kqueue alone used 121.5 MB on the 100,000-file tree.

### Local repositories

`fswatch count` on the 99 git repositories under `~/git` (depth 3 or less), with the ignored directory names skipped:

| | p50 | p90 | max |
|---|---|---|---|
| Files | 241 | 5,508 | 341,127 |
| Directories | 48 | 555 | 7,615 |

Five repositories have more than 20,000 files. kqueue needs one descriptor per file and per directory. The largest repository needs about 341,000, more than `kern.maxfilesperproc` (245,760). kqueue cannot watch it. This decides FS_WATCH_KQUEUE.

### Churn in an ignored directory

20,000 files into `node_modules`:

| Backend | Events delivered | Watcher CPU | Drop flags |
|---|---|---|---|
| FSEvents (3 runs) | 19,513–20,014, all filtered as ignored | 39.7–41.5 ms | 3–5, path = the root |
| kqueue (3 runs) | 0 (`node_modules` is not watched) | 1.7–4.1 ms | none |

FSEvents has no subtree exclusion in `fsnotify/fsevents` v0.2.0 (the C API `FSEventStreamSetExclusionPaths` allows 8 paths, but the package does not expose it). The event loop must filter. At about 40 ms of CPU per 20,000 events, the cost is small.

### Drops under churn

20,000 files into `gen` (not ignored) on a 20,000-file tree:

| Backend | Files seen | Watcher CPU | Drop flags |
|---|---|---|---|
| FSEvents | 6,328 / 20,000 | 21.5 ms | 11 x (MustScanSubDirs + UserDropped), path = the root |
| kqueue | 20,000 / 20,000 | 1,178 ms | none |

FSEvents latency sweep on a 2,000-file tree, the same churn, 2 runs per setting (0 s, 10 ms, 50 ms, 50 ms + `NoDefer`, 200 ms, 1 s): files seen 14,788–20,000. 7 of 12 runs lost files. **Every run that lost files had drop flags, and every run without drop flags saw all 20,000 files.** The latency setting does not control the drops.

So FSEvents does not miss events silently: it signals each loss with `MustScanSubDirs` and `UserDropped` on the watched root. A rate of 20,000 files per second into tracked files is rare for an agent (a large `git checkout`, an archive extract, code generation). The collector must turn the flag into a visible state (FS_WATCH_DROPS) instead of a silent gap. A per-file rescan would need an index of the whole tree in memory, so it is out of scope.

Not tested: whether a lighter event callback lowers the drop rate. The cgo callback converts every path to a Go string (`C.GoString`). P2b can measure this with the real event loop.

## Linux: inotify

M0 scenarios: every class, every run: 1000/1000 files seen, 0 missed, 0 unknown events, 1 descriptor (the inotify instance; the first run also opened 2 for the Go runtime's poller, which stay open). 2,000–2,011 events per 1,000 files (Create and Write for each file).

| Class | p95 (3 runs) |
|---|---|
| home (`/root`) | 0.5 ms |
| tmp | 0.3 ms |
| tmp + late sub-directories | 0.3 ms |
| tmp ROOT watch (watcher on `/tmp`) | 0.3–0.5 ms |
| tmp + external writer | 0.2–0.3 ms |
| symlink root | 0.2–0.4 ms |
| `/dev/shm` (tmpfs) | 0.2 ms |

Scale:

| Tree | Arm (wall / CPU) | Descriptors | Peak RSS | Idle CPU (20 s) | Burst | Churn into `gen` | Churn into `node_modules` |
|---|---|---|---|---|---|---|---|
| 20,000 files, 2,201 dirs | 17.9 / 20.3 ms | 1 | 8.2 MB | 0.002% | 200/200, p95 1.1 ms | 20,000/20,000, 169 ms CPU | 0 events, 0.6 ms CPU |
| 100,000 files, 11,001 dirs | 101.8 / 107.1 ms | 1 | 8.8 MB | 0.001% | 200/200, p95 0.5 ms | 20,000/20,000, 154 ms CPU | 0 events, 0.6 ms CPU |

Risk not tested: this kernel's limits are high. Many distributions use `max_queued_events` 16,384. Kernels before 5.11 use `max_user_watches` 8,192, and later kernels scale it with RAM. A burst above the queue size gives `ErrEventOverflow` (handled by FS_WATCH_DROPS). A tree with more directories than `max_user_watches` gets a partial watch: the walk must stop at the limit and report the root as degraded, as `MAX_DIRS` does today. The collector should read both limits at start and include them in the degraded detail. The spike did not change the limits: they are system-wide settings.

## Build

- `GOARCH=amd64 CGO_ENABLED=1 go build` on this arm64 Mac gives a working Mach-O x86_64 binary. No extra toolchain is needed.
- `CGO_ENABLED=0` on darwin fails: `fsnotify/fsevents` needs cgo. darwin builds must run on macOS. A Linux CI cannot build them without a macOS SDK cross-toolchain.
- The binary links `CoreServices.framework` and `CoreFoundation.framework` (system frameworks, always present).
- Spike binaries (both libraries plus the harness): darwin/arm64 3.81 MB, darwin/amd64 3.98 MB, linux/arm64 3.59 MB. The P1 binary was 4.6–4.8 MB, so the size target (20 MB) is not at risk.

## Notes for P2b

1. Resolve roots with `EvalSymlinks` first. Match event paths against the resolved root. FSEvents reports `/private/tmp/…` for a root given as `/tmp/…`.
2. FSEvents also reports events for directories, and for the root itself. Treat a path equal to the root as a root event, not a file change.
3. With fsnotify, add a new directory on Create, then read it and report what is already there. In the late-sub-directory class this found every file that was written before the add.
4. On macOS, kqueue gives sub-millisecond latency, but its descriptor and memory cost grows with every file. FSEvents is the incumbent in practice: Bun's recursive `fs.watch` uses it, which explains the 54 ms p95 in M0.
5. The kqueue per-dir mode of `watch.ts` (ruling R8 fallback) does not carry over to Go on macOS. If FSEvents fails to start, report the root as degraded and do not fall back to kqueue.

Spike files lived under `test/.tmp/` (git-ignored), `/private/var/folders/…/T`, `/private/tmp`, and the container's own filesystem. They were removed after each run.
