# Agent View — local prototype

A local card graph for Codex and Claude Code sessions, with parent links and a live file-change feed. Reads existing logs. Does not launch agents, change agent settings, or send data to a remote service.

## Run

Requires Node.js 22 or later. No packages need to be installed.

```sh
cd /Users/la.kyle.dougan/git/personal/agent-view
npm start
```

Open http://127.0.0.1:4317. To include another directory in file monitoring:

```sh
npm start -- --watch /absolute/project/path
```

Repeat `--watch` for more directories. `PORT=4318 npm start` selects a different local port.

```sh
npm test          # Isolated parser and filesystem tests; no model calls
npm run audit:live # Check real sessions from the running viewer; prints counts only
npm run demo     # Simulated graph, explicitly labeled; uses the same default port
```

## What the graph proves

- **Codex confirmed parent:** `session_meta.parent_thread_id` or `source.subagent.thread_spawn.parent_thread_id`.
- **Claude confirmed parent:** an `Agent` or `Task` tool result with `toolUseResult.agentId`, paired with the calling agent's tool record.
- **Claude dashed session link:** a sub-agent transcript names its owning session. This does not prove an immediate parent. A later explicit tool result replaces this link.
- Missing parents are labeled as outside the loaded history. Message `parentUuid` is never treated as an agent parent.
- Identity includes provider, account scope, session ID, and Claude sub-agent ID. Separate sessions can reuse a tool-call ID safely.
- Working and finished states come from recorded events. “Stale” means no recorded agent event for two minutes. A stale agent might still be running. This is not a process monitor.

## What file attribution proves

The feed has two independent evidence sources:

1. **Confirmed tool receipt:** a paired successful file-tool call and result. Claude `Write`, `Edit`, `MultiEdit`, and `NotebookEdit` are supported. Codex native `FileChange` records with `status: completed` identify the writing thread and changed paths, including patches executed inside `functions.exec`. Direct `apply_patch` call/result pairs remain a fallback. The receipt proves what the tool reported; it does not provide a byte-level operating-system audit.
2. **Unknown filesystem change:** a difference between file metadata snapshots. The collector never assigns this event to an agent based on timing, working directory, or recent activity.

A tool receipt and a filesystem change can describe the same edit. They remain separate to avoid a false merge when two agents write the same file. Failed or incomplete tools do not produce confirmed writes. Arbitrary shell writes and MCP editors are not attributed without an explicit file-change receipt. A completed shell command and its stdout do not prove which files it wrote. Their file changes can still appear as unknown when their directories are monitored. Diffs and file contents are not read from disk. Command output can contain file contents if the command printed them.

## Recorded commands

The Commands view reads native Codex `CommandExecution` records, including commands called inside `functions.exec`. It shows the owning agent, working directory, result, exit code, and recorded output. Click a command to load its details. stdout and stderr may be combined by Codex; the viewer does not invent a separation.

The newest 100 commands are retained. Each command is limited to 4,096 characters and its output to the last 16,384 characters. Truncation is labeled. Full command text and output are fetched only when opened, rather than broadcast with each snapshot. Records update when Codex writes an execution event; this is not a direct OS stdout stream. The Codex execution handle is not treated as an operating-system PID.

## Better process monitoring

Native events solve agent identity and explicit patch attribution. For arbitrary shell scripts, a future collector needs an explicit mapping from agent/tool call to OS process identity, plus OS file events for that process and its descendants. Process identity must include a start time or equivalent token to avoid PID reuse. Several agents can share one Codex process, so process ancestry alone cannot prove the agent identity.

This machine includes `/usr/bin/eslogger`, which can emit Endpoint Security process and file events as JSON lines. It is suitable for a prototype experiment, but Apple's local manual explicitly says its output is not a stable API. It requires root and Full Disk Access for the responsible process. No tracing was started. On macOS, `fs_usage` can also help diagnose filesystem system calls with elevated privileges. A durable collector can use Endpoint Security process and filesystem events, but deployment requires Apple's Endpoint Security entitlement, root privileges, and Full Disk Access. Neither source provides an automatic agent identity or a universal stdout tap. Capture stdout/stderr at process launch, or consume the runtime's output event stream. Do not read another process's pipe as a substitute: doing so can consume its data.

Codex tool hooks also cover nested code-mode calls and can supply tool IDs. Hook installation and privileged process tracing are not enabled by this prototype.

References: [Codex hooks](https://learn.chatgpt.com/docs/hooks), [Codex app server](https://learn.chatgpt.com/docs/app-server), [Apple filesystem tracing](https://developer.apple.com/library/archive/documentation/Performance/Conceptual/FileSystem/Articles/FileSystemCalls.html), [Endpoint Security entitlement](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.developer.endpoint-security.client).

## Sources and limits

- Codex: `$CODEX_HOME/sessions`, or `~/.codex/sessions`, plus existing `~/.codex-accounts/*/sessions`. Real paths remove duplicate account roots.
- Claude Code: `$CLAUDE_CONFIG_DIR/projects`, or `~/.claude/projects`.
- At most 120 recent logs load, divided equally between discovered providers. Only the header and last 4 MiB load for a large log. Earlier call/result pairs can be missing. New log discovery runs every five scan cycles.
- The collector waits for complete JSON lines, preserves split UTF-8, and handles file truncation or replacement. Malformed and oversized records are counted or reported.
- At most 24 project directories are monitored. Each directory has a 12,000-entry scan limit. Over-limit or unreadable directories are skipped and listed in the coverage panel. Failed directories retry after one minute.
- File snapshots use modification time, change time, size, and inode. Each scan is followed by a one-second delay. Large projects take longer. Brief files created and deleted between scans can be missed. Renames appear as create/delete events. Metadata-only changes can appear as changes.
- File monitoring starts with a baseline. Existing files do not appear as newly created. Tool receipts can include loaded history.
- Directory symlinks are not followed. Common dependency, build, cache, Git, and `.worktrees` directories are skipped during scans. An agent's actual worktree can still be monitored as a separate root.
- The newest 500 file events remain in memory. The UI displays at most 150 matching events. Restarting loses filesystem history; available tool receipts reload from logs.
- No SSH support or automatic hook installation yet. Log formats are internal and can change with agent releases.

The coverage panel lists actual log roots, watched directories, parse errors, and scan limits. Coverage is partial; the viewer does not claim to monitor every operation on the machine.

## Local access

The server binds only to `127.0.0.1`. It validates Host and Origin, exposes read-only routes, and serves no arbitrary file paths. It has no account login. Local programs and people who can access the browser on this machine can view project paths, agent names, and tool metadata. Conversation and reasoning records are not sent to the UI. Command previews and command output are local user data and are not automatically redacted. Output can include sensitive text printed by a command. Agent settings and source logs are never written.

## Implementation

- `src/model.mjs`: provider adapters, agent graph, call/result matching, and evidence labels.
- `src/native-events.mjs`: native Codex file changes and bounded command records.
- `src/collector.mjs`: log discovery, incremental JSONL reads, and metadata snapshots.
- `src/server.mjs`: local HTTP and server-sent events.
- `public/`: dependency-free graph, filters, detail dialog, and file feed.
- `test/`: deterministic regression tests and a separate local-log audit.

This is a feasibility prototype, not a complete monitoring agent.
