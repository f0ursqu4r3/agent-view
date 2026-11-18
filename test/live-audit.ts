// Run separately: bun run audit:live [port]. Reads the running hub's /api/snapshot and prints counts only.
// It never prints paths, names, commands, or output.
import assert from 'node:assert/strict';
import type { Agent, Envelope, FileTouch, Provider } from '../src/shared/schema';

const port = Number(process.argv[2] ?? 4317);
assert.ok(Number.isInteger(port) && port > 0 && port < 65536, 'invalid port');

const response = await fetch(`http://127.0.0.1:${port}/api/snapshot`);
assert.equal(response.status, 200, 'snapshot status');
const { envelopes } = (await response.json()) as { envelopes: Envelope[] };
assert.ok(Array.isArray(envelopes), 'envelopes is an array');

const agents = new Map<string, Agent>();
const files: FileTouch[] = [];
let loadedLogs = 0;
let discoveredLogs = 0;
let malformedRecords = 0;
let degradedSources = 0;
for (const env of envelopes) {
  if (env.kind === 'snapshot') {
    for (const a of env.agents) agents.set(a.id, a);
    files.push(...env.files);
  } else if (env.kind === 'coverage') {
    loadedLogs += env.coverage.logsOpen;
    discoveredLogs += env.coverage.logsDiscovered;
    malformedRecords += env.coverage.malformedRecords;
    for (const s of Object.values(env.coverage.sources)) if (s.state !== 'ok') degradedSources++;
  }
}

// The snapshot holds only agents active inside the linger window, so a provider can have none.
assert.ok(agents.size > 0, 'at least one agent');

for (const a of agents.values()) {
  if (!a.parentId) continue;
  assert.notEqual(a.id, a.parentId, 'agent is its own parent');
  const seen = new Set([a.id]);
  for (let p = agents.get(a.parentId); p; p = p.parentId ? agents.get(p.parentId) : undefined) {
    assert.ok(!seen.has(p.id), 'parent cycle');
    seen.add(p.id);
  }
}

// A file node lives 30 minutes, but an agent leaves the snapshot after the 10-minute linger window.
// So an old confirmed write can name an agent that is no longer in the snapshot. That is by design.
// A write inside the linger window must name a known agent. 9 minutes leaves a margin for clock skew.
const RECENT_MS = 9 * 60_000;
const nowMs = Date.now();
let writesByLapsedAgents = 0;
for (const f of files) {
  if (f.attribution !== 'confirmed') continue;
  assert.ok(f.agentId !== undefined && f.agentId !== '', 'confirmed write has no agent ID');
  if (agents.has(f.agentId)) continue;
  writesByLapsedAgents++;
  assert.ok(nowMs - Date.parse(f.lastTouchAt) >= RECENT_MS, 'recent confirmed write has an unknown agent');
}

const counts = {} as Record<Provider, { agents: number; confirmedParents: number; sessionLinks: number; confirmedWrites: number }>;
for (const provider of ['claude', 'codex'] as const) {
  const list = [...agents.values()].filter((a) => a.provider === provider);
  counts[provider] = {
    agents: list.length,
    confirmedParents: list.filter((a) => a.parentId && a.link === 'confirmed').length,
    sessionLinks: list.filter((a) => a.parentId && a.link === 'session').length,
    confirmedWrites: files.filter((f) => f.attribution === 'confirmed' && f.agentId !== undefined && agents.get(f.agentId)?.provider === provider).length,
  };
}
console.log(JSON.stringify({ counts, loadedLogs, discoveredLogs, malformedRecords, degradedSources, writesByLapsedAgents }, null, 2));
