import { LIFECYCLE, isStale, presentAgentIds } from '../src/shared/lifecycle';
import type { LifecycleOptions } from '../src/shared/lifecycle';
import type { Agent, Call, Coverage, Envelope, HostStatus } from '../src/shared/schema';

// Browser-safe: no DOM and no Node imports. The store is also tested with `bun test`.

export type ViewAgent = Agent & { host: string; stale: boolean; lastEventMs: number };
export type ViewCall = Call & { host: string; startMs: number; endMs: number | null; ok: boolean | null; output: string; outputTruncated: boolean };
export type Touch = { host: string; path: string; projectRoot: string | null; attribution: 'confirmed' | 'unattributed'; agentId?: string; atMs: number };

/** A grey flicker is held this long, and a confirmed touch this close to it suppresses it. */
const FLICKER_MS = 2000;
const OUTPUT_KEEP = 16 * 1024;
const MAX_CALLS_PER_HOST = 2000;
const MAX_RECENT_PER_PATH = 16;
const MAX_RECENT_PATHS = 4000;
const MAX_HELD = 10_000;
const MAX_CONFIRMED = 10_000;
const SWEEP_EVERY_MS = 1000;
/** An agent is dropped after this many lingers without an event, unless a present agent descends from it. */
const DROP_AFTER_LINGERS = 2;

type AgentEntry = { agent: Agent; lastEventMs: number };
type HostState = {
  offset: number;
  demo: boolean;
  coverage: Coverage | undefined;
  agents: Map<string, AgentEntry>;
  /** First-appearance rank of every agent seen on this host. Survives snapshots. */
  order: Map<string, number>;
  calls: Map<string, ViewCall>;
  openByAgent: Map<string, Map<string, ViewCall>>;
  confirmedQueue: Touch[];
  held: Touch[];
  /** Recent confirmed touch times per path, for suppressing grey flickers. */
  recent: Map<string, number[]>;
  /** Present agents for one `(version, now)`. */
  presence?: { version: number; now: number; agents: AgentEntry[] };
};

const callKey = (agentId: string, callId: string): string => `${agentId}\u0000${callId}`;
const isStr = (v: unknown): v is string => typeof v === 'string';
const isObj = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null;

export class ViewerStore {
  focus: string | null = null;
  /** Diagnostic: how many times presence was computed. Repeated reads with one `now` must not add to it. */
  presenceComputes = 0;
  private readonly nowFn: () => number;
  private readonly lifecycle: Partial<LifecycleOptions>;
  private readonly hostStates = new Map<string, HostState>();
  private hostList: HostStatus[] = [];
  private orderCounter = 0;
  /** Bumped by every `apply`. A cached presence result is valid for one version. */
  private version = 0;
  private lastSweepMs = Number.NEGATIVE_INFINITY;

  constructor(opts?: { now?: () => number; lifecycle?: Partial<LifecycleOptions> }) {
    this.nowFn = opts?.now ?? Date.now;
    this.lifecycle = { ...LIFECYCLE, ...opts?.lifecycle };
  }

  apply(e: Envelope): void {
    if (!isObj(e) || e.v !== 1 || !isStr(e.host) || !isStr(e.kind)) return;
    this.version++;
    try {
      this.dispatch(e);
    } catch {
      // A malformed event must never break the viewer.
    }
  }

  setHosts(h: HostStatus[]): void {
    this.hostList = h;
  }

  hosts(): HostStatus[] {
    return this.hostList;
  }

  setFocus(id: string | null): void {
    this.focus = id;
  }

  coverage(host: string): Coverage | undefined {
    return this.hostStates.get(host)?.coverage;
  }

  isDemo(): boolean {
    for (const h of this.hostStates.values()) if (h.demo) return true;
    return false;
  }

  /** Agents held in memory, present or not. */
  storedAgentCount(): number {
    let n = 0;
    for (const h of this.hostStates.values()) n += h.agents.size;
    return n;
  }

  agents(now: number): ViewAgent[] {
    this.sweep(now);
    const out: ViewAgent[] = [];
    for (const [host, h] of this.hostStates) {
      for (const a of this.presentAgents(h, now)) {
        const adjusted = a.agent;
        out.push({ ...adjusted, host, stale: isStale(adjusted, now, this.lifecycle), lastEventMs: a.lastEventMs });
      }
    }
    return out;
  }

  /** Present agent ids: hosts by first appearance; main agents, each followed by its sub-agents depth first. */
  laneOrder(now: number): string[] {
    this.sweep(now);
    const out: string[] = [];
    for (const h of this.hostStates.values()) {
      const present = [...this.presentAgents(h, now)].sort((a, b) => rank(h, a.agent.id) - rank(h, b.agent.id));
      const ids = new Set(present.map((a) => a.agent.id));
      const children = new Map<string, Agent[]>();
      const mains: Agent[] = [];
      for (const { agent } of present) {
        if (agent.parentId !== null && ids.has(agent.parentId) && agent.parentId !== agent.id) {
          const list = children.get(agent.parentId) ?? [];
          list.push(agent);
          children.set(agent.parentId, list);
        } else {
          mains.push(agent);
        }
      }
      const seen = new Set<string>();
      const walk = (a: Agent): void => {
        if (seen.has(a.id)) return;
        seen.add(a.id);
        out.push(a.id);
        for (const k of children.get(a.id) ?? []) walk(k);
      };
      for (const m of mains) walk(m);
      // Parent cycles never reach a main agent. Keep them visible.
      for (const { agent } of present) walk(agent);
    }
    return out;
  }

  /** Calls of present agents that are open or ended inside the window, oldest start first. */
  calls(now: number, windowMs = 16_000): ViewCall[] {
    this.sweep(now);
    const out: ViewCall[] = [];
    for (const h of this.hostStates.values()) {
      const present = new Set(this.presentAgents(h, now).map((a) => a.agent.id));
      for (const c of h.calls.values()) {
        if (!present.has(c.agentId)) continue;
        if (c.endMs === null || c.endMs >= now - windowMs) out.push(c);
      }
    }
    return out.sort((a, b) => a.startMs - b.startMs);
  }

  openCall(agentId: string): ViewCall | undefined {
    let found: ViewCall | undefined;
    for (const h of this.hostStates.values()) {
      const open = h.openByAgent.get(agentId);
      if (!open) continue;
      for (const c of open.values()) if (!found || c.startMs >= found.startMs) found = c;
    }
    return found;
  }

  /** Newest command calls first, for one agent when `focus` is set. */
  commands(focus: string | null, limit = 3): ViewCall[] {
    const out: ViewCall[] = [];
    for (const h of this.hostStates.values()) {
      for (const c of h.calls.values()) {
        if (c.category === 'command' && (focus === null || c.agentId === focus)) out.push(c);
      }
    }
    return out.sort((a, b) => b.startMs - a.startMs).slice(0, limit);
  }

  /** Confirmed touches at once. Unattributed ones after the hold, unless a confirmed touch of the same path is close. */
  takeTouches(now: number): Touch[] {
    const out: Touch[] = [];
    for (const h of this.hostStates.values()) {
      if (h.confirmedQueue.length > 0) {
        out.push(...h.confirmedQueue);
        h.confirmedQueue = [];
      }
      if (h.held.length > 0) {
        const keep: Touch[] = [];
        for (const t of h.held) {
          if (now - t.atMs < FLICKER_MS) {
            keep.push(t);
          } else if (!(h.recent.get(t.path) ?? []).some((c) => Math.abs(c - t.atMs) <= FLICKER_MS)) {
            out.push(t);
          }
        }
        h.held = keep;
      }
      // A held touch needs confirmed times back to `atMs - 2 s`, and `atMs > now - 2 s` while held.
      for (const [path, times] of h.recent) {
        const live = times.filter((t) => t >= now - 2 * FLICKER_MS);
        if (live.length === 0) h.recent.delete(path);
        else if (live.length !== times.length) h.recent.set(path, live);
      }
    }
    return out;
  }

  // ---- internals -------------------------------------------------------

  private host(label: string): HostState {
    let h = this.hostStates.get(label);
    if (!h) {
      h = {
        offset: 0, demo: false, coverage: undefined, agents: new Map(), order: new Map(), calls: new Map(),
        openByAgent: new Map(), confirmedQueue: [], held: [], recent: new Map(),
      };
      this.hostStates.set(label, h);
    }
    return h;
  }

  /** Present agents of one host, with skew-corrected `lastEventAt`. Cached per `(version, now)`. Do not mutate the result. */
  private presentAgents(h: HostState, now: number): AgentEntry[] {
    if (h.presence && h.presence.version === this.version && h.presence.now === now) return h.presence.agents;
    this.presenceComputes++;
    const adjusted = [...h.agents.values()].map((a) => ({
      agent: { ...a.agent, lastEventAt: new Date(a.lastEventMs).toISOString() },
      lastEventMs: a.lastEventMs,
    }));
    const present = presentAgentIds(adjusted.map((a) => a.agent), now, this.lifecycle);
    const agents = adjusted.filter((a) => present.has(a.agent.id));
    h.presence = { version: this.version, now, agents };
    return agents;
  }

  /** Drops agents far past linger, and their calls. At most once per second of `now`. */
  private sweep(now: number): void {
    if (Math.abs(now - this.lastSweepMs) < SWEEP_EVERY_MS) return;
    this.lastSweepMs = now;
    const horizon = DROP_AFTER_LINGERS * (this.lifecycle.lingerMs ?? LIFECYCLE.lingerMs);
    let any = false;
    for (const h of this.hostStates.values()) {
      let dropped = false;
      let present: Set<string> | undefined;
      for (const [id, a] of [...h.agents]) {
        if (now - a.lastEventMs < horizon) continue;
        present ??= new Set(this.presentAgents(h, now).map((p) => p.agent.id));
        if (present.has(id)) continue; // an ancestor of a present agent
        h.agents.delete(id);
        h.order.delete(id);
        h.openByAgent.delete(id);
        dropped = true;
      }
      if (!dropped) continue;
      any = true;
      for (const [key, c] of [...h.calls]) if (!h.agents.has(c.agentId)) h.calls.delete(key);
    }
    if (any) this.version++;
  }

  private ms(h: HostState, iso: unknown): number {
    const t = isStr(iso) ? Date.parse(iso) : NaN;
    return Number.isNaN(t) ? this.nowFn() : t + h.offset;
  }

  private agentMs(h: HostState, iso: unknown): number {
    const t = isStr(iso) ? Date.parse(iso) : NaN;
    return Number.isNaN(t) ? 0 : t + h.offset;
  }

  private note(h: HostState, id: string): void {
    if (!h.order.has(id)) h.order.set(id, this.orderCounter++);
  }

  private bump(h: HostState, agentId: string, atMs: number): void {
    const a = h.agents.get(agentId);
    if (a) a.lastEventMs = Math.max(a.lastEventMs, atMs);
  }

  private dispatch(e: Envelope): void {
    const h = this.host(e.host);
    const at = this.ms(h, e.ts);
    switch (e.kind) {
      case 'hello': {
        const t = Date.parse(e.now);
        h.offset = Number.isNaN(t) ? 0 : this.nowFn() - t;
        h.demo = e.demo === true;
        break;
      }
      case 'snapshot':
        this.snapshot(h, e.host, e);
        break;
      case 'agent.upsert': {
        const incoming = e.agent;
        if (!isObj(incoming) || !isStr(incoming.id)) return;
        const seen = Math.max(this.agentMs(h, incoming.lastEventAt), at);
        const cur = h.agents.get(incoming.id);
        this.note(h, incoming.id);
        if (!cur) {
          h.agents.set(incoming.id, { agent: { ...incoming }, lastEventMs: seen });
        } else {
          // Like the collector: an upsert never overrides the tracked status.
          const { status: _s, lastEventAt: _l, ...descriptive } = incoming;
          cur.agent = { ...cur.agent, ...descriptive };
          cur.lastEventMs = Math.max(cur.lastEventMs, seen);
        }
        break;
      }
      case 'agent.status': {
        const a = h.agents.get(e.agentId);
        if (a) a.agent = { ...a.agent, status: e.status };
        this.bump(h, e.agentId, at);
        break;
      }
      case 'tool.start':
        this.toolStart(h, e.host, e.call, at);
        break;
      case 'tool.update': {
        const call = h.calls.get(callKey(e.agentId, e.callId));
        this.bump(h, e.agentId, at);
        if (!Array.isArray(e.files)) return;
        if (call) {
          if (call.endMs !== null) return;
          call.files = [...new Set([...(call.files ?? []), ...e.files])];
          if (call.category !== 'search') this.confirm(h, e.host, e.files, e.agentId, at);
        } else {
          this.confirm(h, e.host, e.files, e.agentId, at);
        }
        break;
      }
      case 'tool.output': {
        const call = h.calls.get(callKey(e.agentId, e.callId));
        this.bump(h, e.agentId, at);
        if (!call || !isStr(e.chunk)) return;
        let output = call.output + e.chunk;
        let truncated = call.outputTruncated || e.truncated === true;
        if (output.length > OUTPUT_KEEP) {
          output = output.slice(output.length - OUTPUT_KEEP);
          truncated = true;
        }
        call.output = output;
        call.outputTruncated = truncated;
        break;
      }
      case 'tool.end': {
        const key = callKey(e.agentId, e.callId);
        const call = h.calls.get(key);
        this.bump(h, e.agentId, at);
        if (call) {
          if (call.endMs !== null) return;
          call.endMs = at;
          call.ok = e.ok;
          this.closeOpen(h, call);
        }
        for (const w of Array.isArray(e.writes) ? e.writes : []) {
          if (isObj(w) && isStr(w.path)) this.confirm(h, e.host, [w.path], e.agentId, at);
        }
        break;
      }
      case 'fs.change':
        if (!isStr(e.path)) return;
        h.held.push({ host: e.host, path: e.path, projectRoot: null, attribution: 'unattributed', atMs: at });
        if (h.held.length > MAX_HELD) h.held.splice(0, h.held.length - MAX_HELD);
        break;
      case 'coverage':
        if (isObj(e.coverage)) h.coverage = e.coverage;
        break;
      default:
        break;
    }
  }

  private snapshot(h: HostState, host: string, e: Extract<Envelope, { kind: 'snapshot' }>): void {
    if (!Array.isArray(e.agents) || !Array.isArray(e.openCalls) || !Array.isArray(e.files)) return;
    h.agents = new Map();
    h.calls = new Map();
    h.openByAgent = new Map();
    h.confirmedQueue = [];
    h.held = [];
    h.recent = new Map();
    for (const a of e.agents) {
      if (!isObj(a) || !isStr(a.id)) continue;
      this.note(h, a.id);
      h.agents.set(a.id, { agent: { ...a }, lastEventMs: this.agentMs(h, a.lastEventAt) });
    }
    for (const c of e.openCalls) {
      if (!isObj(c) || !isStr(c.agentId) || !isStr(c.callId)) continue;
      this.addCall(h, host, c, this.ms(h, c.startedAt));
    }
    for (const f of e.files) {
      if (!isObj(f) || !isStr(f.path)) continue;
      const atMs = this.ms(h, f.lastTouchAt);
      if (f.attribution === 'confirmed') {
        this.pushConfirmed(h, host, f.path, f.agentId, atMs);
      } else {
        h.held.push({ host, path: f.path, projectRoot: null, attribution: 'unattributed', atMs });
      }
    }
  }

  private toolStart(h: HostState, host: string, c: Call, at: number): void {
    if (!isObj(c) || !isStr(c.agentId) || !isStr(c.callId)) return;
    const prev = h.calls.get(callKey(c.agentId, c.callId));
    if (prev) this.closeOpen(h, prev);
    this.addCall(h, host, c, this.ms(h, c.startedAt));
    const a = h.agents.get(c.agentId);
    if (a) a.agent = { ...a.agent, status: 'working' };
    this.bump(h, c.agentId, at);
    if (c.category !== 'search' && Array.isArray(c.files)) this.confirm(h, host, c.files, c.agentId, at);
    this.trimCalls(h);
  }

  private addCall(h: HostState, host: string, c: Call, startMs: number): void {
    const call: ViewCall = {
      ...c, ...(c.files ? { files: [...c.files] } : {}),
      host, startMs, endMs: null, ok: null, output: '', outputTruncated: false,
    };
    const key = callKey(c.agentId, c.callId);
    h.calls.delete(key); // a restarted call moves to the newest position
    h.calls.set(key, call);
    let open = h.openByAgent.get(c.agentId);
    if (!open) h.openByAgent.set(c.agentId, (open = new Map()));
    open.set(c.callId, call);
  }

  private closeOpen(h: HostState, call: ViewCall): void {
    const open = h.openByAgent.get(call.agentId);
    if (!open) return;
    if (open.get(call.callId) === call) open.delete(call.callId);
    if (open.size === 0) h.openByAgent.delete(call.agentId);
  }

  /** Oldest ended calls go first. Open calls are never dropped here. */
  private trimCalls(h: HostState): void {
    let excess = h.calls.size - MAX_CALLS_PER_HOST;
    if (excess <= 0) return;
    for (const [key, c] of h.calls) {
      if (excess <= 0) break;
      if (c.endMs === null) continue;
      h.calls.delete(key);
      excess--;
    }
  }

  private confirm(h: HostState, host: string, paths: unknown[], agentId: string, atMs: number): void {
    for (const p of paths) if (isStr(p)) this.pushConfirmed(h, host, p, agentId, atMs);
  }

  private pushConfirmed(h: HostState, host: string, path: string, agentId: string | undefined, atMs: number): void {
    const agent = agentId === undefined ? undefined : h.agents.get(agentId)?.agent;
    const touch: Touch = {
      host, path, projectRoot: agent?.projectRoot ?? null, attribution: 'confirmed',
      ...(agentId === undefined ? {} : { agentId }), atMs,
    };
    h.confirmedQueue.push(touch);
    if (h.confirmedQueue.length > MAX_CONFIRMED) h.confirmedQueue.splice(0, h.confirmedQueue.length - MAX_CONFIRMED);
    const times = h.recent.get(path);
    if (times) {
      times.push(atMs);
      if (times.length > MAX_RECENT_PER_PATH) times.splice(0, times.length - MAX_RECENT_PER_PATH);
    } else {
      h.recent.set(path, [atMs]);
      if (h.recent.size > MAX_RECENT_PATHS) h.recent.delete(h.recent.keys().next().value as string);
    }
  }
}

function rank(h: HostState, id: string): number {
  return h.order.get(id) ?? Number.MAX_SAFE_INTEGER;
}
