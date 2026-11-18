import fs from 'node:fs';
import path from 'node:path';

export type WatchEvent = 'rename' | 'change';
export type WatchMode = 'recursive' | 'per-dir';
export type TreeWatcher = { close(): void; readonly degraded: string | null };

const DEFAULT_MAX_DIRS = 12_000;

const reason = (e: unknown): string => {
  const err = e as NodeJS.ErrnoException;
  return err?.code ?? err?.message ?? String(e);
};

/**
 * Watch a directory tree and report each change as a path relative to `root`.
 *
 * - `recursive`: one `fs.watch(root, { recursive: true })`.
 * - `per-dir`: one non-recursive watcher per directory, found by a bounded scan. New directories
 *   are watched as they appear. Symlinked directories and ignored paths are skipped.
 *
 * Failures never throw. They set `degraded` to a reason and the watcher keeps what it has.
 */
export function watchTree(
  root: string,
  onChange: (relPath: string, event: WatchEvent) => void,
  mode: WatchMode,
  opts: { maxDirs?: number; ignore?: (relPath: string) => boolean } = {},
): TreeWatcher {
  const ignore = opts.ignore ?? (() => false);
  const maxDirs = opts.maxDirs ?? DEFAULT_MAX_DIRS;
  const watchers = new Map<string, fs.FSWatcher>();
  let degraded: string | null = null;
  let closed = false;

  const degrade = (why: string) => { degraded ??= why; };
  const emit = (rel: string, event: WatchEvent) => {
    if (closed || ignore(rel)) return;
    try { onChange(rel, event); } catch { /* a consumer error must not break the watcher */ }
  };
  const open = (target: string, options: fs.WatchOptions, listener: fs.WatchListener<string>): fs.FSWatcher | null => {
    try {
      const w = fs.watch(target, { ...options, encoding: 'utf8' }, listener);
      w.on('error', (e) => { degrade(`watcher error on ${target}: ${reason(e)}`); watchers.get(target)?.close(); watchers.delete(target); });
      return w;
    } catch (e) {
      degrade(`cannot watch ${target}: ${reason(e)}`);
      return null;
    }
  };

  if (mode === 'recursive') {
    const w = open(root, { recursive: true }, (event, filename) => {
      if (filename) emit(filename, event as WatchEvent);
    });
    if (w) watchers.set(root, w);
  } else {
    /** Watch `dir` and every directory below it. Returns the entries found when `report` is set. */
    const addTree = (dir: string, report: boolean): void => {
      const rel = path.relative(root, dir);
      if (rel && ignore(rel)) return;
      if (watchers.has(dir)) return;
      if (watchers.size >= maxDirs) { degrade(`more than ${maxDirs} directories; later directories are not watched`); return; }
      const w = open(dir, {}, (event, filename) => {
        if (!filename) return;
        const child = path.join(dir, filename);
        const childRel = path.relative(root, child);
        if (event === 'rename') onRename(child);
        emit(childRel, event as WatchEvent);
      });
      if (!w) return;
      watchers.set(dir, w);
      let entries: fs.Dirent[];
      try { entries = fs.readdirSync(dir, { withFileTypes: true }); } catch (e) { degrade(`cannot read ${dir}: ${reason(e)}`); return; }
      for (const entry of entries) {
        const child = path.join(dir, entry.name);
        if (report) emit(path.relative(root, child), 'rename');
        if (entry.isDirectory()) addTree(child, report);
      }
    };
    const onRename = (child: string): void => {
      let stat: fs.Stats | null = null;
      try { stat = fs.lstatSync(child); } catch { /* gone */ }
      if (stat?.isDirectory()) { addTree(child, true); return; }
      if (stat) return;
      // Removed: close the watcher for it and for anything below it.
      for (const [dir, w] of watchers) {
        if (dir === child || dir.startsWith(child + path.sep)) { w.close(); watchers.delete(dir); }
      }
    };
    addTree(root, false);
  }

  return {
    close() {
      closed = true;
      for (const w of watchers.values()) w.close();
      watchers.clear();
    },
    get degraded() { return degraded; },
  };
}
