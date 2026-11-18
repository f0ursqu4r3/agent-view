import { expect, test } from 'bun:test';
import { commandSignature } from '../../viewer/drawer/drawer';
import { laneAt } from '../../viewer/drawer/timeline';

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
