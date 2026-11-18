import { expect, test } from 'bun:test';
import fs from 'node:fs/promises';
import path from 'node:path';
import type { LogRoot } from '../../src/collector/discovery';
import { LogSource } from '../../src/collector/sources/log-source';
import type { SourceEvent, SourceState } from '../../src/shared/schema';
import type { watchTree } from '../../src/collector/watch';

const TMP = path.join(import.meta.dir, '..', '.tmp');

async function withDir(fn: (dir: string) => Promise<void>): Promise<void> {
  await fs.mkdir(TMP, { recursive: true });
  const dir = await fs.mkdtemp(path.join(TMP, 'logsrc-'));
  try {
    await fn(await fs.realpath(dir));
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
}

const toolLine = (id: string, file = '/project/a.ts') =>
  JSON.stringify({
    sessionId: 'root', cwd: '/project', timestamp: '2026-01-01T00:00:01.000Z', type: 'assistant',
    message: { content: [{ type: 'tool_use', id, name: 'Read', input: { file_path: file } }] },
  }) + '\n';

const noWatch: typeof watchTree = () => ({ close() {}, degraded: null });

/** Wires a LogSource to recorders and offers event-based waits. */
function harness(roots: LogRoot[], extra: Partial<ConstructorParameters<typeof LogSource>[0]> = {}) {
  const events: SourceEvent[] = [];
  const statuses: [string, SourceState][] = [];
  const waiters: { ok: () => boolean; done: () => void }[] = [];
  const poke = () => { for (const w of [...waiters]) if (w.ok()) { waiters.splice(waiters.indexOf(w), 1); w.done(); } };
  const source = new LogSource({
    // Unless a test says otherwise, the safety rescan is as fast with healthy watchers as without.
    host: 'mac', roots, pollMs: 50, rescanMs: 100, healthyRescanMs: extra.healthyRescanMs ?? extra.rescanMs ?? 100,
    emit: (batch) => { events.push(...batch); poke(); },
    onStatus: (s, st) => { statuses.push([s, st]); poke(); },
    ...extra,
  });
  const until = (ok: () => boolean, ms = 3000, what = 'condition'): Promise<void> => {
    if (ok()) return Promise.resolve();
    return new Promise((resolve, reject) => {
      // Emit and status pokes wake the waiter at once. The interval covers conditions that raise no event.
      const timer = setTimeout(() => { clearInterval(tick); reject(new Error(`timeout waiting for ${what}`)); }, ms);
      const tick = setInterval(poke, 20);
      waiters.push({ ok, done: () => { clearTimeout(timer); clearInterval(tick); resolve(); } });
    });
  };
  const toolStarts = () => events.filter((e) => e.kind === 'tool.start').map((e) => (e as Extract<SourceEvent, { kind: 'tool.start' }>).call.callId);
  return { source, events, statuses, until, toolStarts };
}

const claudeRoot = (dir: string): LogRoot => ({ provider: 'claude', path: path.join(dir, 'projects'), scope: 'default' });

test('LogSource emits Claude events within 1 second of an append (fs.watch path)', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    const log = path.join(root.path, 'proj', 'main.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log, toolLine('t0'));
    // The poll is far slower than the budget, so only the watcher can meet it.
    const h = harness([root], { pollMs: 60_000, rescanMs: 60_000 });
    await h.source.start();
    try {
      expect(h.toolStarts()).toEqual(['t0']);
      const t = performance.now();
      await fs.appendFile(log, toolLine('t1'));
      await h.until(() => h.toolStarts().includes('t1'), 1000, 'tool.start t1');
      expect(performance.now() - t).toBeLessThan(1000);
      expect(h.events.find((e) => e.kind === 'tool.start' && e.call.callId === 't1')).toMatchObject({
        call: { agentId: 'mac/claude:default:root', category: 'read', files: ['/project/a.ts'] },
      });
    } finally {
      h.source.stop();
    }
  });
});

test('LogSource safety poll picks up appends when watch callbacks are suppressed', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    const log = path.join(root.path, 'proj', 'main.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log, toolLine('t0'));
    const h = harness([root], { watch: noWatch, rescanMs: 60_000 });
    await h.source.start();
    try {
      await fs.appendFile(log, toolLine('t1'));
      await h.until(() => h.toolStarts().includes('t1'), 2000, 'poll to pick up t1');
    } finally {
      h.source.stop();
    }
  });
});

test('LogSource finds a new log file after a rescan, with the watcher suppressed and with it active', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    await fs.mkdir(path.join(root.path, 'proj'), { recursive: true });
    const polled = harness([root], { watch: noWatch });
    await polled.source.start();
    try {
      await fs.writeFile(path.join(root.path, 'proj', 'new.jsonl'), toolLine('n1'));
      await polled.until(() => polled.toolStarts().includes('n1'), 2000, 'rescan to find n1');
    } finally {
      polled.source.stop();
    }
    // With the watcher active and a slow rescan, the new file arrives through the watcher-triggered rescan.
    const watched = harness([root], { pollMs: 60_000, rescanMs: 60_000 });
    await watched.source.start();
    try {
      await fs.mkdir(path.join(root.path, 'other'), { recursive: true });
      await fs.writeFile(path.join(root.path, 'other', 'new2.jsonl'), toolLine('n2'));
      await watched.until(() => watched.toolStarts().includes('n2'), 1500, 'watcher-triggered rescan to find n2');
    } finally {
      watched.source.stop();
    }
  });
});

test('I5: with healthy watchers the safety rescan is slow; a degraded watcher brings back the fast rescan', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    await fs.mkdir(path.join(root.path, 'proj'), { recursive: true });
    // The watcher looks healthy but reports nothing, so only a periodic rescan can find a new file.
    const healthy = harness([root], { watch: noWatch, rescanMs: 50, healthyRescanMs: 60_000 });
    await healthy.source.start();
    try {
      await fs.writeFile(path.join(root.path, 'proj', 'quiet.jsonl'), toolLine('q1'));
      await Bun.sleep(300); // 6x the degraded interval
      expect(healthy.toolStarts()).toEqual([]);
    } finally {
      healthy.source.stop();
    }
    const degradedWatch: typeof watchTree = () => ({ close() {}, degraded: 'watcher error' });
    const degraded = harness([root], { watch: degradedWatch, rescanMs: 50, healthyRescanMs: 60_000 });
    await degraded.source.start();
    try {
      await fs.writeFile(path.join(root.path, 'proj', 'loud.jsonl'), toolLine('l1'));
      await degraded.until(() => degraded.toolStarts().includes('l1'), 2000, 'fast rescan to find l1');
    } finally {
      degraded.source.stop();
    }
  });
});

test('LogSource never reads one file twice at once: a burst of appends emits each record once', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    const log = path.join(root.path, 'proj', 'main.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log, toolLine('t0'));
    const h = harness([root], { pollMs: 1, rescanMs: 60_000 });
    await h.source.start();
    try {
      const total = 150;
      for (let i = 1; i <= total; i++) {
        await fs.appendFile(log, toolLine(`b${i}`));
        if (i % 10 === 0) await Bun.sleep(1);
      }
      await h.until(() => h.toolStarts().includes(`b${total}`), 3000, 'last burst record');
      await Bun.sleep(100);
      const ids = h.toolStarts();
      expect(ids.length).toBe(total + 1);
      expect(new Set(ids).size).toBe(total + 1);
    } finally {
      h.source.stop();
    }
  });
});

test('LogSource picks the adapter from the root provider', async () => {
  await withDir(async (dir) => {
    const root: LogRoot = { provider: 'codex', path: path.join(dir, 'sessions'), scope: 'work' };
    const log = path.join(root.path, '2026', 'rollout.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log, JSON.stringify({ type: 'session_meta', timestamp: '2026-01-01T00:00:01.000Z', payload: { id: 'main', cwd: '/project' } }) + '\n');
    const h = harness([root]);
    await h.source.start();
    try {
      const upsert = h.events.find((e) => e.kind === 'agent.upsert');
      expect(upsert).toMatchObject({ agent: { id: 'mac/codex:work:main', provider: 'codex' } });
    } finally {
      h.source.stop();
    }
  });
});

test('LogSource forgets a file that drops out of the selection', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    const log = path.join(root.path, 'proj', 'main.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log, toolLine('t0'));
    const h = harness([root], { sinceMs: 60_000, watch: noWatch });
    await h.source.start();
    try {
      expect(h.source.coverage()).toMatchObject({ logsOpen: 1, logsDiscovered: 1 });
      const old = new Date(Date.now() - 3600_000);
      await fs.utimes(log, old, old);
      await h.until(() => h.source.coverage().logsOpen === 0, 2000, 'file to drop out');
      expect(h.source.coverage()).toMatchObject({ logsOpen: 0, logsDiscovered: 0 });
    } finally {
      h.source.stop();
    }
  });
});

test('LogSource coverage lists roots and counts malformed records', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    const log = path.join(root.path, 'proj', 'main.jsonl');
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log, 'not json\n' + toolLine('t0'));
    const h = harness([root]);
    await h.source.start();
    try {
      expect(h.source.coverage()).toEqual({ logRoots: [root.path], logsOpen: 1, logsDiscovered: 1, malformedRecords: 1 });
    } finally {
      h.source.stop();
    }
  });
});

test('LogSource reports source.status degraded for an unreadable root and keeps running', async () => {
  if (process.getuid?.() === 0) return; // root ignores permission bits
  await withDir(async (dir) => {
    const bad: LogRoot = { provider: 'codex', path: path.join(dir, 'locked'), scope: 'default' };
    const good = claudeRoot(dir);
    const missing: LogRoot = { provider: 'codex', path: path.join(dir, 'missing'), scope: 'x' };
    const log = path.join(good.path, 'proj', 'main.jsonl');
    await fs.mkdir(bad.path, { recursive: true });
    await fs.mkdir(path.dirname(log), { recursive: true });
    await fs.writeFile(log, toolLine('t0'));
    await fs.chmod(bad.path, 0o000);
    const h = harness([bad, good, missing]);
    try {
      await h.source.start();
      const key = `logs:${bad.path}`;
      expect(h.statuses.filter(([s]) => s === key)).toEqual([[key, { state: 'degraded', detail: 'EACCES' }]]);
      expect(h.statuses.some(([s]) => s === `logs:${missing.path}`)).toBe(false);
      // The healthy root keeps working.
      expect(h.toolStarts()).toEqual(['t0']);
      await fs.appendFile(log, toolLine('t1'));
      await h.until(() => h.toolStarts().includes('t1'), 2000, 'healthy root append');
      // The root recovers.
      await fs.chmod(bad.path, 0o755);
      await h.until(() => h.statuses.some(([s, st]) => s === key && st.state === 'ok'), 3000, 'root to recover');
      expect(h.statuses.filter(([s]) => s === key).map(([, st]) => st.state)).toEqual(['degraded', 'ok']);
    } finally {
      h.source.stop();
      await fs.chmod(bad.path, 0o755);
    }
  });
});

/** Track every interval and recursive watcher so a test can prove stop() released them. */
function trackResources() {
  const intervals = new Set<unknown>();
  const realSet = globalThis.setInterval;
  const realClear = globalThis.clearInterval;
  globalThis.setInterval = ((...args: Parameters<typeof setInterval>) => {
    const h = realSet(...args);
    intervals.add(h);
    return h;
  }) as typeof setInterval;
  globalThis.clearInterval = ((h?: Parameters<typeof clearInterval>[0]) => {
    intervals.delete(h);
    return realClear(h);
  }) as typeof clearInterval;
  let opened = 0;
  let closed = 0;
  const watch: typeof watchTree = () => {
    opened++;
    return { close() { closed++; }, degraded: null };
  };
  return {
    watch,
    liveIntervals: () => intervals.size,
    liveWatchers: () => opened - closed,
    restore: () => { globalThis.setInterval = realSet; globalThis.clearInterval = realClear; for (const h of intervals) realClear(h as never); },
  };
}

test('stop() during start() leaves no timers and no watchers, and emits nothing afterwards', async () => {
  await withDir(async (dir) => {
    const root = claudeRoot(dir);
    for (const n of ['a', 'b', 'c']) {
      const log = path.join(root.path, 'proj', `${n}.jsonl`);
      await fs.mkdir(path.dirname(log), { recursive: true });
      await fs.writeFile(log, toolLine(`t-${n}`));
    }
    const res = trackResources();
    try {
      // Stop while the initial scan is still running.
      const early = harness([root], { watch: res.watch });
      const starting = early.source.start();
      early.source.stop();
      await starting;
      await Bun.sleep(200);
      expect(early.events).toEqual([]);
      expect(res.liveIntervals()).toBe(0);
      expect(res.liveWatchers()).toBe(0);

      // Stop while the initial reads are running: the first batch is the last.
      let source: LogSource | undefined;
      const batches: SourceEvent[][] = [];
      source = new LogSource({
        host: 'mac', roots: [root], pollMs: 50, rescanMs: 100, watch: res.watch,
        emit: (b) => { batches.push(b); source!.stop(); },
        onStatus: () => {},
      });
      await source.start();
      await Bun.sleep(200);
      expect(batches.length).toBe(1);
      expect(res.liveIntervals()).toBe(0);
      expect(res.liveWatchers()).toBe(0);
    } finally {
      res.restore();
    }
  });
});
