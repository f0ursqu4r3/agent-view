import type { Touch } from '../store';

export type NodeKind = 'host' | 'project' | 'dir' | 'file' | 'other';
export type TreeNode = {
  id: string; name: string; kind: NodeKind; depth: number;
  parent: TreeNode | null; children: TreeNode[];
  bornAt: number; lastTouchAt: number; ang: number; tgt: number;
};

const DEFAULT_TTL_MS = 30 * 60 * 1000;
const DEFAULT_NEW_PER_SECOND = 20;
const CAP_WINDOW_MS = 1000;
const EASE_RATE = 3;
const OTHER = '<other>';

/** Cached layout order for one host. Containers come after their children (post-order). */
type HostLayout = {
  /** Every node under the host, children before parents. */
  order: TreeNode[];
  /** Index of each node's parent in `order`, or -1 for the host. */
  parentIdx: Int32Array;
  isLeaf: Uint8Array;
  sumAng: Float64Array;
  sumTgt: Float64Array;
  count: Float64Array;
};

type HostState = { root: TreeNode; projects: Set<string>; layout: HostLayout | null };

const trimPath = (p: string): string => (p.length > 1 ? p.replace(/\/+$/, '') || '/' : p);
const segments = (p: string): string[] => p.split('/').filter((s) => s.length > 0);
const isUnder = (root: string, path: string): boolean =>
  root === '/' ? path.startsWith('/') : path === root || path.startsWith(`${root}/`);
const lastSegment = (p: string): string => segments(p).at(-1) ?? '/';
const parentPath = (p: string): string => {
  const i = p.lastIndexOf('/');
  return i <= 0 ? '/' : p.slice(0, i);
};

/** Insert keeping children sorted by name; equal names keep insertion order. */
function insertSorted(list: TreeNode[], n: TreeNode): void {
  let lo = 0;
  let hi = list.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (list[mid]!.name <= n.name) lo = mid + 1;
    else hi = mid;
  }
  list.splice(lo, 0, n);
}

/** The growing radial file tree: pure data, no rendering. The stage maps `depth` and `ang` to pixels. */
export class TreeModel {
  private readonly fileTtlMs: number;
  private readonly newPerSecond: number;
  private readonly hostStates = new Map<string, HostState>();
  private readonly nodes = new Map<string, TreeNode>();
  /** Creation times of recent unattributed file nodes per project (or host `other`) id. */
  private readonly creations = new Map<string, number[]>();
  /** Leaves not yet placed by `layout`. They start at their target. */
  private readonly fresh = new Set<TreeNode>();
  /** The path of each project node. */
  private readonly roots = new Map<TreeNode, string>();
  /** Containers removed by moves, reported by the next `prune`. */
  private queued: TreeNode[] = [];
  /** The latest time seen in a touch or a `prune`. */
  private latestMs = Number.NEGATIVE_INFINITY;

  constructor(opts: { fileTtlMs?: number; newNodesPerSecond?: number } = {}) {
    this.fileTtlMs = opts.fileTtlMs ?? DEFAULT_TTL_MS;
    this.newPerSecond = opts.newNodesPerSecond ?? DEFAULT_NEW_PER_SECOND;
  }

  hosts(): TreeNode[] {
    return [...this.hostStates.values()].map((h) => h.root);
  }

  find(host: string, path: string): TreeNode | undefined {
    return this.nodes.get(`${host}|${trimPath(path)}`);
  }

  /** Places a touch. A touch already past the file lifetime (against the latest known time) places nothing. */
  touch(t: Touch): TreeNode | null {
    if (this.latestMs - t.atMs >= this.fileTtlMs) return null;
    if (t.atMs > this.latestMs) this.latestMs = t.atMs;
    const path = trimPath(t.path);
    const hs = this.hostState(t.host);
    if (t.attribution === 'confirmed' && t.projectRoot !== null) this.learnRoot(hs, t.host, trimPath(t.projectRoot), t.atMs);
    // Placement is the same for every touch: the longest known project root, else `other`.
    const root = this.longestProject(hs, path);
    if (root !== null && path === root) return this.refresh(this.ensureProject(hs, t.host, root, t.atMs), t.atMs);
    const existing = this.nodes.get(`${t.host}|${path}`);
    if (existing) {
      if (existing.kind === 'file' && this.rootOf(existing) !== root) {
        this.unlink(existing);
        this.cleanupEmpty(existing.parent);
        this.attach(hs, t.host, path, root, t.atMs, existing);
      }
      return this.refresh(existing, t.atMs);
    }
    if (t.attribution === 'unattributed' && !this.takeSlot(`${t.host}|${root ?? OTHER}`, t.atMs)) return null;
    return this.refresh(this.attach(hs, t.host, path, root, t.atMs), t.atMs);
  }

  prune(now: number): TreeNode[] {
    if (now > this.latestMs) this.latestMs = now;
    const removed: TreeNode[] = [];
    for (const n of this.nodes.values()) {
      if (n.kind === 'file' && now - n.lastTouchAt >= this.fileTtlMs) removed.push(n);
    }
    const parents = new Set(removed.map((n) => n.parent));
    for (const n of removed) this.detach(n);
    for (const p of parents) this.cleanupEmpty(p);
    for (const n of [...this.nodes.values()]) {
      if (n.kind !== 'host' && n.kind !== 'file' && n.children.length === 0 && this.nodes.get(n.id) === n) this.cleanupEmpty(n);
    }
    removed.push(...this.queued);
    this.queued = [];
    return removed;
  }

  layout(dt: number): void {
    const k = 1 - Math.exp(-EASE_RATE * dt);
    for (const hs of this.hostStates.values()) {
      const L = (hs.layout ??= this.buildLayout(hs));
      const { order, parentIdx, isLeaf, sumAng, sumTgt, count } = L;
      sumAng.fill(0);
      sumTgt.fill(0);
      count.fill(0);
      for (let i = 0; i < order.length; i++) {
        const n = order[i]!;
        if (isLeaf[i]) {
          n.ang += (n.tgt - n.ang) * k;
          sumAng[i] = n.ang;
          sumTgt[i] = n.tgt;
          count[i] = 1;
        } else if (count[i]! > 0) {
          n.ang = sumAng[i]! / count[i]!;
          n.tgt = sumTgt[i]! / count[i]!;
        }
        const p = parentIdx[i]!;
        if (p >= 0) {
          sumAng[p] = sumAng[p]! + sumAng[i]!;
          sumTgt[p] = sumTgt[p]! + sumTgt[i]!;
          count[p] = count[p]! + count[i]!;
        }
      }
    }
  }

  // --- internals ---

  private hostState(host: string): HostState {
    let hs = this.hostStates.get(host);
    if (!hs) {
      const root: TreeNode = {
        id: `${host}|`, name: host, kind: 'host', depth: 0, parent: null, children: [],
        bornAt: 0, lastTouchAt: 0, ang: 0, tgt: 0,
      };
      hs = { root, projects: new Set(), layout: null };
      this.hostStates.set(host, hs);
      this.nodes.set(root.id, root);
    }
    return hs;
  }

  private longestProject(hs: HostState, path: string): string | null {
    let best: string | null = null;
    for (const r of hs.projects) {
      if (isUnder(r, path) && (best === null || r.length > best.length)) best = r;
    }
    return best;
  }

  private ensureProject(hs: HostState, host: string, root: string, atMs: number): TreeNode {
    const n = this.child(hs.root, `${host}|${root}`, lastSegment(root), 'project', atMs);
    this.roots.set(n, root);
    return n;
  }

  /** The project root a node sits under, or null under `other`. */
  private rootOf(n: TreeNode): string | null {
    for (let c = n.parent; c; c = c.parent) {
      if (c.kind === 'project') return this.roots.get(c) ?? null;
      if (c.kind === 'other') return null;
    }
    return null;
  }

  /** Build the container chain for `path` and add the file (a new node, or `file` when it moves). */
  private attach(hs: HostState, host: string, path: string, root: string | null, atMs: number, file?: TreeNode): TreeNode {
    let parent: TreeNode;
    let leafName: string;
    if (root === null) {
      const other = this.child(hs.root, `${host}|${OTHER}`, 'other', 'other', atMs);
      const dir = parentPath(path);
      parent = this.child(other, `${host}|${OTHER}${dir}`, dir, 'dir', atMs);
      leafName = lastSegment(path);
    } else {
      parent = this.ensureProject(hs, host, root, atMs);
      const rest = segments(path.slice(root === '/' ? 0 : root.length));
      let acc = root === '/' ? '' : root;
      for (let i = 0; i < rest.length - 1; i++) {
        acc += `/${rest[i]}`;
        parent = this.child(parent, `${host}|${acc}`, rest[i]!, 'dir', atMs);
      }
      leafName = rest.at(-1)!;
    }
    if (!file) return this.child(parent, `${host}|${path}`, leafName, 'file', atMs);
    file.parent = parent;
    file.depth = parent.depth + 1;
    insertSorted(parent.children, file);
    this.invalidate(file);
    return file;
  }

  /** Register a project root. Files of the host whose longest root changed move under it. */
  private learnRoot(hs: HostState, host: string, root: string, atMs: number): void {
    if (hs.projects.has(root)) return;
    hs.projects.add(root);
    const moving: { n: TreeNode; path: string; root: string | null }[] = [];
    const walk = (n: TreeNode): void => {
      if (n.kind === 'file') {
        const path = n.id.slice(host.length + 1);
        const want = this.longestProject(hs, path);
        if (want !== path && want !== this.rootOf(n)) moving.push({ n, path, root: want });
      }
      for (const c of n.children) walk(c);
    };
    walk(hs.root);
    // Unlink all first, so an emptied directory frees its id before a project takes it.
    for (const m of moving) {
      this.unlink(m.n);
      this.cleanupEmpty(m.n.parent);
    }
    for (const m of moving) {
      this.attach(hs, host, m.path, m.root, atMs, m.n);
      this.refresh(m.n, m.n.lastTouchAt);
    }
  }

  /** Remove `n` and then each ancestor left without children. The host stays. */
  private cleanupEmpty(n: TreeNode | null): void {
    while (n && n.kind !== 'host' && n.kind !== 'file' && n.children.length === 0 && this.nodes.get(n.id) === n) {
      const up: TreeNode | null = n.parent;
      this.detach(n);
      this.queued.push(n);
      n = up;
    }
  }

  /** Fixed budget over a sliding one-second window of the touches' own clock. */
  private takeSlot(key: string, atMs: number): boolean {
    let times = this.creations.get(key);
    if (!times) this.creations.set(key, (times = []));
    const live = times.filter((x) => x > atMs - CAP_WINDOW_MS && x <= atMs + CAP_WINDOW_MS);
    if (live.length >= this.newPerSecond) {
      this.creations.set(key, live);
      return false;
    }
    live.push(atMs);
    this.creations.set(key, live);
    return true;
  }

  /**
   * Find or create a child. When the wanted id is taken by a node elsewhere (a project that is
   * also a directory of an outer project), the id gets the parent's id appended to stay unique.
   */
  private child(parent: TreeNode, baseId: string, name: string, kind: NodeKind, atMs: number): TreeNode {
    let id = baseId;
    let existing = this.nodes.get(id);
    if (existing && existing.parent !== parent) {
      id = `${baseId}@${parent.id}`;
      existing = this.nodes.get(id);
    }
    if (existing) return existing;
    const n: TreeNode = {
      id, name, kind, depth: parent.depth + 1, parent, children: [],
      bornAt: atMs, lastTouchAt: atMs, ang: 0, tgt: 0,
    };
    this.nodes.set(id, n);
    insertSorted(parent.children, n);
    if (kind === 'file') this.fresh.add(n);
    this.invalidate(n);
    return n;
  }

  private refresh(n: TreeNode, atMs: number): TreeNode {
    for (let c: TreeNode | null = n; c; c = c.parent) {
      if (atMs > c.lastTouchAt) c.lastTouchAt = atMs;
    }
    return n;
  }

  private detach(n: TreeNode): void {
    this.nodes.delete(n.id);
    this.fresh.delete(n);
    this.roots.delete(n);
    this.unlink(n);
  }

  /** Take `n` out of its parent's children. It keeps its `parent` until it is attached again. */
  private unlink(n: TreeNode): void {
    if (!n.parent) return;
    const i = n.parent.children.indexOf(n);
    if (i >= 0) n.parent.children.splice(i, 1);
    this.invalidate(n.parent);
  }

  private hostOf(n: TreeNode): HostState | undefined {
    let c = n;
    while (c.parent) c = c.parent;
    return this.hostStates.get(c.name);
  }

  private invalidate(n: TreeNode): void {
    const hs = this.hostOf(n);
    if (hs) hs.layout = null;
  }

  /** Rebuild the cached order, assign leaf targets, and place fresh leaves at their targets. */
  private buildLayout(hs: HostState): HostLayout {
    const order: TreeNode[] = [];
    const parentIdx: number[] = [];
    const walk = (n: TreeNode, parentSlot: number): number => {
      // Children first, then the node. Its own slot is known only afterwards.
      const childSlots: number[] = [];
      for (const c of n.children) childSlots.push(walk(c, -2));
      const slot = order.length;
      order.push(n);
      parentIdx.push(parentSlot);
      for (const cs of childSlots) parentIdx[cs] = slot;
      return slot;
    };
    walk(hs.root, -1);
    const isLeaf = new Uint8Array(order.length);
    let leaves = 0;
    order.forEach((n, i) => {
      if (n.kind === 'file') {
        isLeaf[i] = 1;
        leaves++;
      }
    });
    let li = 0;
    // Leaf order is depth-first, which is the same relative order as the post-order walk.
    for (let i = 0; i < order.length; i++) {
      if (!isLeaf[i]) continue;
      const n = order[i]!;
      n.tgt = -Math.PI / 2 + ((li + 0.5) / leaves) * 2 * Math.PI;
      li++;
      if (this.fresh.delete(n)) n.ang = n.tgt;
    }
    return {
      order, parentIdx: Int32Array.from(parentIdx), isLeaf,
      sumAng: new Float64Array(order.length), sumTgt: new Float64Array(order.length),
      count: new Float64Array(order.length),
    };
  }
}
