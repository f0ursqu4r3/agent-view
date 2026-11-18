import { CATEGORY_COLORS, PROVIDER_COLORS } from '../../src/shared/schema';
import type { ViewAgent, ViewCall, ViewerStore } from '../store';

/** Space above the first lane, for the time ticks. */
export const TIMELINE_TOP = 26;
export const LANE_HEIGHT = 23;
export const WINDOW_MS = 16_000;

const MONO = 'ui-monospace, SFMono-Regular, Menlo, monospace';
const SANS = '-apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';
const RIGHT_PAD = 20;

export type TimelineOpts = { laneHeight: number; windowMs: number; focus: string | null };

/** The lane id under `y`, or null above the first lane, below the last, or when there are no lanes. */
export function laneAt(y: number, top: number, laneHeight: number, lanes: string[]): string | null {
  if (!(laneHeight > 0) || y < top) return null;
  const i = Math.floor((y - top) / laneHeight);
  return i >= 0 && i < lanes.length ? lanes[i]! : null;
}

/** The lane ids drawn for a canvas of height `h`: present agents in lane order, as many as fit. */
export function timelineLanes(store: ViewerStore, now: number, h: number, laneHeight: number): string[] {
  return store.laneOrder(now).slice(0, visibleLaneCount(h, TIMELINE_TOP, laneHeight));
}

/** How many lanes fit in a canvas of height `h`. */
export function visibleLaneCount(h: number, top: number, laneHeight: number): number {
  return Math.max(0, Math.floor((h - top) / laneHeight));
}

function hexA(hex: string, a: number): string {
  const n = Number.parseInt(hex.slice(1), 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`;
}

function text(ctx: CanvasRenderingContext2D, s: string, x: number, y: number, color: string, font: string, align: CanvasTextAlign = 'left'): void {
  ctx.font = font;
  ctx.fillStyle = color;
  ctx.textAlign = align;
  ctx.fillText(s, x, y);
}

const oneLine = (s: string): string => s.replace(/\s+/g, ' ').trim();
const base = (p: string): string => p.slice(p.lastIndexOf('/') + 1) || p;

/** The text inside a bar: `$ cmd`, `Edit file.ts`, `Search "pattern"`. Agent data is drawn with `fillText`, never as HTML. */
export function barLabel(c: ViewCall): string {
  const file = c.files?.[0];
  switch (c.category) {
    case 'command': return `$ ${oneLine(c.command ?? c.rawName)}`;
    case 'edit': return file ? `Edit ${base(file)}` : 'Edit';
    case 'write': return file ? `Write ${base(file)}` : 'Write';
    case 'read': return file ? `Read ${base(file)}` : 'Read';
    case 'search': return c.pattern ? `Search "${oneLine(c.pattern)}"` : 'Search';
    default: return oneLine(c.rawName);
  }
}

export function agentColor(a: { provider: ViewAgent['provider'] }): string {
  return PROVIDER_COLORS[a.provider][0]!;
}

function laneStatus(a: ViewAgent): { label: string; color: string } {
  if (a.status === 'working') return { label: 'working', color: '#6dff9e' };
  return { label: a.status, color: '#56617a' };
}

/**
 * Draws the timeline: one lane per present agent in `store.laneOrder(now)`, a bar per call in the window.
 * `ctx` must already be scaled to CSS pixels. Lanes that do not fit in `size.h` are not drawn.
 */
export function drawTimeline(
  ctx: CanvasRenderingContext2D, size: { w: number; h: number }, store: ViewerStore, now: number, opts: TimelineOpts,
): void {
  const { w, h } = size;
  const { laneHeight: lane, windowMs: win, focus } = opts;
  const top = TIMELINE_TOP;
  const small = lane < 28;
  ctx.clearRect(0, 0, w, h);

  const agents = new Map(store.agents(now).map((a) => [a.id, a]));
  const lanes = timelineLanes(store, now, h, lane);
  const index = new Map(lanes.map((id, i) => [id, i]));
  const L = Math.round(Math.min(176, Math.max(124, w * 0.3)));
  const nowX = w - RIGHT_PAD;
  const X = (t: number): number => nowX - ((now - t) / win) * (nowX - L);
  const bot = top + lane * lanes.length;
  ctx.textBaseline = 'alphabetic';

  // Time grid: every 2 s when there is room, else every 4 s.
  const step = nowX - L > 380 ? 2 : 4;
  for (let s = 0; s <= win / 1000; s += step) {
    const gx = nowX - (s / (win / 1000)) * (nowX - L);
    ctx.strokeStyle = 'rgba(120,140,180,.08)';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(gx, top - 4);
    ctx.lineTo(gx, Math.max(bot, top));
    ctx.stroke();
    text(ctx, s ? `-${s}s` : 'now', gx, top - 9, s ? '#56617a' : '#b9a6ff', `10px ${MONO}`, 'center');
  }

  lanes.forEach((id, i) => {
    const a = agents.get(id)!;
    const y = top + i * lane;
    const cy = y + lane / 2;
    const nested = a.parentId !== null && index.has(a.parentId);
    const ind = nested ? 16 : 0;
    const working = a.status === 'working';
    if (focus === id) {
      ctx.fillStyle = 'rgba(160,124,255,.12)';
      ctx.fillRect(0, y, nowX + RIGHT_PAD, lane);
    } else if (i % 2 === 0) {
      ctx.fillStyle = 'rgba(255,255,255,.018)';
      ctx.fillRect(0, y, nowX + RIGHT_PAD, lane);
    }
    if (nested) {
      ctx.strokeStyle = 'rgba(140,155,190,.3)';
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(15, cy - lane / 2 + 5);
      ctx.lineTo(15, cy);
      ctx.lineTo(22, cy);
      ctx.stroke();
    }
    const color = agentColor(a);
    if (working) {
      ctx.shadowColor = color;
      ctx.shadowBlur = 10;
    }
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.arc(14 + ind + 4, cy, small ? 3.2 : 4, 0, Math.PI * 2);
    ctx.fill();
    ctx.shadowBlur = 0;
    // The name is clipped to the label column so a long name never reaches the status text.
    ctx.save();
    ctx.beginPath();
    ctx.rect(0, y, L - 62, lane);
    ctx.clip();
    text(ctx, a.name, 14 + ind + 13, cy + 4, working ? '#eef2fa' : '#9aa5bd', `${nested ? '' : '600 '}${small ? 11 : 12}px ${SANS}`);
    ctx.restore();
    const st = laneStatus(a);
    text(ctx, st.label, L - 10, cy + 3.5, st.color, `${small ? 9.5 : 10}px ${MONO}`, 'right');
  });

  ctx.strokeStyle = 'rgba(120,140,180,.18)';
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(L, top - 4);
  ctx.lineTo(L, Math.max(bot, top));
  ctx.stroke();

  const bh = Math.min(16, lane - 8);
  for (const c of store.calls(now, win)) {
    const i = index.get(c.agentId);
    if (i === undefined) continue;
    const running = c.endMs === null;
    const x1 = Math.max(L + 1, X(c.startMs));
    const x2 = X(Math.min(c.endMs ?? now, now));
    if (x2 <= L + 1) continue;
    const y = top + i * lane + lane / 2;
    const col = CATEGORY_COLORS[c.category];
    const bw = Math.max(3, x2 - x1);
    const dim = focus !== null && focus !== c.agentId ? 0.22 : 1;
    ctx.fillStyle = hexA(col, (running ? 0.92 : 0.5) * dim);
    ctx.beginPath();
    ctx.roundRect(x1, y - bh / 2, bw, bh, 4);
    ctx.fill();
    if (running && dim === 1) {
      ctx.shadowColor = col;
      ctx.shadowBlur = 18;
      ctx.fillStyle = '#fff';
      ctx.fillRect(x2 - 2, y - bh / 2, 2, bh);
      ctx.shadowBlur = 0;
    }
    if (bw > 46 && dim === 1) {
      ctx.save();
      ctx.beginPath();
      ctx.rect(x1 + 4, y - bh / 2, bw - 8, bh);
      ctx.clip();
      text(ctx, barLabel(c), x1 + 6, y + 3.5, '#0a0d14', `600 ${small ? 9.5 : 10.5}px ${MONO}`);
      ctx.restore();
    }
  }

  // The "now" line.
  ctx.shadowColor = '#a07cff';
  ctx.shadowBlur = 14;
  ctx.strokeStyle = 'rgba(176,150,255,.9)';
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  ctx.moveTo(nowX, top - 4);
  ctx.lineTo(nowX, Math.max(bot, top));
  ctx.stroke();
  ctx.shadowBlur = 0;
}
