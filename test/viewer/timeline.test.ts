import { expect, test } from 'bun:test';
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
