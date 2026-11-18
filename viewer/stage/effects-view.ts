import type { ViewCall, ViewerStore } from '../store';
import type { AgentsView } from './agents-view';
import type { LineBatch, RingBatch, SpriteBatch } from './batch';
import { EFFECT_MS, EFFECT_TAIL_MS, effectFade, effectFor } from './effects';
import type { Effect } from './effects';
import { TEX_SIZE } from './textures';
import { findNear } from './tree-view';
import type { Pt, TreeView } from './tree-view';
import type { TreeModel } from './tree-model';

export type EffectLayers = {
  lines: LineBatch;
  /** Additive lines under beams: their glow. */
  glowLines: LineBatch;
  /** Additive dots: particles and soft pulses. */
  sparks: SpriteBatch;
  rings: RingBatch;
};

/** A beam sends particles along itself at this period. */
const PARTICLE_MS = 280;
/** The ring on an edited file expands over this long, once the beam has reached it. */
const FILE_RING_MS = 900;
/** At most this many files of one call get a line. */
const MAX_TARGETS = 3;
const FORGET_MS = 5000;
const hex = (s: string): number => parseInt(s.slice(1), 16);

/**
 * Draws one effect per recent call (`effectFor`) between its agent and the files the call names.
 * A stale agent draws none: it shows only its dim, slow pulse.
 */
export class EffectsView {
  /** When each call was first drawn, by host, agent and call id (calls are replaced on a snapshot). */
  private readonly first = new Map<string, { at: number; seen: number }>();
  private lastForget = 0;
  private readonly targets: Pt[] = [];

  constructor(private readonly l: EffectLayers) {}

  draw(now: number, store: ViewerStore, tree: TreeModel, view: TreeView, agents: AgentsView, reduced: boolean): void {
    for (const call of store.calls(now, EFFECT_TAIL_MS)) {
      const key = `${call.host}\u0000${call.agentId}\u0000${call.callId}`;
      let rec = this.first.get(key);
      if (!rec) {
        // A call is first drawn only when it is not backlog. Progress then counts from this frame.
        if (effectFor(call, now, reduced) === null) continue;
        this.first.set(key, (rec = { at: now, seen: now }));
      }
      rec.seen = now;
      // Kept but not drawn while the agent is stale, so the effect comes back when the agent does.
      if (agents.isStale(call.agentId)) continue;
      const e = effectFor(call, now, reduced, rec.at);
      if (!e) continue;
      const a = agents.pos(call.agentId);
      if (!a) continue;
      const alpha = effectFade(call, now) * agents.alphaOf(call.agentId);
      if (alpha <= 0.003) continue;
      this.drawOne(e, call, a, alpha, now, tree, view, agents, reduced);
    }
    if (now - this.lastForget > FORGET_MS) {
      this.lastForget = now;
      for (const [key, rec] of this.first) if (now - rec.seen > FORGET_MS) this.first.delete(key);
    }
  }

  private drawOne(e: Effect, call: ViewCall, a: Pt, alpha: number, now: number, tree: TreeModel, view: TreeView, agents: AgentsView, reduced: boolean): void {
    const { lines, glowLines, sparks, rings } = this.l;
    const color = hex(e.color);
    const elapsed = e.progress * EFFECT_MS[e.type];
    const p = Math.min(1, e.progress);
    const open = call.endMs === null;
    switch (e.type) {
      case 'beam': {
        // A solid beam that draws out to the file, particles along it, and a ring on the file once it lands.
        for (const f of this.fileTargets(call, tree, view, agents, false)) {
          const ex = a.x + (f.x - a.x) * p;
          const ey = a.y + (f.y - a.y) * p;
          glowLines.line(a.x, a.y, ex, ey, 7, color, 0.22 * alpha);
          lines.line(a.x, a.y, ex, ey, 2, color, 0.9 * alpha);
          if (reduced) continue;
          if (open && p >= 1) {
            for (let j = 0; j < 3; j++) {
              const q = (elapsed / PARTICLE_MS + j / 3) % 1;
              const d = 6 / TEX_SIZE.glow;
              sparks.put(a.x + (f.x - a.x) * q, a.y + (f.y - a.y) * q, d, d, 0xffffff, alpha);
            }
          }
          const rk = (elapsed - EFFECT_MS.beam) / FILE_RING_MS;
          if (rk >= 0 && rk < 1) rings.ring(f.x, f.y, 6 + rk * 18, 2, color, (1 - rk) * alpha);
        }
        break;
      }
      case 'dashed': {
        // A dashed line from the file that moves toward the agent.
        const phase = reduced ? 0 : elapsed / 30;
        for (const f of this.fileTargets(call, tree, view, agents, false)) {
          lines.dashed(f.x, f.y, a.x, a.y, 1.4, color, 0.85 * p * alpha, 4, 5, phase);
        }
        break;
      }
      case 'sweep': {
        // Flickering lines to the folder or files the search names, else to the agent's project.
        let j = 0;
        for (const f of this.fileTargets(call, tree, view, agents, true)) {
          const flicker = reduced ? 0.5 : 0.35 + 0.35 * Math.sin(now / 70 + j * 2);
          lines.line(a.x, a.y, f.x, f.y, 1.2, color, flicker * p * alpha);
          j++;
        }
        break;
      }
      case 'pulse': {
        // Rings around the agent while the command runs; the command text is on the agent label.
        if (reduced) {
          rings.ring(a.x, a.y, 16, 1.6, color, 0.6 * alpha);
        } else if (open || e.progress < 1) {
          const k = e.progress % 1;
          rings.ring(a.x, a.y, 10 + k * 22, 1.6, color, (1 - k) * 0.8 * alpha);
        }
        break;
      }
      case 'link': {
        // A link that draws itself out to the new sub-agent.
        const c = agents.childOf(call.agentId, call.startMs);
        if (c) {
          lines.line(a.x, a.y, a.x + (c.x - a.x) * p, a.y + (c.y - a.y) * p, 1.5, color, 0.8 * alpha);
          break;
        }
        this.soft(e, a, color, alpha, reduced);
        break;
      }
      case 'soft':
        this.soft(e, a, color, alpha, reduced);
        break;
    }
  }

  /** A single soft pulse at the agent. */
  private soft(e: Effect, a: Pt, color: number, alpha: number, reduced: boolean): void {
    if (reduced) {
      const d = 40 / TEX_SIZE.glow;
      this.l.sparks.put(a.x, a.y, d, d, color, 0.35 * alpha);
      return;
    }
    if (e.progress >= 1) return;
    const d = (20 + 44 * e.progress) / TEX_SIZE.glow;
    this.l.sparks.put(a.x, a.y, d, d, color, (1 - e.progress) * 0.7 * alpha);
  }

  /** Drawn positions of the call's files (newest last). Searches fall back to the nearest folder, then the project. */
  private fileTargets(call: ViewCall, tree: TreeModel, view: TreeView, agents: AgentsView, search: boolean): Pt[] {
    const out = this.targets;
    out.length = 0;
    const files = call.files ?? [];
    for (let i = Math.max(0, files.length - MAX_TARGETS); i < files.length; i++) {
      const node = search ? findNear(tree, call.host, files[i]!) : tree.find(call.host, files[i]!);
      const pt = view.pos(node);
      if (pt) out.push(pt);
    }
    if (search && out.length === 0) {
      const root = agents.projectOf(call.agentId);
      const pt = root === null ? null : view.pos(tree.find(call.host, root));
      if (pt) out.push(pt);
    }
    return out;
  }
}
