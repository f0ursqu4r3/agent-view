import { expect, test } from 'bun:test';
import os from 'node:os';
import path from 'node:path';
import { parseArgs, remoteCommandOverride, UsageError } from '../src/cli';
import { LineSplitter, parseLine } from '../src/shared/ndjson';
import type { Envelope } from '../src/shared/schema';

const CLI = path.join(import.meta.dir, '..', 'src', 'cli.ts');

test('parseArgs: defaults, --remote repeatable, --linger minutes → ms, collect --stdio --label', () => {
  expect(parseArgs([])).toMatchObject({ mode: 'hub', port: 4317, open: true, lingerMs: 600_000, remotes: [] });
  expect(parseArgs([])).toEqual({
    mode: 'hub', remotes: [], demo: false, demoScale: 'normal', port: 4317, open: true, watch: [], lingerMs: 600_000, label: null,
  });
  expect(parseArgs(['collect', '--stdio', '--label', 'devbox'])).toMatchObject({ mode: 'collect', label: 'devbox' });
  expect(parseArgs(['collect', '--stdio'])).toMatchObject({ mode: 'collect', label: null });
  expect(parseArgs(['--remote', 'a', '--remote', 'b'])).toMatchObject({ remotes: ['a', 'b'] });
  expect(parseArgs(['--linger', '2'])).toMatchObject({ lingerMs: 120_000 });
  expect(parseArgs(['--port', '0', '--no-open', '--demo', '--demo-scale', 'stress'])).toMatchObject({
    port: 0, open: false, demo: true, demoScale: 'stress',
  });
  expect(parseArgs(['--watch', 'rel/dir', '--watch', '/abs/dir']).watch).toEqual([path.resolve('rel/dir'), '/abs/dir']);
  expect(parseArgs(['collect', '--stdio', '--demo', '--demo-scale', 'stress'])).toMatchObject({ mode: 'collect', demo: true, demoScale: 'stress' });
});

test('parseArgs rejects --remote values that could inject ssh options or shell text', () => {
  for (const bad of ['-oProxyCommand=x', 'host;id', 'a b', '$(x)', '`id`', '', 'a\u001b[31mb']) {
    let err: unknown;
    try { parseArgs(['--remote', bad]); } catch (e) { err = e; }
    expect(err).toBeInstanceOf(UsageError);
    expect((err as Error).message).toContain('--remote');
    expect((err as Error).message).not.toContain(bad || '\u0000');
  }
  expect(parseArgs(['--remote', 'user@host.example']).remotes).toEqual(['user@host.example']);
});

test('remoteCommandOverride: unset is null; a JSON array of strings is the argv; anything else is an error', () => {
  expect(remoteCommandOverride(undefined)).toBeNull();
  expect(remoteCommandOverride('')).toBeNull();
  expect(remoteCommandOverride('["sh","-c","exit 3"]')).toEqual(['sh', '-c', 'exit 3']);
  for (const bad of ['sh', '[]', '[1]', '{"a":1}', 'null']) expect(() => remoteCommandOverride(bad)).toThrow(UsageError);
});

test('parseArgs rejects unknown flags, bad values, and collect without --stdio', () => {
  expect(() => parseArgs(['--nope'])).toThrow(UsageError);
  expect(() => parseArgs(['collect'])).toThrow(UsageError);
  expect(() => parseArgs(['serve'])).toThrow(UsageError);
  expect(() => parseArgs(['--port'])).toThrow(UsageError);
  expect(() => parseArgs(['--port', 'abc'])).toThrow(UsageError);
  expect(() => parseArgs(['--port', '70000'])).toThrow(UsageError);
  expect(() => parseArgs(['--linger', '0'])).toThrow(UsageError);
  expect(() => parseArgs(['--demo-scale', 'huge'])).toThrow(UsageError);
  expect(() => parseArgs(['--label', 'x'])).toThrow(UsageError);
  expect(() => parseArgs(['--stdio'])).toThrow(UsageError);
});

test('`bun src/cli.ts collect --stdio --demo` writes valid NDJSON: hello, snapshot, events', async () => {
  const proc = Bun.spawn(['bun', CLI, 'collect', '--stdio', '--demo', '--label', 'demo-box'], { stdout: 'pipe', stderr: 'pipe' });
  const splitter = new LineSplitter();
  const envs: Envelope[] = [];
  const reader = proc.stdout.getReader();
  const deadline = Date.now() + 15_000;
  while (envs.length < 5 && Date.now() < deadline) {
    const { value, done } = await reader.read();
    if (done) break;
    for (const line of splitter.push(value)) {
      const env = parseLine(line);
      expect(env).not.toBeNull();
      envs.push(env!);
    }
  }
  expect(envs.length).toBeGreaterThanOrEqual(5);
  expect(envs[0]).toMatchObject({ v: 1, host: 'demo-box', kind: 'hello', demo: true, seq: 0 });
  expect(envs[1]?.kind).toBe('snapshot');
  expect(envs.slice(2).some((e) => e.kind === 'agent.upsert')).toBe(true);
  expect(envs[1]!.seq).toBe(0);
  for (let i = 2; i < envs.length; i++) expect(envs[i]!.seq).toBeGreaterThan(envs[i - 1]!.seq);
  proc.kill('SIGTERM');
  expect(await proc.exited).toBe(0);
}, 30_000);

test('collect --stdio without --label uses the host name', async () => {
  const proc = Bun.spawn(['bun', CLI, 'collect', '--stdio', '--demo'], { stdout: 'pipe', stderr: 'pipe' });
  const reader = proc.stdout.getReader();
  const splitter = new LineSplitter();
  let hello: Envelope | null = null;
  while (!hello) {
    const { value, done } = await reader.read();
    if (done) break;
    const first = splitter.push(value)[0];
    if (first) hello = parseLine(first);
  }
  expect(hello?.host).toBe(os.hostname());
  proc.kill('SIGINT');
  expect(await proc.exited).toBe(0);
}, 30_000);

test('usage errors print to stderr and exit with code 2', async () => {
  for (const argv of [['collect'], ['--nope']]) {
    const proc = Bun.spawn(['bun', CLI, ...argv], { stdout: 'pipe', stderr: 'pipe' });
    expect(await proc.exited).toBe(2);
    expect(await new Response(proc.stderr).text()).toMatch(/usage/i);
    expect(await new Response(proc.stdout).text()).toBe('');
  }
}, 30_000);

/** Starts hub mode and resolves with the URL it prints. */
async function startHub(extra: string[], env: Record<string, string> = {}) {
  const proc = Bun.spawn(['bun', CLI, '--no-open', '--port', '0', ...extra], { stdout: 'pipe', stderr: 'pipe', env: { ...process.env, ...env } });
  const reader = proc.stdout.getReader();
  const decoder = new TextDecoder();
  let out = '';
  const deadline = Date.now() + 15_000;
  while (!/Agent View: \S+\n/.test(out) && Date.now() < deadline) {
    const { value, done } = await reader.read();
    if (done) break;
    out += decoder.decode(value);
  }
  const url = /Agent View: (\S+)\n/.exec(out)?.[1];
  return { proc, url, out };
}

test('hub mode prints the URL, serves /api/snapshot with the local host up, and exits 0 on SIGTERM', async () => {
  const { proc, url } = await startHub(['--demo']);
  try {
    expect(url).toMatch(/^http:\/\/127\.0\.0\.1:\d+$/);
    const label = os.hostname().split('.')[0];
    let body: { hosts: { label: string; kind: string; state: string }[]; envelopes: Envelope[] } | undefined;
    for (let i = 0; i < 50; i++) {
      body = await (await fetch(url + '/api/snapshot')).json() as typeof body;
      if (body!.hosts[0]?.state === 'up') break;
      await Bun.sleep(100);
    }
    expect(body!.hosts).toEqual([{ label, kind: 'local', state: 'up' }]);
    expect(body!.envelopes[0]).toMatchObject({ host: label, kind: 'hello', demo: true });
    expect(body!.envelopes[1]?.kind).toBe('snapshot');
  } finally {
    proc.kill('SIGTERM');
  }
  expect(await proc.exited).toBe(0);
}, 30_000);

test('hub mode: --remote adds one remote host per flag; an unreachable remote never stops the hub', async () => {
  // The test-only override replaces ssh, so the test never runs ssh through the owner's SSH config.
  const { proc, url } = await startHub(['--demo', '--remote', 'box1.invalid', '--remote', 'box1.invalid'], {
    AGENT_VIEW_REMOTE_COMMAND: JSON.stringify(['sh', '-c', 'exit 3']),
  });
  try {
    let body: { hosts: { label: string; kind: string; state: string; detail?: string }[] } | undefined;
    for (let i = 0; i < 50; i++) {
      body = await (await fetch(url + '/api/snapshot')).json() as typeof body;
      if (body!.hosts[0]?.state === 'up' && body!.hosts.slice(1).every((h) => h.state === 'down')) break;
      await Bun.sleep(100);
    }
    expect(body!.hosts.map((h) => [h.label, h.kind])).toEqual([
      [os.hostname().split('.')[0], 'local'], ['box1.invalid', 'remote'], ['box1.invalid#2', 'remote'],
    ]);
    expect(body!.hosts[0]!.state).toBe('up');
    expect(body!.hosts.slice(1).map((h) => h.detail)).toEqual(['collector exited 3', 'collector exited 3']);
  } finally {
    proc.kill('SIGINT');
  }
  expect(await proc.exited).toBe(0);
  const err = await new Response(proc.stderr).text();
  expect(err).not.toContain('later version');
}, 30_000);
