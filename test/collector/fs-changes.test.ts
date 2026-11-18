import { expect, test } from 'bun:test';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import type { SourceEvent, SourceState } from '../../src/shared/schema';
import { FsChangeSource, GitignoreMatcher, IGNORED_DIRS, RateLimiter } from '../../src/collector/sources/fs-changes';

const TMP = path.join(import.meta.dir, '..', '.tmp');

async function withDir(fn: (dir: string) => Promise<void>): Promise<void> {
  await fs.mkdir(TMP, { recursive: true });
  const dir = await fs.realpath(await fs.mkdtemp(path.join(TMP, 'fsc-')));
  try {
    await fn(dir);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
}

function harness(extra: Partial<ConstructorParameters<typeof FsChangeSource>[0]> = {}) {
  const events: SourceEvent[] = [];
  const statuses: { source: string; s: SourceState }[] = [];
  const waiters: { match: (e: SourceEvent) => boolean; done: () => void }[] = [];
  const source = new FsChangeSource({
    emit: (batch) => {
      for (const e of batch) {
        events.push(e);
        for (const w of [...waiters]) if (w.match(e)) { waiters.splice(waiters.indexOf(w), 1); w.done(); }
      }
    },
    onStatus: (source, s) => statuses.push({ source, s }),
    ...extra,
  });
  const wait = (match: (e: SourceEvent) => boolean, ms = 4000): Promise<void> => {
    if (events.some(match)) return Promise.resolve();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`no matching event; saw ${JSON.stringify(events)}`)), ms);
      waiters.push({ match, done: () => { clearTimeout(timer); resolve(); } });
    });
  };
  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
  return { source, events, statuses, wait, sleep };
}

const isFs = (p: string) => (e: SourceEvent) => e.kind === 'fs.change' && e.path === p;

test('a write under a watched root emits fs.change create with no agentId', async () => {
  await withDir(async (dir) => {
    const h = harness({ now: () => Date.parse('2026-11-17T10:00:00.000Z') });
    h.source.setRoots([dir]);
    try {
      await fs.mkdir(path.join(dir, 'src'));
      await fs.writeFile(path.join(dir, 'src', 'a.ts'), 'x');
      await h.wait((e) => e.kind === 'fs.change' && e.path === path.join(dir, 'src', 'a.ts') && e.op === 'create');
      const e = h.events.find(isFs(path.join(dir, 'src', 'a.ts')))!;
      expect(e).toEqual({ kind: 'fs.change', path: path.join(dir, 'src', 'a.ts'), op: 'create', ts: '2026-11-17T10:00:00.000Z' });
      expect('agentId' in e).toBe(false);
      // The directory itself is never emitted.
      expect(h.events.some(isFs(path.join(dir, 'src')))).toBe(false);
      // A later write is reported again (macOS reports it as a rename, Linux as a change); a removal is a delete.
      await h.sleep(300);
      const before = h.events.length;
      await fs.appendFile(path.join(dir, 'src', 'a.ts'), 'y');
      await h.wait(() => h.events.length > before);
      expect(h.events.slice(before).every((e) => e.kind === 'fs.change' && (e.op === 'create' || e.op === 'change'))).toBe(true);
      await fs.rm(path.join(dir, 'src', 'a.ts'));
      await h.wait((e) => e.kind === 'fs.change' && e.op === 'delete');
    } finally {
      h.source.stop();
    }
  });
});

test('node_modules, .git and .gitignored paths emit nothing', async () => {
  await withDir(async (dir) => {
    await fs.mkdir(path.join(dir, 'node_modules'));
    await fs.mkdir(path.join(dir, '.git'));
    await fs.mkdir(path.join(dir, 'gen'));
    await fs.writeFile(path.join(dir, '.gitignore'), 'gen/\n*.log\n');
    const h = harness();
    h.source.setRoots([dir]);
    try {
      await fs.writeFile(path.join(dir, 'node_modules', 'x.js'), '1');
      await fs.writeFile(path.join(dir, '.git', 'HEAD'), '1');
      await fs.writeFile(path.join(dir, 'gen', 'out.ts'), '1');
      await fs.writeFile(path.join(dir, 'debug.log'), '1');
      await fs.writeFile(path.join(dir, 'keep.ts'), '1');
      await h.wait(isFs(path.join(dir, 'keep.ts')));
      await h.sleep(150);
      const paths = h.events.map((e) => (e.kind === 'fs.change' ? path.relative(dir, e.path) : e.kind));
      expect(paths.some((p) => p.startsWith('node_modules') || p.startsWith('.git' + path.sep) || p.startsWith('gen') || p === 'debug.log')).toBe(false);
    } finally {
      h.source.stop();
    }
  });
});

test('RateLimiter allows 50 per second per key and counts the rest as dropped', () => {
  let t = 0;
  const limiter = new RateLimiter(50, () => t);
  let allowed = 0;
  for (let i = 0; i < 60; i++) if (limiter.allow('a')) allowed++;
  expect(allowed).toBe(50);
  // Another key has its own window.
  expect(limiter.allow('b')).toBe(true);
  t = 999;
  expect(limiter.allow('a')).toBe(false);
  t = 1001;
  expect(limiter.allow('a')).toBe(true);
});

test('FsChangeSource counts events past the limit as dropped', async () => {
  await withDir(async (dir) => {
    const h = harness({ maxPerSecond: 3, now: () => 0 });
    h.source.setRoots([dir]);
    try {
      for (let i = 0; i < 8; i++) await fs.writeFile(path.join(dir, `f${i}.txt`), '1');
      for (let i = 0; i < 100 && h.source.dropped < 5; i++) await h.sleep(40);
      await h.sleep(150);
      expect(h.events.length).toBe(3);
      expect(h.source.dropped).toBeGreaterThanOrEqual(5);
    } finally {
      h.source.stop();
    }
  });
});

test('setRoots refuses home, its ancestors and /, and replaces watchers on diff', async () => {
  await withDir(async (dir) => {
    const home = path.join(dir, 'home');
    const a = path.join(dir, 'home', 'a');
    const b = path.join(dir, 'home', 'b');
    await fs.mkdir(a, { recursive: true });
    await fs.mkdir(b);
    const h = harness({ home });
    try {
      h.source.setRoots([home, '/', dir, a]);
      expect(h.source.roots).toEqual([a]);
      const refused = h.statuses.filter((x) => x.s.detail === 'refused: too broad');
      expect(refused.map((x) => x.source).sort()).toEqual([`fs:${home}`, 'fs:/', `fs:${dir}`].sort());
      expect(refused.every((x) => x.s.state === 'degraded')).toBe(true);
      // Each refused root is reported once, however often setRoots repeats.
      h.source.setRoots([home, '/', dir, a]);
      expect(h.statuses.filter((x) => x.s.detail === 'refused: too broad').length).toBe(3);

      // Diff: a is closed, b is started.
      h.source.setRoots([b]);
      expect(h.source.roots).toEqual([b]);
      await fs.writeFile(path.join(a, 'old.txt'), '1');
      await fs.writeFile(path.join(b, 'new.txt'), '1');
      await h.wait(isFs(path.join(b, 'new.txt')));
      await h.sleep(100);
      expect(h.events.some(isFs(path.join(a, 'old.txt')))).toBe(false);

      // A root nested in another watched root is skipped.
      h.source.setRoots([b, path.join(b, 'sub')]);
      expect(h.source.roots).toEqual([b]);
    } finally {
      h.source.stop();
    }
    expect(h.source.roots).toEqual([]);
  });
});

test('a missing root is reported degraded and starts once it exists', async () => {
  await withDir(async (dir) => {
    const root = path.join(dir, 'later');
    const h = harness();
    try {
      h.source.setRoots([root]);
      expect(h.source.roots).toEqual([]);
      expect(h.statuses.some((x) => x.source === `fs:${root}` && x.s.state === 'degraded')).toBe(true);
      await fs.mkdir(root);
      h.source.setRoots([root]);
      expect(h.source.roots).toEqual([root]);
    } finally {
      h.source.stop();
    }
  });
});

test('IGNORED_DIRS holds the prototype list plus .superpowers, target and out', () => {
  for (const d of ['.git', 'node_modules', '.worktrees', 'dist', 'build', '.serena', '.superpowers', 'target', 'out']) {
    expect(IGNORED_DIRS.has(d)).toBe(true);
  }
});

test('GitignoreMatcher reads root, nested and info/exclude rules and refreshes on invalidate', async () => {
  await withDir(async (dir) => {
    await fs.mkdir(path.join(dir, '.git', 'info'), { recursive: true });
    await fs.mkdir(path.join(dir, 'pkg', 'inner'), { recursive: true });
    await fs.writeFile(path.join(dir, '.gitignore'), '*.tmp\nroot-only.txt\n');
    await fs.writeFile(path.join(dir, '.git', 'info', 'exclude'), 'excluded.txt\n');
    await fs.writeFile(path.join(dir, 'pkg', '.gitignore'), 'local.txt\n/anchored.txt\n!keep.tmp\n');
    const m = new GitignoreMatcher(dir);
    expect(m.isIgnored(path.join(dir, 'a.tmp'))).toBe(true);
    expect(m.isIgnored(path.join(dir, 'pkg', 'inner', 'b.tmp'))).toBe(true);
    expect(m.isIgnored(path.join(dir, 'excluded.txt'))).toBe(true);
    expect(m.isIgnored(path.join(dir, 'a.ts'))).toBe(false);
    // Nested rules apply relative to their own directory.
    expect(m.isIgnored(path.join(dir, 'pkg', 'inner', 'local.txt'))).toBe(true);
    expect(m.isIgnored(path.join(dir, 'local.txt'))).toBe(false);
    expect(m.isIgnored(path.join(dir, 'pkg', 'anchored.txt'))).toBe(true);
    expect(m.isIgnored(path.join(dir, 'pkg', 'inner', 'anchored.txt'))).toBe(false);
    expect(m.isIgnored(path.join(dir, 'pkg', 'keep.tmp'))).toBe(false);
    // Paths outside the root are not ignored.
    expect(m.isIgnored(path.join(path.dirname(dir), 'x.tmp'))).toBe(false);

    // Cached until the .gitignore changes.
    await fs.writeFile(path.join(dir, '.gitignore'), '*.tmp\nroot-only.txt\nnew.txt\n');
    expect(m.isIgnored(path.join(dir, 'new.txt'))).toBe(false);
    m.invalidate(path.join(dir, '.gitignore'));
    expect(m.isIgnored(path.join(dir, 'new.txt'))).toBe(true);
  });
});

test('GitignoreMatcher ignores .git/info/exclude when .git is a file', async () => {
  await withDir(async (dir) => {
    await fs.writeFile(path.join(dir, '.git'), 'gitdir: /elsewhere\n');
    const m = new GitignoreMatcher(dir);
    expect(m.isIgnored(path.join(dir, 'a.ts'))).toBe(false);
  });
});

test('a .gitignore edit takes effect for later events', async () => {
  await withDir(async (dir) => {
    await fs.writeFile(path.join(dir, '.gitignore'), 'a.txt\n');
    const h = harness();
    h.source.setRoots([dir]);
    try {
      await fs.writeFile(path.join(dir, '.gitignore'), 'a.txt\nb.txt\n');
      await h.wait(isFs(path.join(dir, '.gitignore')));
      await fs.writeFile(path.join(dir, 'b.txt'), '1');
      await fs.writeFile(path.join(dir, 'c.txt'), '1');
      await h.wait(isFs(path.join(dir, 'c.txt')));
      await h.sleep(100);
      expect(h.events.some(isFs(path.join(dir, 'b.txt')))).toBe(false);
    } finally {
      h.source.stop();
    }
  });
});

test('default home is os.homedir()', async () => {
  const h = harness();
  try {
    h.source.setRoots([os.homedir()]);
    expect(h.source.roots).toEqual([]);
    expect(h.statuses.some((x) => x.s.detail === 'refused: too broad')).toBe(true);
  } finally {
    h.source.stop();
  }
});
