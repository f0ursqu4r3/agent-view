import { expect, test } from 'bun:test';
import { ViewerStore } from '../../viewer/store';
import type { Agent, Call, Coverage, Envelope, FileTouch, SourceEvent } from '../../src/shared/schema';

const NOW = Date.parse('2026-01-01T01:00:00.000Z');
const iso = (ms: number) => new Date(ms).toISOString();

let seq = 0;
type Body = SourceEvent extends infer E ? (E extends unknown ? Omit<E, 'ts'> : never) : never;
const env = (host: string, e: Body, ts = NOW): Envelope =>
  ({ ...e, ts: iso(ts), v: 1, host, seq: seq++ }) as Envelope;
const hello = (host: string, now = NOW, extra: { demo?: true } = {}) =>
  env(host, { kind: 'hello', now: iso(now), collectorVersion: 'x', platform: 'linux', ...extra }, now);
const agent = (id: string, over: Partial<Agent> = {}): Agent => ({
  id, provider: 'claude', name: id, role: 'main', parentId: null, link: 'root', cwd: '/p',
  projectRoot: '/p', status: 'idle', lastEventAt: iso(NOW), ...over,
});
const call = (agentId: string, callId: string, over: Partial<Call> = {}): Call => ({
  agentId, callId, startedAt: iso(NOW), category: 'read', rawName: 'Read', ...over,
});
const snapshot = (host: string, agents: Agent[], openCalls: Call[] = [], files: FileTouch[] = []) =>
  env(host, { kind: 'snapshot', agents, openCalls, files });
const coverage = (n: number): Coverage => ({ logRoots: [], logsOpen: n, logsDiscovered: n, watchRoots: [], malformedRecords: 0, fsDropped: 0, sources: {} });

function seeded(now = NOW, host = 'local'): ViewerStore {
  const s = new ViewerStore({ now: () => now });
  s.apply(hello(host, now));
  s.apply(snapshot(host, [agent('a')]));
  return s;
}

test('snapshot replaces that host state; deltas update it; other hosts untouched', () => {
  const s = new ViewerStore({ now: () => NOW });
  s.apply(hello('h1'));
  s.apply(hello('h2'));
  s.apply(snapshot('h1', [agent('a'), agent('b')], [call('a', 'c1')]));
  s.apply(snapshot('h2', [agent('z')], [call('z', 'c9')]));
  expect(s.agents(NOW).map((a) => a.id).sort()).toEqual(['a', 'b', 'z']);
  expect(s.calls(NOW).map((c) => c.callId).sort()).toEqual(['c1', 'c9']);

  s.apply(env('h1', { kind: 'agent.upsert', agent: agent('c') }));
  s.apply(env('h1', { kind: 'agent.status', agentId: 'a', status: 'working' }));
  expect(s.agents(NOW).find((a) => a.id === 'a')?.status).toBe('working');
  expect(s.agents(NOW).find((a) => a.id === 'a')?.host).toBe('h1');
  expect(s.agents(NOW).map((a) => a.id).sort()).toEqual(['a', 'b', 'c', 'z']);

  s.apply(snapshot('h1', [agent('n')], []));
  expect(s.agents(NOW).map((a) => a.id).sort()).toEqual(['n', 'z']);
  expect(s.calls(NOW).map((c) => c.callId)).toEqual(['c9']);
  expect(s.openCall('z')?.callId).toBe('c9');
  expect(s.openCall('a')).toBeUndefined();
});

test('tool lifecycle: start, update, output, end', () => {
  const s = seeded();
  s.apply(env('local', { kind: 'tool.start', call: call('a', 'c1', { startedAt: iso(NOW - 3000), category: 'edit', files: ['/p/a.ts'] }) }, NOW - 3000));
  expect(s.openCall('a')?.endMs).toBeNull();
  expect(s.openCall('a')?.startMs).toBe(NOW - 3000);
  s.apply(env('local', { kind: 'tool.update', agentId: 'a', callId: 'c1', files: ['/p/b.ts'] }, NOW - 2000));
  expect(s.openCall('a')?.files).toEqual(['/p/a.ts', '/p/b.ts']);
  s.apply(env('local', { kind: 'tool.end', agentId: 'a', callId: 'c1', ok: true }, NOW - 1000));
  const c = s.calls(NOW)[0]!;
  expect(c.endMs).toBe(NOW - 1000);
  expect(c.ok).toBe(true);
  expect(s.openCall('a')).toBeUndefined();
  expect(s.agents(NOW).find((x) => x.id === 'a')?.status).toBe('working');
});

test('agents() excludes agents past linger and marks stale working agents', () => {
  const s = new ViewerStore({ now: () => NOW });
  s.apply(hello('local'));
  s.apply(snapshot('local', [
    agent('fresh', { status: 'working', lastEventAt: iso(NOW - 1000) }),
    agent('stale', { status: 'working', lastEventAt: iso(NOW - 130_000) }),
    agent('gone', { status: 'idle', lastEventAt: iso(NOW - 600_000) }),
    agent('done', { status: 'finished', lastEventAt: iso(NOW - 61_000) }),
    agent('parent', { status: 'idle', lastEventAt: iso(NOW - 900_000) }),
    agent('kid', { parentId: 'parent', role: 'sub-agent', status: 'idle', lastEventAt: iso(NOW - 1000) }),
  ]));
  const list = s.agents(NOW);
  expect(list.map((a) => a.id).sort()).toEqual(['fresh', 'kid', 'parent', 'stale']);
  expect(list.find((a) => a.id === 'fresh')?.stale).toBe(false);
  expect(list.find((a) => a.id === 'stale')?.stale).toBe(true);
  expect(list.find((a) => a.id === 'fresh')?.lastEventMs).toBe(NOW - 1000);
  // lingering agents return on their next event
  s.apply(env('local', { kind: 'agent.status', agentId: 'gone', status: 'working' }));
  expect(s.agents(NOW).map((a) => a.id)).toContain('gone');
  // time passes
  expect(s.agents(NOW + 700_000).length).toBe(0);
});

test('Review Focus 2: a host whose hello.now is 5 minutes behind still shows its new events as present and in the timeline window', () => {
  const s = new ViewerStore({ now: () => NOW });
  s.apply(env('remote', { kind: 'hello', now: iso(NOW - 300_000), collectorVersion: 'x', platform: 'linux' }, NOW - 300_000));
  s.apply(env('remote', { kind: 'snapshot', agents: [agent('r', { lastEventAt: iso(NOW - 300_000) })], openCalls: [], files: [] }, NOW - 300_000));
  const c = call('r', 'c1', { startedAt: iso(NOW - 300_000 + 1000) });
  s.apply(env('remote', { kind: 'tool.start', call: c }, NOW - 300_000 + 1000));
  expect(s.calls(NOW, 16_000).length).toBe(1);
  expect(s.calls(NOW, 16_000)[0]!.startMs).toBe(NOW - 299_000 + 300_000);
  expect(s.agents(NOW).map((a) => a.id)).toEqual(['r']);
  expect(s.agents(NOW)[0]!.lastEventMs).toBe(NOW - 299_000 + 300_000);
  expect(s.agents(NOW)[0]!.stale).toBe(false);
});

test('calls() windows ended calls and hides calls of absent agents', () => {
  const s = new ViewerStore({ now: () => NOW });
  s.apply(hello('local'));
  s.apply(snapshot('local', [agent('a'), agent('old', { lastEventAt: iso(NOW - 700_000) })]));
  s.apply(env('local', { kind: 'tool.start', call: call('a', 'recent', { startedAt: iso(NOW - 10_000) }) }, NOW - 10_000));
  s.apply(env('local', { kind: 'tool.end', agentId: 'a', callId: 'recent', ok: true }, NOW - 9_000));
  s.apply(env('local', { kind: 'tool.start', call: call('a', 'ancient', { startedAt: iso(NOW - 60_000) }) }, NOW - 60_000));
  s.apply(env('local', { kind: 'tool.end', agentId: 'a', callId: 'ancient', ok: true }, NOW - 50_000));
  s.apply(env('local', { kind: 'tool.start', call: call('a', 'open', { startedAt: iso(NOW - 120_000) }) }, NOW - 120_000));
  s.apply(env('local', { kind: 'tool.start', call: call('old', 'x', { startedAt: iso(NOW - 700_000) }) }, NOW - 700_000));
  expect(s.calls(NOW).map((c) => c.callId).sort()).toEqual(['open', 'recent']);
  expect(s.calls(NOW, 40_000).map((c) => c.callId).sort()).toEqual(['open', 'recent']);
  expect(s.calls(NOW, 60_000).map((c) => c.callId).sort()).toEqual(['ancient', 'open', 'recent']);
});

test('unattributed touch is held 2 s and suppressed by a confirmed touch of the same path', () => {
  const s = seeded();
  s.apply(env('local', { kind: 'fs.change', path: '/p/x.ts', op: 'change' }, NOW));
  s.apply(env('local', { kind: 'fs.change', path: '/p/y.ts', op: 'change' }, NOW));
  expect(s.takeTouches(NOW + 1999)).toEqual([]);
  // a confirmed write for x arrives during the hold
  s.apply(env('local', { kind: 'tool.end', agentId: 'a', callId: 'w', ok: true, writes: [{ path: '/p/x.ts', op: 'edit' }] }, NOW + 1500));
  const out = s.takeTouches(NOW + 2000);
  expect(out.map((t) => `${t.attribution}:${t.path}`)).toEqual(['confirmed:/p/x.ts', 'unattributed:/p/y.ts']);
  const grey = out[1]!;
  expect(grey.projectRoot).toBeNull();
  expect(grey.agentId).toBeUndefined();
  expect(grey.atMs).toBe(NOW);
  expect(out[0]!.agentId).toBe('a');
  expect(out[0]!.projectRoot).toBe('/p');
  expect(s.takeTouches(NOW + 5000)).toEqual([]);
});

test('unattributed touch is also suppressed by a confirmed touch up to 2 s before it', () => {
  const s = seeded();
  s.apply(env('local', { kind: 'tool.end', agentId: 'a', callId: 'w', ok: true, writes: [{ path: '/p/x.ts', op: 'edit' }] }, NOW));
  expect(s.takeTouches(NOW).map((t) => t.attribution)).toEqual(['confirmed']);
  s.apply(env('local', { kind: 'fs.change', path: '/p/x.ts', op: 'change' }, NOW + 2000));
  s.apply(env('local', { kind: 'fs.change', path: '/p/x.ts', op: 'change' }, NOW + 2001));
  const out = s.takeTouches(NOW + 4001);
  expect(out.map((t) => t.atMs)).toEqual([NOW + 2001]);
});

test('touch sources: start/update files confirm, search never touches, snapshot seeds files', () => {
  const s = new ViewerStore({ now: () => NOW });
  s.apply(hello('local'));
  s.apply(snapshot('local', [agent('a')], [], [
    { path: '/p/seed.ts', lastTouchAt: iso(NOW - 5000), attribution: 'confirmed', agentId: 'a' },
    { path: '/p/grey.ts', lastTouchAt: iso(NOW - 5000), attribution: 'unattributed' },
  ]));
  const seeded1 = s.takeTouches(NOW);
  expect(seeded1.map((t) => `${t.attribution}:${t.path}:${t.atMs}`)).toEqual([`confirmed:/p/seed.ts:${NOW - 5000}`, `unattributed:/p/grey.ts:${NOW - 5000}`]);
  expect(seeded1[0]!.projectRoot).toBe('/p');

  s.apply(env('local', { kind: 'tool.start', call: call('a', 'r1', { files: ['/p/r.ts'] }) }));
  s.apply(env('local', { kind: 'tool.start', call: call('a', 's1', { category: 'search', rawName: 'Grep', files: ['/p/src'] }) }));
  s.apply(env('local', { kind: 'tool.update', agentId: 'a', callId: 'r1', files: ['/p/r2.ts'] }));
  s.apply(env('local', { kind: 'tool.update', agentId: 'a', callId: 's1', files: ['/p/src/more'] }));
  expect(s.takeTouches(NOW).map((t) => t.path)).toEqual(['/p/r.ts', '/p/r2.ts']);
});

test('commands(focus) returns the newest 3 command calls, filtered by agent when focused; output keeps last 16 KiB', () => {
  const s = new ViewerStore({ now: () => NOW });
  s.apply(hello('local'));
  s.apply(snapshot('local', [agent('a'), agent('b')]));
  for (let i = 0; i < 5; i++) {
    const who = i % 2 === 0 ? 'a' : 'b';
    s.apply(env('local', { kind: 'tool.start', call: call(who, `c${i}`, { category: 'command', rawName: 'Bash', command: `cmd${i}`, startedAt: iso(NOW - 5000 + i * 100) }) }, NOW - 5000 + i * 100));
  }
  s.apply(env('local', { kind: 'tool.start', call: call('a', 'r', { startedAt: iso(NOW) }) }, NOW));
  expect(s.commands(null).map((c) => c.callId)).toEqual(['c4', 'c3', 'c2']);
  expect(s.commands('b').map((c) => c.callId)).toEqual(['c3', 'c1']);
  expect(s.commands('a', 2).map((c) => c.callId)).toEqual(['c4', 'c2']);

  const chunk = (ch: string, n: number) => ch.repeat(n);
  s.apply(env('local', { kind: 'tool.output', agentId: 'a', callId: 'c4', chunk: chunk('x', 4096), truncated: false }));
  expect(s.commands(null)[0]!.output.length).toBe(4096);
  expect(s.commands(null)[0]!.outputTruncated).toBe(false);
  for (const ch of ['y', 'z', 'w', 'v']) s.apply(env('local', { kind: 'tool.output', agentId: 'a', callId: 'c4', chunk: chunk(ch, 4096), truncated: false }));
  const out = s.commands(null)[0]!;
  expect(out.output.length).toBe(16 * 1024);
  expect(out.output.startsWith('y')).toBe(true);
  expect(out.output.endsWith('v')).toBe(true);
  expect(out.outputTruncated).toBe(true);

  s.apply(env('local', { kind: 'tool.output', agentId: 'b', callId: 'c3', chunk: 'hi', truncated: true }));
  expect(s.commands('b')[0]!.outputTruncated).toBe(true);
  s.apply(env('local', { kind: 'tool.end', agentId: 'b', callId: 'c3', ok: false, exitCode: 2 }));
  expect(s.commands('b')[0]!.ok).toBe(false);
});

test('laneOrder nests sub-agents under parents per host', () => {
  const s = new ViewerStore({ now: () => NOW });
  s.apply(hello('h1'));
  s.apply(hello('h2'));
  s.apply(snapshot('h1', [
    agent('m1'),
    agent('s2', { parentId: 'm1', role: 'sub-agent' }),
    agent('m2'),
    agent('s1', { parentId: 'm1', role: 'sub-agent' }),
    agent('g1', { parentId: 's2', role: 'sub-agent' }),
    agent('orphan', { parentId: 'missing', role: 'sub-agent' }),
  ]));
  s.apply(snapshot('h2', [agent('x1')]));
  expect(s.laneOrder(NOW)).toEqual(['m1', 's2', 'g1', 's1', 'm2', 'orphan', 'x1']);
  // agents seen later go after earlier ones
  s.apply(env('h1', { kind: 'agent.upsert', agent: agent('m0') }));
  s.apply(env('h1', { kind: 'agent.upsert', agent: agent('s3', { parentId: 'm2', role: 'sub-agent' }) }));
  expect(s.laneOrder(NOW)).toEqual(['m1', 's2', 'g1', 's1', 'm2', 's3', 'orphan', 'm0', 'x1']);
});

test('hosts, coverage, demo flag and focus', () => {
  const s = new ViewerStore({ now: () => NOW });
  expect(s.isDemo()).toBe(false);
  s.setHosts([{ label: 'local', kind: 'local', state: 'up' }]);
  expect(s.hosts()).toEqual([{ label: 'local', kind: 'local', state: 'up' }]);
  s.apply(hello('local', NOW, { demo: true }));
  expect(s.isDemo()).toBe(true);
  expect(s.coverage('local')).toBeUndefined();
  s.apply(env('local', { kind: 'coverage', coverage: coverage(1) }));
  s.apply(env('local', { kind: 'coverage', coverage: coverage(2) }));
  expect(s.coverage('local')?.logsOpen).toBe(2);
  s.apply(hello('local'));
  expect(s.isDemo()).toBe(false);
  expect(s.focus).toBeNull();
  s.setFocus('a');
  expect(s.focus).toBe('a');
  s.setFocus(null);
  expect(s.focus).toBeNull();
});

test('keeps at most 2000 calls per host, dropping the oldest ended calls first', () => {
  const s = seeded();
  s.apply(env('local', { kind: 'tool.start', call: call('a', 'keep-open', { startedAt: iso(NOW - 1000) }) }, NOW - 1000));
  for (let i = 0; i < 2100; i++) {
    s.apply(env('local', { kind: 'tool.start', call: call('a', `c${i}`) }));
    s.apply(env('local', { kind: 'tool.end', agentId: 'a', callId: `c${i}`, ok: true }));
  }
  const all = s.calls(NOW, 1000);
  expect(all.length).toBe(2000);
  expect(all.some((c) => c.callId === 'keep-open')).toBe(true);
  expect(all.some((c) => c.callId === 'c0')).toBe(false);
  expect(all.some((c) => c.callId === 'c2099')).toBe(true);
});

test('ignores unknown and malformed envelopes without throwing', () => {
  const s = seeded();
  const bad: unknown[] = [
    null, 42, 'x', {}, { v: 1, host: 'local', seq: 1, ts: iso(NOW) }, { v: 1, host: 5, kind: 'hello' },
    { v: 1, host: 'local', seq: 1, ts: iso(NOW), kind: 'nope' },
    { v: 2, host: 'local', seq: 1, ts: iso(NOW), kind: 'agent.upsert', agent: agent('v2') },
    { v: 1, host: 'local', seq: 1, ts: iso(NOW), kind: 'snapshot' },
    { v: 1, host: 'local', seq: 1, ts: iso(NOW), kind: 'tool.start' },
    { v: 1, host: 'local', seq: 1, ts: iso(NOW), kind: 'tool.output', agentId: 'a', callId: 'none', chunk: 1 },
    { v: 1, host: 'local', seq: 1, ts: iso(NOW), kind: 'fs.change' },
  ];
  for (const b of bad) expect(() => s.apply(b as Envelope)).not.toThrow();
  expect(s.agents(NOW).map((a) => a.id)).toEqual(['a']);
  expect(s.takeTouches(NOW + 5000)).toEqual([]);
});

test('I1: agents idle for over 2x linger are dropped with their calls; a present child keeps its idle parent', () => {
  const s = new ViewerStore({ now: () => NOW });
  s.apply(hello('local'));
  s.apply(snapshot('local', [
    agent('fresh', { lastEventAt: iso(NOW - 1000) }),
    agent('mid', { lastEventAt: iso(NOW - 900_000) }),
    agent('dead', { lastEventAt: iso(NOW - 1_300_000) }),
    agent('parent', { lastEventAt: iso(NOW - 1_300_000) }),
    agent('kid', { parentId: 'parent', role: 'sub-agent', lastEventAt: iso(NOW - 1000) }),
  ], [call('dead', 'open-dead', { startedAt: iso(NOW - 1_300_000) })]));
  s.apply(env('local', { kind: 'tool.start', call: call('dead', 'x', { startedAt: iso(NOW - 1_300_000) }) }, NOW - 1_300_000));
  s.apply(env('local', { kind: 'tool.end', agentId: 'dead', callId: 'x', ok: true }, NOW - 1_299_000));
  expect(s.storedAgentCount()).toBe(5);
  expect(s.laneOrder(NOW)).toEqual(['fresh', 'parent', 'kid']);
  // 'dead' is gone; the old parent stays for its present child; 'mid' (1.5x linger) stays stored but absent
  expect(s.storedAgentCount()).toBe(4);
  expect(s.agents(NOW).map((a) => a.id).sort()).toEqual(['fresh', 'kid', 'parent']);
  expect(s.openCall('dead')).toBeUndefined();
  expect(s.calls(NOW, 10_000_000).length).toBe(0);
  // later, the stale ones go too
  expect(s.laneOrder(NOW + 2_000)).toEqual(['fresh', 'parent', 'kid']);
  expect(s.storedAgentCount()).toBe(4);
  s.apply(env('local', { kind: 'agent.upsert', agent: agent('dead', { lastEventAt: iso(NOW) }) }));
  expect(s.laneOrder(NOW + 3_000)).toContain('dead');
  // sweep runs at most once per second of now
  s.apply(snapshot('local', [agent('old', { lastEventAt: iso(NOW - 1_300_000) })]));
  expect(s.storedAgentCount()).toBe(1);
  s.agents(NOW + 3_500);
  expect(s.storedAgentCount()).toBe(1);
  s.agents(NOW + 4_100);
  expect(s.storedAgentCount()).toBe(0);
});

test('I1: repeated reads with the same now do not recompute presence, and writes invalidate it', () => {
  const s = seeded();
  s.agents(NOW);
  const before = s.presenceComputes;
  s.agents(NOW);
  s.calls(NOW);
  s.laneOrder(NOW);
  s.agents(NOW);
  expect(s.presenceComputes).toBe(before);
  s.apply(env('local', { kind: 'agent.upsert', agent: agent('b') }));
  expect(s.agents(NOW).map((a) => a.id).sort()).toEqual(['a', 'b']);
  expect(s.presenceComputes).toBe(before + 1);
  s.agents(NOW + 1);
  expect(s.presenceComputes).toBe(before + 2);
});

test('I2: the confirmed touch queue holds at most 10,000 touches, dropping the oldest', () => {
  const s = seeded();
  for (let i = 0; i < 10_050; i++) {
    s.apply(env('local', { kind: 'tool.update', agentId: 'a', callId: 'none', files: [`/p/f${i}.ts`] }, NOW + i));
  }
  const out = s.takeTouches(NOW + 20_000);
  expect(out.length).toBe(10_000);
  expect(out[0]!.path).toBe('/p/f50.ts');
  expect(out[out.length - 1]!.path).toBe('/p/f10049.ts');
});
