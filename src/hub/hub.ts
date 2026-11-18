import type { Collector } from '../collector/collector';
import { CollectorState } from '../collector/state';
import { LIFECYCLE } from '../shared/lifecycle';
import type { LifecycleOptions } from '../shared/lifecycle';
import { SCHEMA_VERSION } from '../shared/schema';
import type { Envelope, Event, HostStatus, SourceEvent } from '../shared/schema';

type SnapshotEvent = Extract<Event, { kind: 'snapshot' }>;

export interface HubSource {
  readonly label: string;
  readonly kind: 'local' | 'remote';
  subscribe(fn: (e: Envelope) => void): () => void;
  status(): HostStatus;
  start(): Promise<void>;
  stop(): Promise<void>;
  /** Optional: tells the hub when `status()` changes. */
  onStatusChange?(fn: () => void): () => void;
}

/** An in-process collector. Its status is `connecting` until `start` resolves, `up` after, `down` after `stop`. */
export class LocalSource implements HubSource {
  readonly kind = 'local' as const;
  private state: HostStatus['state'] = 'connecting';
  private detail?: string;
  private readonly listeners = new Set<() => void>();

  constructor(private readonly collector: Collector, readonly label: string) {}

  subscribe(fn: (e: Envelope) => void): () => void {
    return this.collector.subscribe(fn);
  }

  status(): HostStatus {
    return { label: this.label, kind: 'local', state: this.state, ...(this.detail ? { detail: this.detail } : {}) };
  }

  onStatusChange(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => { this.listeners.delete(fn); };
  }

  async start(): Promise<void> {
    try {
      await this.collector.start();
    } catch (e) {
      this.set('down', (e as Error).message);
      throw e;
    }
    // A stop that raced with start wins.
    if (this.state === 'connecting') this.set('up');
  }

  async stop(): Promise<void> {
    try {
      await this.collector.stop();
    } finally {
      this.set('down');
    }
  }

  private set(state: HostStatus['state'], detail?: string): void {
    this.state = state;
    this.detail = detail;
    for (const fn of [...this.listeners]) {
      try { fn(); } catch { /* a failing listener must not break the others */ }
    }
  }
}

type Mirror = {
  source: HubSource;
  state: CollectorState;
  hello?: Extract<Envelope, { kind: 'hello' }>;
  /** Hub clock minus host clock, measured when the hello arrived. */
  offset: number;
  coverage?: Envelope;
  sourceStatus: Map<string, Envelope>;
  seq: number;
  off: () => void;
  offStatus?: () => void;
};

/** How often the hub ages each mirror (ends timed-out calls, drops old files). */
const TICK_MS = 1000;

/**
 * Merges the sources. For each host it keeps a `CollectorState` mirror, so a new subscriber gets
 * hello, snapshot, and coverage first, then live envelopes.
 * Each mirror is judged by its host's clock: the hub keeps the skew it saw at the host's hello.
 */
export class Hub {
  private readonly mirrors = new Map<string, Mirror>();
  private readonly usedLabels = new Set<string>();
  private readonly subscribers = new Set<(e: Envelope) => void>();
  private readonly hostListeners = new Set<(hosts: HostStatus[]) => void>();
  private tickTimer?: ReturnType<typeof setInterval>;
  private stopped = false;

  constructor(private readonly opts: { lifecycle?: Partial<LifecycleOptions>; tickMs?: number } = {}) {}

  /** The linger the hub judges presence with. The viewer gets it with the host list. */
  get lingerMs(): number {
    return this.opts.lifecycle?.lingerMs ?? LIFECYCLE.lingerMs;
  }

  /** The first use of a label returns it. Later uses return `label#2`, `label#3`, and so on. */
  uniqueLabel(wanted: string): string {
    let label = wanted;
    for (let n = 2; this.usedLabels.has(label); n++) label = `${wanted}#${n}`;
    this.usedLabels.add(label);
    return label;
  }

  addSource(src: HubSource): void {
    if (this.mirrors.has(src.label)) throw new Error(`duplicate host label: ${src.label}`);
    this.usedLabels.add(src.label);
    const mirror: Mirror = {
      source: src, state: new CollectorState(this.opts.lifecycle), offset: 0, sourceStatus: new Map(), seq: 0, off: () => {},
    };
    this.mirrors.set(src.label, mirror);
    mirror.offStatus = src.onStatusChange?.(() => this.notifyHosts());
    mirror.off = src.subscribe((e) => this.ingest(mirror, e));
    this.startTicking();
    this.notifyHosts();
  }

  /** hello, snapshot, and coverage for each host with a known hello, then every live envelope. */
  subscribe(fn: (e: Envelope) => void): () => void {
    for (const env of this.snapshotAll()) this.deliverTo(fn, env);
    this.subscribers.add(fn);
    return () => { this.subscribers.delete(fn); };
  }

  /**
   * Each host's hello, snapshot, and coverage. The hello carries the host's clock now, not at its first
   * hello: the viewer measures skew from it. The snapshot is judged at the host's now.
   */
  snapshotAll(): Envelope[] {
    const out: Envelope[] = [];
    const hubNow = Date.now();
    for (const [host, m] of this.mirrors) {
      if (!m.hello) continue;
      const now = hubNow - m.offset;
      const ts = new Date(now).toISOString();
      out.push({ ...m.hello, now: ts, ts });
      out.push({ v: SCHEMA_VERSION, host, seq: m.seq, ...m.state.snapshot(now), ts });
      if (m.coverage) out.push(m.coverage);
    }
    return out;
  }

  /** Diagnostic: file touches held by all mirrors. */
  storedFileCount(): number {
    let n = 0;
    for (const m of this.mirrors.values()) n += m.state.fileCount();
    return n;
  }

  hosts(): HostStatus[] {
    return [...this.mirrors.values()].map((m) => m.source.status());
  }

  onHostsChange(fn: (hosts: HostStatus[]) => void): () => void {
    this.hostListeners.add(fn);
    return () => { this.hostListeners.delete(fn); };
  }

  async stop(): Promise<void> {
    this.stopped = true;
    clearInterval(this.tickTimer);
    this.tickTimer = undefined;
    const mirrors = [...this.mirrors.values()];
    for (const m of mirrors) m.off();
    await Promise.allSettled(mirrors.map((m) => m.source.stop()));
    for (const m of mirrors) m.offStatus?.();
    this.subscribers.clear();
    this.notifyHosts();
    this.hostListeners.clear();
  }

  private ingest(m: Mirror, raw: Envelope): void {
    if (this.stopped) return;
    const env: Envelope = { ...raw, host: m.source.label };
    if (typeof env.seq === 'number') m.seq = env.seq;
    try {
      switch (env.kind) {
        case 'hello': {
          m.hello = env;
          const t = Date.parse(env.now);
          m.offset = Number.isNaN(t) ? 0 : Date.now() - t;
          break;
        }
        case 'snapshot': m.state.load(env as SnapshotEvent); break;
        case 'coverage': m.coverage = env; break;
        case 'source.status': m.sourceStatus.set(env.source, env); break;
        default: m.state.apply(env as SourceEvent);
      }
    } catch {
      // Bad input must not break the hub.
    }
    for (const fn of [...this.subscribers]) this.deliverTo(fn, env);
  }

  /**
   * Ages every mirror once per tick at its host's clock. The events `tick` returns are dropped:
   * each collector sends its own timed-out `tool.end`. The timer does not keep the process alive.
   */
  private startTicking(): void {
    if (this.tickTimer || this.stopped) return;
    this.tickTimer = setInterval(() => {
      const hubNow = Date.now();
      for (const m of this.mirrors.values()) {
        try { m.state.tick(hubNow - m.offset); } catch { /* bad state must not break the hub */ }
      }
    }, this.opts.tickMs ?? TICK_MS);
    this.tickTimer.unref?.();
  }

  private notifyHosts(): void {
    if (this.hostListeners.size === 0) return;
    const hosts = this.hosts();
    for (const fn of [...this.hostListeners]) {
      try { fn(hosts); } catch { /* a failing listener must not break the others */ }
    }
  }

  private deliverTo(fn: (e: Envelope) => void, env: Envelope): void {
    try { fn(env); } catch { /* one failing subscriber must not break the others */ }
  }
}
