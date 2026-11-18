import { LineSplitter, parseLine } from '../shared/ndjson';
import type { Envelope, HostStatus } from '../shared/schema';
import type { HubSource } from './hub';

const STABLE_UP_MS = 60_000;
const KILL_AFTER_MS = 2_000;
const MAX_DETAIL = 200;
const MAX_STDERR_PENDING = 4_096;
const MAX_LINE_BYTES = 8 * 1024 * 1024;

/** Host names and `user@host`. No leading `-` (ssh would read an option), no spaces, no shell characters. */
const SSH_HOST_RE = /^[A-Za-z0-9_][A-Za-z0-9._@:-]*$/;

export function isValidSshHost(sshHost: string): boolean {
  return SSH_HOST_RE.test(sshHost);
}

/** Quotes text for a POSIX remote shell. */
function shellQuote(text: string): string {
  return `'${text.replace(/'/g, `'\\''`)}'`;
}

export type RemoteOptions = {
  sshHost: string;
  label: string;
  /** Builds the child command. The default runs `agent-view collect --stdio` over `ssh`. */
  command?: (sshHost: string, label: string) => string[];
  /** `[first, max]` retry delay in ms. */
  backoffMs?: [number, number];
  spawn?: typeof Bun.spawn;
  /** Longest child stdout line kept, in bytes. A longer line is dropped and counted as malformed. */
  maxLineBytes?: number;
};

export function defaultCommand(sshHost: string, label: string): string[] {
  return ['ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ServerAliveInterval=15', sshHost, 'agent-view', 'collect', '--stdio', '--label', shellQuote(label)];
}

/** True when the line has the envelope fields the hub needs. `parseLine` has already checked `v`. */
function isEnvelope(e: Envelope | null): e is Envelope {
  if (!e) return false;
  const x = e as Record<string, unknown>;
  return typeof x.kind === 'string' && typeof x.host === 'string' && typeof x.seq === 'number' && typeof x.ts === 'string';
}

/**
 * A collector on another machine. It runs `agent-view collect --stdio` over SSH, reads NDJSON from
 * the child, and respawns the child with backoff when it exits. Each respawn is a fresh process:
 * its `hello` and `snapshot` flow through `subscribe` like any other envelope.
 */
export class RemoteCollector implements HubSource {
  readonly kind = 'remote' as const;
  readonly label: string;
  /** Child lines that were not valid envelopes. They are skipped. */
  malformedLines = 0;

  private readonly sshHost: string;
  private readonly command: (sshHost: string, label: string) => string[];
  private readonly firstDelay: number;
  private readonly maxDelay: number;
  private readonly spawn: typeof Bun.spawn;
  private readonly maxLineBytes: number;
  private readonly subs = new Set<(e: Envelope) => void>();
  private readonly listeners = new Set<() => void>();
  private state: HostStatus['state'] = 'connecting';
  private detail?: string;
  private delay: number;
  private child?: ReturnType<typeof Bun.spawn>;
  private running?: Promise<void>;
  private retryTimer?: ReturnType<typeof setTimeout>;
  private stableTimer?: ReturnType<typeof setTimeout>;
  private started = false;
  private stopped = false;

  constructor(opts: RemoteOptions) {
    if (!isValidSshHost(opts.sshHost)) throw new Error('invalid ssh host: use a host name or user@host');
    this.sshHost = opts.sshHost;
    this.label = opts.label;
    this.command = opts.command ?? defaultCommand;
    [this.firstDelay, this.maxDelay] = opts.backoffMs ?? [1_000, 30_000];
    this.spawn = opts.spawn ?? Bun.spawn;
    this.maxLineBytes = opts.maxLineBytes ?? MAX_LINE_BYTES;
    this.delay = this.firstDelay;
  }

  subscribe(fn: (e: Envelope) => void): () => void {
    this.subs.add(fn);
    return () => { this.subs.delete(fn); };
  }

  status(): HostStatus {
    return { label: this.label, kind: 'remote', state: this.state, ...(this.detail ? { detail: this.detail } : {}) };
  }

  onStatusChange(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => { this.listeners.delete(fn); };
  }

  /** Starts the first child. It does not wait for the connection. */
  async start(): Promise<void> {
    if (this.started || this.stopped) return;
    this.started = true;
    this.launch();
  }

  /** Cancels the timers and ends the child. SIGTERM first, SIGKILL after 2 s. It never respawns. */
  async stop(): Promise<void> {
    this.stopped = true;
    clearTimeout(this.retryTimer);
    clearTimeout(this.stableTimer);
    this.retryTimer = undefined;
    this.stableTimer = undefined;
    const child = this.child;
    if (child) {
      try { child.kill('SIGTERM'); } catch { /* already gone */ }
      const killer = setTimeout(() => { try { child.kill('SIGKILL'); } catch { /* already gone */ } }, KILL_AFTER_MS);
      try {
        await this.running;
      } finally {
        clearTimeout(killer);
      }
    }
    this.set('down');
  }

  private launch(): void {
    if (this.stopped) return;
    this.set('connecting');
    let child: ReturnType<typeof Bun.spawn>;
    try {
      child = this.spawn(this.command(this.sshHost, this.label), { stdin: 'ignore', stdout: 'pipe', stderr: 'pipe' });
    } catch (e) {
      this.set('down', `spawn failed: ${(e as Error).message}`.slice(0, MAX_DETAIL));
      this.scheduleRetry();
      return;
    }
    this.child = child;
    this.running = this.run(child);
  }

  private async run(child: ReturnType<typeof Bun.spawn>): Promise<void> {
    let sawHello = false;
    let lastStderr = '';
    const readStdout = async (): Promise<void> => {
      const splitter = new LineSplitter();
      let discarding = false;
      try {
        for await (let chunk of child.stdout as ReadableStream<Uint8Array>) {
          if (discarding) {
            // An oversize line: drop input up to and including its newline.
            const nl = chunk.indexOf(10);
            if (nl === -1) continue;
            chunk = chunk.subarray(nl + 1);
            discarding = false;
          }
          for (const line of splitter.push(chunk)) {
            if (line.trim() === '') continue;
            const env = parseLine(line);
            if (!isEnvelope(env)) { this.malformedLines++; continue; }
            this.deliver(env);
            if (env.kind === 'hello' && !sawHello && !this.stopped) {
              sawHello = true;
              this.set('up');
              this.stableTimer = setTimeout(() => { this.delay = this.firstDelay; }, STABLE_UP_MS);
            }
          }
          if (splitter.pendingBytes > this.maxLineBytes) {
            splitter.reset();
            this.malformedLines++;
            discarding = true;
          }
        }
      } catch { /* a broken pipe ends the stream; the exit code tells the rest */ }
    };
    const readStderr = async (): Promise<void> => {
      const decoder = new TextDecoder();
      let pending = '';
      const take = (line: string): void => { if (line.trim() !== '') lastStderr = line.trim().slice(0, MAX_DETAIL); };
      try {
        for await (const chunk of child.stderr as ReadableStream<Uint8Array>) {
          pending += decoder.decode(chunk, { stream: true });
          const parts = pending.split('\n');
          pending = parts.pop()!.slice(-MAX_STDERR_PENDING);
          for (const part of parts) take(part);
        }
      } catch { /* ignore */ }
      take(pending + decoder.decode());
    };
    await Promise.all([readStdout(), readStderr(), child.exited]);
    clearTimeout(this.stableTimer);
    this.stableTimer = undefined;
    if (this.child === child) this.child = undefined;
    if (this.stopped) return;
    const code = child.exitCode;
    let detail: string;
    if (code === 127) detail = `agent-view not found on ${this.sshHost}`;
    else if (code === 255) detail = `ssh failed: ${lastStderr}`;
    else detail = `collector exited ${code ?? child.signalCode ?? 'unknown'}`;
    this.set('down', detail.slice(0, MAX_DETAIL));
    this.scheduleRetry();
  }

  private scheduleRetry(): void {
    if (this.stopped) return;
    const wait = this.delay;
    this.delay = Math.min(this.delay * 2, this.maxDelay);
    this.retryTimer = setTimeout(() => { this.retryTimer = undefined; this.launch(); }, wait);
  }

  private deliver(env: Envelope): void {
    for (const fn of [...this.subs]) {
      try { fn(env); } catch { /* one failing subscriber must not break the others */ }
    }
  }

  private set(state: HostStatus['state'], detail?: string): void {
    if (this.state === state && this.detail === detail) return;
    this.state = state;
    this.detail = detail;
    for (const fn of [...this.listeners]) {
      try { fn(); } catch { /* a failing listener must not break the others */ }
    }
  }
}
