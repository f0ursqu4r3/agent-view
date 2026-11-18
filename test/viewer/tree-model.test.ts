import { expect, test } from 'bun:test';
import { TreeModel, type TreeNode } from '../../viewer/stage/tree-model';
import type { Touch } from '../../viewer/store';

const T0 = 1_000_000;
const conf = (path: string, over: Partial<Touch> = {}): Touch =>
  ({ host: 'h', path, projectRoot: '/p', attribution: 'confirmed', agentId: 'a', atMs: T0, ...over });
const unat = (path: string, over: Partial<Touch> = {}): Touch =>
  ({ host: 'h', path, projectRoot: null, attribution: 'unattributed', atMs: T0, ...over });
const chain = (n: TreeNode | undefined): string[] => {
  const out: string[] = [];
  for (let c = n ?? null; c; c = c.parent) out.unshift(`${c.kind}:${c.name}`);
  return out;
};
const leaves = (n: TreeNode): TreeNode[] => (n.kind === 'file' ? [n] : n.children.flatMap(leaves));

test('confirmed touch creates host → project → dirs → file chain', () => {
  const m = new TreeModel();
  const f = m.touch(conf('/p/src/a/x.ts', { atMs: T0 + 5 }))!;
  expect(chain(f)).toEqual(['host:h', 'project:p', 'dir:src', 'dir:a', 'file:x.ts']);
  expect(f.id).toBe('h|/p/src/a/x.ts');
  expect(f.depth).toBe(4);
  expect(f.bornAt).toBe(T0 + 5);
  expect(f.parent!.id).toBe('h|/p/src/a');
  expect(f.parent!.parent!.parent!.id).toBe('h|/p');
  expect(m.hosts().map((h) => h.id)).toEqual(['h|']);
  expect(m.hosts()[0]!.depth).toBe(0);
  expect(m.find('h', '/p/src/a/x.ts')).toBe(f);
  expect(m.find('h', '/p')!.kind).toBe('project');
  expect(m.find('h', '/p/src')!.kind).toBe('dir');
  expect(m.find('h', '/nope')).toBeUndefined();
  // a repeat touch returns the same node and refreshes the file and its ancestors
  const again = m.touch(conf('/p/src/a/x.ts', { atMs: T0 + 50 }))!;
  expect(again).toBe(f);
  expect(f.lastTouchAt).toBe(T0 + 50);
  expect(f.parent!.lastTouchAt).toBe(T0 + 50);
  expect(m.find('h', '/p')!.lastTouchAt).toBe(T0 + 50);
  expect(f.bornAt).toBe(T0 + 5);
  // an older touch never lowers lastTouchAt
  m.touch(conf('/p/src/a/x.ts', { atMs: T0 }));
  expect(f.lastTouchAt).toBe(T0 + 50);
});

test('confirmed touch outside its projectRoot or with a null root goes under other', () => {
  const m = new TreeModel();
  const a = m.touch(conf('/etc/hosts'))!;
  expect(chain(a)).toEqual(['host:h', 'other:other', 'dir:/etc', 'file:hosts']);
  expect(a.parent!.parent!.id).toBe('h|<other>');
  expect(a.depth).toBe(3);
  const b = m.touch(conf('/p/x.ts', { projectRoot: null }))!;
  expect(chain(b)).toEqual(['host:h', 'other:other', 'dir:/p', 'file:x.ts']);
  const c = m.touch(conf('/pp/x.ts'))!; // not segment-aware prefix of /p
  expect(chain(c)).toEqual(['host:h', 'other:other', 'dir:/pp', 'file:x.ts']);
  // one dir node per parent path, deep paths stay bounded
  const d = m.touch(conf('/var/log/deep/er/z.log', { projectRoot: null }))!;
  expect(chain(d)).toEqual(['host:h', 'other:other', 'dir:/var/log/deep/er', 'file:z.log']);
  const e = m.touch(conf('/etc/passwd'))!;
  expect(e.parent).toBe(a.parent);
});

test('unattributed touch maps to the longest known project prefix; unknown → other', () => {
  const m = new TreeModel();
  expect(chain(m.touch(unat('/p/early.ts'))!)).toEqual(['host:h', 'other:other', 'dir:/p', 'file:early.ts']);
  m.touch(conf('/p/seed.ts'));
  m.touch(conf('/p/sub/seed.ts', { projectRoot: '/p/sub' }));
  m.touch(conf('/q/seed.ts', { projectRoot: '/q', host: 'other-host' }));
  expect(chain(m.touch(unat('/p/lib/u.ts'))!)).toEqual(['host:h', 'project:p', 'dir:lib', 'file:u.ts']);
  expect(chain(m.touch(unat('/p/sub/deep/u.ts'))!)).toEqual(['host:h', 'project:sub', 'dir:deep', 'file:u.ts']);
  expect(chain(m.touch(unat('/pp/u.ts'))!)).toEqual(['host:h', 'other:other', 'dir:/pp', 'file:u.ts']);
  // projects are per host
  expect(chain(m.touch(unat('/q/u.ts'))!)).toEqual(['host:h', 'other:other', 'dir:/q', 'file:u.ts']);
  expect(chain(m.touch(unat('/q/u.ts', { host: 'other-host' }))!)).toEqual(['host:other-host', 'project:q', 'file:u.ts']);
});

test('unattributed new-node cap: 21st new file in the same second returns null; confirmed never capped', () => {
  const m = new TreeModel();
  m.touch(conf('/p/seed.ts'));
  for (let i = 0; i < 20; i++) expect(m.touch(unat(`/p/d/f${i}.ts`, { atMs: T0 + i }))).not.toBeNull();
  const before = leaves(m.hosts()[0]!).length;
  expect(m.touch(unat('/p/d/f20.ts', { atMs: T0 + 500 }))).toBeNull();
  expect(m.find('h', '/p/d/f20.ts')).toBeUndefined();
  expect(leaves(m.hosts()[0]!).length).toBe(before);
  // existing nodes are never capped and get refreshed
  const ex = m.touch(unat('/p/d/f3.ts', { atMs: T0 + 600 }))!;
  expect(ex.lastTouchAt).toBe(T0 + 600);
  // confirmed is never capped
  for (let i = 0; i < 30; i++) expect(m.touch(conf(`/p/c/f${i}.ts`, { atMs: T0 + 700 }))).not.toBeNull();
  // another project and the other node have their own budget
  m.touch(conf('/r/seed.ts', { projectRoot: '/r' }));
  expect(m.touch(unat('/r/x.ts', { atMs: T0 + 700 }))).not.toBeNull();
  expect(m.touch(unat('/zzz/x.ts', { atMs: T0 + 700 }))).not.toBeNull();
  // a second later the window has moved on
  expect(m.touch(unat('/p/d/f20.ts', { atMs: T0 + 1100 }))).not.toBeNull();
});

test('other node has its own 20 per second cap', () => {
  const m = new TreeModel({ newNodesPerSecond: 3 });
  for (let i = 0; i < 3; i++) expect(m.touch(unat(`/x/f${i}`, { atMs: T0 }))).not.toBeNull();
  expect(m.touch(unat('/x/f3', { atMs: T0 }))).toBeNull();
  expect(m.find('h', '/x')).toBeUndefined();
});

test('prune removes files untouched 30 min and empty parents, returns removed nodes', () => {
  const m = new TreeModel();
  const old = m.touch(conf('/p/a/b/old.ts', { atMs: T0 }))!;
  const keep = m.touch(conf('/p/a/keep.ts', { atMs: T0 + 60_000 }))!;
  const other = m.touch(conf('/etc/hosts', { atMs: T0 }))!;
  expect(m.prune(T0 + 1_799_999)).toEqual([]);
  const removed = m.prune(T0 + 1_800_000);
  expect(new Set(removed.map((n) => n.id))).toEqual(
    new Set([old.id, 'h|/p/a/b', other.id, other.parent!.id, 'h|<other>']),
  );
  expect(m.find('h', '/p/a/b/old.ts')).toBeUndefined();
  expect(m.find('h', '/p/a/b')).toBeUndefined();
  expect(m.find('h', '/p/a/keep.ts')).toBe(keep);
  expect(keep.parent!.children).toEqual([keep]);
  // everything goes eventually, the host stays
  const rest = m.prune(T0 + 60_000 + 1_800_000);
  expect(rest.map((n) => n.id).sort()).toEqual(['h|/p', 'h|/p/a', 'h|/p/a/keep.ts']);
  expect(m.hosts().length).toBe(1);
  expect(m.hosts()[0]!.children).toEqual([]);
  // a touch refreshes the lifetime
  const m2 = new TreeModel({ fileTtlMs: 1000 });
  m2.touch(conf('/p/x.ts', { atMs: T0 }));
  m2.touch(conf('/p/x.ts', { atMs: T0 + 900 }));
  expect(m2.prune(T0 + 1500)).toEqual([]);
  expect(m2.prune(T0 + 1900).length).toBe(2);
});

test('layout converges ang to tgt and keeps leaf order stable when a leaf is added', () => {
  const m = new TreeModel();
  const names = ['b', 'd', 'f'];
  for (const n of names) m.touch(conf(`/p/${n}.ts`));
  m.layout(0);
  const f = (n: string) => m.find('h', `/p/${n}.ts`)!;
  const tgtOf = (i: number, cnt: number) => -Math.PI / 2 + ((i + 0.5) / cnt) * 2 * Math.PI;
  names.forEach((n, i) => {
    expect(f(n).tgt).toBeCloseTo(tgtOf(i, 3), 10);
    expect(f(n).ang).toBeCloseTo(tgtOf(i, 3), 10); // new leaves start at their target
  });
  // add a leaf in the middle, order of siblings stays, others slide
  m.touch(conf('/p/c.ts', { atMs: T0 + 1 }));
  m.layout(0);
  expect(f('c').ang).toBeCloseTo(tgtOf(1, 4), 10);
  ['b', 'c', 'd', 'f'].forEach((n, i) => expect(f(n).tgt).toBeCloseTo(tgtOf(i, 4), 10));
  const order = ['b', 'c', 'd', 'f'].map((n) => f(n).tgt);
  expect([...order].sort((a, b) => a - b)).toEqual(order);
  expect(f('d').ang).toBeCloseTo(tgtOf(1, 3), 10); // not yet moved
  const gap = f('d').tgt - f('d').ang;
  m.layout(0.1);
  const k = 1 - Math.exp(-0.3);
  expect(f('d').ang).toBeCloseTo(tgtOf(1, 3) + gap * k, 10);
  for (let i = 0; i < 200; i++) m.layout(0.1);
  for (const n of names) expect(f(n).ang).toBeCloseTo(f(n).tgt, 6);
  // containers: mean of leaf values
  const p = m.find('h', '/p')!;
  const ls = leaves(p);
  expect(p.ang).toBeCloseTo(ls.reduce((s, l) => s + l.ang, 0) / ls.length, 10);
  expect(p.tgt).toBeCloseTo(ls.reduce((s, l) => s + l.tgt, 0) / ls.length, 10);
});

test('layout is depth-first within a host and independent between hosts', () => {
  const m = new TreeModel();
  m.touch(conf('/p/z/1.ts'));
  m.touch(conf('/p/a/2.ts'));
  m.touch(conf('/p/a/1.ts'));
  m.touch(conf('/p/0.ts'));
  m.touch(conf('/p/x.ts', { host: 'g' }));
  m.layout(0);
  const order = ['/p/0.ts', '/p/a/1.ts', '/p/a/2.ts', '/p/z/1.ts'].map((p) => m.find('h', p)!.tgt);
  expect(order).toEqual([...order].sort((a, b) => a - b));
  expect(m.find('h', '/p/0.ts')!.tgt).toBeCloseTo(-Math.PI / 2 + (0.5 / 4) * 2 * Math.PI, 10);
  expect(m.find('g', '/p/x.ts')!.tgt).toBeCloseTo(-Math.PI / 2 + Math.PI, 10);
  // pruning re-spaces the remaining leaves
  m.touch(conf('/p/0.ts', { atMs: T0 + 1_800_000 }));
  m.prune(T0 + 1_800_000);
  m.layout(0);
  expect(m.find('h', '/p/0.ts')!.tgt).toBeCloseTo(-Math.PI / 2 + Math.PI, 10);
});

test('same path on two hosts gives two nodes', () => {
  const m = new TreeModel();
  const a = m.touch(conf('/p/x.ts', { host: 'h1' }))!;
  const b = m.touch(conf('/p/x.ts', { host: 'h2' }))!;
  expect(a).not.toBe(b);
  expect(a.id).toBe('h1|/p/x.ts');
  expect(b.id).toBe('h2|/p/x.ts');
  expect(m.find('h1', '/p/x.ts')).toBe(a);
  expect(m.find('h2', '/p/x.ts')).toBe(b);
  expect(m.hosts().map((h) => h.id)).toEqual(['h1|', 'h2|']);
});

test('nested project roots keep a unique id for a dir that is also a project', () => {
  const m = new TreeModel();
  m.touch(conf('/p/sub/own.ts', { projectRoot: '/p/sub' }));
  const f = m.touch(conf('/p/sub/x.ts'))!; // confirmed under the outer project
  expect(chain(f)).toEqual(['host:h', 'project:p', 'dir:sub', 'file:x.ts']);
  expect(f.parent!.id).not.toBe('h|/p/sub');
  expect(m.find('h', '/p/sub')!.kind).toBe('project');
  expect(m.find('h', '/p/sub/own.ts')!.parent).toBe(m.find('h', '/p/sub')!);
});
