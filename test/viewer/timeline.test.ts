import { expect, test } from 'bun:test';
import { commandSignature } from '../../viewer/drawer/drawer';
import { drawTimeline, laneAt } from '../../viewer/drawer/timeline';
import { ViewerStore } from '../../viewer/store';
import { PROVIDER_COLORS } from '../../src/shared/schema';
import type { Envelope } from '../../src/shared/schema';

test('laneAt maps y to lane id and returns null outside lanes', () => {
  expect(laneAt(26 + 23 * 2 + 5, 26, 23, ['a', 'b', 'c', 'd'])).toBe('c');
  expect(laneAt(10, 26, 23, ['a'])).toBeNull();
});

test('laneAt edges: first lane starts at top, the lane after the last is null', () => {
  expect(laneAt(26, 26, 23, ['a', 'b'])).toBe('a');
  expect(laneAt(26 + 23 - 0.1, 26, 23, ['a', 'b'])).toBe('a');
  expect(laneAt(26 + 23, 26, 23, ['a', 'b'])).toBe('b');
  expect(laneAt(26 + 46, 26, 23, ['a', 'b'])).toBeNull();
  expect(laneAt(30, 26, 23, [])).toBeNull();
});

test('commandSignature changes when output slides at a constant length', () => {
  const base = { agentId: 'a', callId: 'c', startedAt: '', category: 'command', rawName: 'Bash', host: 'h', startMs: 0, endMs: null, ok: null, output: '', outputTruncated: true } as const;
  const a = { ...base, output: `${'x'.repeat(16 * 1024 - 6)}line-A\n` };
  const b = { ...base, output: `${'x'.repeat(16 * 1024 - 6)}line-B\n` };
  expect(a.output.length).toBe(b.output.length);
  expect(commandSignature(null, [a], new Map())).not.toBe(commandSignature(null, [b], new Map()));
  expect(commandSignature(null, [a], new Map())).toBe(commandSignature(null, [{ ...a }], new Map()));
});

test('Minor 1: an unknown category or provider draws with the fallback color instead of throwing', () => {
  const NOW = Date.parse('2026-01-01T01:00:00.000Z');
  const iso = (ms: number) => new Date(ms).toISOString();
  const store = new ViewerStore({ now: () => NOW });
  const env = (e: object, seq: number) => ({ v: 1, host: 'h', seq, ts: iso(NOW - 1000), ...e }) as Envelope;
  store.apply(env({ kind: 'hello', now: iso(NOW), collectorVersion: 'x', platform: 'x' }, 0));
  store.apply(env({ kind: 'snapshot', agents: [{
    id: 'a', provider: 'mystery', name: 'a', role: 'main', parentId: null, link: 'root', cwd: null, projectRoot: null,
    status: 'working', lastEventAt: iso(NOW - 1000),
  }], openCalls: [], files: [] }, 1));
  store.apply(env({ kind: 'tool.start', call: { agentId: 'a', callId: 'c', startedAt: iso(NOW - 1000), category: 'mystery', rawName: 'X' } }, 2));
  const fills: unknown[] = [];
  const ctx = new Proxy({} as Record<string | symbol, unknown>, {
    get: (t, k) => (k in t ? t[k] : () => {}),
    set: (t, k, v) => { if (k === 'fillStyle') fills.push(v); t[k] = v; return true; },
  }) as unknown as CanvasRenderingContext2D;
  expect(() => drawTimeline(ctx, { w: 800, h: 300 }, store, NOW, { laneHeight: 23, windowMs: 16_000, focus: null })).not.toThrow();
  expect(fills).toContain(PROVIDER_COLORS.claude[0]);
  expect(fills.some((f) => typeof f === 'string' && f.startsWith('rgba(185,166,255,'))).toBe(true); // CATEGORY_COLORS.other
});
