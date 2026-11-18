import { CATEGORY_COLORS } from '../../src/shared/schema';
import type { Category } from '../../src/shared/schema';
import type { ViewCall } from '../store';

// Browser-safe: no DOM and no Pixi imports. Tested with `bun test`.

export type EffectType = 'beam' | 'dashed' | 'sweep' | 'pulse' | 'link' | 'soft';
/**
 * `progress` counts effect durations (`EFFECT_MS[type]`) since the effect began. 0 to 1 is the first play:
 * a beam or link drawing out, the first pulse ring, the soft pulse. It keeps counting while the call runs,
 * so looping forms use its fraction. With reduced motion it is fixed at 1: the static, fully drawn form.
 */
export type Effect = { type: EffectType; color: string; progress: number };

const TYPES: Record<Category, EffectType> = {
  edit: 'beam', write: 'beam', read: 'dashed', search: 'sweep', command: 'pulse', agent: 'link', other: 'soft',
};

/** One play of each effect, in milliseconds. */
export const EFFECT_MS: Record<EffectType, number> = { beam: 400, dashed: 400, sweep: 300, pulse: 900, link: 600, soft: 900 };
/** An ended call keeps its effect this long while it fades. */
export const EFFECT_TAIL_MS = 1500;
/** A call never drawn and started longer ago than this is backlog (a background tab): no effect. */
export const BACKLOG_MS = 2000;

/**
 * The effect a call draws now, or null.
 * `firstDrawnMs` is when the stage first drew this call; leave it undefined until then.
 */
export function effectFor(call: ViewCall, now: number, reducedMotion: boolean, firstDrawnMs?: number): Effect | null {
  if (call.endMs !== null && now - call.endMs > EFFECT_TAIL_MS) return null;
  if (firstDrawnMs === undefined && now - call.startMs > BACKLOG_MS) return null;
  const type = TYPES[call.category] ?? 'soft';
  const color = CATEGORY_COLORS[call.category] ?? CATEGORY_COLORS.other;
  if (reducedMotion) return { type, color, progress: 1 };
  const from = Math.max(call.startMs, firstDrawnMs ?? call.startMs);
  return { type, color, progress: Math.max(0, now - from) / EFFECT_MS[type] };
}

/** Opacity of a call's effect: 1 while open, then down to 0 over the tail after the end. */
export function effectFade(call: ViewCall, now: number): number {
  if (call.endMs === null) return 1;
  return Math.min(1, Math.max(0, 1 - (now - call.endMs) / EFFECT_TAIL_MS));
}
