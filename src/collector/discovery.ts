import fsp from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import type { Provider } from '../shared/schema';

export type LogRoot = { provider: Provider; path: string; scope: string };
export type RecentLog = LogRoot & { file: string; mtimeMs: number };
/** Called once per root with null when the scan worked, or the first non-ENOENT error code. */
export type RootResult = (root: LogRoot, code: string | null) => void;

const MAX_DEPTH = 8;

/**
 * Find the log roots that exist: Codex `sessions` (CODEX_HOME or ~/.codex), one per account under
 * ~/.codex-accounts, and Claude `projects` (CLAUDE_CONFIG_DIR or ~/.claude). Roots are deduplicated by realpath.
 */
export async function discoverRoots(
  env: Record<string, string | undefined> = process.env,
  home: string = os.homedir(),
): Promise<LogRoot[]> {
  const roots: LogRoot[] = [];
  const seen = new Set<string>();
  const add = async (provider: Provider, folder: string, scope: string): Promise<void> => {
    let real: string;
    try { real = await fsp.realpath(folder); } catch { return; }
    if (seen.has(real)) return;
    seen.add(real);
    const inAccount = real.includes(`${path.sep}.codex-accounts${path.sep}`);
    roots.push({ provider, path: real, scope: inAccount ? path.basename(path.dirname(real)) : scope });
  };
  await add('codex', path.join(env.CODEX_HOME || path.join(home, '.codex'), 'sessions'), 'default');
  const accounts = path.join(home, '.codex-accounts');
  const entries = await fsp.readdir(accounts, { withFileTypes: true }).catch(() => []);
  for (const entry of entries) {
    if (entry.isDirectory()) await add('codex', path.join(accounts, entry.name, 'sessions'), entry.name);
  }
  await add('claude', path.join(env.CLAUDE_CONFIG_DIR || path.join(home, '.claude'), 'projects'), 'default');
  return roots;
}

/** Every `.jsonl` under each root modified within `sinceMs`, newest first. Reports each root's outcome. */
export async function scanLogs(roots: LogRoot[], sinceMs: number, onRoot?: RootResult): Promise<RecentLog[]> {
  const cutoff = Date.now() - sinceMs;
  const found: RecentLog[] = [];
  for (const root of roots) {
    let code: string | null = null;
    let missing = false;
    const visit = async (dir: string, depth: number): Promise<void> => {
      if (depth > MAX_DEPTH) return;
      let entries;
      try {
        entries = await fsp.readdir(dir, { withFileTypes: true });
      } catch (e) {
        const c = (e as NodeJS.ErrnoException).code ?? 'UNKNOWN';
        if (c === 'ENOENT') { if (depth === 0) missing = true; return; }
        code ??= c;
        return;
      }
      await Promise.all(entries.map(async (entry) => {
        const file = path.join(dir, entry.name);
        if (entry.isDirectory()) return visit(file, depth + 1);
        if (!entry.isFile() || !entry.name.endsWith('.jsonl')) return;
        const stat = await fsp.stat(file).catch(() => null);
        if (stat && stat.mtimeMs >= cutoff) found.push({ ...root, file, mtimeMs: stat.mtimeMs });
      }));
    };
    await visit(root.path, 0);
    if (!missing) onRoot?.(root, code);
  }
  return found.sort((a, b) => b.mtimeMs - a.mtimeMs);
}

/** Keep the newest logs, `floor(maxLogs / providers)` per provider that has logs. Input is newest first. */
export function selectLogs(all: RecentLog[], maxLogs: number): RecentLog[] {
  const providers = new Set(all.map((l) => l.provider));
  const quota = Math.floor(maxLogs / Math.max(providers.size, 1));
  const taken = new Map<Provider, number>();
  return all.filter((l) => {
    const n = taken.get(l.provider) ?? 0;
    if (n >= quota) return false;
    taken.set(l.provider, n + 1);
    return true;
  });
}

export async function listRecentLogs(roots: LogRoot[], sinceMs: number, maxLogs: number, onRoot?: RootResult): Promise<RecentLog[]> {
  return selectLogs(await scanLogs(roots, sinceMs, onRoot), maxLogs);
}

/** The nearest ancestor of `cwd` (including `cwd`) that holds a `.git` entry, else `cwd`. */
export async function findProjectRoot(cwd: string): Promise<string> {
  let dir = path.resolve(cwd);
  for (;;) {
    if (await fsp.lstat(path.join(dir, '.git')).then(() => true, () => false)) return dir;
    const parent = path.dirname(dir);
    if (parent === dir) return cwd;
    dir = parent;
  }
}
