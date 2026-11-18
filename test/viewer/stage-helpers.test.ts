import { expect, test } from 'bun:test';
import { activityText, agentColor, soleChild } from '../../viewer/stage/agents-view';
import { hostOrder } from '../../viewer/stage/stage';
import { TreeModel } from '../../viewer/stage/tree-model';
import { fileColor, findNear } from '../../viewer/stage/tree-view';
import type { ViewAgent, ViewCall } from '../../viewer/store';

const agent = (over: Partial<ViewAgent> = {}): ViewAgent => ({
  id: 'a', provider: 'claude', name: 'Lead', role: 'main', parentId: null, link: 'root', cwd: '/p', projectRoot: '/p',
  status: 'working', lastEventAt: '', host: 'h', stale: false, lastEventMs: 0, ...over,
});
const call = (over: Partial<ViewCall>): ViewCall => ({
  agentId: 'a', callId: 'c', startedAt: '', category: 'read', rawName: 'Read', host: 'h', startMs: 0, endMs: null,
  ok: null, output: '', outputTruncated: false, ...over,
});

test('agent colors: orange tones for Claude Code, blue for Codex, index 0 for a main agent and lighter tones for sub-agents', () => {
  expect(agentColor(agent(), 0)).toBe('#ff8a4c');
  expect(agentColor(agent({ provider: 'codex' }), 0)).toBe('#36c6ff');
  const sub = agent({ role: 'sub-agent', parentId: 'p' });
  expect(agentColor(sub, 0)).toBe('#ffad78');
  expect(agentColor(sub, 1)).toBe('#ffcda1');
  expect(agentColor(sub, 2)).toBe('#ffad78');
  expect(agentColor({ ...sub, provider: 'codex' }, 1)).toBe('#b0ecff');
});

test('activity label: the open call, else the lifecycle state', () => {
  expect(activityText(agent(), call({ files: ['/p/src/a.ts'] }))).toBe('read a.ts');
  expect(activityText(agent(), call({ category: 'edit', rawName: 'Edit', files: ['/p/b.ts'] }))).toBe('edit b.ts');
  expect(activityText(agent(), call({ category: 'command', rawName: 'Bash', command: 'bun test' }))).toBe('$ bun test');
  expect(activityText(agent(), call({ category: 'search', rawName: 'Grep', pattern: 'TODO' }))).toBe('search "TODO"');
  expect(activityText(agent(), call({ category: 'other', rawName: 'WebFetch' }))).toBe('WebFetch');
  expect(activityText(agent(), call({ category: 'command', command: 'x'.repeat(80) })).length).toBe(48);
  expect(activityText(agent({ status: 'idle' }), undefined)).toBe('idle');
  expect(activityText(agent({ stale: true }), undefined)).toBe('stale');
});

test('hosts keep the hub order, then hosts seen only in the tree or on an agent', () => {
  const tree = new TreeModel();
  tree.touch({ host: 'tree-only', path: '/x/a.ts', projectRoot: null, attribution: 'unattributed', atMs: 1 });
  expect(hostOrder(
    [{ label: 'local', kind: 'local', state: 'up' }, { label: 'remote', kind: 'remote', state: 'down' }],
    tree.hosts(),
    [agent({ host: 'agent-only' }), agent({ host: 'local' })],
  )).toEqual(['local', 'remote', 'tree-only', 'agent-only']);
});

test('findNear returns the named node, else its nearest ancestor, never the host root', () => {
  const tree = new TreeModel();
  tree.touch({ host: 'h', path: '/p/src/a.ts', projectRoot: '/p', attribution: 'confirmed', agentId: 'a', atMs: 1 });
  expect(findNear(tree, 'h', '/p/src/a.ts')?.name).toBe('a.ts');
  expect(findNear(tree, 'h', '/p/src/deep/missing')?.name).toBe('src');
  expect(findNear(tree, 'h', '/p/')?.kind).toBe('project');
  expect(findNear(tree, 'h', '/elsewhere/x')).toBeUndefined();
});

test('file colors follow the extension', () => {
  expect(fileColor('a.ts')).toBe(0x4f8cff);
  expect(fileColor('README.MD')).toBe(0xe9e9f2);
  expect(fileColor('Makefile')).toBe(0xaab0c0);
});

test('Minor 3: the spawn link points to the only new child; none or several candidates give no link', () => {
  const kid = (parentId: string | null, firstMs: number, goneMs: number | null = null) => ({ parentId, firstMs, goneMs });
  const only = kid('p', 1000);
  expect(soleChild([only, kid('other', 1000), kid('p', -5000)], 'p', 1000)).toBe(only);
  // Parallel Agent calls: two candidates, so the link would be a guess.
  expect(soleChild([kid('p', 1000), kid('p', 1200)], 'p', 1000)).toBeNull();
  expect(soleChild([kid('p', -5000)], 'p', 1000)).toBeNull();
  expect(soleChild([kid('p', 1000, 1500)], 'p', 1000)).toBeNull();
});
