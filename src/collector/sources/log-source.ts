import path from 'node:path';
import type { Coverage, SourceEvent, SourceState } from '../../shared/schema';
import { scanLogs, selectLogs } from '../discovery';
import type { LogRoot, RecentLog } from '../discovery';
import { LogTailer } from '../tail';
import { watchTree } from '../watch';
import type { TreeWatcher } from '../watch';
import { ClaudeLogAdapter } from './claude-logs';
import { CodexLogAdapter } from './codex-logs';

const HOUR = 3600 * 1000;
/** Quiet time after a watcher hint before a rescan, so a burst of new files costs one scan. */
const RESCAN_DEBOUNCE_MS = 50;

type Opts = {
  host: string;
  roots: LogRoot[];
  emit: (events: SourceEvent[]) => void;
  onStatus: (source: string, s: SourceState) => void;
  maxLogs?: number;
  sinceMs?: number;
  pollMs?: number;
  /** Safety rescan interval while any root's watcher is missing or degraded. */
  rescanMs?: number;
  /** Safety rescan interval while every root has a healthy watcher. */
  healthyRescanMs?: number;
  onWarning?: (msg: string) => void;
  /** Replaceable for tests. */
  watch?: typeof watchTree;
};

/**
 * Tails the newest Claude and Codex logs under the given roots and emits adapter events.
 * File watchers give low latency, a poll of the selected files covers missed callbacks, and a rescan finds new logs.
 * A watcher hint for an unknown log starts a rescan at once. The safety rescan walks every root, which is costly
 * on large log trees: it runs every 120 s while every root has a healthy watcher, and every 10 s otherwise.
 */
export class LogSource {
  private readonly maxLogs: number;
  private readonly sinceMs: number;
  private readonly pollMs: number;
  private readonly rescanMs: number;
  private readonly healthyRescanMs: number;
  private readonly watch: typeof watchTree;
  private readonly claude: ClaudeLogAdapter;
  private readonly codex: CodexLogAdapter;
  private readonly tailer: LogTailer;
  private readonly selected = new Map<string, RecentLog>();
  private readonly watchers = new Map<string, TreeWatcher>();
  private readonly reading = new Map<string, { again: boolean; done: Promise<void> }>();
  private readonly reported = new Map<string, string>();
  private batch: SourceEvent[] = [];
  private discovered = 0;
  private lastScanMs = Number.NEGATIVE_INFINITY;
  private stopped = false;
  private pollTimer?: ReturnType<typeof setInterval>;
  private rescanTimer?: ReturnType<typeof setInterval>;
  private debounceTimer?: ReturnType<typeof setTimeout>;
  private scanning: { again: boolean; done: Promise<void> } | null = null;

  constructor(private readonly opts: Opts) {
    this.maxLogs = opts.maxLogs ?? 120;
    this.sinceMs = opts.sinceMs ?? 24 * HOUR;
    this.pollMs = opts.pollMs ?? 2000;
    this.rescanMs = opts.rescanMs ?? 10_000;
    this.healthyRescanMs = opts.healthyRescanMs ?? 120_000;
    this.watch = opts.watch ?? watchTree;
    this.claude = new ClaudeLogAdapter(opts.host);
    this.codex = new CodexLogAdapter(opts.host);
    this.tailer = new LogTailer((record, file) => this.ingest(record, file), { onWarning: (m) => this.warn(m) });
  }

  async start(): Promise<void> {
    this.stopped = false;
    await this.rescan();
    if (this.stopped) return;
    this.pollTimer = setInterval(() => this.poll(), this.pollMs);
    this.rescanTimer = setInterval(() => this.safetyRescan(), this.checkMs());
  }

  stop(): void {
    this.stopped = true;
    clearInterval(this.pollTimer);
    clearInterval(this.rescanTimer);
    clearTimeout(this.debounceTimer);
    for (const w of this.watchers.values()) w.close();
    this.watchers.clear();
  }

  coverage(): Pick<Coverage, 'logRoots' | 'logsOpen' | 'logsDiscovered' | 'malformedRecords'> {
    return {
      logRoots: this.opts.roots.map((r) => r.path),
      logsOpen: this.tailer.openFiles,
      logsDiscovered: this.discovered,
      malformedRecords: this.tailer.malformed,
    };
  }

  private warn(msg: string): void {
    this.opts.onWarning?.(msg);
  }

  private ingest(record: unknown, file: string): void {
    const log = this.selected.get(file);
    if (!log) return;
    const adapter = log.provider === 'claude' ? this.claude : this.codex;
    this.batch.push(...adapter.ingest(record, file, log.scope));
  }

  private flush(): void {
    if (this.batch.length === 0) return;
    const events = this.batch;
    this.batch = [];
    if (this.stopped) return;
    try {
      this.opts.emit(events);
    } catch (e) {
      this.warn(`Event consumer failed: ${(e as Error).message}`);
    }
  }

  /** Read one file. A trigger during a read schedules exactly one follow-up read. */
  private readFile(file: string): Promise<void> {
    if (this.stopped) return Promise.resolve();
    const running = this.reading.get(file);
    if (running) { running.again = true; return running.done; }
    const state = { again: false, done: Promise.resolve() };
    this.reading.set(file, state);
    state.done = (async () => {
      try {
        do {
          state.again = false;
          try {
            await this.tailer.read(file);
          } catch (e) {
            const code = (e as NodeJS.ErrnoException).code;
            if (code !== 'ENOENT') this.warn(`Cannot read ${file}: ${code ?? (e as Error).message}`);
          }
          this.flush();
        } while (state.again && !this.stopped && this.selected.has(file));
      } finally {
        this.reading.delete(file);
      }
    })();
    return state.done;
  }

  private poll(): void {
    if (this.stopped) return;
    for (const file of this.selected.keys()) void this.readFile(file);
  }

  /** How often the safety rescan checks whether a rescan is due. */
  private checkMs(): number {
    return Math.min(this.rescanMs, this.healthyRescanMs);
  }

  /** True when every root has a watcher that reports no fault. */
  private watchersHealthy(): boolean {
    return this.opts.roots.every((r) => {
      const w = this.watchers.get(r.path);
      return w !== undefined && w.degraded === null;
    });
  }

  /** A rescan when the interval for the current watcher health has passed since the last scan of any kind. */
  private safetyRescan(): void {
    if (this.stopped) return;
    const due = this.watchersHealthy() ? this.healthyRescanMs : this.rescanMs;
    // Half a check of slack, so timer jitter never pushes a due rescan to the next check.
    if (Date.now() - this.lastScanMs + this.checkMs() / 2 >= due) void this.rescan();
  }

  /** At most one rescan runs. A request during a scan schedules one follow-up scan. */
  private rescan(): Promise<void> {
    if (this.stopped) return Promise.resolve();
    if (this.scanning) { this.scanning.again = true; return this.scanning.done; }
    const state = { again: false, done: Promise.resolve() };
    this.scanning = state;
    state.done = (async () => {
      try {
        do {
          state.again = false;
          await this.scan();
        } while (state.again && !this.stopped);
      } catch (e) {
        this.warn(`Log scan failed: ${(e as Error).message}`);
      } finally {
        this.scanning = null;
      }
    })();
    return state.done;
  }

  private async scan(): Promise<void> {
    this.lastScanMs = Date.now();
    const codes = new Map<string, string | null>();
    const all = await scanLogs(this.opts.roots, this.sinceMs, (root, code) => codes.set(root.path, code));
    if (this.stopped) return;
    this.discovered = all.length;
    const chosen = selectLogs(all, this.maxLogs);
    const keep = new Set(chosen.map((l) => l.file));
    const dropped = [...this.selected.keys()].filter((file) => !keep.has(file));
    for (const file of dropped) this.selected.delete(file);
    for (const file of dropped) {
      // A read in flight creates its cursor after the stat call, so forget only once it ends.
      await this.reading.get(file)?.done;
      if (this.stopped) return;
      if (!this.selected.has(file)) this.tailer.forget(file);
    }
    const fresh: RecentLog[] = [];
    for (const log of chosen) {
      if (!this.selected.has(log.file)) fresh.push(log);
      this.selected.set(log.file, log);
    }
    for (const root of this.opts.roots) {
      if (!codes.has(root.path)) continue;
      this.ensureWatcher(root, codes.get(root.path) ?? null);
    }
    // Oldest first, so the newest activity is the last thing emitted.
    for (const log of fresh.reverse()) {
      if (this.stopped) return;
      await this.readFile(log.file);
    }
  }

  /** Watch a readable root, and report its status when it changes. */
  private ensureWatcher(root: LogRoot, code: string | null): void {
    let w = this.watchers.get(root.path);
    if (code === null && w?.degraded) { w.close(); this.watchers.delete(root.path); w = undefined; }
    if (code === null && !w) {
      w = this.watch(root.path, (rel) => this.onWatch(root, rel), 'recursive');
      this.watchers.set(root.path, w);
    }
    const detail = code ?? w?.degraded ?? null;
    const state: SourceState = detail === null ? { state: 'ok' } : { state: 'degraded', detail };
    const key = `${state.state}:${state.detail ?? ''}`;
    const source = `logs:${root.path}`;
    if (this.reported.get(source) === key) return;
    this.reported.set(source, key);
    this.opts.onStatus(source, state);
  }

  private onWatch(root: LogRoot, rel: string): void {
    if (this.stopped || !rel.endsWith('.jsonl')) return;
    const file = path.join(root.path, rel);
    if (this.selected.has(file)) void this.readFile(file);
    else this.requestRescan();
  }

  private requestRescan(): void {
    if (this.debounceTimer) return;
    this.debounceTimer = setTimeout(() => {
      this.debounceTimer = undefined;
      void this.rescan();
    }, RESCAN_DEBOUNCE_MS);
  }
}
