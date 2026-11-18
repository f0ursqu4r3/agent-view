import { expect, test } from 'bun:test';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Collector } from '../../src/collector/collector';
import { DemoSource, demoFilePaths } from '../../src/collector/demo';
import type { LogRoot } from '../../src/collector/discovery';
import type { Envelope, SourceEvent } from '../../src/shared/schema';

const TMP = path.join(import.meta.dir, '..', '.tmp');

async function withDir(fn: (dir: string) => Promise<void>): Promise<void> {
  await fs.mkdir(TMP, { recursive: true });
  const dir = await fs.realpath(await fs.mkdtemp(path.join(TMP, 'collector-')));
  try {
    await fn(dir);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
}

/** Records envelopes and offers event-driven waits. */
function recorder() {
  const envs: Envelope[] = [];
  const waiters: { ok: () => boolean; done: () => void }[] = [];
  const poke = () => { for (const w of [...waiters]) if (w.ok()) { waiters.splice(waiters.indexOf(w), 1); w.done(); } };
  const fn = (e: Envelope) => { envs.push(e); poke(); };
  const until = (ok: () => boolean, ms = 8000, what = 'condition'): Promise<void> => {
    if (ok()) return Promise.resolve();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { clearInterval(tick); reject(new Error(`timeout waiting for ${what}`)); }, ms);
      const tick = setInterval(poke, 25);
      waiters.push({ ok, done: () => { clearTimeout(timer); clearInterval(tick); resolve(); } });
    });
  };
  return { envs, fn, until };
}

const nowIso = () => new Date().toISOString();

const claudeRoot = (dir: string): LogRoot => ({ provider: 'claude', path: path.join(dir, 'logs', 'projects'), scope: 'default' });

const toolUse = (id: string, name: string, input: object, cwd: string, ts = nowIso()) => JSON.stringify({
  sessionId: 'sess', cwd, timestamp: ts, type: 'assistant',
  message: { content: [{ type: 'tool_use', id, name, input }] },
}) + '\n';

const toolResult = (id: string, cwd: string, ts = nowIso()) => JSON.stringify({
  sessionId: 'sess', cwd, timestamp: ts, type: 'user',
  message: { content: [{ type: 'tool_result', tool_use_id: id, content: 'ok' }] },
}) + '\n';

test('subscribe gets hello (with now) then snapshot then live events with increasing seq', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    const log = path.join(root.path, 'p', 'sess.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log, toolUse('t0', 'Read', { file_path: '/project/a.ts' }, '/project'));
    const collector = new Collector({ host: 'mac', roots: [root] });
    await collector.start();
    const rec = recorder();
    const before = Date.now();
    const unsubscribe = collector.subscribe(rec.fn);
    try {
      expect(rec.envs[0]).toMatchObject({ v: 1, host: 'mac', kind: 'hello', platform: `${process.platform}-${process.arch}` });
      const hello = rec.envs[0] as Extract<Envelope, { kind: 'hello' }>;
      expect(typeof hello.collectorVersion).toBe('string');
      expect('demo' in hello).toBe(false);
      expect(Date.parse(hello.now)).toBeGreaterThanOrEqual(before - 1);
      expect(Date.parse(hello.now)).toBeLessThanOrEqual(Date.now());
      expect(rec.envs[1]?.kind).toBe('snapshot');
      const snap = rec.envs[1] as Extract<Envelope, { kind: 'snapshot' }>;
      expect(snap.agents.map((a) => a.id)).toEqual(['mac/claude:default:sess']);
      expect(snap.openCalls.map((c) => c.callId)).toEqual(['t0']);

      await fs.appendFile(log, toolUse('t1', 'Read', { file_path: '/project/b.ts' }, '/project'));
      await rec.until(() => rec.envs.some((e) => e.kind === 'tool.start' && e.call.callId === 't1'), 8000, 'tool.start t1');
      // hello and snapshot carry the current seq without taking a number; live envelopes strictly increase.
      expect(rec.envs[1]!.seq).toBe(rec.envs[0]!.seq);
      const seqs = rec.envs.map((e) => e.seq);
      for (let i = 2; i < seqs.length; i++) expect(seqs[i]!).toBeGreaterThan(seqs[i - 1]!);
      expect(seqs.at(-1)!).toBeGreaterThan(seqs[1]!);
    } finally {
      unsubscribe();
      await collector.stop();
    }
  });
}, 30_000);

test('demo collector labels hello demo: true and emits agents within 1 second', async () => {
  const collector = new Collector({ host: 'demo-host', demo: true });
  const rec = recorder();
  collector.subscribe(rec.fn);
  await collector.start();
  try {
    expect(rec.envs[0]).toMatchObject({ kind: 'hello', demo: true, host: 'demo-host' });
    expect(rec.envs[1]?.kind).toBe('snapshot');
    await rec.until(() => rec.envs.filter((e) => e.kind === 'agent.upsert').length >= 6, 1000, 'demo agents');
    const agents = rec.envs.flatMap((e) => (e.kind === 'agent.upsert' ? [e.agent] : []));
    expect(agents.filter((a) => a.provider === 'claude')).toHaveLength(3);
    expect(agents.filter((a) => a.provider === 'codex')).toHaveLength(3);
    for (const a of agents.filter((x) => x.role === 'sub-agent')) {
      expect(a.link).toBe('confirmed');
      expect(agents.some((p) => p.id === a.parentId)).toBe(true);
    }
    // A demo collector runs only the demo source: it reports no roots.
    expect(collector.coverage()).toMatchObject({ logRoots: [], watchRoots: [], logsOpen: 0, logsDiscovered: 0, fsDropped: 0 });
  } finally {
    await collector.stop();
  }
});

test('a late subscriber to a demo collector gets those agents in its snapshot', async () => {
  const collector = new Collector({ host: 'demo-host', demo: true });
  await collector.start();
  try {
    const rec = recorder();
    collector.subscribe(rec.fn);
    const snap = rec.envs[1] as Extract<Envelope, { kind: 'snapshot' }>;
    expect(snap.kind).toBe('snapshot');
    expect(snap.agents).toHaveLength(6);
  } finally {
    await collector.stop();
  }
});

test('Review Focus 3: a write through a symlinked project dir and its fs.change resolve to the same path', async () => {
  await withDir(async (dir) => {
    const proj = path.join(dir, 'real', 'proj');
    await fs.mkdir(path.join(proj, '.git'), { recursive: true });
    await fs.symlink(path.join(dir, 'real'), path.join(dir, 'link'));
    const viaLink = path.join(dir, 'link', 'proj', 'a.ts');
    const realFile = path.join(proj, 'a.ts');

    const root = claudeRoot(dir);
    const log = path.join(root.path, 'p', 'sess.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log,
      toolUse('w1', 'Write', { file_path: viaLink, content: 'x' }, path.join(dir, 'link', 'proj'))
      + toolResult('w1', path.join(dir, 'link', 'proj')));

    const collector = new Collector({ host: 'mac', roots: [root] });
    const rec = recorder();
    collector.subscribe(rec.fn);
    await collector.start();
    try {
      await rec.until(() => rec.envs.some((e) => e.kind === 'tool.end' && e.callId === 'w1'), 8000, 'tool.end w1');
      // The agent's project root resolves through the symlink, and that root becomes a watch root.
      await rec.until(() => collector.coverage().watchRoots.includes(proj), 8000, 'watch root');
      const upsert = rec.envs.find((e) => e.kind === 'agent.upsert');
      expect(upsert).toMatchObject({ agent: { projectRoot: proj } });

      // A watcher can miss a write made in the first moments after it starts, so write again until one is seen.
      const seen = () => rec.envs.some((e) => e.kind === 'fs.change' && e.path === realFile);
      const writer = setInterval(() => { void fs.writeFile(viaLink, `changed ${Date.now()}`); }, 250);
      try {
        await rec.until(seen, 10_000, 'fs.change');
      } finally {
        clearInterval(writer);
      }

      const end = rec.envs.find((e) => e.kind === 'tool.end' && e.callId === 'w1') as Extract<Envelope, { kind: 'tool.end' }>;
      const change = rec.envs.find((e) => e.kind === 'fs.change' && e.path === realFile) as Extract<Envelope, { kind: 'fs.change' }>;
      expect(end.writes?.[0]?.path).toBe(change.path);
      expect(change.path).toBe(await fs.realpath(viaLink));
      const start = rec.envs.find((e) => e.kind === 'tool.start' && e.call.callId === 'w1') as Extract<Envelope, { kind: 'tool.start' }>;
      expect(start.call.files).toEqual([realFile]);
    } finally {
      await collector.stop();
    }
  });
}, 30_000);

test('events keep their order through enrichment', async () => {
  await withDir(async (dir) => {
    const proj = path.join(dir, 'proj');
    await fs.mkdir(path.join(proj, '.git'), { recursive: true });
    const root = claudeRoot(dir);
    const log = path.join(root.path, 'p', 'sess.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    let text = '';
    for (let i = 0; i < 30; i++) text += toolUse(`c${i}`, 'Read', { file_path: path.join(proj, `f${i}.ts`) }, proj) + toolResult(`c${i}`, proj);
    await fs.writeFile(log, text);
    const collector = new Collector({ host: 'mac', roots: [root] });
    const rec = recorder();
    collector.subscribe(rec.fn);
    await collector.start();
    try {
      await rec.until(() => rec.envs.filter((e) => e.kind === 'tool.end').length === 30, 8000, '30 tool.end');
      const order = rec.envs.flatMap((e) => (e.kind === 'tool.start' ? [`s${e.call.callId}`] : e.kind === 'tool.end' ? [`e${e.callId}`] : []));
      const expected = Array.from({ length: 30 }, (_, i) => [`sc${i}`, `ec${i}`]).flat();
      expect(order).toEqual(expected);
    } finally {
      await collector.stop();
    }
  });
}, 30_000);

test('S1: a joining subscriber does not leave a gap in another subscriber\'s seq', async () => {
  const collector = new Collector({ host: 'demo-host', demo: true });
  const a = recorder();
  collector.subscribe(a.fn);
  await collector.start();
  try {
    await a.until(() => a.envs.length > 6, 3000, 'events for A');
    const b = recorder();
    collector.subscribe(b.fn);
    expect(b.envs[0]!.seq).toBe(b.envs[1]!.seq);
    await a.until(() => a.envs.length > 30, 8000, 'more events for A');
    const seqs = a.envs.slice(2).map((e) => e.seq);
    for (let i = 1; i < seqs.length; i++) expect(seqs[i]).toBe(seqs[i - 1]! + 1);
    // B's hello and snapshot carry the seq of the last live envelope A had seen when B joined.
    expect(a.envs.some((e) => e.seq === b.envs[0]!.seq)).toBe(true);
  } finally {
    await collector.stop();
  }
});

test('I1: an open call that times out yields a live tool.end with ok null and no writes', async () => {
  await withDir(async (dir) => {
    const proj = path.join(dir, 'proj');
    await fs.mkdir(path.join(proj, '.git'), { recursive: true });
    const root = claudeRoot(dir);
    const log = path.join(root.path, 'p', 'sess.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log, toolUse('slow1', 'Write', { file_path: path.join(proj, 'a.ts'), content: 'x' }, proj));
    const collector = new Collector({ host: 'mac', roots: [root], lifecycle: { callTimeoutMs: 300 } });
    const rec = recorder();
    collector.subscribe(rec.fn);
    await collector.start();
    try {
      await rec.until(() => rec.envs.some((e) => e.kind === 'tool.end' && e.callId === 'slow1'), 8000, 'timed-out tool.end');
      const end = rec.envs.find((e) => e.kind === 'tool.end' && e.callId === 'slow1') as Extract<Envelope, { kind: 'tool.end' }>;
      expect(end.ok).toBeNull();
      expect('writes' in end).toBe(false);
      expect(end.agentId).toBe('mac/claude:default:sess');
      const startIdx = rec.envs.findIndex((e) => e.kind === 'tool.start' && e.call.callId === 'slow1');
      expect(startIdx).toBeGreaterThan(-1);
      expect(rec.envs.indexOf(end)).toBeGreaterThan(startIdx);
      // The call is closed in the state too: a later subscriber sees no open call.
      const late = recorder();
      collector.subscribe(late.fn);
      expect((late.envs[1] as Extract<Envelope, { kind: 'snapshot' }>).openCalls).toEqual([]);
    } finally {
      await collector.stop();
    }
  });
}, 30_000);

test('I2: a Write into a not-yet-existing subdirectory of a symlinked project resolves under the real project root', async () => {
  await withDir(async (dir) => {
    const proj = path.join(dir, 'real', 'proj');
    await fs.mkdir(path.join(proj, '.git'), { recursive: true });
    await fs.symlink(path.join(dir, 'real'), path.join(dir, 'link'));
    const viaLink = path.join(dir, 'link', 'proj', 'newdir', 'sub', 'a.ts');
    const expected = path.join(proj, 'newdir', 'sub', 'a.ts');
    const cwd = path.join(dir, 'link', 'proj');
    const root = claudeRoot(dir);
    const log = path.join(root.path, 'p', 'sess.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    // Claude logs tool_use before the tool creates the directory, and the result comes right after.
    await fs.writeFile(log, toolUse('w1', 'Write', { file_path: viaLink, content: 'x' }, cwd) + toolResult('w1', cwd));
    const collector = new Collector({ host: 'mac', roots: [root] });
    const rec = recorder();
    collector.subscribe(rec.fn);
    await collector.start();
    try {
      await rec.until(() => rec.envs.some((e) => e.kind === 'tool.end' && e.callId === 'w1'), 8000, 'tool.end w1');
      const start = rec.envs.find((e) => e.kind === 'tool.start' && e.call.callId === 'w1') as Extract<Envelope, { kind: 'tool.start' }>;
      const end = rec.envs.find((e) => e.kind === 'tool.end' && e.callId === 'w1') as Extract<Envelope, { kind: 'tool.end' }>;
      expect(start.call.files).toEqual([expected]);
      expect(end.writes?.map((w) => w.path)).toEqual([expected]);
    } finally {
      await collector.stop();
    }
  });
}, 30_000);

test('a throwing subscriber does not break the others, and unsubscribe stops delivery', async () => {
  const collector = new Collector({ host: 'demo-host', demo: true });
  const good = recorder();
  collector.subscribe(() => { throw new Error('boom'); });
  const unsubscribe = collector.subscribe(good.fn);
  await collector.start();
  try {
    await good.until(() => good.envs.some((e) => e.kind === 'agent.upsert'), 2000, 'agent.upsert');
    unsubscribe();
    const n = good.envs.length;
    await new Promise((r) => setTimeout(r, 700));
    expect(good.envs.length).toBe(n);
  } finally {
    await collector.stop();
  }
});

test('stop is idempotent and nothing is emitted after it resolves', async () => {
  const collector = new Collector({ host: 'demo-host', demo: true });
  const rec = recorder();
  collector.subscribe(rec.fn);
  await collector.start();
  await rec.until(() => rec.envs.length > 8, 3000, 'some events');
  await Promise.all([collector.stop(), collector.stop()]);
  await collector.stop();
  const n = rec.envs.length;
  await new Promise((r) => setTimeout(r, 900));
  expect(rec.envs.length).toBe(n);
});

test('a non-demo collector emits coverage with log roots and source status', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    await fs.mkdir(root.path, { recursive: true });
    const collector = new Collector({ host: 'mac', roots: [root] });
    const rec = recorder();
    collector.subscribe(rec.fn);
    await collector.start();
    try {
      await rec.until(() => rec.envs.some((e) => e.kind === 'coverage'), 3000, 'coverage');
      const cov = rec.envs.find((e) => e.kind === 'coverage') as Extract<Envelope, { kind: 'coverage' }>;
      expect(cov.coverage.logRoots).toEqual([root.path]);
      expect(cov.coverage.sources[`logs:${root.path}`]).toEqual({ state: 'ok' });
      expect(collector.coverage().sources[`logs:${root.path}`]).toEqual({ state: 'ok' });
    } finally {
      await collector.stop();
    }
  });
});

// DemoSource

const VALID_KINDS = new Set(['agent.upsert', 'agent.status', 'tool.start', 'tool.update', 'tool.output', 'tool.end', 'fs.change']);

function runDemo(scale: 'normal' | 'stress', ms: number): Promise<SourceEvent[]> {
  const events: SourceEvent[] = [];
  const source = new DemoSource({ host: 'h', scale, emit: (batch) => events.push(...batch) });
  source.start();
  return new Promise((resolve) => setTimeout(() => { source.stop(); resolve(events); }, ms));
}

test('DemoSource normal: valid events, paths under /demo, tools Read/Edit/Bash/Grep', async () => {
  const events = await runDemo('normal', 4500);
  const upserts = events.filter((e) => e.kind === 'agent.upsert');
  expect(upserts).toHaveLength(6);
  expect(new Set(upserts.map((e) => (e.kind === 'agent.upsert' ? e.agent.id : ''))).size).toBe(6);
  for (const e of events) {
    expect(VALID_KINDS.has(e.kind)).toBe(true);
    expect(Number.isNaN(Date.parse(e.ts))).toBe(false);
  }
  const starts = events.flatMap((e) => (e.kind === 'tool.start' ? [e.call] : []));
  expect(starts.length).toBeGreaterThan(5);
  expect(new Set(starts.map((c) => c.category)).size).toBeGreaterThanOrEqual(3);
  for (const c of starts) for (const f of c.files ?? []) expect(f.startsWith('/demo/agent-view/') || f.startsWith('/demo/search-service/')).toBe(true);
  // Every started call ends, except calls still open when the source stopped (at most one per agent).
  const ended = new Set(events.flatMap((e) => (e.kind === 'tool.end' ? [`${e.agentId}|${e.callId}`] : [])));
  expect(starts.filter((c) => !ended.has(`${c.agentId}|${c.callId}`)).length).toBeLessThanOrEqual(6);
  // Confirmed writes appear only on edit ends that report ok.
  for (const e of events) if (e.kind === 'tool.end' && e.writes) expect(e.ok).toBe(true);
});

test('DemoSource stress: 50 agents, 3,000 distinct files, about 200 events per second', async () => {
  expect(new Set(demoFilePaths('stress')).size).toBe(3000);
  const events = await runDemo('stress', 2000);
  expect(events.filter((e) => e.kind === 'agent.upsert')).toHaveLength(50);
  const roots = new Set(events.flatMap((e) => (e.kind === 'agent.upsert' ? [e.agent.projectRoot] : [])));
  expect(roots.size).toBeGreaterThanOrEqual(4);
  const live = events.filter((e) => e.kind !== 'agent.upsert').length;
  const rate = live / 2;
  expect(rate).toBeGreaterThan(120);
  expect(rate).toBeLessThan(320);
  for (const e of events) expect(Number.isNaN(Date.parse(e.ts))).toBe(false);
});

test('DemoSource stop halts emission', async () => {
  const events: SourceEvent[] = [];
  const source = new DemoSource({ host: 'h', emit: (b) => events.push(...b) });
  source.start();
  await new Promise((r) => setTimeout(r, 600));
  source.stop();
  source.stop();
  const n = events.length;
  await new Promise((r) => setTimeout(r, 1500));
  expect(events.length).toBe(n);
});
