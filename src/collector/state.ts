import os from 'node:os';
import { LIFECYCLE, presentAgentIds, type LifecycleOptions } from '../shared/lifecycle';
import type { Agent, Call, Event, FileTouch, SourceEvent } from '../shared/schema';

type Snapshot = Extract<Event, { kind: 'snapshot' }>;
type OpenCall = { call: Call; lastEventMs: number };

const ENDED_LIMIT = 20_000;

function msOf(iso: string): number {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? 0 : t;
}

/** The later of two ISO times. An empty time loses to any other time. */
function laterIso(a: string, b: string): string {
  return msOf(b) > msOf(a) ? b : a;
}

const callKey = (agentId: string, callId: string) => `${agentId}|${callId}`;

/**
 * The collector's model of live agent activity. Every source event goes through `apply`.
 * The hub keeps one instance per host as a mirror and fills it with `load`.
 */
export class CollectorState {
  private readonly opts: LifecycleOptions;
  private agents = new Map<string, Agent>();
  private open = new Map<string, OpenCall>();
  private files = new Map<string, FileTouch>();
  /** Keys of ended calls, oldest first. Used only to drop duplicate events. */
  private ended = new Set<string>();

  constructor(opts?: Partial<LifecycleOptions>) {
    this.opts = { ...LIFECYCLE, ...opts };
  }

  /** Returns the event to forward, or `null` when the event is a duplicate. */
  apply(e: SourceEvent): SourceEvent | null {
    switch (e.kind) {
      case 'agent.upsert': return this.applyUpsert(e);
      case 'agent.status': {
        const a = this.agents.get(e.agentId);
        if (a) a.status = e.status;
        this.bump(e.agentId, e.ts);
        return e;
      }
      case 'tool.start': return this.applyStart(e);
      case 'tool.update': return this.applyUpdate(e);
      case 'tool.output': {
        const open = this.open.get(callKey(e.agentId, e.callId));
        if (open) open.lastEventMs = Math.max(open.lastEventMs, msOf(e.ts));
        this.bump(e.agentId, e.ts);
        return e;
      }
      case 'tool.end': return this.applyEnd(e);
      case 'fs.change':
        this.touch(e.path, e.ts, 'unattributed');
        return e;
      default:
        return e;
    }
  }

  /** Ends calls that have had no event for `callTimeoutMs`, and drops old file touches. */
  tick(now: number): SourceEvent[] {
    const out: SourceEvent[] = [];
    for (const [key, o] of [...this.open]) {
      if (now - o.lastEventMs < this.opts.callTimeoutMs) continue;
      this.open.delete(key);
      this.markEnded(key);
      out.push({ kind: 'tool.end', agentId: o.call.agentId, callId: o.call.callId, ok: null, ts: new Date(now).toISOString() });
    }
    for (const [path, f] of [...this.files]) {
      if (now - msOf(f.lastTouchAt) >= this.opts.fileTtlMs) this.files.delete(path);
    }
    return out;
  }

  snapshot(now: number): Snapshot {
    const present = this.presentIds(now);
    const agents = [...this.agents.values()].filter((a) => present.has(a.id))
      .sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
      .map((a) => ({ ...a }));
    const openCalls = [...this.open.entries()].filter(([, o]) => present.has(o.call.agentId))
      .sort(([ka, a], [kb, b]) => msOf(a.call.startedAt) - msOf(b.call.startedAt) || (ka < kb ? -1 : ka > kb ? 1 : 0))
      .map(([, o]) => ({ ...o.call, ...(o.call.files ? { files: [...o.call.files] } : {}) }));
    const files = [...this.files.values()].filter((f) => now - msOf(f.lastTouchAt) < this.opts.fileTtlMs)
      .sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0))
      .map((f) => ({ ...f }));
    return { kind: 'snapshot', agents, openCalls, files };
  }

  /** Replaces all state from a snapshot. Ended-call memory is cleared. */
  load(s: Snapshot): void {
    this.agents = new Map(s.agents.map((a) => [a.id, { ...a }]));
    this.open = new Map(s.openCalls.map((c) => [
      callKey(c.agentId, c.callId),
      { call: { ...c, ...(c.files ? { files: [...c.files] } : {}) }, lastEventMs: msOf(c.startedAt) },
    ]));
    this.files = new Map(s.files.map((f) => [f.path, { ...f }]));
    this.ended = new Set();
  }

  /** Project roots of present agents, newest event first. Never the home directory or `/`. */
  watchRoots(now: number, max = 24): string[] {
    const present = this.presentIds(now);
    const home = os.homedir();
    const roots: string[] = [];
    const byNewest = [...this.agents.values()].filter((a) => present.has(a.id))
      .sort((a, b) => msOf(b.lastEventAt) - msOf(a.lastEventAt));
    for (const a of byNewest) {
      const r = a.projectRoot;
      if (r === null || r === '/' || r === home || roots.includes(r)) continue;
      roots.push(r);
      if (roots.length >= max) break;
    }
    return roots;
  }

  agent(id: string): Agent | undefined {
    return this.agents.get(id);
  }

  private presentIds(now: number): Set<string> {
    return presentAgentIds([...this.agents.values()], now, this.opts);
  }

  private bump(agentId: string, ts: string): void {
    const a = this.agents.get(agentId);
    if (a) a.lastEventAt = laterIso(a.lastEventAt, ts);
  }

  private markEnded(key: string): void {
    this.ended.delete(key);
    this.ended.add(key);
    if (this.ended.size > ENDED_LIMIT) this.ended.delete(this.ended.values().next().value as string);
  }

  /** The newest touch wins. On a tie, a confirmed touch replaces an unattributed one. */
  private touch(path: string, ts: string, attribution: FileTouch['attribution'], agentId?: string): void {
    const cur = this.files.get(path);
    if (cur) {
      const diff = msOf(ts) - msOf(cur.lastTouchAt);
      if (diff < 0 || (diff === 0 && !(attribution === 'confirmed' && cur.attribution === 'unattributed'))) return;
    }
    this.files.set(path, attribution === 'confirmed'
      ? { path, lastTouchAt: ts, attribution, agentId: agentId! }
      : { path, lastTouchAt: ts, attribution });
  }

  private applyUpsert(e: Extract<SourceEvent, { kind: 'agent.upsert' }>): SourceEvent {
    const incoming = e.agent;
    const cur = this.agents.get(incoming.id);
    const seen = laterIso(incoming.lastEventAt, e.ts);
    if (!cur) {
      this.agents.set(incoming.id, { ...incoming, lastEventAt: seen });
      return e;
    }
    const { status: _status, lastEventAt: _last, ...descriptive } = incoming;
    Object.assign(cur, descriptive);
    cur.lastEventAt = laterIso(cur.lastEventAt, seen);
    return e;
  }

  private applyStart(e: Extract<SourceEvent, { kind: 'tool.start' }>): SourceEvent | null {
    const c = e.call;
    const key = callKey(c.agentId, c.callId);
    if (this.open.has(key) || this.ended.has(key)) {
      if (this.ended.has(key)) this.markEnded(key);
      return null;
    }
    this.open.set(key, { call: { ...c, ...(c.files ? { files: [...c.files] } : {}) }, lastEventMs: msOf(e.ts) });
    const a = this.agents.get(c.agentId);
    if (a) a.status = 'working';
    this.bump(c.agentId, e.ts);
    if (c.category !== 'search') for (const p of c.files ?? []) this.touch(p, e.ts, 'confirmed', c.agentId);
    return e;
  }

  private applyUpdate(e: Extract<SourceEvent, { kind: 'tool.update' }>): SourceEvent {
    const key = callKey(e.agentId, e.callId);
    const open = this.open.get(key);
    this.bump(e.agentId, e.ts);
    if (open) {
      open.lastEventMs = Math.max(open.lastEventMs, msOf(e.ts));
      open.call.files = [...new Set([...(open.call.files ?? []), ...e.files])];
      if (open.call.category !== 'search') for (const p of e.files) this.touch(p, e.ts, 'confirmed', e.agentId);
    } else if (!this.ended.has(key)) {
      for (const p of e.files) this.touch(p, e.ts, 'confirmed', e.agentId);
    }
    return e;
  }

  private applyEnd(e: Extract<SourceEvent, { kind: 'tool.end' }>): SourceEvent | null {
    const key = callKey(e.agentId, e.callId);
    if (this.ended.has(key)) {
      this.markEnded(key);
      return null;
    }
    this.open.delete(key);
    this.markEnded(key);
    this.bump(e.agentId, e.ts);
    for (const w of e.writes ?? []) this.touch(w.path, e.ts, 'confirmed', e.agentId);
    return e;
  }
}
