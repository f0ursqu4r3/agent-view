import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import ignore from 'ignore';
import type { Ignore } from 'ignore';
import type { SourceEvent, SourceState } from '../../shared/schema';
import { watchTree } from '../watch';
import type { TreeWatcher, WatchEvent, WatchMode } from '../watch';

/** M0 decision: recursive `fs.watch` for every path class. The per-dir mode stays in `watchTree` as a fallback. */
export const FS_WATCH_MODE: WatchMode = 'recursive';
/** Upper bound on watched directories per root (applies to the per-dir mode only). */
const MAX_DIRS = 12_000;
const DEFAULT_MAX_PER_SECOND = 50;
const WINDOW_MS = 1000;

/** Directory names that are never reported, wherever they appear in a path. */
export const IGNORED_DIRS: ReadonlySet<string> = new Set([
  '.git', '.worktrees', '.pytest_cache', '__pycache__', '.turbo', 'coverage', 'node_modules', '.next',
  '.cache', '.venv', 'venv', 'dist', 'build', '.serena',
  '.superpowers', 'target', 'out',
]);

/** A fixed one-second window per key, starting at the key's first allowed event. */
export class RateLimiter {
  private readonly windows = new Map<string, { start: number; count: number }>();
  constructor(private readonly perSecond: number, private readonly now: () => number = Date.now) {}

  allow(key: string): boolean {
    const t = this.now();
    let w = this.windows.get(key);
    if (!w || t - w.start >= WINDOW_MS) {
      w = { start: t, count: 0 };
      this.windows.set(key, w);
    }
    if (w.count >= this.perSecond) return false;
    w.count++;
    return true;
  }
}

const readText = (file: string): string | null => {
  try { return fs.readFileSync(file, 'utf8'); } catch { return null; }
};

/**
 * Answers "would git ignore this path?" for paths under `root`. Rules come from the root `.gitignore`,
 * `.git/info/exclude` (only when `.git` is a directory) and the `.gitignore` of every directory between
 * the root and the path. Each file applies relative to its own directory. Rules are cached per directory.
 */
export class GitignoreMatcher {
  private readonly cache = new Map<string, Ignore | null>();
  constructor(private readonly root: string) {}

  /** Drop the cached rules for the directory that holds `gitignorePath`. */
  invalidate(gitignorePath: string): void {
    this.cache.delete(path.dirname(gitignorePath));
  }

  isIgnored(absPath: string): boolean {
    const rel = path.relative(this.root, absPath);
    if (!rel || rel.startsWith('..') || path.isAbsolute(rel)) return false;
    const segments = rel.split(path.sep);
    let ignored = false;
    let dir = this.root;
    // Shallow to deep: a deeper file overrides a shallower one, as in git.
    for (let i = 0; i < segments.length; i++) {
      const matcher = this.rulesFor(dir);
      if (matcher) {
        const r = matcher.test(segments.slice(i).join('/'));
        if (r.ignored) ignored = true;
        else if (r.unignored) ignored = false;
      }
      if (i < segments.length - 1) dir = path.join(dir, segments[i]!);
    }
    return ignored;
  }

  private rulesFor(dir: string): Ignore | null {
    if (this.cache.has(dir)) return this.cache.get(dir)!;
    const ig = ignore();
    let any = false;
    const add = (text: string | null) => { if (text !== null) { ig.add(text); any = true; } };
    add(readText(path.join(dir, '.gitignore')));
    if (dir === this.root && isDirectory(path.join(dir, '.git'))) add(readText(path.join(dir, '.git', 'info', 'exclude')));
    const result = any ? ig : null;
    this.cache.set(dir, result);
    return result;
  }
}

const isDirectory = (p: string): boolean => {
  try { return fs.statSync(p).isDirectory(); } catch { return false; }
};

const realOr = (p: string): string => {
  try { return fs.realpathSync(p); } catch { return path.resolve(p); }
};

const isWithin = (child: string, parent: string): boolean =>
  child === parent || child.startsWith(parent.endsWith(path.sep) ? parent : parent + path.sep);

/**
 * macOS reports every write as a watch `rename`. A regular file whose last write is more than a second
 * after its birth was modified, not created. Without a birth time (0 or NaN) the answer stays `create`.
 */
const looksModified = (stat: fs.Stats): boolean =>
  stat.isFile() && stat.birthtimeMs > 0 && stat.mtimeMs - stat.birthtimeMs > 1000;

type Opts = {
  emit: (events: SourceEvent[]) => void;
  onStatus: (source: string, s: SourceState) => void;
  maxPerSecond?: number;
  home?: string;
  now?: () => number;
  /** Replaceable for tests. */
  watch?: typeof watchTree;
};

type Active = { watcher: TreeWatcher };

/**
 * Watches project roots and emits `fs.change` events. These events never carry an agent id:
 * the viewer draws them as unattributed activity.
 */
export class FsChangeSource {
  private readonly active = new Map<string, Active>();
  private readonly limiter: RateLimiter;
  private readonly now: () => number;
  private readonly home: string;
  /** Root names already reported as refused or missing, so a repeated `setRoots` stays quiet. */
  private readonly reported = new Set<string>();
  /** The degraded detail last reported for each root, so a repeated failure is reported once. */
  private readonly degradedSeen = new Map<string, string>();
  private _dropped = 0;

  constructor(private readonly opts: Opts) {
    this.now = opts.now ?? Date.now;
    this.limiter = new RateLimiter(opts.maxPerSecond ?? DEFAULT_MAX_PER_SECOND, this.now);
    this.home = realOr(opts.home ?? os.homedir());
  }

  get dropped(): number { return this._dropped; }
  get roots(): string[] { return [...this.active.keys()]; }

  setRoots(roots: string[]): void {
    // A watcher that has failed delivers no events. Report it, close it and drop it here; a later call restarts it.
    const failed = new Set<string>();
    for (const [root, a] of this.active) {
      if (a.watcher.degraded === null) continue;
      this.reportIfDegraded(root);
      a.watcher.close();
      this.active.delete(root);
      failed.add(root);
    }
    const wanted = this.resolve(roots);
    for (const [root, a] of this.active) {
      if (!wanted.includes(root)) { a.watcher.close(); this.active.delete(root); }
    }
    for (const root of wanted) if (!this.active.has(root) && !failed.has(root)) this.start(root);
  }

  stop(): void {
    for (const a of this.active.values()) a.watcher.close();
    this.active.clear();
  }

  /** Real paths of the roots to watch: refuse broad roots, skip missing ones, drop nested ones. */
  private resolve(roots: string[]): string[] {
    const seen = new Set(roots);
    for (const key of [...this.reported]) if (!seen.has(key)) this.reported.delete(key);
    const report = (root: string, detail: string) => {
      if (this.reported.has(root)) return;
      this.reported.add(root);
      this.opts.onStatus('fs:' + root, { state: 'degraded', detail });
    };
    const real = new Set<string>();
    for (const root of roots) {
      let rp: string;
      try { rp = fs.realpathSync(root); } catch { report(root, 'cannot watch: root not found'); continue; }
      if (!isDirectory(rp)) { report(root, 'cannot watch: not a directory'); continue; }
      if (rp === path.parse(rp).root || isWithin(this.home, rp)) { report(root, 'refused: too broad'); continue; }
      this.reported.delete(root);
      real.add(rp);
    }
    // Shortest first, so a parent is kept and the roots nested in it are skipped.
    const kept: string[] = [];
    for (const rp of [...real].sort((a, b) => a.length - b.length || a.localeCompare(b))) {
      if (!kept.some((k) => isWithin(rp, k))) kept.push(rp);
    }
    return kept;
  }

  private start(root: string): void {
    const matcher = new GitignoreMatcher(root);
    const ignored = (rel: string): boolean => {
      const abs = path.join(root, rel);
      const base = path.basename(rel);
      if (base === '.gitignore') matcher.invalidate(abs);
      else if (rel === path.join('.git', 'info', 'exclude')) matcher.invalidate(path.join(root, '.gitignore'));
      return rel.split(path.sep).some((s) => IGNORED_DIRS.has(s)) || matcher.isIgnored(abs);
    };
    const watcher = (this.opts.watch ?? watchTree)(root, (rel, event) => this.onChange(root, rel, event), FS_WATCH_MODE, { maxDirs: MAX_DIRS, ignore: ignored });
    this.active.set(root, { watcher });
    if (!this.reportIfDegraded(root)) {
      this.degradedSeen.delete(root);
      this.opts.onStatus('fs:' + root, { state: 'ok' });
    }
  }

  private onChange(root: string, rel: string, event: WatchEvent): void {
    const abs = path.join(root, rel);
    let stat: fs.Stats | null = null;
    try { stat = fs.lstatSync(abs); } catch { /* gone */ }
    if (stat?.isDirectory()) return;
    const op = event === 'change' ? 'change' : !stat ? 'delete' : looksModified(stat) ? 'change' : 'create';
    if (!this.limiter.allow(root)) { this._dropped++; return; }
    this.opts.emit([{ kind: 'fs.change', path: abs, op, ts: new Date(this.now()).toISOString() }]);
    this.reportIfDegraded(root);
  }

  /** Report a watcher's degraded reason once. Returns true when the watcher is degraded. */
  private reportIfDegraded(root: string): boolean {
    const a = this.active.get(root);
    const detail = a?.watcher.degraded ?? null;
    if (!a || detail === null) return false;
    if (this.degradedSeen.get(root) !== detail) {
      this.degradedSeen.set(root, detail);
      this.opts.onStatus('fs:' + root, { state: 'degraded', detail });
    }
    return true;
  }
}
