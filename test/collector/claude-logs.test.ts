import { expect, test } from 'bun:test';
import fs from 'node:fs';
import path from 'node:path';
import { ClaudeLogAdapter } from '../../src/collector/sources/claude-logs';
import type { SourceEvent } from '../../src/shared/schema';

const T1 = '2026-01-01T00:00:01.000Z';
const T2 = '2026-01-01T00:00:02.000Z';
const FIXTURES = path.join(import.meta.dir, '..', 'fixtures', 'claude');

type Content = Record<string, unknown>[] | string;
function rec(agentId: string | null, content: Content, extra: Record<string, unknown> = {}, ts = T1) {
  return {
    sessionId: 'root', ...(agentId ? { agentId } : {}), cwd: '/project', timestamp: ts, type: 'assistant',
    message: { content }, ...extra,
  };
}
const use = (id: string, name: string, input: Record<string, unknown>) => ({ type: 'tool_use', id, name, input });
const result = (id: string, content: unknown = 'x', is_error?: boolean) =>
  ({ type: 'tool_result', tool_use_id: id, content, ...(is_error ? { is_error } : {}) });
const feed = (a: ClaudeLogAdapter, r: unknown, file = '/r/proj/main.jsonl') => a.ingest(r, file, 'default');
const kinds = (events: SourceEvent[]) => events.map((e) => e.kind);
const only = <K extends SourceEvent['kind']>(events: SourceEvent[], kind: K) =>
  events.filter((e): e is Extract<SourceEvent, { kind: K }> => e.kind === kind);
const upserts = (events: SourceEvent[]) => only(events, 'agent.upsert').map((e) => e.agent);

test('Claude session membership does not claim an immediate parent; Agent result confirms nested links', () => {
  const a = new ClaudeLogAdapter('mac');
  const nested = upserts(feed(a, rec('nested', [])));
  expect(nested).toHaveLength(1);
  expect(nested[0]).toMatchObject({ id: 'mac/claude:default:root:nested', parentId: 'mac/claude:default:root', link: 'session', role: 'sub-agent' });

  feed(a, rec('child', [use('spawn', 'Agent', { description: 'Nested test' })]));
  const out = feed(a, rec('child', [result('spawn', [])], { toolUseResult: { agentId: 'nested' } }));
  const confirmed = upserts(out).find((x) => x.id === 'mac/claude:default:root:nested')!;
  expect(confirmed).toMatchObject({ parentId: 'mac/claude:default:root:child', link: 'confirmed', name: 'Nested test' });
  // The confirmed link survives the nested agent's own later records.
  const later = feed(a, rec('nested', []));
  expect(upserts(later)).toHaveLength(0);
  expect(kinds(out).indexOf('agent.upsert')).toBeLessThan(kinds(out).indexOf('tool.end'));
});

test('failed Agent result does not confirm a parent link', () => {
  const a = new ClaudeLogAdapter('mac');
  feed(a, rec('child', [use('spawn', 'Agent', { description: 'D' })]));
  const out = feed(a, rec('child', [result('spawn', 'x', true)], { toolUseResult: { agentId: 'nested' } }));
  expect(upserts(out).some((x) => x.id.endsWith(':nested'))).toBe(false);
});

test('a record with several tool_result blocks confirms no parent; every call still ends', () => {
  const a = new ClaudeLogAdapter('mac');
  feed(a, rec('p', [use('s1', 'Agent', { description: 'One' }), use('s2', 'Task', { description: 'Two' })]));
  const out = feed(a, rec('p', [result('s1'), result('s2')], { toolUseResult: { agentId: 'kid' } }));
  expect(upserts(out).filter((x) => x.link === 'confirmed')).toHaveLength(0);
  expect(upserts(out).some((x) => x.id.endsWith(':kid'))).toBe(false);
  expect(only(out, 'tool.end').map((e) => [e.callId, e.ok])).toEqual([['s1', true], ['s2', true]]);
});

test('two agents writing the same file retain separate confirmed receipts', () => {
  const a = new ClaudeLogAdapter('mac');
  const writes: { agentId: string; path: string }[] = [];
  for (const agent of ['a', 'b']) {
    feed(a, rec(agent, [use('write', 'Write', { file_path: 'same.txt', content: 'x' })]));
    const out = feed(a, rec(agent, [result('write')]));
    for (const e of only(out, 'tool.end')) for (const w of e.writes ?? []) writes.push({ agentId: e.agentId, path: w.path });
  }
  expect(writes).toEqual([
    { agentId: 'mac/claude:default:root:a', path: '/project/same.txt' },
    { agentId: 'mac/claude:default:root:b', path: '/project/same.txt' },
  ]);
});

test('failed and unfinished file tools do not claim writes', () => {
  const a = new ClaudeLogAdapter('mac');
  feed(a, rec('a', [use('a', 'Edit', { file_path: 'a.txt' })]));
  const failed = feed(a, rec('a', [result('a', 'failed', true)]));
  const end = only(failed, 'tool.end')[0]!;
  expect(end.ok).toBe(false);
  expect(end.writes).toBeUndefined();
  // An unfinished call carries files at start but no end, so no write is ever emitted.
  const open = feed(a, rec('a', [use('b', 'Write', { file_path: 'b.txt' })]));
  expect(only(open, 'tool.start')[0]!.call.files).toEqual(['/project/b.txt']);
  expect(only(open, 'tool.end')).toHaveLength(0);
});

test('shell commands and opaque wrapper tools cannot invent file attribution', () => {
  const a = new ClaudeLogAdapter('mac');
  const start = feed(a, rec(null, [use('s', 'Bash', { command: 'echo x > file' })]));
  expect(only(start, 'tool.start')[0]!.call.files).toBeUndefined();
  const end = feed(a, rec(null, [result('s', 'ok')]));
  expect(only(end, 'tool.end')[0]!.writes).toBeUndefined();
  const wrapped = feed(a, rec(null, [use('m', 'mcp__x__write', { file_path: 'f.txt' })]));
  expect(only(wrapped, 'tool.start')[0]!.call).toMatchObject({ category: 'other' });
  expect(only(wrapped, 'tool.start')[0]!.call.files).toBeUndefined();
  const wend = feed(a, rec(null, [result('m')]));
  expect(only(wend, 'tool.end')[0]!.writes).toBeUndefined();
});

test('tool_use emits tool.start with category, files resolved against cwd, command capped at 4096', () => {
  const a = new ClaudeLogAdapter('mac');
  const out = feed(a, rec(null, [
    use('r', 'Read', { file_path: 'src/a.ts' }),
    use('c', 'Bash', { command: 'x'.repeat(5000) }),
    use('abs', 'Read', { file_path: '/other/b.ts' }),
  ]));
  const [r, c, abs] = only(out, 'tool.start').map((e) => e.call);
  expect(r).toMatchObject({ agentId: 'mac/claude:default:root', callId: 'r', category: 'read', rawName: 'Read', startedAt: T1, files: ['/project/src/a.ts'] });
  expect(c!.category).toBe('command');
  expect(c!.command).toHaveLength(4096);
  expect(c!.files).toBeUndefined();
  expect(abs!.files).toEqual(['/other/b.ts']);
  expect(only(out, 'tool.start').every((e) => e.ts === T1)).toBe(true);
  expect(only(out, 'agent.status').map((e) => e.status)).toEqual(['working']);
});

test('tool_result emits tool.output (≤4 KiB chunks, last 16 KiB) then tool.end ok = !is_error with writes for Write/Edit', () => {
  const a = new ClaudeLogAdapter('mac');
  feed(a, rec(null, [use('c', 'Bash', { command: 'ls' }), use('w', 'Write', { file_path: 'o.txt' }), use('e', 'Edit', { file_path: 'e.txt' }), use('r', 'Read', { file_path: 'r.txt' })]));

  const big = 'a'.repeat(10000) + 'b'.repeat(10000);
  const cOut = feed(a, rec(null, [result('c', big)], { type: 'user' }, T2));
  expect(kinds(cOut)).toEqual(['tool.output', 'tool.output', 'tool.output', 'tool.output', 'tool.end']);
  const chunks = only(cOut, 'tool.output');
  expect(chunks.every((c) => Buffer.byteLength(c.chunk) <= 4096)).toBe(true);
  expect(chunks.map((c) => c.truncated)).toEqual([true, false, false, false]);
  const joined = chunks.map((c) => c.chunk).join('');
  expect(joined.length).toBe(16384);
  expect(joined).toBe(big.slice(-16384));
  expect(only(cOut, 'tool.end')[0]).toMatchObject({ agentId: 'mac/claude:default:root', callId: 'c', ok: true, ts: T2 });

  const wOut = feed(a, rec(null, [result('w', 'x')], {}, T2));
  expect(kinds(wOut)).toEqual(['tool.end']);
  expect(only(wOut, 'tool.end')[0]!.writes).toEqual([{ path: '/project/o.txt', op: 'edit' }]);
  const eOut = feed(a, rec(null, [result('e', 'x', true)], {}, T2));
  expect(only(eOut, 'tool.end')[0]).toMatchObject({ ok: false });
  expect(only(eOut, 'tool.end')[0]!.writes).toBeUndefined();
  // Read results (file contents) never become output or writes.
  const rOut = feed(a, rec(null, [result('r', 'file contents')], {}, T2));
  expect(kinds(rOut)).toEqual(['tool.end']);
  expect(only(rOut, 'tool.end')[0]!.writes).toBeUndefined();
});

test('tool.output accepts text-block arrays and keeps multibyte characters whole', () => {
  const a = new ClaudeLogAdapter('mac');
  feed(a, rec(null, [use('c', 'Bash', { command: 'ls' }), use('d', 'Bash', { command: 'ls' })]));
  const blocks = feed(a, rec(null, [result('c', [{ type: 'text', text: 'one' }, { type: 'text', text: 'two' }])], {}, T2));
  expect(only(blocks, 'tool.output').map((c) => c.chunk)).toEqual(['one\ntwo']);
  const wide = '日'.repeat(3000);
  const out = only(feed(a, rec(null, [result('d', wide)], {}, T2)), 'tool.output');
  expect(out.every((c) => Buffer.byteLength(c.chunk) <= 4096 && !c.chunk.includes('�'))).toBe(true);
  expect(out.map((c) => c.chunk).join('')).toBe(wide);
  // Empty output emits no chunk.
  feed(a, rec(null, [use('e', 'Bash', { command: 'true' })]));
  expect(kinds(feed(a, rec(null, [result('e', '')], {}, T2)))).toEqual(['tool.end']);
});

test('end_turn and turn_duration emit agent.status idle', () => {
  const a = new ClaudeLogAdapter('mac');
  feed(a, rec(null, [use('c', 'Bash', { command: 'ls' })]));
  const end = feed(a, rec(null, [{ type: 'text', text: 'x' }], { message: { stop_reason: 'end_turn', content: [] } }, T2));
  expect(only(end, 'agent.status')).toEqual([{ kind: 'agent.status', ts: T2, agentId: 'mac/claude:default:root', status: 'idle' }]);
  // Already idle: no repeat.
  expect(only(feed(a, rec(null, [], { message: { stop_reason: 'end_turn', content: [] } }, T2)), 'agent.status')).toHaveLength(0);

  feed(a, rec(null, [use('d', 'Bash', { command: 'ls' })]));
  const dur = feed(a, { sessionId: 'root', cwd: '/project', timestamp: T2, type: 'system', subtype: 'turn_duration' });
  expect(only(dur, 'agent.status').map((e) => e.status)).toEqual(['idle']);
});

test('Grep/Glob tool.start carries pattern and files from input path/glob only', () => {
  const a = new ClaudeLogAdapter('mac');
  const out = feed(a, rec(null, [
    use('g1', 'Grep', { pattern: 'foo', path: 'src', glob: '*.ts' }),
    use('g2', 'Glob', { pattern: '**/*.ts' }),
  ]));
  const [g1, g2] = only(out, 'tool.start').map((e) => e.call);
  expect(g1).toMatchObject({ category: 'search', pattern: 'foo', files: ['/project/src'] });
  expect(g2).toMatchObject({ category: 'search', pattern: '**/*.ts' });
  expect(g2!.files).toBeUndefined();
  // Search results never produce files or output.
  const end = feed(a, rec(null, [result('g1', '/project/src/leak.ts\n/project/src/other.ts')], {}, T2));
  expect(kinds(end)).toEqual(['tool.end']);
  expect(only(end, 'tool.end')[0]!.writes).toBeUndefined();
});

test('Review Focus 1: two transcript files with the same sessionId yield one agent id', () => {
  const a = new ClaudeLogAdapter('mac');
  const e1 = a.ingest({ sessionId: 's1', cwd: '/p', type: 'user', timestamp: T1, message: { content: 'x' } }, '/r/proj/one.jsonl', 'default');
  const e2 = a.ingest({ sessionId: 's1', cwd: '/p', type: 'user', timestamp: T2, message: { content: 'y' } }, '/r/proj/two.jsonl', 'default');
  const ids = new Set([...e1, ...e2].filter((e) => e.kind === 'agent.upsert').map((e) => e.agent.id));
  expect([...ids]).toEqual(['mac/claude:default:s1']);
  expect(e2.filter((e) => e.kind === 'agent.upsert')).toHaveLength(0);
});

test('provider scope and host prevent identity collisions', () => {
  const a = new ClaudeLogAdapter('mac');
  const r = { sessionId: 's1', cwd: '/p', type: 'user', timestamp: T1, message: { content: 'x' } };
  const ids = ['work', 'personal'].flatMap((scope) => upserts(a.ingest(r, '/r/a.jsonl', scope)).map((x) => x.id));
  expect(ids).toEqual(['mac/claude:work:s1', 'mac/claude:personal:s1']);
});

test('agent.upsert fires only when identity fields change; status and lastEventAt do not trigger one', () => {
  const a = new ClaudeLogAdapter('mac');
  const first = upserts(feed(a, rec(null, [])));
  expect(first[0]).toMatchObject({ provider: 'claude', role: 'main', link: 'root', parentId: null, projectRoot: null, status: 'idle', lastEventAt: T1, cwd: '/project' });
  expect(upserts(feed(a, rec(null, [use('c', 'Bash', { command: 'ls' })], {}, T2)))).toHaveLength(0);
  expect(upserts(feed(a, rec(null, [], { cwd: '/elsewhere' }, T2)))[0]).toMatchObject({ cwd: '/elsewhere', status: 'working' });
  expect(upserts(feed(a, { sessionId: 'root', type: 'custom-title', customTitle: 'Named', timestamp: T2 }))[0]!.name).toBe('Named');
  expect(upserts(feed(a, rec(null, [], { message: { model: 'm-1', content: [] } }, T2)))[0]!.model).toBe('m-1');
});

test('records without a timestamp learn identity but emit no tool events', () => {
  const a = new ClaudeLogAdapter('mac');
  const bare = rec(null, [use('c', 'Bash', { command: 'ls' })]);
  delete (bare as { timestamp?: string }).timestamp;
  expect(feed(a, bare)).toEqual([]);
  // Identity is emitted on the next timestamped record.
  const next = feed(a, rec(null, [], {}, T2));
  expect(upserts(next)).toHaveLength(1);
  // The untimestamped call never started, so a result for it emits nothing.
  expect(feed(a, rec(null, [result('c')], {}, T2))).toEqual([]);
});

test('invalid records and unknown results emit nothing', () => {
  const a = new ClaudeLogAdapter('mac');
  expect(a.ingest(null, '/f.jsonl', 'default')).toEqual([]);
  expect(a.ingest('x', '/f.jsonl', 'default')).toEqual([]);
  expect(a.ingest({ type: 'user', timestamp: T1 }, '/f.jsonl', 'default')).toEqual([]);
  expect(only(feed(a, rec(null, [result('missing')])), 'tool.end')).toHaveLength(0);
});

test('pending calls are capped at 5000', () => {
  const a = new ClaudeLogAdapter('mac');
  for (let i = 0; i < 5001; i++) feed(a, rec(null, [use(`c${i}`, 'Read', { file_path: 'a' })]));
  expect(only(feed(a, rec(null, [result('c0')], {}, T2)), 'tool.end')).toHaveLength(0);
  expect(only(feed(a, rec(null, [result('c1')], {}, T2)), 'tool.end')).toHaveLength(1);
});

test('fixtures: sub-agent id comes from the file name and the session replays end to end', () => {
  const a = new ClaudeLogAdapter('mac');
  const read = (f: string) => fs.readFileSync(path.join(FIXTURES, f), 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  const main = read('session.jsonl').flatMap((r) => a.ingest(r, path.join(FIXTURES, 'session.jsonl'), 'default'));
  const sub = read('subagents/agent-sub1.jsonl').flatMap((r) => a.ingest(r, path.join(FIXTURES, 'subagents', 'agent-sub1.jsonl'), 'default'));

  expect(only(main, 'tool.start').map((e) => e.call.category)).toEqual(['read', 'command', 'write', 'search', 'edit']);
  expect(only(main, 'tool.start').map((e) => e.call.files)).toEqual([['/p/proj/src/a.ts'], undefined, ['/p/proj/out.txt'], ['/p/proj/src'], ['/p/proj/bad.txt']]);
  expect(only(main, 'tool.end').map((e) => e.ok)).toEqual([true, true, true, true, false]);
  expect(only(main, 'tool.end').flatMap((e) => e.writes ?? [])).toEqual([{ path: '/p/proj/out.txt', op: 'edit' }]);
  expect(only(main, 'tool.output').map((e) => e.chunk)).toEqual(['x']);
  expect(only(main, 'agent.status').map((e) => e.status)).toEqual(['working', 'idle']);

  expect(upserts(sub)[0]).toMatchObject({ id: 'mac/claude:default:s1:sub1', parentId: 'mac/claude:default:s1', link: 'session', role: 'sub-agent' });
  expect(only(sub, 'tool.start')[0]!.call.agentId).toBe('mac/claude:default:s1:sub1');
  expect(only(sub, 'agent.status').map((e) => e.status)).toEqual(['working', 'idle']);
});
