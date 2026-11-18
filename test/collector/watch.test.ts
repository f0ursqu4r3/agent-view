import { expect, test } from 'bun:test';
import fs from 'node:fs/promises';
import path from 'node:path';
import { watchTree } from '../../src/collector/watch';

const TMP = path.join(import.meta.dir, '..', '.tmp');

async function withDir(fn: (dir: string) => Promise<void>): Promise<void> {
  await fs.mkdir(TMP, { recursive: true });
  const dir = await fs.mkdtemp(path.join(TMP, 'watch-'));
  try {
    await fn(dir);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
}

/** Collect callbacks and wait for one that matches. */
function recorder() {
  const seen: string[] = [];
  const waiters: { match: (rel: string) => boolean; done: () => void }[] = [];
  return {
    seen,
    onChange: (rel: string) => {
      seen.push(rel);
      for (const w of [...waiters]) if (w.match(rel)) { waiters.splice(waiters.indexOf(w), 1); w.done(); }
    },
    wait(match: (rel: string) => boolean, ms = 3000): Promise<void> {
      if (seen.some(match)) return Promise.resolve();
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error(`no matching change; saw ${JSON.stringify(seen)}`)), ms);
        waiters.push({ match, done: () => { clearTimeout(timer); resolve(); } });
      });
    },
  };
}

test('recursive mode reports a nested file as a relative path', async () => {
  await withDir(async (dir) => {
    await fs.mkdir(path.join(dir, 'a', 'b'), { recursive: true });
    const r = recorder();
    const w = watchTree(dir, r.onChange, 'recursive');
    try {
      expect(w.degraded).toBeNull();
      await fs.writeFile(path.join(dir, 'a', 'b', 'x.txt'), 'x');
      await r.wait((rel) => rel === path.join('a', 'b', 'x.txt'));
    } finally {
      w.close();
    }
  });
});

test('per-dir mode watches nested and newly created directories', async () => {
  await withDir(async (dir) => {
    await fs.mkdir(path.join(dir, 'a', 'b'), { recursive: true });
    const r = recorder();
    const w = watchTree(dir, r.onChange, 'per-dir');
    try {
      expect(w.degraded).toBeNull();
      await fs.writeFile(path.join(dir, 'a', 'b', 'x.txt'), 'x');
      await r.wait((rel) => rel === path.join('a', 'b', 'x.txt'));
      await fs.mkdir(path.join(dir, 'c', 'd'), { recursive: true });
      await fs.writeFile(path.join(dir, 'c', 'd', 'y.txt'), 'y');
      await r.wait((rel) => rel === path.join('c', 'd', 'y.txt'));
    } finally {
      w.close();
    }
  });
});

test('per-dir mode skips ignored and symlinked directories', async () => {
  await withDir(async (dir) => {
    const outside = path.join(dir, 'outside');
    const root = path.join(dir, 'root');
    await fs.mkdir(path.join(root, 'node_modules'), { recursive: true });
    await fs.mkdir(path.join(root, 'src'), { recursive: true });
    await fs.mkdir(outside);
    await fs.symlink(outside, path.join(root, 'link'));
    const r = recorder();
    const w = watchTree(root, r.onChange, 'per-dir', { ignore: (rel) => rel.split(path.sep).includes('node_modules') });
    try {
      await fs.writeFile(path.join(root, 'node_modules', 'ignored.txt'), 'x');
      await fs.writeFile(path.join(outside, 'linked.txt'), 'x');
      await fs.writeFile(path.join(root, 'src', 'canary.txt'), 'x');
      await r.wait((rel) => rel === path.join('src', 'canary.txt'));
      await Bun.sleep(150);
      expect(r.seen.filter((rel) => rel.includes('ignored') || rel.includes('linked') || rel.startsWith('link' + path.sep))).toEqual([]);
    } finally {
      w.close();
    }
  });
});

test('per-dir mode stops at maxDirs and reports degraded', async () => {
  await withDir(async (dir) => {
    for (const name of ['a', 'b', 'c', 'd']) await fs.mkdir(path.join(dir, name));
    const w = watchTree(dir, () => {}, 'per-dir', { maxDirs: 3 });
    try {
      expect(w.degraded).toMatch(/3/);
    } finally {
      w.close();
    }
  });
});

test('a missing root sets degraded and does not throw, in both modes', async () => {
  await withDir(async (dir) => {
    for (const mode of ['recursive', 'per-dir'] as const) {
      const w = watchTree(path.join(dir, 'missing'), () => {}, mode);
      expect(w.degraded).toBeTruthy();
      w.close();
    }
  });
});

test('close stops callbacks', async () => {
  await withDir(async (dir) => {
    for (const mode of ['recursive', 'per-dir'] as const) {
      const r = recorder();
      const w = watchTree(dir, r.onChange, mode);
      w.close();
      await fs.writeFile(path.join(dir, `${mode}.txt`), 'x');
      await Bun.sleep(200);
      expect(r.seen).toEqual([]);
    }
  });
});
