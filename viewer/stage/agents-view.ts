import { CATEGORY_COLORS, PROVIDER_COLORS } from '../../src/shared/schema';
import type { ViewAgent, ViewCall, ViewerStore } from '../store';
import type { LineBatch, RingBatch, SpriteBatch } from './batch';
import type { LabelPool } from './labels';
import { TEX_SIZE } from './textures';
import { findNear } from './tree-view';
import type { Pt, TreeView } from './tree-view';
import type { TreeModel } from './tree-model';

export type AgentLayers = {
  links: LineBatch;
  trails: LineBatch;
  glows: SpriteBatch;
  cores: SpriteBatch;
  rings: RingBatch;
  initials: LabelPool<string>;
  names: LabelPool<string>;
  activity: LabelPool<string>;
};

type State = {
  id: string; host: string; x: number; y: number;
  trail: Float32Array; trailLen: number; trailHead: number;
  firstMs: number; goneMs: number | null; goneAlpha: number; status: string; flashMs: number;
  color: number; alpha: number; busy: boolean; stale: boolean;
  parentId: string | null; link: ViewAgent['link']; project: string | null;
};

const TRAIL = 16;
const APPEAR_MS = 400;
const GONE_MS = 600;
const FLASH_MS = 900;
const FINISHED_FADE_MS = 60_000;
const OFFSET_PX = 28;
const HOME_GAP_PX = 56;
const RED = 0xff4d5e;
const FOCUS = 0xb096ff;
const hex = (s: string): number => parseInt(s.slice(1), 16);
const basename = (p: string): string => p.slice(p.lastIndexOf('/') + 1) || p;
const cut = (s: string, n: number): string => (s.length > n ? `${s.slice(0, n - 1)}…` : s);
const mix = (a: number, b: number, t: number): number => {
  const ch = (s: number) => Math.round(((a >> s) & 255) * (1 - t) + ((b >> s) & 255) * t) << s;
  return ch(16) | ch(8) | ch(0);
};

/**
 * The sub-agent of `parentId` that a spawn link points to: the only drawn child that appeared near or after
 * `sinceMs`. Null when there is no such child or more than one (parallel spawns): a guessed link would be wrong.
 */
export function soleChild<T extends { parentId: string | null; firstMs: number; goneMs: number | null }>(
  drawn: readonly T[], parentId: string, sinceMs: number,
): T | null {
  let found: T | null = null;
  for (const s of drawn) {
    if (s.parentId !== parentId || s.goneMs !== null || s.firstMs < sinceMs - 2000) continue;
    if (found) return null;
    found = s;
  }
  return found;
}

/** Orange tones for Claude Code, blue for Codex: index 0 for a main agent, lighter tones for sub-agents. */
export function agentColor(a: ViewAgent, siblingIndex: number): string {
  const tones = PROVIDER_COLORS[a.provider] ?? PROVIDER_COLORS.claude;
  return a.role === 'sub-agent' || a.parentId !== null ? tones[1 + (siblingIndex % 2)]! : tones[0]!;
}

/** The agent's current activity, for its label. */
export function activityText(a: ViewAgent, call: ViewCall | undefined): string {
  if (!call) return a.stale ? 'stale' : a.status;
  const file = call.files && call.files.length > 0 ? basename(call.files[call.files.length - 1]!) : '';
  switch (call.category) {
    case 'read': case 'edit': case 'write': return cut(file ? `${call.category} ${file}` : call.rawName, 48);
    case 'command': return cut(`$ ${call.command ?? call.rawName}`, 48);
    case 'search': return cut(call.pattern ? `search "${call.pattern}"` : file ? `search ${file}` : 'search', 48);
    case 'agent': return 'agent';
    default: return cut(call.rawName, 48);
  }
}

/** Agent dots: eased moves toward the open call's file, a home ring when idle, trails, lifecycle looks. */
export class AgentsView {
  private readonly states = new Map<string, State>();
  /** Agents drawn this frame, newest first per parent: used to find the child a link points to. */
  private readonly drawn: State[] = [];

  constructor(private readonly l: AgentLayers) {}

  pos(id: string): Pt | null {
    const s = this.states.get(id);
    return s && s.goneMs === null ? s : null;
  }

  projectOf(id: string): string | null {
    return this.states.get(id)?.project ?? null;
  }

  /** Opacity multiplier for everything an agent draws (appear, fade, focus dim). */
  alphaOf(id: string): number {
    return this.states.get(id)?.alpha ?? 0;
  }

  /** The only sub-agent of `id` that appeared near or after `sinceMs`, if drawn. See `soleChild`. */
  childOf(id: string, sinceMs: number): Pt | null {
    return soleChild(this.drawn, id, sinceMs);
  }

  /** True while the drawn agent is stale: it shows only its dim, slow pulse. */
  isStale(id: string): boolean {
    const s = this.states.get(id);
    return s !== undefined && s.goneMs === null && s.stale;
  }

  /** The nearest drawn agent within `radius` pixels. */
  hit(x: number, y: number, radius = 14): string | null {
    let best: string | null = null;
    let bestD = radius * radius;
    for (const s of this.drawn) {
      if (s.goneMs !== null || s.alpha < 0.1) continue;
      const d = (s.x - x) ** 2 + (s.y - y) ** 2;
      if (d <= bestD) { bestD = d; best = s.id; }
    }
    return best;
  }

  update(now: number, dt: number, agents: ViewAgent[], store: ViewerStore, tree: TreeModel, view: TreeView, reduced: boolean): void {
    const k = reduced ? 1 : 1 - Math.exp(-5 * dt);
    const byId = new Map(agents.map((a) => [a.id, a]));
    const siblings = new Map<string, number>();
    const homes = this.homes(agents, tree, view);
    this.drawn.length = 0;

    for (const a of agents) {
      const call = store.openCall(a.id);
      const geom = view.geom(a.host);
      const home = homes.get(a.id) ?? (geom ? { x: geom.cx, y: geom.cy } : { x: 0, y: 0 });
      let target: Pt = home;
      if (call && geom && call.files && call.files.length > 0 && call.category !== 'command' && call.category !== 'agent') {
        const path = call.files[call.files.length - 1]!;
        const node = call.category === 'search' ? findNear(tree, a.host, path) : tree.find(a.host, path);
        const fp = view.pos(node);
        if (fp) {
          const dx = geom.cx - fp.x;
          const dy = geom.cy - fp.y;
          const d = Math.hypot(dx, dy) || 1;
          target = { x: fp.x + (dx / d) * OFFSET_PX, y: fp.y + (dy / d) * OFFSET_PX };
        }
      }

      let s = this.states.get(a.id);
      if (!s) {
        const parent = a.parentId === null ? undefined : this.states.get(a.parentId);
        const start = parent && parent.goneMs === null ? parent : home;
        s = {
          id: a.id, host: a.host, x: start.x, y: start.y, trail: new Float32Array(TRAIL * 2), trailLen: 0, trailHead: 0,
          firstMs: now, goneMs: null, goneAlpha: 0, status: a.status, flashMs: 0, color: 0, alpha: 0, busy: false, stale: false,
          parentId: a.parentId, link: a.link, project: a.projectRoot,
        };
        // An agent that arrives already interrupted flashes only when that just happened.
        if (a.status === 'interrupted' && now - a.lastEventMs < FLASH_MS) s.flashMs = now;
        this.states.set(a.id, s);
      } else if (a.status !== s.status && a.status === 'interrupted') {
        s.flashMs = now;
      }
      s.status = a.status;
      s.goneMs = null;
      s.parentId = a.parentId;
      s.link = a.link;
      s.project = a.projectRoot;
      s.busy = call !== undefined;
      s.stale = a.stale;
      let sib = 0;
      if (a.parentId !== null) {
        sib = siblings.get(a.parentId) ?? 0;
        siblings.set(a.parentId, sib + 1);
      }
      s.color = hex(agentColor(a, sib));
      s.x += (target.x - s.x) * k;
      s.y += (target.y - s.y) * k;
      if (reduced) {
        s.trailLen = 0;
      } else {
        s.trail[s.trailHead * 2] = s.x;
        s.trail[s.trailHead * 2 + 1] = s.y;
        s.trailHead = (s.trailHead + 1) % TRAIL;
        if (s.trailLen < TRAIL) s.trailLen++;
      }
      let alpha = Math.min(1, (now - s.firstMs) / APPEAR_MS);
      if (a.status === 'finished') alpha *= Math.max(0, 1 - (now - a.lastEventMs) / FINISHED_FADE_MS);
      if (store.focus !== null && store.focus !== a.id) alpha *= 0.18;
      s.alpha = alpha;
      this.drawn.push(s);
      this.drawAgent(s, a, call, now, reduced, byId);
    }

    // Agents that left the store fade out where they were.
    for (const [id, s] of this.states) {
      if (byId.has(id)) continue;
      if (s.goneMs === null) {
        s.goneMs = now;
        s.goneAlpha = s.alpha;
      }
      const t = (now - s.goneMs) / GONE_MS;
      if (t >= 1 || t < 0) {
        this.states.delete(id);
        continue;
      }
      s.alpha = s.goneAlpha * (1 - t);
      this.l.cores.put(s.x, s.y, 18 / TEX_SIZE.dot, 18 / TEX_SIZE.dot, s.color, s.alpha);
    }
  }

  private drawAgent(s: State, a: ViewAgent, call: ViewCall | undefined, now: number, reduced: boolean, byId: Map<string, ViewAgent>): void {
    const { links, trails, glows, cores, rings, initials, names, activity } = this.l;
    const alpha = s.alpha;
    if (alpha <= 0.003) return;

    // Hierarchy: a thin line to the parent while both are visible; dashed for a `session` link.
    if (s.parentId !== null && byId.has(s.parentId)) {
      const p = this.states.get(s.parentId);
      if (p && p.goneMs === null) {
        const la = 0.35 * Math.min(alpha, p.alpha);
        if (s.link === 'session') links.dashed(p.x, p.y, s.x, s.y, 1, 0x8c9bbe, la, 4, 4);
        else links.line(p.x, p.y, s.x, s.y, 1, 0x8c9bbe, la);
      }
    }

    for (let i = 1; i < s.trailLen; i++) {
      const i0 = (s.trailHead - s.trailLen + i - 1 + TRAIL) % TRAIL;
      const i1 = (s.trailHead - s.trailLen + i + TRAIL) % TRAIL;
      trails.line(s.trail[i0 * 2]!, s.trail[i0 * 2 + 1]!, s.trail[i1 * 2]!, s.trail[i1 * 2 + 1]!, 2, s.color, (i / s.trailLen) * 0.35 * alpha);
    }

    const glowScale = 1 / TEX_SIZE.glow;
    let core = s.color;
    let coreAlpha = alpha;
    if (a.stale) {
      // Stale: a dim, slow pulse.
      const pulse = reduced ? 0.5 : 0.5 + 0.5 * Math.sin((now / 2400) * Math.PI * 2);
      coreAlpha = alpha * (0.3 + 0.25 * pulse);
      glows.put(s.x, s.y, 44 * glowScale, 44 * glowScale, s.color, 0.22 * pulse * alpha);
    } else if (a.status === 'working') {
      const d = (s.busy ? 58 : 40) * glowScale;
      glows.put(s.x, s.y, d, d, s.color, (s.busy ? 0.85 : 0.45) * alpha);
    }
    const fk = (now - s.flashMs) / FLASH_MS;
    if (s.flashMs > 0 && fk >= 0 && fk < 1) {
      // Interrupted: one red flash, then the idle look.
      glows.put(s.x, s.y, 72 * glowScale, 72 * glowScale, RED, (1 - fk) * alpha);
      rings.ring(s.x, s.y, reduced ? 16 : 10 + fk * 26, 2, RED, (1 - fk) * 0.9 * alpha);
      core = mix(RED, s.color, fk);
    }
    if (a.status === 'finished') coreAlpha *= 0.7;
    cores.put(s.x, s.y, 18 / TEX_SIZE.dot, 18 / TEX_SIZE.dot, core, coreAlpha);

    initials.show(a.id, (a.name.trim()[0] ?? '?').toUpperCase(), s.x, s.y + 0.5, 0x0a0d14, coreAlpha, 0.5, 0.5);
    names.show(a.id, cut(a.name, 36), s.x + 13, s.y - 1, s.busy ? 0xeef2fa : 0xaab6cd, (s.busy ? 1 : 0.75) * alpha, 0, 1);
    // Interrupted shows as idle once its flash is over.
    const flashing = s.flashMs > 0 && fk >= 0 && fk < 1;
    const text = call ? activityText(a, call) : flashing ? 'interrupted' : a.status === 'interrupted' ? 'idle' : activityText(a, undefined);
    const color = call ? hex(CATEGORY_COLORS[call.category] ?? CATEGORY_COLORS.other)
      : flashing ? 0xff5f6d : a.stale ? 0x8792a8 : 0x56617a;
    activity.show(a.id, text, s.x + 13, s.y + 1, color, alpha, 0, 0);
  }

  /** The focus ring around the focused agent. */
  drawFocus(focus: string | null, now: number, reduced: boolean): void {
    if (focus === null) return;
    const s = this.states.get(focus);
    if (!s || s.goneMs !== null) return;
    this.l.rings.ring(s.x, s.y, 15 + (reduced ? 0 : Math.sin(now / 200) * 1.5), 1.5, FOCUS, 0.9);
  }

  /** Home ring: around each host center at 1.5 steps, near the agent's project node, else spread evenly. */
  private homes(agents: ViewAgent[], tree: TreeModel, view: TreeView): Map<string, Pt> {
    const out = new Map<string, Pt>();
    const groups = new Map<string, { host: string; ang: number | null; ids: string[] }>();
    for (const a of agents) {
      const project = a.projectRoot === null ? undefined : tree.find(a.host, a.projectRoot);
      const ang = project && view.pos(project) ? project.ang : null;
      const key = ang === null ? `${a.host}\u0000` : `${a.host}\u0000${a.projectRoot}`;
      let g = groups.get(key);
      if (!g) groups.set(key, (g = { host: a.host, ang, ids: [] }));
      g.ids.push(a.id);
    }
    const groupsPerHost = new Map<string, number>();
    for (const g of groups.values()) groupsPerHost.set(g.host, (groupsPerHost.get(g.host) ?? 0) + 1);
    for (const g of groups.values()) {
      const geom = view.geom(g.host);
      if (!geom) continue;
      const ring = geom.step * 1.5;
      const m = g.ids.length;
      // Neighbors keep a gap, but one project's agents never take more than its share of the ring.
      const gap = Math.min(HOME_GAP_PX / Math.max(1, ring), (2 * Math.PI) / (groupsPerHost.get(g.host) ?? 1) / m);
      for (let i = 0; i < m; i++) {
        const ang = g.ang === null
          ? (i * Math.PI) / 3 + Math.PI / 6 + Math.floor(i / 6) * (Math.PI / 6)
          : g.ang + (i - (m - 1) / 2) * gap;
        out.set(g.ids[i]!, { x: geom.cx + Math.cos(ang) * ring * geom.sx, y: geom.cy + Math.sin(ang) * ring });
      }
    }
    return out;
  }
}
