import { expect, test } from 'bun:test';
import { Collector } from '../../src/collector/collector';
import { Hub, LocalSource } from '../../src/hub/hub';
import type { Envelope, HostStatus, SourceEvent } from '../../src/shared/schema';
import { agent, FakeSource, TS, upsert } from './helpers';

test('uniqueLabel appends #2, #3 for repeated labels', () => {
  const h = new Hub();
  expect(h.uniqueLabel('mac')).toBe('mac');
  expect(h.uniqueLabel('mac')).toBe('mac#2');
  expect(h.uniqueLabel('mac')).toBe('mac#3');
  expect(h.uniqueLabel('other')).toBe('other');
});

test('new subscriber receives hello + snapshot per host before live events', async () => {
  const hub = new Hub();
  const a = new FakeSource('alpha');
  const b = new FakeSource('beta');
  hub.addSource(a);
  hub.addSource(b);
  a.emit(upsert('a1'));
  b.emit(upsert('b1'));

  const got: Envelope[] = [];
  hub.subscribe((e) => got.push(e));
  expect(got.map((e) => `${e.host}:${e.kind}`)).toEqual(['alpha:hello', 'alpha:snapshot', 'beta:hello', 'beta:snapshot']);
  const snaps = got.filter((e) => e.kind === 'snapshot') as Extract<Envelope, { kind: 'snapshot' }>[];
  expect(snaps[0]!.agents.map((x) => x.id)).toEqual(['a1']);
  expect(snaps[1]!.agents.map((x) => x.id)).toEqual(['b1']);
  expect(snaps[0]!.seq).toBe(1);

  a.emit(upsert('a2'));
  expect(got.at(-1)).toMatchObject({ host: 'alpha', kind: 'agent.upsert', seq: 2 });
  expect(hub.snapshotAll().filter((e) => e.kind === 'snapshot')[0]).toMatchObject({ seq: 2 });
});

test('top-level host is rewritten to the source label', () => {
  const hub = new Hub();
  const a = new FakeSource('label', 'collector-own-name');
  hub.addSource(a);
  const got: Envelope[] = [];
  hub.subscribe((e) => got.push(e));
  a.emit(upsert('x'));
  expect(got.every((e) => e.host === 'label')).toBe(true);
  expect(hub.snapshotAll().every((e) => e.host === 'label')).toBe(true);
});

test('snapshotAll includes the latest coverage per host and ignores source.status', () => {
  const hub = new Hub();
  const a = new FakeSource('alpha');
  hub.addSource(a);
  const cov = (n: number): SourceEvent => ({
    kind: 'coverage', ts: TS,
    coverage: { logRoots: [], logsOpen: n, logsDiscovered: n, watchRoots: [], malformedRecords: 0, fsDropped: 0, sources: {} },
  });
  a.emit(cov(1));
  a.emit(cov(2));
  a.emit({ kind: 'source.status', source: 'claude-logs', state: 'degraded', ts: TS });
  const all = hub.snapshotAll();
  expect(all.map((e) => e.kind)).toEqual(['hello', 'snapshot', 'coverage']);
  expect(all[2]).toMatchObject({ coverage: { logsOpen: 2 } });
});

test('a snapshot envelope replaces the mirror; bad input does not break the hub', () => {
  const hub = new Hub();
  const a = new FakeSource('alpha');
  hub.addSource(a);
  a.emit(upsert('old'));
  a.emit({ kind: 'snapshot', agents: [agent('fresh')], openCalls: [], files: [], ts: TS });
  const got: Envelope[] = [];
  hub.subscribe((e) => got.push(e));
  const snap = got.find((e) => e.kind === 'snapshot') as Extract<Envelope, { kind: 'snapshot' }>;
  expect(snap.agents.map((x) => x.id)).toEqual(['fresh']);

  const before = got.length;
  expect(() => a.emit({ kind: 'tool.start', call: null, ts: TS } as unknown as SourceEvent)).not.toThrow();
  expect(() => a.emit({ kind: 'agent.upsert', ts: TS } as unknown as SourceEvent)).not.toThrow();
  expect(got.length).toBe(before + 2);
  a.emit(upsert('after'));
  expect(got.at(-1)).toMatchObject({ kind: 'agent.upsert' });
});

test('a throwing subscriber does not break other subscribers', () => {
  const hub = new Hub();
  const a = new FakeSource('alpha');
  hub.addSource(a);
  hub.subscribe(() => { throw new Error('boom'); });
  const got: Envelope[] = [];
  hub.subscribe((e) => got.push(e));
  a.emit(upsert('x'));
  expect(got.at(-1)).toMatchObject({ kind: 'agent.upsert' });
});

test('unsubscribe stops delivery', () => {
  const hub = new Hub();
  const a = new FakeSource('alpha');
  hub.addSource(a);
  const got: Envelope[] = [];
  const off = hub.subscribe((e) => got.push(e));
  off();
  const n = got.length;
  a.emit(upsert('x'));
  expect(got.length).toBe(n);
});

test('hosts() reports each source status; onHostsChange fires with the new list', async () => {
  const hub = new Hub();
  const collector = new Collector({ host: 'local', demo: true });
  const local = new LocalSource(collector, 'local');
  const seen: HostStatus[][] = [];
  const off = hub.onHostsChange((h) => seen.push(h));
  hub.addSource(local);
  hub.addSource(new FakeSource('remote1'));
  expect(hub.hosts().map((h) => `${h.label}:${h.kind}`)).toEqual(['local:local', 'remote1:remote']);
  expect(hub.hosts()[0]!.state).toBe('connecting');
  seen.length = 0;
  await local.start();
  expect(hub.hosts()[0]!.state).toBe('up');
  expect(seen.at(-1)!.map((h) => h.state)).toEqual(['up', 'up']);
  await hub.stop();
  expect(hub.hosts()[0]!.state).toBe('down');
  expect(seen.at(-1)![0]!.state).toBe('down');
  off();
});

test('LocalSource forwards collector envelopes through the hub', async () => {
  const hub = new Hub();
  const collector = new Collector({ host: 'local', demo: true });
  const local = new LocalSource(collector, 'local');
  hub.addSource(local);
  const got: Envelope[] = [];
  await local.start();
  hub.subscribe((e) => got.push(e));
  try {
    expect(got.map((e) => e.kind).slice(0, 2)).toEqual(['hello', 'snapshot']);
    expect(got[0]).toMatchObject({ host: 'local', demo: true });
  } finally {
    await hub.stop();
  }
});
