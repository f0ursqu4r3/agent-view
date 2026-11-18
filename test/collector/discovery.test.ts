import { expect, test } from 'bun:test';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { discoverRoots, findProjectRoot, listRecentLogs } from '../../src/collector/discovery';
import type { LogRoot } from '../../src/collector/discovery';

const TMP = path.join(import.meta.dir, '..', '.tmp');

async function withDir(fn: (dir: string) => Promise<void>): Promise<void> {
  await fs.mkdir(TMP, { recursive: true });
  const dir = await fs.mkdtemp(path.join(TMP, 'disc-'));
  try {
    await fn(await fs.realpath(dir));
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
}

const mk = (...p: string[]) => fs.mkdir(path.join(...p), { recursive: true });
const DAY = 24 * 3600 * 1000;

async function touch(file: string, ageMs: number): Promise<void> {
  await fs.mkdir(path.dirname(file), { recursive: true });
  await fs.writeFile(file, '{}\n');
  const t = new Date(Date.now() - ageMs);
  await fs.utimes(file, t, t);
}

test('discoverRoots: CODEX_HOME, ~/.codex-accounts/* (scope = account dir), CLAUDE_CONFIG_DIR; realpath dedupe', async () => {
  await withDir(async (home) => {
    await mk(home, '.codex', 'sessions');
    await mk(home, '.codex-accounts', 'work', 'sessions');
    await mk(home, '.codex-accounts', 'empty'); // no sessions dir: skipped
    await mk(home, '.claude', 'projects');
    expect(await discoverRoots({}, home)).toEqual([
      { provider: 'codex', path: path.join(home, '.codex', 'sessions'), scope: 'default' },
      { provider: 'codex', path: path.join(home, '.codex-accounts', 'work', 'sessions'), scope: 'work' },
      { provider: 'claude', path: path.join(home, '.claude', 'projects'), scope: 'default' },
    ]);

    // Env overrides replace the default locations.
    await mk(home, 'other-codex', 'sessions');
    await mk(home, 'other-claude', 'projects');
    const overridden = await discoverRoots(
      { CODEX_HOME: path.join(home, 'other-codex'), CLAUDE_CONFIG_DIR: path.join(home, 'other-claude') }, home);
    expect(overridden.map((r) => [r.provider, r.path, r.scope])).toEqual([
      ['codex', path.join(home, 'other-codex', 'sessions'), 'default'],
      ['codex', path.join(home, '.codex-accounts', 'work', 'sessions'), 'work'],
      ['claude', path.join(home, 'other-claude', 'projects'), 'default'],
    ]);

    // A symlinked CODEX_HOME that resolves to an account root yields one root, scoped to the account.
    await fs.symlink(path.join(home, '.codex-accounts', 'work'), path.join(home, 'alias'));
    const deduped = await discoverRoots({ CODEX_HOME: path.join(home, 'alias') }, home);
    const codex = deduped.filter((r) => r.provider === 'codex');
    expect(codex).toEqual([{ provider: 'codex', path: path.join(home, '.codex-accounts', 'work', 'sessions'), scope: 'work' }]);
  });
});

test('discoverRoots returns nothing when no root exists', async () => {
  await withDir(async (home) => {
    expect(await discoverRoots({}, home)).toEqual([]);
  });
});

test('listRecentLogs: only .jsonl modified within sinceMs, newest first, quota split equally per provider', async () => {
  await withDir(async (dir) => {
    const codex: LogRoot = { provider: 'codex', path: path.join(dir, 'codex'), scope: 'default' };
    const claude: LogRoot = { provider: 'claude', path: path.join(dir, 'claude'), scope: 'default' };
    await touch(path.join(codex.path, 'c1.jsonl'), 1000);
    await touch(path.join(codex.path, '2026', 'c2.jsonl'), 2000);
    await touch(path.join(codex.path, 'c3.jsonl'), 3000);
    await touch(path.join(codex.path, 'c-old.jsonl'), 2 * DAY);
    await touch(path.join(codex.path, 'notes.txt'), 10);
    for (let i = 0; i < 4; i++) await touch(path.join(claude.path, 'proj', `a${i}.jsonl`), 10_000 + i * 1000);

    const all = await listRecentLogs([codex, claude], DAY, 100);
    expect(all.map((l) => path.basename(l.file))).toEqual(['c1.jsonl', 'c2.jsonl', 'c3.jsonl', 'a0.jsonl', 'a1.jsonl', 'a2.jsonl', 'a3.jsonl']);
    expect(all[0]).toMatchObject({ provider: 'codex', scope: 'default', path: codex.path, file: path.join(codex.path, 'c1.jsonl') });
    expect(typeof all[0]!.mtimeMs).toBe('number');

    // maxLogs 4 across two providers: 2 each, newest first.
    const split = await listRecentLogs([codex, claude], DAY, 4);
    expect(split.map((l) => path.basename(l.file))).toEqual(['c1.jsonl', 'c2.jsonl', 'a0.jsonl', 'a1.jsonl']);

    // A provider with no recent logs does not take a share.
    const solo = await listRecentLogs([codex, claude], 5000, 4);
    expect(solo.map((l) => path.basename(l.file))).toEqual(['c1.jsonl', 'c2.jsonl', 'c3.jsonl']);
  });
});

test('listRecentLogs: depth limit 8 and missing roots are skipped', async () => {
  await withDir(async (dir) => {
    const root: LogRoot = { provider: 'claude', path: path.join(dir, 'r'), scope: 'default' };
    await touch(path.join(root.path, 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'ok.jsonl'), 10); // depth 8
    await touch(path.join(root.path, 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'deep.jsonl'), 10); // depth 9
    const missing: LogRoot = { provider: 'codex', path: path.join(dir, 'nope'), scope: 'default' };
    const logs = await listRecentLogs([missing, root], DAY, 10);
    expect(logs.map((l) => path.basename(l.file))).toEqual(['ok.jsonl']);
  });
});

test('listRecentLogs: reports a root error code through the callback', async () => {
  await withDir(async (dir) => {
    const root: LogRoot = { provider: 'claude', path: path.join(dir, 'file'), scope: 'default' };
    await fs.writeFile(root.path, 'not a directory');
    const results: [string, string | null][] = [];
    await listRecentLogs([root], DAY, 10, (r, code) => results.push([r.path, code]));
    expect(results).toEqual([[root.path, 'ENOTDIR']]);
  });
});

test('findProjectRoot returns cwd when no ancestor has .git', async () => {
  const dir = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), 'av-root-')));
  try {
    const cwd = path.join(dir, 'x', 'y');
    await fs.mkdir(cwd, { recursive: true });
    expect(await findProjectRoot(cwd)).toBe(cwd);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
});

test('findProjectRoot returns nearest .git ancestor, else cwd', async () => {
  await withDir(async (dir) => {
    await mk(dir, 'repo', '.git');
    await mk(dir, 'repo', 'packages', 'app', 'src');
    expect(await findProjectRoot(path.join(dir, 'repo', 'packages', 'app', 'src'))).toBe(path.join(dir, 'repo'));
    expect(await findProjectRoot(path.join(dir, 'repo'))).toBe(path.join(dir, 'repo'));

    // A worktree has a .git file; the nearest one wins over an outer repo.
    await fs.mkdir(path.join(dir, 'repo', 'wt', 'sub'), { recursive: true });
    await fs.writeFile(path.join(dir, 'repo', 'wt', '.git'), 'gitdir: ../.git/worktrees/wt\n');
    expect(await findProjectRoot(path.join(dir, 'repo', 'wt', 'sub'))).toBe(path.join(dir, 'repo', 'wt'));
  });
  expect(await findProjectRoot('/')).toBe('/');
});
