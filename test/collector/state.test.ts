import { expect, test } from 'bun:test';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { CollectorState } from '../../src/collector/state';
import type { Agent, Call, SourceEvent } from '../../src/shared/schema';

const BASE = Date.parse('2026-01-01T01:00:00.000Z');
const iso = (offsetMs: number) => new Date(BASE + offsetMs).toISOString();
const A = 'mac/claude:default:s1';

const agent = (id: string, over: Partial<Agent> = {}): Agent => ({
  id, provider: 'claude', name: id, role: 'main', parentId: null, link: 'root', cwd: '/p',
  projectRoot: '/p', status: 'idle', lastEventAt: iso(0), ...over,
});
const upsert = (a: Agent, ts = a.lastEventAt): SourceEvent => ({ kind: 'agent.upsert', agent: a, ts });
const call = (agentId: string, callId: string, over: Partial<Call> = {}): Call => ({
  agentId, callId, startedAt: iso(0), category: 'read', rawName: 'Read', ...over,
});
const start = (c: Call, ts = c.startedAt): SourceEvent => ({ kind: 'tool.start', call: c, ts });
const end = (agentId: string, callId: string, ts: string, extra: Partial<Extract<SourceEvent, { kind: 'tool.end' }>> = {}): SourceEvent =>
  ({ kind: 'tool.end', agentId, callId, ok: true, ts, ...extra });

test('history loading cannot push old events ahead of new events', () => {
  const s = new CollectorState();
  s.apply(upsert(agent(A, { lastEventAt: iso(5_000) }), iso(5_000)));
  s.apply({ kind: 'agent.status', agentId: A, status: 'working', ts: iso(1_000) });
  expect(s.agent(A)!.lastEventAt).toBe(iso(5_000));
  s.apply(upsert(agent(A, { lastEventAt: iso(2_000) }), iso(2_000)));
  expect(s.agent(A)!.lastEventAt).toBe(iso(5_000));
  s.apply({ kind: 'agent.status', agentId: A, status: 'idle', ts: iso(9_000) });
  expect(s.agent(A)!.lastEventAt).toBe(iso(9_000));
});

test('apply drops duplicate tool.start and tool.end for the same agentId+callId (daemon/log overlap)', () => {
  const s = new CollectorState();
  s.apply(upsert(agent(A)));
  const st = start(call(A, 'c1'));
  expect(s.apply(st)).not.toBeNull();
  expect(s.apply(st)).toBeNull();
  const en = end(A, 'c1', iso(1_000));
  expect(s.apply(en)).not.toBeNull();
  expect(s.apply(en)).toBeNull();
  // An ended call cannot be started again.
  expect(s.apply(st)).toBeNull();
  // Same callId on another agent is a different call.
  expect(s.apply(start(call('other', 'c1')))).not.toBeNull();
});

test('unknown tool.end is forwarded once and remembered as ended; output and update are forwarded', () => {
  const s = new CollectorState();
  const en = end(A, 'ghost', iso(0));
  expect(s.apply(en)).toEqual(en);
  expect(s.apply(en)).toBeNull();
  const out: SourceEvent = { kind: 'tool.output', agentId: A, callId: 'x', chunk: 'hi', truncated: false, ts: iso(0) };
  const upd: SourceEvent = { kind: 'tool.update', agentId: A, callId: 'x', files: ['/p/a'], ts: iso(0) };
  expect(s.apply(out)).toEqual(out);
  expect(s.apply(upd)).toEqual(upd);
});

test('tool.start sets the agent working; tool.end writes add confirmed FileTouch; fs.change adds unattributed FileTouch without agentId', () => {
  const s = new CollectorState();
  s.apply(upsert(agent(A)));
  expect(s.agent(A)!.status).toBe('idle');
  s.apply(start(call(A, 'c1', { category: 'edit', rawName: 'Edit', files: ['/p/in.ts'] }), iso(1_000)));
  expect(s.agent(A)!.status).toBe('working');
  s.apply(end(A, 'c1', iso(2_000), { writes: [{ path: '/p/out.ts', op: 'create' }] }));
  s.apply({ kind: 'fs.change', path: '/p/disk.ts', op: 'change', ts: iso(3_000) });
  const files = s.snapshot(BASE + 4_000).files;
  expect(files).toEqual([
    { path: '/p/disk.ts', lastTouchAt: iso(3_000), attribution: 'unattributed' },
    { path: '/p/in.ts', lastTouchAt: iso(1_000), attribution: 'confirmed', agentId: A },
    { path: '/p/out.ts', lastTouchAt: iso(2_000), attribution: 'confirmed', agentId: A },
  ]);
  expect('agentId' in files[0]!).toBe(false);
});

test('file touches: search never touches; latest touch wins per path; tool.update touches and extends the open call', () => {
  const s = new CollectorState();
  s.apply(upsert(agent(A)));
  s.apply(start(call(A, 'g', { category: 'search', rawName: 'Grep', pattern: 'x', files: ['/p/s.ts'] }), iso(100)));
  s.apply({ kind: 'tool.update', agentId: A, callId: 'g', files: ['/p/s2.ts'], ts: iso(200) });
  s.apply(start(call(A, 'b', { category: 'command', rawName: 'Bash', command: 'ls' }), iso(300)));
  s.apply({ kind: 'tool.update', agentId: A, callId: 'b', files: ['/p/u.ts'], ts: iso(400) });
  s.apply({ kind: 'fs.change', path: '/p/u.ts', op: 'change', ts: iso(500) });
  s.apply({ kind: 'fs.change', path: '/p/u.ts', op: 'change', ts: iso(450) }); // older: ignored
  let snap = s.snapshot(BASE + 1_000);
  expect(snap.files).toEqual([{ path: '/p/u.ts', lastTouchAt: iso(500), attribution: 'unattributed' }]);
  expect(snap.openCalls.find((c) => c.callId === 'b')!.files).toEqual(['/p/u.ts']);
  s.apply(end(A, 'b', iso(600), { writes: [{ path: '/p/u.ts', op: 'edit' }] }));
  snap = s.snapshot(BASE + 1_000);
  expect(snap.files).toEqual([{ path: '/p/u.ts', lastTouchAt: iso(600), attribution: 'confirmed', agentId: A }]);
});

test('agent.upsert merges descriptive fields and sets status only for a new agent', () => {
  const s = new CollectorState();
  s.apply(upsert(agent(A, { status: 'working' })));
  s.apply({ kind: 'agent.status', agentId: A, status: 'idle', ts: iso(1_000) });
  s.apply(upsert(agent(A, { status: 'working', name: 'renamed', link: 'confirmed', model: 'm1' }), iso(2_000)));
  expect(s.agent(A)).toMatchObject({ name: 'renamed', link: 'confirmed', model: 'm1', status: 'idle' });
});

test('tick ends calls open longer than 10 minutes with ok:null and no writes', () => {
  const s = new CollectorState();
  s.apply(upsert(agent(A)));
  s.apply(start(call(A, 'old', { startedAt: iso(0) })));
  s.apply(start(call(A, 'live', { startedAt: iso(0) })));
  s.apply({ kind: 'tool.output', agentId: A, callId: 'live', chunk: 'x', truncated: false, ts: iso(300_000) });
  expect(s.tick(BASE + 599_999)).toEqual([]);
  const now = BASE + 600_000;
  const out = s.tick(now);
  expect(out).toEqual([{ kind: 'tool.end', agentId: A, callId: 'old', ok: null, ts: new Date(now).toISOString() }]);
  expect('writes' in out[0]!).toBe(false);
  expect(s.tick(now + 1)).toEqual([]);
  expect(s.snapshot(now).openCalls.map((c) => c.callId)).toEqual(['live']);
  // The timed-out call is remembered as ended.
  expect(s.apply(end(A, 'old', iso(700_000)))).toBeNull();
  expect(s.tick(BASE + 300_000 + 600_000).map((e) => (e as { callId: string }).callId)).toEqual(['live']);
});

test('tick drops file touches older than 30 minutes', () => {
  const s = new CollectorState();
  s.apply({ kind: 'fs.change', path: '/p/a', op: 'change', ts: iso(0) });
  s.apply({ kind: 'fs.change', path: '/p/b', op: 'change', ts: iso(1_000) });
  s.tick(BASE + 1_800_000);
  expect(s.snapshot(BASE + 1_800_000).files.map((f) => f.path)).toEqual(['/p/b']);
  s.tick(BASE + 1_801_000);
  expect(s.snapshot(BASE + 1_801_000).files).toEqual([]);
});

test('snapshot holds only present agents and their ancestors, their open calls, and files touched within 30 minutes', () => {
  const s = new CollectorState();
  const root = agent('root', { lastEventAt: iso(-3_000_000) });
  const child = agent('child', { parentId: 'root', role: 'sub-agent', lastEventAt: iso(-1_000) });
  const gone = agent('gone', { lastEventAt: iso(-3_000_000) });
  for (const a of [root, child, gone]) s.apply(upsert(a));
  s.apply(start(call('gone', 'g1', { startedAt: iso(-3_000_000) }), iso(-3_000_000)));
  s.apply(start(call('root', 'r1', { startedAt: iso(-3_000_000) }), iso(-3_000_000)));
  s.apply({ kind: 'fs.change', path: '/p/fresh', op: 'create', ts: iso(-1_000) });
  s.apply({ kind: 'fs.change', path: '/p/stale', op: 'create', ts: iso(-1_800_001) });
  const snap = s.snapshot(BASE);
  expect(snap.kind).toBe('snapshot');
  expect(snap.agents.map((a) => a.id).sort()).toEqual(['child', 'root']);
  expect(snap.openCalls.map((c) => c.callId)).toEqual(['r1']);
  expect(snap.files.map((f) => f.path)).toEqual(['/p/fresh']);
});

test('load replaces all state and clears ended-call memory', () => {
  const s = new CollectorState();
  s.apply(upsert(agent('old')));
  s.apply(start(call('old', 'c')));
  s.apply(end('old', 'c', iso(1)));
  s.load({ kind: 'snapshot', agents: [agent(A)], openCalls: [call(A, 'c9')], files: [{ path: '/p/f', lastTouchAt: iso(0), attribution: 'unattributed' }] });
  expect(s.agent('old')).toBeUndefined();
  expect(s.agent(A)).toBeDefined();
  expect(s.snapshot(BASE).openCalls.map((c) => c.callId)).toEqual(['c9']);
  expect(s.apply(start(call('old', 'c')))).not.toBeNull();
  expect(s.apply(start(call(A, 'c9')))).toBeNull(); // still open
});

test('ended-call memory is a bounded LRU of 20,000 keys', () => {
  const s = new CollectorState();
  for (let i = 0; i < 20_000; i++) s.apply(end(A, `k${i}`, iso(0)));
  expect(s.apply(end(A, 'k0', iso(0)))).toBeNull(); // still remembered (and now most recent)
  s.apply(end(A, 'extra', iso(0))); // evicts k1, the oldest
  expect(s.apply(end(A, 'k0', iso(0)))).toBeNull();
  expect(s.apply(end(A, 'k1', iso(0)))).not.toBeNull();
});

test('snapshot + later deltas equals full replay (golden file)', () => {
  const lines = fs.readFileSync(path.join(import.meta.dir, '..', 'fixtures', 'golden', 'session.ndjson'), 'utf8')
    .split('\n').filter(Boolean);
  const events = lines.map((l) => JSON.parse(l) as SourceEvent);
  expect(events.length).toBeGreaterThanOrEqual(10);
  const now = Date.parse('2026-01-01T00:00:30.000Z');
  const half = Math.floor(events.length / 2);

  const full = new CollectorState();
  for (const e of events) full.apply(e);
  const expected = full.snapshot(now);
  expect(expected.agents.length).toBe(2);
  expect(expected.openCalls.length).toBeGreaterThan(0);
  expect(expected.files.length).toBeGreaterThan(0);

  const first = new CollectorState();
  for (const e of events.slice(0, half)) first.apply(e);
  const mid = first.snapshot(now);
  expect(mid.openCalls.length).toBeGreaterThan(0); // the cut falls while a call is open
  const second = new CollectorState();
  second.load(JSON.parse(JSON.stringify(mid)));
  for (const e of events.slice(half)) second.apply(e);
  expect(second.snapshot(now)).toEqual(expected);

  // Hand-written fixture rules hold in the replay.
  expect(expected.files.find((f) => f.path === '/work/proj/e.ts')).toBeUndefined();
  expect(expected.files.find((f) => f.path === '/work/proj/a.ts')).toMatchObject({ attribution: 'unattributed' });
  expect(expected.files.find((f) => f.path === '/work/proj/new.ts')).toMatchObject({ attribution: 'confirmed' });
});

test('watchRoots: projectRoots of present agents, newest first, max 24, never home or /', () => {
  const s = new CollectorState();
  const home = os.homedir();
  const mk = (id: string, root: string | null, ageMs: number, over: Partial<Agent> = {}) =>
    s.apply(upsert(agent(id, { projectRoot: root, lastEventAt: iso(-ageMs), ...over })));
  mk('a', '/r/a', 3_000);
  mk('b', '/r/b', 1_000);
  mk('b2', '/r/b', 500);
  mk('c', '/r/c', 2_000);
  mk('home', home, 100);
  mk('slash', '/', 100);
  mk('none', null, 100);
  mk('expired', '/r/expired', 700_000);
  expect(s.watchRoots(BASE)).toEqual(['/r/b', '/r/c', '/r/a']);

  const many = new CollectorState();
  for (let i = 0; i < 30; i++) many.apply(upsert(agent(`m${i}`, { projectRoot: `/m/${i}`, lastEventAt: iso(-i * 1_000) })));
  const roots = many.watchRoots(BASE);
  expect(roots).toHaveLength(24);
  expect(roots[0]).toBe('/m/0');
  expect(roots[23]).toBe('/m/23');
  expect(many.watchRoots(BASE, 3)).toEqual(['/m/0', '/m/1', '/m/2']);
});

test('custom lifecycle options apply to snapshot and tick', () => {
  const s = new CollectorState({ lingerMs: 1_000, callTimeoutMs: 2_000 });
  s.apply(upsert(agent(A)));
  s.apply(start(call(A, 'c')));
  expect(s.snapshot(BASE + 999).agents).toHaveLength(1);
  expect(s.snapshot(BASE + 1_000).agents).toHaveLength(0);
  expect(s.tick(BASE + 2_000)).toHaveLength(1);
});
