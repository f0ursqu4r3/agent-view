import { expect, test } from 'bun:test';
import { CATEGORY_COLORS } from '../../src/shared/schema';
import type { Category } from '../../src/shared/schema';
import { EFFECT_MS, effectFade, effectFor } from '../../viewer/stage/effects';
import type { EffectType } from '../../viewer/stage/effects';
import type { AgentsView } from '../../viewer/stage/agents-view';
import { EffectsView } from '../../viewer/stage/effects-view';
import { TreeModel } from '../../viewer/stage/tree-model';
import type { TreeView } from '../../viewer/stage/tree-view';
import type { ViewCall, ViewerStore } from '../../viewer/store';

const NOW = Date.parse('2026-01-01T01:00:00.000Z');

const call: ViewCall = {
  agentId: 'a', callId: 'c1', startedAt: new Date(NOW - 100).toISOString(), category: 'edit', rawName: 'Edit',
  files: ['/p/src/a.ts'], host: 'local', startMs: NOW - 100, endMs: null, ok: null, output: '', outputTruncated: false,
};

const TYPES: Record<Category, EffectType> = {
  edit: 'beam', write: 'beam', read: 'dashed', search: 'sweep', command: 'pulse', agent: 'link', other: 'soft',
};

test('effectFor maps categories to effect types and colors', () => {
  for (const category of Object.keys(TYPES) as Category[]) {
    const e = effectFor({ ...call, category }, NOW, false);
    expect(e).not.toBeNull();
    expect(e!.type).toBe(TYPES[category]);
    expect(e!.color).toBe(CATEGORY_COLORS[category]);
  }
  // The values from the global constraints, spelled out.
  expect(effectFor({ ...call, category: 'edit' }, NOW, false)!.color).toBe('#ff5fa8');
  expect(effectFor({ ...call, category: 'read' }, NOW, false)!.color).toBe('#8f9bff');
  expect(effectFor({ ...call, category: 'command' }, NOW, false)!.color).toBe('#6dff9e');
  expect(effectFor({ ...call, category: 'search' }, NOW, false)!.color).toBe('#ffd166');
  expect(effectFor({ ...call, category: 'agent' }, NOW, false)!.color).toBe('#b9a6ff');
});

test('progress is (now - startMs) / duration of the effect type', () => {
  const beam = effectFor({ ...call, startMs: NOW - EFFECT_MS.beam / 2 }, NOW, false)!;
  expect(beam.progress).toBeCloseTo(0.5, 6);
  // Progress keeps counting past 1 while the call runs: a pulse loops on it.
  const pulse = effectFor({ ...call, category: 'command', startMs: NOW - EFFECT_MS.pulse * 1.5 }, NOW, false)!;
  expect(pulse.progress).toBeCloseTo(1.5, 6);
  // A start in the future (clock skew) is progress 0, never negative.
  expect(effectFor({ ...call, startMs: NOW + 500 }, NOW, false)!.progress).toBe(0);
});

test('effectFor returns null 1.5 s after end', () => {
  const ended = (ago: number) => effectFor({ ...call, startMs: NOW - ago - 300, endMs: NOW - ago }, NOW, false, NOW - ago - 300);
  expect(ended(0)).not.toBeNull();
  expect(ended(1_400)).not.toBeNull();
  expect(ended(1_500)).not.toBeNull();
  expect(ended(1_501)).toBeNull();
  expect(ended(60_000)).toBeNull();
});

test('Review Focus 4: after a 5-minute background gap, calls that started more than 2 s ago produce no effect', () => {
  expect(effectFor({ ...call, startMs: NOW - 300_000, endMs: null }, NOW, false)).toBeNull();
  expect(effectFor({ ...call, startMs: NOW - 2_001, endMs: null }, NOW, false)).toBeNull();
  // An ended call from the backlog stays dark too.
  expect(effectFor({ ...call, startMs: NOW - 3_000, endMs: NOW - 200 }, NOW, false)).toBeNull();
  // A call that is only a little late still plays, from its start.
  const late = effectFor({ ...call, startMs: NOW - 1_000, endMs: null }, NOW, false);
  expect(late).not.toBeNull();
  expect(late!.progress).toBeCloseTo(1_000 / EFFECT_MS.beam, 6);
});

test('a call drawn before the gap keeps its effect, with progress counted from its first drawn frame', () => {
  const drawn = effectFor({ ...call, startMs: NOW - 300_000, endMs: null }, NOW, false, NOW - 299_990);
  expect(drawn).not.toBeNull();
  expect(drawn!.progress).toBeCloseTo(299_990 / EFFECT_MS.beam, 6);
  // First drawn after the start: progress starts at the first drawn frame.
  const delayed = effectFor({ ...call, startMs: NOW - 1_000, endMs: null }, NOW, false, NOW - 100);
  expect(delayed!.progress).toBeCloseTo(100 / EFFECT_MS.beam, 6);
});

test('reducedMotion turns beam into a static line effect with progress 1', () => {
  expect(effectFor({ ...call, startMs: NOW - 10 }, NOW, true)).toEqual({ type: 'beam', color: '#ff5fa8', progress: 1 });
  for (const category of Object.keys(TYPES) as Category[]) {
    expect(effectFor({ ...call, category, startMs: NOW - 10 }, NOW, true)).toEqual({
      type: TYPES[category], color: CATEGORY_COLORS[category], progress: 1,
    });
  }
  // The null rules still apply.
  expect(effectFor({ ...call, startMs: NOW - 300_000 }, NOW, true)).toBeNull();
  expect(effectFor({ ...call, startMs: NOW - 3_000, endMs: NOW - 1_600 }, NOW, true, NOW - 3_000)).toBeNull();
});

test('effectFade is 1 while the call is open and falls to 0 over 1.5 s after the end', () => {
  expect(effectFade(call, NOW)).toBe(1);
  expect(effectFade({ ...call, endMs: NOW }, NOW)).toBe(1);
  expect(effectFade({ ...call, endMs: NOW - 750 }, NOW)).toBeCloseTo(0.5, 6);
  expect(effectFade({ ...call, endMs: NOW - 1_500 }, NOW)).toBe(0);
  expect(effectFade({ ...call, endMs: NOW - 9_000 }, NOW)).toBe(0);
});

test('Minor 4: an open call of a stale agent draws no beam, particles or sweep; a fresh agent draws them', () => {
  const tree = new TreeModel();
  tree.touch({ host: 'local', path: '/p/src/a.ts', projectRoot: '/p', attribution: 'confirmed', agentId: 'a', atMs: NOW - 100 });
  const view = { pos: (n: unknown) => (n ? { x: 100, y: 100 } : null) } as unknown as TreeView;
  const store = { calls: () => [call, { ...call, callId: 's1', category: 'search', rawName: 'Grep' }] } as unknown as ViewerStore;
  const drawsFor = (stale: boolean): number => {
    let n = 0;
    const layer = { line: () => n++, dashed: () => n++, put: () => n++, ring: () => n++ };
    const agents = { pos: () => ({ x: 0, y: 0 }), alphaOf: () => 1, projectOf: () => '/p', isStale: () => stale } as unknown as AgentsView;
    new EffectsView({ lines: layer, glowLines: layer, sparks: layer, rings: layer } as never).draw(NOW, store, tree, view, agents, false);
    return n;
  };
  expect(drawsFor(false)).toBeGreaterThan(0);
  expect(drawsFor(true)).toBe(0);
});
