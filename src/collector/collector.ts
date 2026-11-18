import fsp from 'node:fs/promises';
import path from 'node:path';
import pkg from '../../package.json';
import { SCHEMA_VERSION } from '../shared/schema';
import type { Coverage, Envelope, SourceEvent, SourceState } from '../shared/schema';
import type { LifecycleOptions } from '../shared/lifecycle';
import { CollectorState } from './state';
import { discoverRoots, findProjectRoot } from './discovery';
import type { LogRoot } from './discovery';
import { DemoSource } from './demo';
import { LogSource } from './sources/log-source';
import { FsChangeSource } from './sources/fs-changes';

type Opts = {
  host: string;
  demo?: boolean;
  demoScale?: 'normal' | 'stress';
  watch?: string[];
  lifecycle?: Partial<LifecycleOptions>;
  roots?: LogRoot[];
};

const TICK_MS = 1000;
const COVERAGE_MS = 5000;
const MAX_WATCH_ROOTS = 24;
const CACHE_SIZE = 5000;
/** Least-recently-used cache of resolved paths. Misses are never stored: the path may exist a moment later. */
class PathCache {
  private readonly map = new Map<string, string>();
  constructor(private readonly max: number) {}

  get(key: string): string | undefined {
    const hit = this.map.get(key);
    if (hit === undefined) return undefined;
    this.map.delete(key);
    this.map.set(key, hit);
    return hit;
  }

  set(key: string, value: string): void {
    this.map.delete(key);
    this.map.set(key, value);
    if (this.map.size > this.max) this.map.delete(this.map.keys().next().value as string);
  }
}

/**
 * One collector: sources feed one ordered queue (enrich, state, envelope, subscribers).
 * A demo collector runs only the demo source.
 */
export class Collector {
  private readonly state: CollectorState;
  private readonly subscribers = new Set<(e: Envelope) => void>();
  private readonly sources = new Map<string, SourceState>();
  private readonly dirs = new PathCache(CACHE_SIZE);
  private readonly projectRoots = new PathCache(CACHE_SIZE);
  private logs?: LogSource;
  private fs?: FsChangeSource;
  private demo?: DemoSource;
  private tickTimer?: ReturnType<typeof setInterval>;
  private coverageTimer?: ReturnType<typeof setInterval>;
  private queue: Promise<void> = Promise.resolve();
  private seq = 0;
  private stopped = false;
  private startPromise?: Promise<void>;
  private stopPromise?: Promise<void>;

  constructor(private readonly opts: Opts) {
    this.state = new CollectorState(opts.lifecycle);
  }

  start(): Promise<void> {
    this.startPromise ??= this.run();
    return this.startPromise;
  }

  stop(): Promise<void> {
    this.stopPromise ??= (async () => {
      this.stopped = true;
      clearInterval(this.tickTimer);
      clearInterval(this.coverageTimer);
      this.demo?.stop();
      this.logs?.stop();
      this.fs?.stop();
      await this.startPromise?.catch(() => {});
      this.logs?.stop();
      this.fs?.stop();
      await this.queue;
    })();
    return this.stopPromise;
  }

  /** Sends `hello`, then `snapshot`, then every later envelope. Returns the unsubscribe function. */
  subscribe(fn: (e: Envelope) => void): () => void {
    const now = Date.now();
    const ts = new Date(now).toISOString();
    // hello and snapshot carry the current seq without taking a number, so other subscribers see no gap.
    this.deliverTo(fn, this.envelope({
      kind: 'hello', collectorVersion: pkg.version, platform: `${process.platform}-${process.arch}`, now: ts,
      ...(this.opts.demo ? { demo: true as const } : {}), ts,
    }, false));
    this.deliverTo(fn, this.envelope({ ...this.state.snapshot(now), ts }, false));
    this.subscribers.add(fn);
    return () => { this.subscribers.delete(fn); };
  }

  coverage(): Coverage {
    const base = this.logs?.coverage() ?? { logRoots: [], logsOpen: 0, logsDiscovered: 0, malformedRecords: 0 };
    return {
      ...base,
      watchRoots: this.fs?.roots ?? [],
      fsDropped: this.fs?.dropped ?? 0,
      sources: Object.fromEntries(this.sources),
    };
  }

  private async run(): Promise<void> {
    const { host } = this.opts;
    this.tickTimer = setInterval(() => this.tick(), TICK_MS);
    this.coverageTimer = setInterval(() => this.pushCoverage(), COVERAGE_MS);
    if (this.opts.demo) {
      this.demo = new DemoSource({ host, scale: this.opts.demoScale, emit: (events) => this.push(events) });
      if (!this.stopped) this.demo.start();
      this.pushCoverage();
      await this.queue;
      return;
    }
    const roots = this.opts.roots ?? await discoverRoots();
    if (this.stopped) return;
    const onStatus = (source: string, s: SourceState) => this.onStatus(source, s);
    this.fs = new FsChangeSource({ emit: (events) => this.push(events), onStatus });
    this.logs = new LogSource({ host, roots, emit: (events) => this.push(events), onStatus });
    this.syncRoots();
    await this.logs.start();
    if (this.stopped) return;
    this.pushCoverage();
    // The initial scan's events are in the state when `start` resolves.
    await this.queue;
  }

  private onStatus(source: string, s: SourceState): void {
    this.sources.set(source, s);
    this.push([{ kind: 'source.status', source, ...s, ts: new Date().toISOString() }]);
  }

  private pushCoverage(): void {
    this.push([{ kind: 'coverage', coverage: this.coverage(), ts: new Date().toISOString() }]);
  }

  /**
   * Runs inside the queue, so it sees the state after every earlier event, including those still waiting on enrichment.
   * Its events (timed-out calls) are final: they go out directly and are not applied to the state again.
   */
  private tick(): void {
    if (this.stopped) return;
    this.queue = this.queue.then(() => {
      if (this.stopped) return;
      for (const e of this.state.tick(Date.now())) this.deliver(this.envelope(e));
      this.syncRoots();
    }).catch(() => {});
  }

  /** Watch the `--watch` directories plus the project roots of present agents. */
  private syncRoots(): void {
    if (!this.fs || this.stopped) return;
    const roots = [...new Set([...(this.opts.watch ?? []), ...this.state.watchRoots(Date.now())])];
    try {
      this.fs.setRoots(roots.slice(0, MAX_WATCH_ROOTS));
    } catch (e) {
      process.stderr.write(`agent-view: cannot update watch roots: ${(e as Error).message}\n`);
    }
  }

  /** Every batch joins one queue, so events keep their order even when enrichment waits on the disk. */
  private push(events: SourceEvent[]): void {
    if (this.stopped || events.length === 0) return;
    this.queue = this.queue.then(() => this.process(events)).catch(() => {});
  }

  private async process(events: SourceEvent[]): Promise<void> {
    let sawAgent = false;
    for (const raw of events) {
      if (this.stopped) return;
      let e = raw;
      if (!this.opts.demo) {
        try { e = await this.enrich(raw); } catch { e = raw; }
        if (this.stopped) return;
      }
      if (e.kind === 'agent.upsert') sawAgent = true;
      let out: SourceEvent | null;
      try { out = this.state.apply(e); } catch { continue; }
      if (out) this.deliver(this.envelope(out));
    }
    if (sawAgent) this.syncRoots();
  }

  private envelope(e: SourceEvent, live = true): Envelope {
    return { v: SCHEMA_VERSION, host: this.opts.host, seq: live ? ++this.seq : this.seq, ...e };
  }

  private deliver(env: Envelope): void {
    if (this.stopped) return;
    for (const fn of [...this.subscribers]) this.deliverTo(fn, env);
  }

  private deliverTo(fn: (e: Envelope) => void, env: Envelope): void {
    try { fn(env); } catch { /* one failing subscriber must not break the others */ }
  }

  // Enrichment

  private async enrich(e: SourceEvent): Promise<SourceEvent> {
    switch (e.kind) {
      case 'agent.upsert': {
        const { cwd, projectRoot } = e.agent;
        if (!cwd || projectRoot !== null) return e;
        const root = await this.projectRootFor(cwd);
        return root === null ? e : { ...e, agent: { ...e.agent, projectRoot: root } };
      }
      case 'tool.start': {
        const files = e.call.files;
        if (!files?.length) return e;
        return { ...e, call: { ...e.call, files: await this.mapPaths(files) } };
      }
      case 'tool.update':
        return e.files.length ? { ...e, files: await this.mapPaths(e.files) } : e;
      case 'tool.end': {
        const writes = e.writes;
        if (!writes?.length) return e;
        const paths = await this.mapPaths(writes.map((w) => w.path));
        return { ...e, writes: writes.map((w, i) => ({ ...w, path: paths[i]! })) };
      }
      default:
        return e;
    }
  }

  private async projectRootFor(cwd: string): Promise<string | null> {
    const cached = this.projectRoots.get(cwd);
    if (cached !== undefined) return cached;
    try {
      const root = await fsp.realpath(await findProjectRoot(cwd));
      this.projectRoots.set(cwd, root);
      return root;
    } catch {
      return null;
    }
  }

  private mapPaths(paths: string[]): Promise<string[]> {
    return Promise.all(paths.map((p) => this.realPath(p)));
  }

  /**
   * The real path of the parent directory plus the base name. The file, and even its directory, may not exist
   * yet: Claude logs a `Write` before the tool creates it. The nearest existing ancestor is resolved
   * and the missing segments are appended.
   */
  private async realPath(p: string): Promise<string> {
    if (!path.isAbsolute(p)) return p;
    const dir = await this.realDir(path.dirname(p));
    return dir === null ? p : path.join(dir, path.basename(p));
  }

  private async realDir(dir: string): Promise<string | null> {
    const missing: string[] = [];
    let cur = dir;
    for (;;) {
      let real: string | undefined = this.dirs.get(cur);
      if (real === undefined) {
        try {
          real = await fsp.realpath(cur);
          this.dirs.set(cur, real);
        } catch { /* walk up */ }
      }
      if (real !== undefined) return missing.length ? path.join(real, ...missing.reverse()) : real;
      const parent = path.dirname(cur);
      if (parent === cur) return null;
      missing.push(path.basename(cur));
      cur = parent;
    }
  }
}
