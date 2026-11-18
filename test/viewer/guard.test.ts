import { expect, test } from 'bun:test';
import { makeGuard } from '../../viewer/guard';

test('Minor 1: a failing subsystem never stops the others, and logs at most once per subsystem per minute', () => {
  let now = 0;
  const logs: string[] = [];
  const guard = makeGuard((msg) => logs.push(msg), () => now);
  const ran: string[] = [];
  const frame = (): void => {
    guard('stage', () => { throw new Error('boom'); });
    guard('drawer', () => ran.push('drawer'));
    guard('hud', () => { throw new Error('hud'); });
  };
  frame();
  now = 30_000;
  frame();
  expect(ran).toEqual(['drawer', 'drawer']);
  expect(logs).toEqual(['agent-view: stage failed', 'agent-view: hud failed']);
  now = 60_000;
  frame();
  expect(logs).toHaveLength(4);
});
