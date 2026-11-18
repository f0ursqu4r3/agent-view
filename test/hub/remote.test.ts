import { afterEach, expect, test } from 'bun:test';
import path from 'node:path';
import { defaultCommand, RemoteCollector } from '../../src/hub/remote';
import type { Envelope } from '../../src/shared/schema';

const CLI = path.join(import.meta.dir, '..', '..', 'src', 'cli.ts');

type Proc = ReturnType<typeof Bun.spawn>;

const live = new Set<Proc>();
const collectors: RemoteCollector[] = [];
const spawnTimes: number[] = [];

/** Tracks every child so `afterEach` can kill what a test leaves behind. */
const trackedSpawn = ((cmd: string[], opts: Parameters<typeof Bun.spawn>[1]) => {
  const proc = Bun.spawn(cmd, opts);
  live.add(proc);
  spawnTimes.push(Date.now());
  void proc.exited.then(() => live.delete(proc));
  return proc;
}) as unknown as typeof Bun.spawn;

afterEach(async () => {
  await Promise.allSettled(collectors.splice(0).map((c) => c.stop()));
  for (const proc of live) proc.kill('SIGKILL');
  live.clear();
  spawnTimes.length = 0;
});

const demoCommand = (_host: string, label: string): string[] => ['bun', CLI, 'collect', '--stdio', '--demo', '--label', label];

function make(opts: Partial<ConstructorParameters<typeof RemoteCollector>[0]> = {}): RemoteCollector {
  const c = new RemoteCollector({
    sshHost: 'devbox', label: 'devbox', command: demoCommand, backoffMs: [50, 400], spawn: trackedSpawn, ...opts,
  });
  collectors.push(c);
  return c;
}

async function until(cond: () => boolean, ms = 15_000): Promise<void> {
  const deadline = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > deadline) throw new Error('timed out waiting for condition');
    await Bun.sleep(10);
  }
}

function watchStatus(c: RemoteCollector): string[] {
  const seen: string[] = [];
  c.onStatusChange(() => {
    const s = c.status();
    seen.push(s.detail ? `${s.state}:${s.detail}` : s.state);
  });
  return seen;
}

test('remote source parses NDJSON from the child and reports up after hello', async () => {
  const c = make();
  const got: Envelope[] = [];
  c.subscribe((e) => got.push(e));
  const states = watchStatus(c);
  expect(c.status()).toEqual({ label: 'devbox', kind: 'remote', state: 'connecting' });
  await c.start();
  await until(() => c.status().state === 'up' && got.some((e) => e.kind === 'snapshot'));
  expect(got[0]).toMatchObject({ v: 1, kind: 'hello', host: 'devbox' });
  expect(got.some((e) => e.kind === 'snapshot')).toBe(true);
  expect(states).toEqual(['up']);
  expect(c.kind).toBe('remote');
});

test('child exit triggers reconnect with backoff and a fresh hello + snapshot', async () => {
  const c = make();
  const got: Envelope[] = [];
  c.subscribe((e) => got.push(e));
  const states = watchStatus(c);
  await c.start();
  await until(() => c.status().state === 'up');
  const first = [...live][0]!;
  first.kill('SIGTERM');
  await until(() => states.filter((s) => s === 'up').length >= 2 && got.filter((e) => e.kind === 'snapshot').length >= 2, 20_000);
  expect(states[0]).toBe('up');
  expect(states[1]).toStartWith('down:collector exited');
  expect(states[2]).toBe('connecting');
  expect(states[3]).toBe('up');
  expect(got.filter((e) => e.kind === 'hello').length).toBe(2);
  expect(got.filter((e) => e.kind === 'snapshot').length).toBeGreaterThanOrEqual(2);
  expect(spawnTimes.length).toBe(2);
});

test('Review Focus 5: exit 127 sets down with "agent-view not found on devbox" and retries with growing delay', async () => {
  const c = make({ command: () => ['sh', '-c', 'exit 127'] });
  const states = watchStatus(c);
  await c.start();
  await until(() => spawnTimes.length >= 4);
  expect(states).toContain('down:agent-view not found on devbox');
  expect(c.status()).toMatchObject({ state: expect.stringMatching(/down|connecting/) });
  const gaps = spawnTimes.slice(1).map((t, i) => t - spawnTimes[i]!);
  expect(gaps[0]!).toBeGreaterThanOrEqual(45);
  expect(gaps[1]!).toBeGreaterThan(gaps[0]!);
  expect(gaps[2]!).toBeGreaterThan(gaps[1]!);
});

test('exit 255 reports the last stderr line; other codes report the code', async () => {
  const ssh = make({ command: () => ['sh', '-c', 'echo first >&2; echo "  Permission denied (publickey).  " >&2; exit 255'], backoffMs: [5000, 5000] });
  await ssh.start();
  await until(() => ssh.status().state === 'down');
  expect(ssh.status().detail).toBe('ssh failed: Permission denied (publickey).');

  const other = make({ command: () => ['sh', '-c', 'exit 3'], backoffMs: [5000, 5000] });
  await other.start();
  await until(() => other.status().state === 'down');
  expect(other.status().detail).toBe('collector exited 3');
});

test('malformed child lines are skipped and counted, stream continues', async () => {
  const hello = '{"v":1,"kind":"hello","host":"x","seq":0,"ts":"2026-01-01T00:00:00.000Z","collectorVersion":"0","platform":"t","now":"2026-01-01T00:00:00.000Z"}';
  const lines = [
    'not json', '[1,2]', '{"v":2,"kind":"hello","host":"x","seq":0,"ts":"t"}', '{"v":1,"kind":"hello"}',
    '{"v":1,"kind":"hello","host":"x","seq":"1","ts":"t"}', hello,
    '{"v":1,"kind":"agent.upsert","host":"x","seq":1,"ts":"2026-01-01T00:00:01.000Z"}',
  ];
  const script = `${lines.map((l) => `printf '%s\\n' '${l}'`).join('; ')}; exec sleep 30`;
  const c = make({ command: () => ['sh', '-c', script] });
  const got: Envelope[] = [];
  c.subscribe((e) => got.push(e));
  await c.start();
  await until(() => got.length >= 2);
  expect(got.map((e) => e.kind)).toEqual(['hello', 'agent.upsert']);
  expect(c.status().state).toBe('up');
  expect(c.malformedLines).toBe(5);
});

test('stop resolves after the child exits and never respawns', async () => {
  const c = make();
  await c.start();
  await until(() => c.status().state === 'up');
  const proc = [...live][0]!;
  await c.stop();
  expect(proc.exitCode !== null || proc.signalCode !== null).toBe(true);
  expect(c.status().state).toBe('down');
  const spawned = spawnTimes.length;
  await Bun.sleep(300);
  expect(spawnTimes.length).toBe(spawned);
  expect(live.size).toBe(0);
});

test('stop during backoff cancels the retry timer', async () => {
  const c = make({ command: () => ['sh', '-c', 'exit 1'], backoffMs: [200, 200] });
  await c.start();
  await until(() => c.status().state === 'down');
  await c.stop();
  const spawned = spawnTimes.length;
  await Bun.sleep(400);
  expect(spawnTimes.length).toBe(spawned);
});

test('a spawn that throws sets down and retries', async () => {
  let calls = 0;
  const failing = (() => { calls++; throw new Error('ENOENT: ssh'); }) as unknown as typeof Bun.spawn;
  const c = make({ spawn: failing });
  await c.start();
  await until(() => calls >= 2);
  expect(c.status().detail).toContain('ENOENT');
});

test('an oversize line without a newline is dropped and counted once; the stream continues', async () => {
  const hello = '{"v":1,"kind":"hello","host":"x","seq":0,"ts":"t","collectorVersion":"0","platform":"t","now":"t"}';
  const small = (n: number): string => `{"v":1,"kind":"agent.upsert","host":"x","seq":${n},"ts":"t"}`;
  const script = [
    `printf '%s\\n' '${hello}'`,
    `printf '{"v":1,"kind":"agent.upsert","host":"x","seq":1,"ts":"t","pad":"'; head -c 300000 /dev/zero | tr '\\0' x; printf '"}\\n'`,
    `printf '%s\\n' '${small(2)}'`,
    'exec sleep 30',
  ].join('; ');
  const c = make({ command: () => ['sh', '-c', script], maxLineBytes: 1024 });
  const got: Envelope[] = [];
  c.subscribe((e) => got.push(e));
  await c.start();
  await until(() => got.length >= 2);
  expect(got.map((e) => `${e.kind}:${e.seq}`)).toEqual(['hello:0', 'agent.upsert:2']);
  expect(c.malformedLines).toBe(1);
});

const BAD_HOSTS = ['-oProxyCommand=x', 'host;id', 'a b', '$(x)', '`id`', '', 'a\nb'];

test('constructor rejects ssh host values that could inject options or shell text', () => {
  for (const sshHost of BAD_HOSTS) expect(() => new RemoteCollector({ sshHost, label: 'x' })).toThrow();
  expect(() => new RemoteCollector({ sshHost: 'user@host.example', label: 'user@host.example' })).not.toThrow();
  expect(() => new RemoteCollector({ sshHost: '_h-1.example:22', label: 'x' })).not.toThrow();
});

test('default command passes the label single-quoted for the remote shell', () => {
  const cmd = defaultCommand('user@host.example', 'user@host.example');
  expect(cmd.slice(0, 7)).toEqual(['ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ServerAliveInterval=15', 'user@host.example']);
  expect(cmd.slice(7, 12)).toEqual(['agent-view', 'collect', '--stdio', '--label', "'user@host.example'"]);
  expect(cmd.at(-1)).toBe("'user@host.example'");
  expect(defaultCommand('h', "a'b;id").at(-1)).toBe("'a'\\''b;id'");
});
