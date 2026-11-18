import path from 'node:path';
import { categoryFor } from '../../shared/schema';
import { MAX_COMMAND_CHARS, MAX_PENDING, isObject, outputEvents, str } from './common';
import type { Json } from './common';
import type { Agent, AgentStatus, Call, Category, SourceEvent, WriteOp } from '../../shared/schema';

type Identity = Pick<Agent, 'name' | 'role' | 'parentId' | 'link' | 'cwd'> & { model?: string };
type AgentState = Identity & { status: AgentStatus; lastEventAt: string };
type Write = { path: string; op: WriteOp };
type Pending = { agentId: string; name: string; category: Category; writes: Write[] };
/** Per log file: which thread agent owns the file's direct records. */
type Source = { agentId: string | null };

const FILE_CHANGE_OPS: Record<string, WriteOp> = { add: 'create', update: 'edit', delete: 'delete' };
const PATCH_OPS: Record<string, WriteOp> = { Add: 'create', Update: 'edit', Delete: 'delete', 'Move to': 'move' };
// The prototype's apply_patch rule. Unknown output is not success.
const PATCH_SUCCESS = /Success\. Updated the following files:/;
const PATCH_FAILURE = /Failed to|Error:|patch rejected/i;

/** A set that forgets its oldest entries beyond `max`. */
class BoundedSet {
  private keys = new Set<string>();
  constructor(private max: number) {}
  has(key: string): boolean { return this.keys.has(key); }
  add(key: string): void {
    this.keys.delete(key);
    this.keys.add(key);
    if (this.keys.size > this.max) this.keys.delete(this.keys.values().next().value!);
  }
}

function parseJson(value: unknown): unknown {
  if (typeof value !== 'string') return value;
  try { return JSON.parse(value); } catch { return null; }
}

function resolveFile(cwd: string | null, file: unknown): string | null {
  if (typeof file !== 'string' || file.length === 0) return null;
  if (path.isAbsolute(file)) return path.resolve(file);
  return cwd ? path.resolve(cwd, file) : null;
}

function outputText(value: unknown): string {
  if (typeof value === 'string') return value;
  if (!Array.isArray(value)) return '';
  return value.filter((v): v is Json => isObject(v) && typeof v.text === 'string').map((v) => v.text as string).join('\n');
}

function commandText(value: unknown): string | undefined {
  if (typeof value === 'string') return value;
  if (Array.isArray(value)) return value.filter((x): x is string => typeof x === 'string').join(' ');
  return undefined;
}

/** categoryFor knows bare names; direct calls may arrive namespaced, such as `functions.apply_patch`. */
function categoryOf(name: string): Category {
  const direct = categoryFor('codex', name);
  return direct === 'other' && name.includes('.') ? categoryFor('codex', name.split('.').at(-1)!) : direct;
}

/** Only apply_patch claims files. Shell text, wrapper code and command output never do. */
function patchWrites(name: string, input: unknown, cwd: string | null): Write[] {
  if (name.split('.').at(-1) !== 'apply_patch') return [];
  const data = isObject(input) ? input : {};
  const patch = typeof input === 'string' ? input : str(data.patch) ?? str(data.input) ?? '';
  const writes: Write[] = [];
  for (const line of patch.split('\n')) {
    const m = line.match(/^\*\*\* (Add|Update|Delete|Move to)(?: File)?: (.+)$/);
    const file = m ? resolveFile(cwd, m[2]) : null;
    if (m && file) writes.push({ path: file, op: PATCH_OPS[m[1]!]! });
  }
  return writes;
}

const unique = (files: string[]): string[] => [...new Set(files)];

/** The exit code of a direct shell result: structured metadata, else Codex's own status line. */
function directExit(text: string): { output: string; exitCode?: number } {
  const parsed = text.startsWith('{') ? parseJson(text) : null;
  if (isObject(parsed) && typeof parsed.output === 'string') {
    const code = isObject(parsed.metadata) ? parsed.metadata.exit_code : undefined;
    return { output: parsed.output, ...(Number.isInteger(code) ? { exitCode: code as number } : {}) };
  }
  const m = text.match(/Process exited with code (-?\d+)/);
  return { output: text, ...(m ? { exitCode: Number(m[1]) } : {}) };
}

/**
 * Pure adapter from Codex rollout records to normalized events.
 * One instance serves every log file; identity comes from the records.
 */
export class CodexLogAdapter {
  private agents = new Map<string, AgentState>();
  /** What the downstream consumer last saw for each agent. */
  private emitted = new Map<string, { identity: Identity; status: AgentStatus }>();
  private sources = new Map<string, Source>();
  private pending = new Map<string, Pending>();
  /** Calls that already produced a tool.start or tool.end. Native and direct receipts share call IDs. */
  private started = new BoundedSet(MAX_PENDING);
  private ended = new BoundedSet(MAX_PENDING);

  constructor(private host: string) {}

  ingest(record: unknown, file: string, scope: string): SourceEvent[] {
    if (!isObject(record)) return [];
    const ts = str(record.timestamp);
    const p = isObject(record.payload) ? record.payload : {};
    const prefix = `${this.host}/codex:${scope}:`;
    let source = this.sources.get(file);
    if (!source) { source = { agentId: null }; this.sources.set(file, source); }

    const touched = new Map<string, AgentState>();
    const toolEvents: SourceEvent[] = [];
    const touch = (id: string, fields: Partial<Identity> = {}): AgentState => {
      const state = this.touch(id, ts, fields);
      touched.set(id, state);
      return state;
    };

    if (record.type === 'session_meta') {
      const threadId = str(p.id) ?? str(p.session_id);
      if (threadId) {
        const id = prefix + threadId;
        source.agentId = id;
        const spawn = isObject(p.source) && isObject(p.source.subagent) && isObject(p.source.subagent.thread_spawn) ? p.source.subagent.thread_spawn : {};
        const parent = str(p.parent_thread_id) ?? str(spawn.parent_thread_id);
        touch(id, {
          cwd: str(p.cwd) ?? this.agents.get(id)?.cwd ?? null,
          parentId: parent ? prefix + parent : null,
          link: parent ? 'confirmed' : 'root',
          role: parent ? 'sub-agent' : 'main',
          name: str(spawn.agent_path)?.split('/').at(-1) ?? (p.thread_source === 'guardian_review' ? 'Approval review' : 'Codex session'),
        });
      }
    }

    if (record.type === 'event_msg' && p.type === 'item_completed') this.nativeItem(source, p, ts, prefix, touch, toolEvents);

    const id = source.agentId;
    if (id) {
      const agent = touch(id);
      if (record.type === 'turn_context') {
        const model = str(p.model);
        if (str(p.cwd)) agent.cwd = str(p.cwd)!;
        if (model) agent.model = model;
      }
      if (p.type === 'task_started') agent.status = 'working';
      // A finished task ends a turn, not the session.
      if (p.type === 'task_complete' || p.type === 'task_completed') agent.status = 'idle';
      if (p.type === 'turn_aborted') agent.status = 'interrupted';
      if (p.type === 'function_call' || p.type === 'custom_tool_call') this.directCall(agent, id, p, ts, toolEvents);
      if (p.type === 'function_call_output' || p.type === 'custom_tool_call_output') this.directOutput(id, p, ts, toolEvents);
    }

    // Order: identity, status, then tool events. Without a timestamp, state is learned but nothing is emitted.
    const events: SourceEvent[] = [];
    if (ts) {
      for (const [agentId, agent] of touched) this.flushAgent(agentId, agent, ts, events);
      for (const [agentId, agent] of touched) this.flushStatus(agentId, agent, ts, events);
      events.push(...toolEvents);
    }
    return events;
  }

  private touch(id: string, ts: string | undefined, fields: Partial<Identity>): AgentState {
    const current: AgentState = this.agents.get(id) ?? {
      name: id.split(':').at(-1)!.slice(0, 12), role: 'main', parentId: null, link: 'root', cwd: null,
      status: 'idle', lastEventAt: ts ?? '',
    };
    Object.assign(current, Object.fromEntries(Object.entries(fields).filter(([, v]) => v !== undefined)));
    if (ts) current.lastEventAt = ts;
    this.agents.set(id, current);
    return current;
  }

  /** Emit agent.upsert when an identity field differs from what was last emitted. */
  private flushAgent(id: string, a: AgentState, ts: string, out: SourceEvent[]): void {
    const prev = this.emitted.get(id);
    const identity: Identity = { name: a.name, role: a.role, parentId: a.parentId, link: a.link, cwd: a.cwd, ...(a.model ? { model: a.model } : {}) };
    if (prev && (['name', 'role', 'parentId', 'link', 'cwd', 'model'] as const).every((k) => prev.identity[k] === identity[k])) return;
    // The status in an upsert is the one the consumer will hold afterwards.
    const status = prev?.status ?? 'idle';
    this.emitted.set(id, { identity, status });
    out.push({
      kind: 'agent.upsert', ts,
      agent: { id, provider: 'codex', ...identity, projectRoot: null, status, lastEventAt: a.lastEventAt },
    });
  }

  private flushStatus(id: string, a: AgentState, ts: string, out: SourceEvent[]): void {
    const prev = this.emitted.get(id);
    if (!prev || prev.status === a.status) return;
    prev.status = a.status;
    out.push({ kind: 'agent.status', ts, agentId: id, status: a.status });
  }

  private directCall(agent: AgentState, agentId: string, p: Json, ts: string | undefined, out: SourceEvent[]): void {
    const callId = str(p.call_id);
    const name = str(p.name);
    if (!callId || !name || !ts) return;
    const key = `${agentId}:${callId}`;
    if (this.started.has(key)) return;
    this.started.add(key);
    const input = parseJson(p.arguments ?? p.input) ?? p.arguments ?? p.input;
    const category = categoryOf(name);
    const writes = patchWrites(name, input, agent.cwd);
    this.pending.set(key, { agentId, name, category, writes });
    if (this.pending.size > MAX_PENDING) this.pending.delete(this.pending.keys().next().value!);

    agent.status = 'working';
    const call: Call = { agentId, callId, startedAt: ts, category, rawName: name };
    if (writes.length) call.files = unique(writes.map((w) => w.path));
    if (category === 'command' && isObject(input)) {
      const command = commandText(input.command) ?? commandText(input.cmd);
      if (command !== undefined) call.command = command.slice(0, MAX_COMMAND_CHARS);
    }
    out.push({ kind: 'tool.start', ts, call });
  }

  private directOutput(agentId: string, p: Json, ts: string | undefined, out: SourceEvent[]): void {
    const callId = str(p.call_id);
    if (!callId || !ts) return;
    const key = `${agentId}:${callId}`;
    const call = this.pending.get(key);
    if (!call) return;
    this.pending.delete(key);
    this.ended.add(key);
    const text = outputText(p.output);

    if (call.category === 'command') {
      const { output, exitCode } = directExit(text);
      out.push(...outputEvents(ts, agentId, callId, output));
      out.push({ kind: 'tool.end', ts, agentId, callId, ok: exitCode === undefined || exitCode === 0, ...(exitCode !== undefined ? { exitCode } : {}) });
      return;
    }
    // Codex apply_patch uses an explicit success receipt. A result without it is not success.
    const isPatch = call.name.split('.').at(-1) === 'apply_patch';
    const ok = isPatch ? PATCH_SUCCESS.test(text) && !PATCH_FAILURE.test(text) : true;
    out.push({ kind: 'tool.end', ts, agentId, callId, ok, ...(ok && call.writes.length ? { writes: call.writes } : {}) });
  }

  /** A native item_completed describes a finished nested tool: it becomes a start and end pair. */
  private nativeItem(
    source: Source, p: Json, recordTs: string | undefined, prefix: string,
    touch: (id: string, fields?: Partial<Identity>) => AgentState, out: SourceEvent[],
  ): void {
    const item = p.item;
    if (!isObject(item)) return;
    const type = str(item.type);
    if (!type || !['CommandExecution', 'FileChange', 'McpToolCall', 'CollabAgentToolCall'].includes(type)) return;
    const callId = str(item.id) ?? str(item.call_id);
    const thread = str(p.thread_id);
    const agentId = thread ? prefix + thread : source.agentId;
    if (!callId || !agentId) return;

    const known = this.agents.get(agentId);
    const itemCwd = str(item.cwd);
    // An unknown thread gets a minimal identity. Its cwd is never guessed from another thread.
    const agent = touch(agentId, known ? {} : { cwd: itemCwd });
    const cwd = itemCwd ?? agent.cwd;

    const ms = p.completed_at_ms;
    const ts = typeof ms === 'number' && Number.isFinite(ms) && Math.abs(ms) < 8.64e15 ? new Date(ms).toISOString() : recordTs;
    if (!ts) return;

    const key = `${agentId}:${callId}`;
    const alreadyStarted = this.started.has(key);
    if (this.ended.has(key)) return;
    this.started.add(key);
    this.ended.add(key);
    this.pending.delete(key);

    const category = categoryFor('codex', type);
    const ok = item.status === 'completed';
    const call: Call = { agentId, callId, startedAt: ts, category, rawName: type };
    let end: Extract<SourceEvent, { kind: 'tool.end' }> = { kind: 'tool.end', ts, agentId, callId, ok };

    if (type === 'CommandExecution') {
      const command = commandText(item.command);
      if (command !== undefined) call.command = command.slice(0, MAX_COMMAND_CHARS);
      const exitCode = Number.isInteger(item.exit_code) ? (item.exit_code as number) : undefined;
      end = { ...end, ok: ok && (exitCode === undefined || exitCode === 0), ...(exitCode !== undefined ? { exitCode } : {}) };
    } else if (type === 'FileChange' && isObject(item.changes)) {
      const files: string[] = [];
      const writes: Write[] = [];
      for (const [file, change] of Object.entries(item.changes)) {
        if (!isObject(change) || typeof change.type !== 'string' || !Object.hasOwn(FILE_CHANGE_OPS, change.type)) continue;
        const resolved = resolveFile(cwd, file);
        if (!resolved) continue;
        files.push(resolved);
        writes.push({ path: resolved, op: FILE_CHANGE_OPS[change.type]! });
        const destination = resolveFile(cwd, change.move_path);
        if (destination) { files.push(destination); writes.push({ path: destination, op: 'move' }); }
      }
      if (files.length) call.files = unique(files);
      if (ok && writes.length) end = { ...end, writes };
    }

    if (!alreadyStarted) out.push({ kind: 'tool.start', ts, call });
    if (type === 'CommandExecution' && typeof item.aggregated_output === 'string') {
      out.push(...outputEvents(ts, agentId, callId, item.aggregated_output));
    }
    out.push(end);
  }
}
