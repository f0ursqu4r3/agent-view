import type { Touch } from '../store';
import type { LineBatch, SpriteBatch } from './batch';
import type { LabelPool } from './labels';
import { TEX_SIZE } from './textures';
import type { TreeModel, TreeNode } from './tree-model';

export type Pt = { x: number; y: number };
/** Pixel geometry of one host's tree for the current frame. */
export type HostGeom = { cx: number; cy: number; step: number; sx: number; maxDepth: number; dirs: number; leaves: number };

type Meta = {
  x: number; y: number; px: number; py: number; frame: number;
  r: number; color: number; alpha: number;
  /** Last confirmed touch (file glow) and last unattributed touch (grey flicker). */
  litAt: number; greyAt: number; confirmed: boolean; fading: boolean;
};
type Fade = { at: number; x: number; y: number; px: number; py: number; r: number; color: number; alpha: number };

export type TreeLayers = {
  edges: LineBatch;
  dots: SpriteBatch;
  glows: SpriteBatch;
  names: LabelPool<TreeNode | string>;
  files: LabelPool<TreeNode>;
};

/** A node grows out from its parent over this long. */
const GROW_MS = 800;
/** M0 culling: a file label exists only while its node was touched in the last 3 s. */
const LABEL_MS = 3000;
const LABEL_FADE_MS = 600;
const LIT_MS = 2600;
const GREY_MS = 1200;
const FADE_MS = 600;
/** Folders are labeled always while a host has at most this many; above it they follow the file rule. */
const DIR_LABELS_ALWAYS = 60;
const EDGE = 0x6e82af;
const GREY = 0x8a92a3;
const EXT: Record<string, number> = {
  ts: 0x4f8cff, mts: 0x4f8cff, cts: 0x4f8cff, tsx: 0x2fd4c8, jsx: 0x2fd4c8, js: 0xe8d46a, mjs: 0xe8d46a, cjs: 0xe8d46a,
  md: 0xe9e9f2, json: 0xffcf5a, css: 0xc58bff, html: 0xff8f6b, py: 0x6fb3ff, rs: 0xff9a6b, go: 0x6fdcff,
  swift: 0xff7a59, sh: 0x9be37d, yml: 0xf2a6ff, yaml: 0xf2a6ff, toml: 0xf2a6ff,
};
const DEFAULT_FILE = 0xaab0c0;

const ease = (p: number): number => (p <= 0 ? 0 : p >= 1 ? 1 : 1 - (1 - p) ** 3);
const clamp = (v: number, lo: number, hi: number): number => (v < lo ? lo : v > hi ? hi : v);

export function fileColor(name: string): number {
  const dot = name.lastIndexOf('.');
  return dot > 0 ? EXT[name.slice(dot + 1).toLowerCase()] ?? DEFAULT_FILE : DEFAULT_FILE;
}

/** The node for `path`, else its nearest ancestor on the tree, never the host root. */
export function findNear(tree: TreeModel, host: string, path: string): TreeNode | undefined {
  let p = path.length > 1 ? path.replace(/\/+$/, '') : path;
  for (let i = 0; i < 64 && p.length > 0; i++) {
    const n = tree.find(host, p);
    if (n && n.kind !== 'host') return n;
    if (p === '/') return undefined;
    const cut = p.lastIndexOf('/');
    p = cut <= 0 ? '/' : p.slice(0, cut);
  }
  return undefined;
}

/** Draws the growing radial trees, one per host, and knows where every node is this frame. */
export class TreeView {
  private readonly meta = new WeakMap<TreeNode, Meta>();
  private readonly geoms = new Map<string, HostGeom>();
  private fades: Fade[] = [];
  private visited: TreeNode[] = [];
  private prevVisited: TreeNode[] = [];
  private frame = 0;

  constructor(private readonly l: TreeLayers) {}

  geom(host: string): HostGeom | undefined {
    return this.geoms.get(host);
  }

  /** Where `n` is drawn this frame, or null when it was not drawn. */
  pos(n: TreeNode | undefined): Pt | null {
    if (!n) return null;
    const m = this.meta.get(n);
    return m && m.frame === this.frame ? m : null;
  }

  noteTouch(n: TreeNode, t: Touch): void {
    const m = this.metaOf(n);
    if (t.attribution === 'confirmed') {
      m.confirmed = true;
      if (t.atMs > m.litAt) m.litAt = t.atMs;
    } else if (t.atMs > m.greyAt) {
      m.greyAt = t.atMs;
    }
  }

  /** Removed nodes fade out where they were last drawn. Keyed by object: a new node may reuse an id. */
  fade(nodes: TreeNode[], now: number): void {
    for (const n of nodes) {
      const m = this.meta.get(n);
      if (m) this.startFade(m, now);
    }
  }

  draw(now: number, dt: number, width: number, height: number, hosts: string[], roots: Map<string, TreeNode>, reduced: boolean): void {
    this.frame++;
    const frame = this.frame;
    const { edges, dots, glows, names, files } = this.l;
    const k = reduced ? 1 : 1 - Math.exp(-3 * dt);
    const n = Math.max(1, hosts.length);
    const regionW = width / n;
    const cy = height / 2 + 6;
    const sx = clamp((regionW / height) * 0.7, 1, 2.2);
    const radius = Math.max(40, Math.min(height * 0.42, (regionW * 0.46) / sx));
    const dotScale = 2 / TEX_SIZE.dot;
    const glowScale = 1 / TEX_SIZE.glow;
    const visited = this.visited;
    visited.length = 0;

    for (let i = 0; i < hosts.length; i++) {
      const host = hosts[i]!;
      const cx = (i + 0.5) * regionW;
      const prev = this.geoms.get(host);
      const target = radius / Math.max(2, prev?.maxDepth ?? 2);
      const step = prev ? prev.step + (target - prev.step) * k : target;
      const g: HostGeom = { cx, cy, step, sx, maxDepth: 1, dirs: 0, leaves: 0 };
      const fileR = clamp(((2 * Math.PI * radius) / Math.max(1, prev?.leaves ?? 1)) * 0.35, 1.4, 4.2);
      const dirLabels = (prev?.dirs ?? 0) <= DIR_LABELS_ALWAYS;

      dots.put(cx, cy, 5 * dotScale, 5 * dotScale, 0xcfd8ee, 1);
      names.show(host, host, cx, cy - 10, 0xcfd8ee, 1, 0.5, 1);
      const root = roots.get(host);
      if (root) {
        const rm = this.metaOf(root);
        rm.x = rm.px = cx;
        rm.y = rm.py = cy;
        rm.frame = frame;
      }

      const visit = (parent: TreeNode, px: number, py: number): void => {
        for (const c of parent.children) {
          const m = this.metaOf(c);
          const breathe = reduced ? 1 : 1 + 0.02 * Math.sin(now / 1500 + c.depth * 1.7);
          const rad = c.depth * step * breathe;
          const ox = cx + Math.cos(c.ang) * rad * sx;
          const oy = cy + Math.sin(c.ang) * rad;
          const grow = reduced ? 1 : ease((now - c.bornAt) / GROW_MS);
          const x = px + (ox - px) * grow;
          const y = py + (oy - py) * grow;
          m.x = x; m.y = y; m.px = px; m.py = py; m.frame = frame;
          visited.push(c);
          if (c.depth > g.maxDepth) g.maxDepth = c.depth;
          edges.line(px, py, x, y, c.depth === 1 ? 1.8 : 1.2, EDGE, 0.24 * grow);

          if (c.kind === 'file') {
            g.leaves++;
            const lit = m.litAt > 0 ? clamp(1 - (now - m.litAt) / LIT_MS, 0, 1) : 0;
            const color = m.confirmed ? fileColor(c.name) : GREY;
            const r = (fileR + lit * 2) * grow;
            m.r = r; m.color = color; m.alpha = (0.55 + 0.45 * lit) * grow;
            dots.put(x, y, r * dotScale, r * dotScale, color, m.alpha);
            if (lit > 0) {
              const d = (10 + 26 * lit) * glowScale;
              glows.put(x, y, d, d, color, 0.75 * lit);
            }
            const gk = (now - m.greyAt) / GREY_MS;
            if (m.greyAt > 0 && gk >= 0 && gk < 1) {
              const flicker = reduced ? 0.5 : 0.3 + 0.5 * Math.abs(Math.sin(now / 45));
              glows.put(x, y, 22 * glowScale, 22 * glowScale, 0xc8cfdc, flicker * (1 - gk));
            }
            const age = now - c.lastTouchAt;
            if (age < LABEL_MS) {
              const ca = Math.cos(c.ang);
              const sa = Math.sin(c.ang);
              const a = (lit > 0 ? 0.5 + 0.5 * lit : 0.55) * Math.min(1, (LABEL_MS - age) / LABEL_FADE_MS) * grow;
              const ax = Math.abs(ca) < 0.3 ? 0.5 : ca > 0 ? 0 : 1;
              files.show(c, c.name, x + ca * 11, y + sa * 11, lit > 0 ? 0xe6ecf8 : m.confirmed ? 0x8291af : 0x9aa1ae, a, ax, 0.5);
            }
            continue;
          }

          if (c.kind === 'project') {
            m.r = 4.5; m.color = 0xc8d2eb; m.alpha = 0.8 * grow;
            names.show(c, c.name, x, y - 8, 0xdfe6f5, grow, 0.5, 1);
          } else if (c.kind === 'other') {
            m.r = 4; m.color = 0x8a93a6; m.alpha = 0.6 * grow;
            names.show(c, 'other', x, y - 8, 0x9aa3b8, 0.8 * grow, 0.5, 1);
          } else {
            g.dirs++;
            m.r = 3; m.color = 0xa5b4d7; m.alpha = 0.55 * grow;
            const age = now - c.lastTouchAt;
            if (dirLabels || age < LABEL_MS) {
              const a = 0.6 * grow * (dirLabels ? 1 : Math.min(1, (LABEL_MS - age) / LABEL_FADE_MS));
              files.show(c, `${c.name}/`, x, y - 7, 0x96a5c8, a, 0.5, 1);
            }
          }
          dots.put(x, y, m.r * dotScale, m.r * dotScale, m.color, m.alpha);
          visit(c, x, y);
        }
      };
      if (root) visit(root, cx, cy);
      this.geoms.set(host, g);
    }

    // A node drawn last frame and not this one left the tree: fade it where it was.
    for (const p of this.prevVisited) {
      const m = this.meta.get(p);
      if (m && m.frame !== frame) this.startFade(m, now);
    }
    this.visited = this.prevVisited;
    this.prevVisited = visited;

    let w = 0;
    for (const f of this.fades) {
      const t = (now - f.at) / FADE_MS;
      if (t >= 1 || t < -1) continue;
      const a = 1 - Math.max(0, t);
      edges.line(f.px, f.py, f.x, f.y, 1.2, EDGE, 0.24 * a);
      dots.put(f.x, f.y, f.r * dotScale, f.r * dotScale, f.color, f.alpha * a);
      this.fades[w++] = f;
    }
    this.fades.length = w;
  }

  private startFade(m: Meta, now: number): void {
    if (m.fading) return;
    m.fading = true;
    m.frame = -1;
    this.fades.push({ at: now, x: m.x, y: m.y, px: m.px, py: m.py, r: m.r, color: m.color, alpha: m.alpha });
  }

  private metaOf(n: TreeNode): Meta {
    let m = this.meta.get(n);
    if (!m) {
      m = { x: 0, y: 0, px: 0, py: 0, frame: 0, r: 0, color: 0, alpha: 0, litAt: 0, greyAt: 0, confirmed: false, fading: false };
      this.meta.set(n, m);
    }
    return m;
  }
}
