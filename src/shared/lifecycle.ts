import type { Agent } from './schema';

// Shared by the collector and the browser viewer: keep this file free of Node imports.
export const LIFECYCLE = {
  lingerMs: 600_000,
  staleMs: 120_000,
  finishedFadeMs: 60_000,
  callTimeoutMs: 600_000,
  fileTtlMs: 1_800_000,
};

export type LifecycleOptions = { -readonly [K in keyof typeof LIFECYCLE]: number };

function resolve(opts?: Partial<LifecycleOptions>): LifecycleOptions {
  return { ...LIFECYCLE, ...opts };
}

/** Milliseconds for an ISO time. An empty or invalid time counts as the epoch. */
function msOf(iso: string): number {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? 0 : t;
}

/** Present agents: those with a recent event, plus every ancestor of a present agent. */
export function presentAgentIds(agents: Agent[], now: number, opts?: Partial<LifecycleOptions>): Set<string> {
  const o = resolve(opts);
  const byId = new Map(agents.map((a) => [a.id, a]));
  const present = new Set<string>();
  for (const agent of agents) {
    const window = agent.status === 'finished' ? o.finishedFadeMs : o.lingerMs;
    if (now - msOf(agent.lastEventAt) >= window) continue;
    let cur: Agent | undefined = agent;
    const seen = new Set<string>();
    while (cur && !seen.has(cur.id)) {
      seen.add(cur.id);
      present.add(cur.id);
      cur = cur.parentId === null ? undefined : byId.get(cur.parentId);
    }
  }
  return present;
}

export function isStale(agent: Agent, now: number, opts?: Partial<LifecycleOptions>): boolean {
  return agent.status === 'working' && now - msOf(agent.lastEventAt) > resolve(opts).staleMs;
}
