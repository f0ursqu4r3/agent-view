import type { HubSource } from '../../src/hub/hub';
import type { Agent, Envelope, HostStatus, SourceEvent } from '../../src/shared/schema';

export const TS = new Date().toISOString();

/** A source the test drives by hand. Like a collector, it sends hello and snapshot on subscribe. */
export class FakeSource implements HubSource {
  readonly kind = 'remote' as const;
  state: HostStatus['state'] = 'up';
  seq = 0;
  private readonly subs = new Set<(e: Envelope) => void>();
  private readonly listeners = new Set<() => void>();
  constructor(readonly label: string, private readonly sourceHost = label) {}
  subscribe(fn: (e: Envelope) => void): () => void {
    fn(this.env({ kind: 'hello', collectorVersion: '0', platform: 'test', now: TS, ts: TS }, false));
    fn(this.env({ kind: 'snapshot', agents: [], openCalls: [], files: [], ts: TS }, false));
    this.subs.add(fn);
    return () => { this.subs.delete(fn); };
  }
  status(): HostStatus { return { label: this.label, kind: this.kind, state: this.state }; }
  onStatusChange(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => { this.listeners.delete(fn); };
  }
  setState(state: HostStatus['state']): void {
    this.state = state;
    for (const fn of [...this.listeners]) fn();
  }
  async start(): Promise<void> {}
  async stop(): Promise<void> {}
  emit(e: SourceEvent): void {
    const env = this.env(e);
    for (const fn of [...this.subs]) fn(env);
  }
  private env(e: SourceEvent, live = true): Envelope {
    return { v: 1, host: this.sourceHost, seq: live ? ++this.seq : this.seq, ...e };
  }
}

export const agent = (id: string): Agent => ({
  id, provider: 'claude', name: id, role: 'main', parentId: null, link: 'root', cwd: '/p', projectRoot: '/p',
  status: 'working', lastEventAt: new Date().toISOString(),
});
export const upsert = (id: string): SourceEvent => ({ kind: 'agent.upsert', agent: agent(id), ts: new Date().toISOString() });
