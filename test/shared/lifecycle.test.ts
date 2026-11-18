import { expect, test } from 'bun:test';
import { LIFECYCLE, isStale, presentAgentIds } from '../../src/shared/lifecycle';
import type { Agent } from '../../src/shared/schema';

const NOW = Date.parse('2026-01-01T01:00:00.000Z');
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const agent = (id: string, over: Partial<Agent> = {}): Agent => ({
  id, provider: 'claude', name: id, role: 'main', parentId: null, link: 'root', cwd: '/p',
  projectRoot: '/p', status: 'idle', lastEventAt: ago(0), ...over,
});

test('LIFECYCLE holds the spec values', () => {
  expect(LIFECYCLE).toEqual({ lingerMs: 600_000, staleMs: 120_000, finishedFadeMs: 60_000, callTimeoutMs: 600_000, fileTtlMs: 1_800_000 });
});

test('present: agent with event inside linger; ancestors of present agents; finished only for 1 minute', () => {
  const agents = [
    agent('fresh', { lastEventAt: ago(599_999) }),
    agent('old', { lastEventAt: ago(600_000) }),
    agent('root-old', { lastEventAt: ago(3_600_000) }),
    agent('mid-old', { parentId: 'root-old', lastEventAt: ago(3_000_000) }),
    agent('leaf', { parentId: 'mid-old', role: 'sub-agent', lastEventAt: ago(1_000) }),
    agent('fin-new', { status: 'finished', lastEventAt: ago(59_999) }),
    agent('fin-old', { status: 'finished', lastEventAt: ago(60_000) }),
    agent('orphan', { parentId: 'missing', lastEventAt: ago(1_000) }),
  ];
  const present = presentAgentIds(agents, NOW);
  expect([...present].sort()).toEqual(['fin-new', 'fresh', 'leaf', 'mid-old', 'orphan', 'root-old']);
});

test('present: options override linger; parent cycles do not hang', () => {
  const agents = [
    agent('a', { parentId: 'b', lastEventAt: ago(1_000) }),
    agent('b', { parentId: 'a', lastEventAt: ago(9_000_000) }),
    agent('c', { lastEventAt: ago(5_000) }),
  ];
  expect([...presentAgentIds(agents, NOW, { lingerMs: 2_000 })].sort()).toEqual(['a', 'b']);
  expect([...presentAgentIds(agents, NOW)].sort()).toEqual(['a', 'b', 'c']);
});

test('isStale only for working agents with no event for 2 minutes', () => {
  expect(isStale(agent('a', { status: 'working', lastEventAt: ago(120_001) }), NOW)).toBe(true);
  expect(isStale(agent('a', { status: 'working', lastEventAt: ago(120_000) }), NOW)).toBe(false);
  expect(isStale(agent('a', { status: 'idle', lastEventAt: ago(500_000) }), NOW)).toBe(false);
  expect(isStale(agent('a', { status: 'working', lastEventAt: ago(30_000) }), NOW, { staleMs: 10_000 })).toBe(true);
});
