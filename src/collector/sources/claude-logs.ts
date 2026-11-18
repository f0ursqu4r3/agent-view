import path from 'node:path';
import { categoryFor } from '../../shared/schema';
import type { Agent, AgentStatus, Call, Category, SourceEvent, WriteOp } from '../../shared/schema';

const MAX_PENDING = 5000;
const MAX_COMMAND_CHARS = 4096;
const MAX_CHUNK_BYTES = 4096;
const MAX_OUTPUT_BYTES = 16 * 1024;

type Json = Record<string, unknown>;
type Identity = Pick<Agent, 'name' | 'role' | 'parentId' | 'link' | 'cwd'> & { model?: string };
type AgentState = Identity & { status: AgentStatus; lastEventAt: string };
type Pending = { agentId: string; name: string; category: Category; input: Json; files?: string[]; writes: WriteOp[] };

const isObject = (v: unknown): v is Json => typeof v === 'object' && v !== null && !Array.isArray(v);
const str = (v: unknown): string | undefined => (typeof v === 'string' && v.length > 0 ? v : undefined);

function resolveFile(cwd: string | null, file: unknown): string | null {
  if (typeof file !== 'string' || file.length === 0) return null;
  if (path.isAbsolute(file)) return path.resolve(file);
  return cwd ? path.resolve(cwd, file) : null;
}

function outputText(content: unknown): string {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  return content
    .filter((b): b is Json => isObject(b) && b.type === 'text' && typeof b.text === 'string')
    .map((b) => b.text as string)
    .join('\n');
}

/** Split UTF-8 bytes into chunks of at most `size` bytes without cutting a character. */
function chunkBytes(buf: Buffer, size: number): string[] {
  const chunks: string[] = [];
  let start = 0;
  while (start < buf.length) {
    let end = Math.min(start + size, buf.length);
    if (end < buf.length) while (end > start && (buf[end]! & 0xc0) === 0x80) end--;
    if (end === start) end = Math.min(start + size, buf.length);
    chunks.push(buf.subarray(start, end).toString('utf8'));
    start = end;
  }
  return chunks;
}

/** The last `max` bytes of the output, starting on a character boundary. */
function tailBytes(buf: Buffer, max: number): { buf: Buffer; cut: boolean } {
  if (buf.length <= max) return { buf, cut: false };
  let start = buf.length - max;
  while (start < buf.length && (buf[start]! & 0xc0) === 0x80) start++;
  return { buf: buf.subarray(start), cut: true };
}

/** Only explicit file tools claim files. Shell text and search results never do. */
function callFiles(name: string, category: Category, input: Json, cwd: string | null): { files?: string[]; writes: WriteOp[] } {
  if (category === 'read') {
    const f = resolveFile(cwd, input.file_path);
    return { files: f ? [f] : undefined, writes: [] };
  }
  if (name === 'Write' || name === 'Edit' || name === 'MultiEdit' || name === 'NotebookEdit') {
    const f = resolveFile(cwd, input.file_path ?? input.notebook_path);
    return f ? { files: [f], writes: ['edit'] } : { writes: [] };
  }
  if (category === 'search') {
    const f = resolveFile(cwd, input.path);
    return { files: f ? [f] : undefined, writes: [] };
  }
  return { writes: [] };
}

/**
 * Pure adapter from Claude Code transcript records to normalized events.
 * One instance serves every transcript file; identity comes from the records.
 */
export class ClaudeLogAdapter {
  private agents = new Map<string, AgentState>();
  /** What the downstream consumer last saw for each agent. */
  private emitted = new Map<string, { identity: Identity; status: AgentStatus }>();
  private pending = new Map<string, Pending>();

  constructor(private host: string) {}

  ingest(record: unknown, file: string, scope: string): SourceEvent[] {
    if (!isObject(record)) return [];
    const sessionId = str(record.sessionId);
    if (!sessionId) return [];
    const ts = str(record.timestamp);
    const events: SourceEvent[] = [];

    const root = `${this.host}/claude:${scope}:${sessionId}`;
    const child = str(record.agentId) ?? file.match(/[/\\]subagents[/\\]agent-(.+)\.jsonl$/)?.[1];
    const id = child ? `${root}:${child}` : root;
    const existing = this.agents.get(id);
    const cwd = str(record.cwd) ?? existing?.cwd ?? null;
    const message = isObject(record.message) ? record.message : undefined;
    const model = message && record.type === 'assistant' ? str(message.model) : undefined;

    const agent = this.touch(id, ts, {
      cwd,
      name: existing?.name ?? (child ? `Sub-agent ${child.slice(0, 8)}` : 'Claude Code session'),
      parentId: existing?.link === 'confirmed' ? existing.parentId : child ? root : null,
      link: existing?.link === 'confirmed' ? 'confirmed' : child ? 'session' : 'root',
      role: child ? 'sub-agent' : 'main',
      ...(model && model !== '<synthetic>' ? { model } : {}),
    });
    if (record.type === 'custom-title' && str(record.customTitle)) agent.name = str(record.customTitle)!;
    if (record.type === 'agent-name' && str(record.agentName)) agent.name = str(record.agentName)!;

    const content = message?.content;
    const blocks = Array.isArray(content) ? content.filter(isObject) : [];
    const toolEvents: SourceEvent[] = [];
    for (const b of blocks) {
      if (b.type === 'tool_use') this.toolUse(agent, id, cwd, b, ts, toolEvents);
      else if (b.type === 'tool_result') this.toolResult(agent, id, root, scope, b, record, ts, toolEvents);
    }

    const endsTurn = (record.type === 'system' && record.subtype === 'turn_duration') || message?.stop_reason === 'end_turn';

    // Order: identity, working status, tool events, then idle status.
    if (ts) {
      this.flushAgent(id, agent, ts, events);
      this.flushStatus(id, agent, ts, events);
      events.push(...toolEvents);
    }
    if (endsTurn) agent.status = 'idle';
    if (ts) this.flushStatus(id, agent, ts, events);
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
      agent: { id, provider: 'claude', ...identity, projectRoot: null, status, lastEventAt: a.lastEventAt },
    });
  }

  private flushStatus(id: string, a: AgentState, ts: string, out: SourceEvent[]): void {
    const prev = this.emitted.get(id);
    if (!prev || prev.status === a.status) return;
    prev.status = a.status;
    out.push({ kind: 'agent.status', ts, agentId: id, status: a.status });
  }

  private toolUse(agent: AgentState, agentId: string, cwd: string | null, b: Json, ts: string | undefined, out: SourceEvent[]): void {
    const callId = str(b.id);
    const name = str(b.name);
    if (!callId || !name || !ts) return;
    const key = `${agentId}:${callId}`;
    if (this.pending.has(key)) return;
    const input = isObject(b.input) ? b.input : {};
    const category = categoryFor('claude', name);
    const { files, writes } = callFiles(name, category, input, cwd);
    this.pending.set(key, { agentId, name, category, input, files, writes });
    if (this.pending.size > MAX_PENDING) this.pending.delete(this.pending.keys().next().value!);

    agent.status = 'working';
    const call: Call = { agentId, callId, startedAt: ts, category, rawName: name };
    if (files) call.files = files;
    if (category === 'command' && typeof input.command === 'string') call.command = input.command.slice(0, MAX_COMMAND_CHARS);
    if (category === 'search' && typeof input.pattern === 'string') call.pattern = input.pattern;
    out.push({ kind: 'tool.start', ts, call });
  }

  private toolResult(agent: AgentState, agentId: string, root: string, scope: string, b: Json, record: Json, ts: string | undefined, out: SourceEvent[]): void {
    const callId = str(b.tool_use_id);
    if (!callId || !ts) return;
    const key = `${agentId}:${callId}`;
    const call = this.pending.get(key);
    if (!call) return;
    this.pending.delete(key);
    const ok = b.is_error !== true;

    // An Agent/Task result that names the sub-agent confirms the parent link.
    const spawned = isObject(record.toolUseResult) ? str(record.toolUseResult.agentId) : undefined;
    if (call.category === 'agent' && spawned && ok) {
      const childId = `${root}:${spawned}`;
      const known = this.agents.get(childId);
      const childAgent = this.touch(childId, ts, {
        parentId: agentId, link: 'confirmed', role: 'sub-agent',
        name: str(call.input.description) ?? `Sub-agent ${spawned.slice(0, 8)}`,
        cwd: known?.cwd ?? agent.cwd,
      });
      out.push(...this.upsertNow(childId, childAgent, ts));
    }

    if (call.category === 'command') {
      const { buf, cut } = tailBytes(Buffer.from(outputText(b.content), 'utf8'), MAX_OUTPUT_BYTES);
      chunkBytes(buf, MAX_CHUNK_BYTES).forEach((chunk, i) =>
        out.push({ kind: 'tool.output', ts, agentId, callId, chunk, truncated: cut && i === 0 }));
    }

    const writes = ok && call.files && call.writes.length ? call.files.map((p) => ({ path: p, op: call.writes[0]! })) : undefined;
    out.push({ kind: 'tool.end', ts, agentId, callId, ok, ...(writes ? { writes } : {}) });
  }

  private upsertNow(id: string, a: AgentState, ts: string): SourceEvent[] {
    const out: SourceEvent[] = [];
    this.flushAgent(id, a, ts, out);
    return out;
  }
}
