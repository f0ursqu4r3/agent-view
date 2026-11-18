import { expect, test } from 'bun:test';
import fs from 'node:fs';
import path from 'node:path';
import { CodexLogAdapter } from '../../src/collector/sources/codex-logs';
import type { SourceEvent } from '../../src/shared/schema';

const T1 = '2026-01-01T00:00:01.000Z';
const T2 = '2026-01-01T00:00:02.000Z';
const FIXTURES = path.join(import.meta.dir, '..', 'fixtures', 'codex');
const A = (id: string) => `mac/codex:work:${id}`;

type Json = Record<string, unknown>;
const meta = (id: string, parent?: string, cwd: string | null = '/project', ts = T1) => ({
  type: 'session_meta', timestamp: ts,
  payload: {
    id, ...(cwd ? { cwd } : {}),
    ...(parent ? { parent_thread_id: parent, source: { subagent: { thread_spawn: { parent_thread_id: parent } } } } : {}),
  },
});
const resp = (payload: Json, ts = T1) => ({ type: 'response_item', timestamp: ts, payload });
const ev = (payload: Json, ts = T1) => ({ type: 'event_msg', timestamp: ts, payload });
const turn = (cwd: string, ts = T1) => ({ type: 'turn_context', timestamp: ts, payload: { cwd, model: 'm-1' } });
const patchCall = (callId: string, input: string, name = 'apply_patch') =>
  resp({ type: 'custom_tool_call', name, call_id: callId, input });
const patchOut = (callId: string, output: unknown) => resp({ type: 'custom_tool_call_output', call_id: callId, output }, T2);
const OK = 'Success. Updated the following files:\nA hello.txt';
const done = (item: Json, thread: string | null = 'main', extra: Json = {}, ts = T1) =>
  ev({ type: 'item_completed', ...(thread ? { thread_id: thread } : {}), item, ...extra }, ts);
const patchItem = (id = 'nested', over: Json = {}) =>
  ({ type: 'FileChange', id, status: 'completed', changes: { '/project/shared.txt': { type: 'update', unified_diff: 'not retained' } }, ...over });

const feed = (a: CodexLogAdapter, r: unknown, file = '/r/main.jsonl') => a.ingest(r, file, 'work');
const setup = () => { const a = new CodexLogAdapter('mac'); feed(a, meta('main')); return a; };
const kinds = (events: SourceEvent[]) => events.map((e) => e.kind);
const only = <K extends SourceEvent['kind']>(events: SourceEvent[], kind: K) =>
  events.filter((e): e is Extract<SourceEvent, { kind: K }> => e.kind === kind);
const upserts = (events: SourceEvent[]) => only(events, 'agent.upsert').map((e) => e.agent);
const writesOf = (events: SourceEvent[]) =>
  only(events, 'tool.end').flatMap((e) => (e.writes ?? []).map((w) => ({ agentId: e.agentId, ...w })));

test('Codex links children before parents and preserves nested parent identity', () => {
  const a = new CodexLogAdapter('mac');
  const out = [
    ...feed(a, meta('grandchild', 'child'), '/r/g.jsonl'),
    ...feed(a, meta('child', 'main'), '/r/c.jsonl'),
    ...feed(a, meta('main'), '/r/m.jsonl'),
  ];
  const byId = new Map(upserts(out).map((x) => [x.id, x]));
  expect(byId.get(A('grandchild'))).toMatchObject({ parentId: A('child'), link: 'confirmed', role: 'sub-agent', provider: 'codex', projectRoot: null });
  expect(byId.get(A('child'))).toMatchObject({ parentId: A('main'), link: 'confirmed' });
  expect(byId.get(A('main'))).toMatchObject({ parentId: null, link: 'root', role: 'main' });
});

test('Codex patch success requires a paired receipt; replay is deduplicated', () => {
  const a = new CodexLogAdapter('mac');
  feed(a, meta('a', 'main'), '/r/a.jsonl');
  const all: SourceEvent[] = [];
  for (let i = 0; i < 2; i++) {
    all.push(...feed(a, patchCall('patch', '*** Begin Patch\n*** Add File: hello.txt\n+hi\n*** End Patch'), '/r/a.jsonl'));
    all.push(...feed(a, patchOut('patch', OK), '/r/a.jsonl'));
  }
  expect(only(all, 'tool.start')).toHaveLength(1);
  expect(only(all, 'tool.end')).toHaveLength(1);
  expect(writesOf(all)).toEqual([{ agentId: A('a'), path: '/project/hello.txt', op: 'create' }]);
});

test('open patch call has category edit and files, but no writes until a receipt', () => {
  const a = setup();
  const start = feed(a, patchCall('p', '*** Update File: a.txt\n*** Move to: b.txt\n*** Delete File: c.txt\n*** Add File: a.txt'));
  expect(only(start, 'tool.start')[0]!.call).toMatchObject({
    agentId: A('main'), callId: 'p', category: 'edit', rawName: 'apply_patch', startedAt: T1,
    files: ['/project/a.txt', '/project/b.txt', '/project/c.txt'],
  });
  expect(only(start, 'tool.end')).toHaveLength(0);
  const end = feed(a, patchOut('p', OK));
  expect(only(end, 'tool.end')[0]).toMatchObject({ ok: true, ts: T2 });
  expect(writesOf(end).map((w) => [w.path, w.op])).toEqual([
    ['/project/a.txt', 'edit'], ['/project/b.txt', 'move'], ['/project/c.txt', 'delete'], ['/project/a.txt', 'create'],
  ]);
});

test('provider, account scope and host prevent identity collisions', () => {
  const a = new CodexLogAdapter('mac');
  const ids = ['work', 'personal'].flatMap((scope) => upserts(a.ingest(meta('same'), '/r/a.jsonl', scope)).map((x) => x.id));
  expect(ids).toEqual(['mac/codex:work:same', 'mac/codex:personal:same']);
  const other = upserts(new CodexLogAdapter('box').ingest(meta('same'), '/r/a.jsonl', 'work')).map((x) => x.id);
  expect(other).toEqual(['box/codex:work:same']);
});

test('Codex sibling writes use agent-scoped call IDs and preserve shared file receipts', () => {
  const a = new CodexLogAdapter('mac');
  const out: SourceEvent[] = [];
  for (const id of ['a', 'b']) {
    out.push(...feed(a, meta(id, 'main'), `/r/${id}.jsonl`));
    out.push(...feed(a, patchCall('same', '*** Update File: shared.txt'), `/r/${id}.jsonl`));
  }
  for (const id of ['b', 'a']) out.push(...feed(a, patchOut('same', 'Success. Updated the following files:\nM shared.txt'), `/r/${id}.jsonl`));
  expect(new Set(writesOf(out).map((w) => w.agentId))).toEqual(new Set([A('a'), A('b')]));
  expect(writesOf(out).every((w) => w.path === '/project/shared.txt' && w.op === 'edit')).toBe(true);
  expect(only(out, 'tool.start').map((e) => e.call.agentId)).toEqual([A('a'), A('b')]);
});

test('Codex failed patch and unpaired result cannot claim a write', () => {
  const a = new CodexLogAdapter('mac');
  feed(a, meta('a'), '/r/a.jsonl');
  feed(a, patchCall('p', '*** Add File: new.txt'), '/r/a.jsonl');
  const failed = feed(a, patchOut('p', 'Error: patch rejected'), '/r/a.jsonl');
  expect(only(failed, 'tool.end')[0]).toMatchObject({ ok: false });
  expect(writesOf(failed)).toEqual([]);
  const unpaired = feed(a, patchOut('missing', 'Success. Updated the following files:\nA new.txt'), '/r/a.jsonl');
  expect(unpaired).toEqual([]);
  // Mixed success and failure text is not a success.
  feed(a, patchCall('q', '*** Add File: q.txt'), '/r/a.jsonl');
  const mixed = feed(a, patchOut('q', `${OK}\nFailed to apply hunk`), '/r/a.jsonl');
  expect(only(mixed, 'tool.end')[0]!.ok).toBe(false);
  expect(writesOf(mixed)).toEqual([]);
});

test('Codex resolves relative paths against the latest turn directory', () => {
  const a = new CodexLogAdapter('mac');
  feed(a, meta('a', undefined, '/old'), '/r/a.jsonl');
  const ctx = feed(a, turn('/new'), '/r/a.jsonl');
  expect(upserts(ctx)[0]).toMatchObject({ cwd: '/new', model: 'm-1' });
  feed(a, patchCall('p', '*** Add File: new.txt'), '/r/a.jsonl');
  expect(writesOf(feed(a, patchOut('p', OK), '/r/a.jsonl'))[0]!.path).toBe('/new/new.txt');
});

test('wrapped patch uses its native thread and receipt, without parsing wrapper code', () => {
  const a = setup();
  const outer = feed(a, resp({ type: 'custom_tool_call', name: 'exec', call_id: 'outer', input: 'throw new Error("must never run")' }));
  expect(only(outer, 'tool.start')[0]!.call).toMatchObject({ category: 'other', rawName: 'exec' });
  expect(only(outer, 'tool.start')[0]!.call.files).toBeUndefined();
  const out = feed(a, done(patchItem(), 'child'));
  expect(only(out, 'tool.start')[0]!.call).toMatchObject({ agentId: A('child'), callId: 'nested', category: 'edit', rawName: 'FileChange', files: ['/project/shared.txt'] });
  expect(writesOf(out)).toEqual([{ agentId: A('child'), path: '/project/shared.txt', op: 'edit' }]);
  expect(kinds(out)).toEqual(['agent.upsert', 'tool.start', 'tool.end']);
});

test('native event for an unknown thread creates a minimal agent', () => {
  const a = setup();
  const out = feed(a, done({ ...patchItem(), cwd: '/elsewhere' }, 'abcdefghijklmnop'));
  expect(upserts(out)).toEqual([{
    id: A('abcdefghijklmnop'), provider: 'codex', name: 'abcdefghijkl', role: 'main', parentId: null, link: 'root',
    cwd: '/elsewhere', projectRoot: null, status: 'idle', lastEventAt: T1,
  }]);
  // A second event for the same thread does not repeat the upsert.
  expect(upserts(feed(a, done(patchItem('other'), 'abcdefghijklmnop')))).toHaveLength(0);
});

test('native events inside a thread log belong to the thread agent, not the log owner', () => {
  const a = setup();
  const out = feed(a, done({ type: 'CommandExecution', id: 'c', status: 'completed', command: 'ls' }, null));
  expect(only(out, 'tool.start')[0]!.call.agentId).toBe(A('main'));
  // Without a thread id and without a known log owner, nothing can be attributed.
  expect(feed(new CodexLogAdapter('mac'), done({ type: 'CommandExecution', id: 'c', status: 'completed' }, null))).toEqual([]);
});

test('native receipts replay once; parallel agents remain distinct', () => {
  const a = setup();
  const out: SourceEvent[] = [];
  for (const thread of ['child-a', 'child-b']) for (let i = 0; i < 2; i++) out.push(...feed(a, done(patchItem(), thread)));
  expect(writesOf(out).map((w) => w.agentId)).toEqual([A('child-a'), A('child-b')]);
  expect(only(out, 'tool.start')).toHaveLength(2);
  expect(only(out, 'tool.end')).toHaveLength(2);
});

test('failed, declined, and incomplete patches never create confirmed writes', () => {
  const a = setup();
  const out: SourceEvent[] = [];
  for (const [i, status] of (['failed', 'declined', 'inProgress', null] as const).entries()) out.push(...feed(a, done(patchItem(`s${i}`, { status }))));
  expect(writesOf(out)).toEqual([]);
  expect(only(out, 'tool.end').map((e) => e.ok)).toEqual([false, false, false, false]);
});

test('native rename retains source and destination; no CWD guess for another thread', () => {
  const a = setup();
  const out = feed(a, done(patchItem('r', { changes: { '/project/old': { type: 'update', move_path: '/project/new' }, relative: { type: 'add' } } }), 'child'));
  expect(writesOf(out).map((w) => [w.path, w.op])).toEqual([['/project/old', 'edit'], ['/project/new', 'move']]);
  // The same relative path resolves for a thread whose own cwd is known.
  const known = feed(a, done(patchItem('r2', { changes: { relative: { type: 'add' } } }), 'main'));
  expect(writesOf(known)).toEqual([{ agentId: A('main'), path: '/project/relative', op: 'create' }]);
  // An item's own cwd wins.
  const own = feed(a, done(patchItem('r3', { cwd: '/own', changes: { relative: { type: 'delete' }, '/abs/x': { type: 'add' } } }), 'child'));
  expect(writesOf(own).map((w) => [w.path, w.op])).toEqual([['/own/relative', 'delete'], ['/abs/x', 'create']]);
});

test('native and direct receipts with the same call ID do not double count', () => {
  const a = setup();
  const out = [
    ...feed(a, patchCall('nested', '*** Update File: shared.txt')),
    ...feed(a, patchOut('nested', 'Success. Updated the following files:\nM shared.txt')),
    ...feed(a, done(patchItem())),
  ];
  expect(only(out, 'tool.start')).toHaveLength(1);
  expect(only(out, 'tool.end')).toHaveLength(1);
  expect(writesOf(out)).toHaveLength(1);
  // Native first, direct second: same result.
  const b = setup();
  const rev = [
    ...feed(b, done(patchItem())),
    ...feed(b, patchCall('nested', '*** Update File: shared.txt')),
    ...feed(b, patchOut('nested', 'Success. Updated the following files:\nM shared.txt')),
  ];
  expect(only(rev, 'tool.start')).toHaveLength(1);
  expect(only(rev, 'tool.end')).toHaveLength(1);
  expect(writesOf(rev)).toHaveLength(1);
});

test('a native receipt closes an open direct call once', () => {
  const a = setup();
  feed(a, patchCall('nested', '*** Update File: shared.txt'));
  const nat = feed(a, done(patchItem()));
  expect(only(nat, 'tool.start')).toHaveLength(0);
  expect(only(nat, 'tool.end')).toHaveLength(1);
  expect(feed(a, patchOut('nested', OK))).toEqual([]);
});

test('native CommandExecution item_completed emits tool.start and tool.end with the same ts and tool.output', () => {
  const a = setup();
  const ms = Date.parse('2026-01-01T00:00:05.000Z');
  const out = feed(a, done({
    type: 'CommandExecution', id: 'cmd', status: 'completed', command: ['sh', '-c', 'echo data > file'], cwd: '/project',
    process_id: '42', exit_code: 0, aggregated_output: 'Success. Updated the following files:\nA file',
  }, 'main', { completed_at_ms: ms }));
  expect(kinds(out)).toEqual(['tool.start', 'tool.output', 'tool.end']);
  const [start] = only(out, 'tool.start');
  const [end] = only(out, 'tool.end');
  expect(start!.ts).toBe('2026-01-01T00:00:05.000Z');
  expect(end!.ts).toBe(start!.ts);
  expect(start!.call).toMatchObject({ agentId: A('main'), callId: 'cmd', category: 'command', rawName: 'CommandExecution', command: 'sh -c echo data > file', startedAt: start!.ts });
  expect(start!.call.files).toBeUndefined();
  expect(only(out, 'tool.output')[0]).toMatchObject({ chunk: 'Success. Updated the following files:\nA file', truncated: false, ts: start!.ts });
  expect(end).toMatchObject({ ok: true, exitCode: 0 });
  // Never infer writes from shell text, stdout or a zero exit.
  expect(end!.writes).toBeUndefined();
  // Without completed_at_ms the record timestamp is used.
  const fb = feed(a, done({ type: 'CommandExecution', id: 'c2', status: 'completed', command: 'ls' }, 'main', {}, T2));
  expect(only(fb, 'tool.start')[0]!.ts).toBe(T2);
  const bad = feed(a, done({ type: 'CommandExecution', id: 'c3', status: 'completed', command: 'ls' }, 'main', { completed_at_ms: Number.NaN }, T2));
  expect(only(bad, 'tool.start')[0]!.ts).toBe(T2);
});

test('CommandExecution ok and exitCode follow status and exit_code', () => {
  const a = setup();
  const end = (id: string, over: Json) => only(feed(a, done({ type: 'CommandExecution', id, command: 'x', ...over })), 'tool.end')[0]!;
  expect(end('a', { status: 'completed', exit_code: 0 })).toMatchObject({ ok: true, exitCode: 0 });
  expect(end('b', { status: 'completed' })).toMatchObject({ ok: true });
  expect(end('b2', { status: 'completed' }).exitCode).toBeUndefined();
  expect(end('c', { status: 'completed', exit_code: 2 })).toMatchObject({ ok: false, exitCode: 2 });
  expect(end('d', { status: 'failed', exit_code: 0 })).toMatchObject({ ok: false, exitCode: 0 });
  expect(end('e', { status: 'declined' })).toMatchObject({ ok: false });
  expect(end('f', { status: 'completed', exit_code: '0' }).exitCode).toBeUndefined();
});

test('native command output and history are bounded', () => {
  const a = setup();
  const out = feed(a, done({ type: 'CommandExecution', id: 'big', status: 'completed', command: ['x'.repeat(5000)], aggregated_output: 'a'.repeat(10000) + 'b'.repeat(10000) + 'END', exit_code: 1 }));
  expect(only(out, 'tool.start')[0]!.call.command).toHaveLength(4096);
  const chunks = only(out, 'tool.output');
  expect(chunks.every((c) => Buffer.byteLength(c.chunk) <= 4096)).toBe(true);
  expect(chunks.map((c) => c.truncated)).toEqual([true, false, false, false]);
  const joined = chunks.map((c) => c.chunk).join('');
  expect(joined).toHaveLength(16384);
  expect(joined.endsWith('END')).toBe(true);
  expect(kinds(out).at(-1)).toBe('tool.end');
  expect(only(out, 'tool.end')[0]).toMatchObject({ ok: false, exitCode: 1 });
  // Empty output emits no chunk; multibyte characters stay whole.
  expect(kinds(feed(a, done({ type: 'CommandExecution', id: 'e', status: 'completed', command: 'true', aggregated_output: '' })))).toEqual(['tool.start', 'tool.end']);
  const wide = '日'.repeat(3000);
  const w = only(feed(a, done({ type: 'CommandExecution', id: 'w', status: 'completed', command: 'x', aggregated_output: wide })), 'tool.output');
  expect(w.every((c) => Buffer.byteLength(c.chunk) <= 4096 && !c.chunk.includes('�'))).toBe(true);
  expect(w.map((c) => c.chunk).join('')).toBe(wide);
});

test('outer exec call is category other; nested CommandExecution is command', () => {
  const a = setup();
  const outer = feed(a, resp({ type: 'custom_tool_call', name: 'exec', call_id: 'outer', input: 'wrapper' }));
  const nested = feed(a, done({ type: 'CommandExecution', id: 'inner', status: 'completed', command: 'ls' }));
  expect(only(outer, 'tool.start')[0]!.call.category).toBe('other');
  expect(only(nested, 'tool.start')[0]!.call.category).toBe('command');
  const end = feed(a, resp({ type: 'custom_tool_call_output', call_id: 'outer', output: 'large wrapper output' }, T2));
  // The outer call is not a command: it never emits tool.output.
  expect(kinds(end)).toEqual(['tool.end']);
});

test('direct shell calls emit command text and output; other direct calls never emit output', () => {
  const a = setup();
  const start = feed(a, resp({ type: 'function_call', name: 'exec_command', call_id: 's', arguments: JSON.stringify({ cmd: 'x'.repeat(5000) }) }));
  expect(only(start, 'tool.start')[0]!.call).toMatchObject({ category: 'command', rawName: 'exec_command' });
  expect(only(start, 'tool.start')[0]!.call.command).toHaveLength(4096);
  const arr = feed(a, resp({ type: 'function_call', name: 'shell', call_id: 's2', arguments: JSON.stringify({ command: ['bash', '-lc', 'ls'] }) }));
  expect(only(arr, 'tool.start')[0]!.call.command).toBe('bash -lc ls');
  const end = feed(a, resp({ type: 'function_call_output', call_id: 's', output: 'hello' }, T2));
  expect(kinds(end)).toEqual(['tool.output', 'tool.end']);
  expect(only(end, 'tool.output')[0]!.chunk).toBe('hello');
  expect(writesOf(end)).toEqual([]);
  // Text-part arrays are accepted.
  const parts = feed(a, resp({ type: 'function_call_output', call_id: 's2', output: [{ type: 'input_text', text: 'one' }, { type: 'input_text', text: 'two' }] }, T2));
  expect(only(parts, 'tool.output')[0]!.chunk).toBe('one\ntwo');
  // Read and search style calls never produce output or writes.
  feed(a, resp({ type: 'function_call', name: 'webSearch', call_id: 'w', arguments: '{}' }));
  const w = feed(a, resp({ type: 'function_call_output', call_id: 'w', output: 'results /project/leak.ts' }, T2));
  expect(kinds(w)).toEqual(['tool.end']);
  expect(writesOf(w)).toEqual([]);
});

test('FileChange never emits tool.output; Mcp and Collab items emit a start and end pair', () => {
  const a = setup();
  const fc = feed(a, done({ ...patchItem('f'), aggregated_output: 'leak' }));
  expect(kinds(fc)).toEqual(['tool.start', 'tool.end']);
  const mcp = feed(a, done({ type: 'McpToolCall', id: 'm', status: 'completed', result: 'secret' }));
  expect(kinds(mcp)).toEqual(['tool.start', 'tool.end']);
  expect(only(mcp, 'tool.start')[0]!.call).toMatchObject({ category: 'other', rawName: 'McpToolCall' });
  expect(only(mcp, 'tool.end')[0]).toMatchObject({ ok: true });
  const collab = feed(a, done({ type: 'CollabAgentToolCall', id: 'k', status: 'failed' }));
  expect(only(collab, 'tool.start')[0]!.call.category).toBe('agent');
  expect(only(collab, 'tool.end')[0]).toMatchObject({ ok: false });
  expect(only(mcp, 'tool.start')[0]!.ts).toBe(only(mcp, 'tool.end')[0]!.ts);
});

test('other native item types are ignored', () => {
  const a = setup();
  for (const type of ['Reasoning', 'AgentMessage', 'UserMessage', 'SubAgentActivity', 'ContextCompaction', 'Extension', 'ImageView', 'WebSearch']) {
    expect(feed(a, done({ type, id: type }))).toEqual([]);
  }
  expect(feed(a, done({ type: 'CommandExecution' }))).toEqual([]);
  expect(feed(a, ev({ type: 'item_completed', thread_id: 'main' }))).toEqual([]);
});

test('task_complete and task_completed emit idle, not finished; turn_aborted emits interrupted', () => {
  const a = setup();
  const started = feed(a, ev({ type: 'task_started' }));
  expect(only(started, 'agent.status')).toEqual([{ kind: 'agent.status', ts: T1, agentId: A('main'), status: 'working' }]);
  // Already working: no repeat.
  expect(only(feed(a, ev({ type: 'task_started' })), 'agent.status')).toHaveLength(0);
  for (const type of ['task_complete', 'task_completed']) {
    feed(a, ev({ type: 'task_started' }));
    expect(only(feed(a, ev({ type }, T2)), 'agent.status')).toEqual([{ kind: 'agent.status', ts: T2, agentId: A('main'), status: 'idle' }]);
  }
  feed(a, ev({ type: 'task_started' }));
  expect(only(feed(a, ev({ type: 'turn_aborted' }, T2)), 'agent.status').map((e) => e.status)).toEqual(['interrupted']);
  // A direct tool call moves the agent back to working.
  const call = feed(a, resp({ type: 'function_call', name: 'shell', call_id: 'w', arguments: '{}' }, T2));
  expect(only(call, 'agent.status').map((e) => e.status)).toEqual(['working']);
});

test('agent.upsert fires only when identity changes; status and lastEventAt do not trigger one', () => {
  const a = new CodexLogAdapter('mac');
  const first = upserts(feed(a, meta('main')));
  expect(first[0]).toMatchObject({ id: A('main'), provider: 'codex', name: 'Codex session', role: 'main', link: 'root', parentId: null, projectRoot: null, status: 'idle', lastEventAt: T1, cwd: '/project' });
  expect(upserts(feed(a, ev({ type: 'task_started' }, T2)))).toHaveLength(0);
  expect(upserts(feed(a, turn('/project', T2))).map((x) => x.model)).toEqual(['m-1']);
  expect(upserts(feed(a, turn('/project', T2)))).toHaveLength(0);
  expect(upserts(feed(a, turn('/next', T2)))[0]).toMatchObject({ cwd: '/next', status: 'working' });
  // Sub-agent names come from the thread_spawn path; guardian reviews are named.
  const sub = upserts(feed(a, { type: 'session_meta', timestamp: T1, payload: { id: 'k', cwd: '/p', parent_thread_id: 'main', source: { subagent: { thread_spawn: { agent_path: 'root/reviewer' } } } } }, '/r/k.jsonl'));
  expect(sub[0]).toMatchObject({ name: 'reviewer', role: 'sub-agent', parentId: A('main'), link: 'confirmed' });
  const guard = upserts(feed(a, { type: 'session_meta', timestamp: T1, payload: { id: 'g', cwd: '/p', thread_source: 'guardian_review' } }, '/r/g.jsonl'));
  expect(guard[0]).toMatchObject({ name: 'Approval review', role: 'main' });
});

test('a minimal agent from a native event is refined by its own session_meta', () => {
  const a = setup();
  feed(a, done(patchItem(), 'kid'));
  const out = feed(a, meta('kid', 'main', '/project', T2), '/r/kid.jsonl');
  expect(upserts(out)[0]).toMatchObject({ id: A('kid'), parentId: A('main'), link: 'confirmed', role: 'sub-agent' });
});

test('records without a timestamp learn identity but emit no events; invalid records emit nothing', () => {
  const a = new CodexLogAdapter('mac');
  const bare = meta('main');
  delete (bare as { timestamp?: string }).timestamp;
  expect(feed(a, bare)).toEqual([]);
  const call = resp({ type: 'function_call', name: 'shell', call_id: 'c', arguments: '{}' });
  delete (call as { timestamp?: string }).timestamp;
  expect(feed(a, call)).toEqual([]);
  expect(upserts(feed(a, turn('/project', T2)))).toHaveLength(1);
  expect(feed(a, resp({ type: 'function_call_output', call_id: 'c', output: 'x' }, T2))).toEqual([]);
  for (const bad of [null, 'x', 7, [], {}, { type: 'response_item' }, { type: 'response_item', payload: 'x', timestamp: T1 }]) {
    expect(a.ingest(bad, '/r/x.jsonl', 'work')).toEqual([]);
  }
  // Records before any session_meta and without a thread id are not attributed.
  expect(new CodexLogAdapter('mac').ingest(resp({ type: 'function_call', name: 'shell', call_id: 'c' }), '/r/y.jsonl', 'work')).toEqual([]);
});

test('call dedupe and pending memory are bounded', () => {
  const a = setup();
  for (let i = 0; i < 5001; i++) feed(a, patchCall(`c${i}`, '*** Add File: a.txt'));
  expect(only(feed(a, patchOut('c0', OK)), 'tool.end')).toHaveLength(0);
  expect(only(feed(a, patchOut('c1', OK)), 'tool.end')).toHaveLength(1);
});

test('fixtures: a main log and a child log replay end to end', () => {
  const a = new CodexLogAdapter('mac');
  const read = (f: string) => fs.readFileSync(path.join(FIXTURES, f), 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  const main = read('main.jsonl').flatMap((r) => a.ingest(r, path.join(FIXTURES, 'main.jsonl'), 'work'));
  const kid = read('child.jsonl').flatMap((r) => a.ingest(r, path.join(FIXTURES, 'child.jsonl'), 'work'));

  expect(upserts(main)[0]).toMatchObject({ id: A('th-main'), cwd: '/p/proj', role: 'main' });
  expect(only(main, 'tool.start').map((e) => [e.call.callId, e.call.category])).toEqual([
    ['c1', 'command'], ['p1', 'edit'], ['outer', 'other'], ['n1', 'command'], ['n2', 'edit'],
  ]);
  expect(only(main, 'tool.output').map((e) => [e.callId, e.chunk])).toEqual([['c1', 'x'], ['n1', 'y\n']]);
  expect(only(main, 'tool.end').map((e) => [e.callId, e.ok])).toEqual([['c1', true], ['p1', true], ['n1', true], ['n2', true], ['outer', true]]);
  expect(writesOf(main).map((w) => [w.path, w.op])).toEqual([['/p/proj/hello.txt', 'create'], ['/p/proj/a.txt', 'edit']]);
  expect(only(main, 'agent.status').map((e) => e.status)).toEqual(['working', 'idle']);

  expect(upserts(kid)[0]).toMatchObject({ id: A('th-kid'), parentId: A('th-main'), link: 'confirmed', name: 'reviewer', role: 'sub-agent' });
  expect(only(kid, 'agent.status').map((e) => e.status)).toEqual(['working', 'interrupted']);
});
