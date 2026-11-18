import { expect, test } from 'bun:test';
import os from 'node:os';
import path from 'node:path';
import { parseArgs, UsageError } from '../src/cli';
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
  expect(envs[0]).toMatchObject({ v: 1, host: 'demo-box', kind: 'hello', demo: true, seq: 1 });
  expect(envs[1]?.kind).toBe('snapshot');
  expect(envs.slice(2).some((e) => e.kind === 'agent.upsert')).toBe(true);
  for (let i = 1; i < envs.length; i++) expect(envs[i]!.seq).toBeGreaterThan(envs[i - 1]!.seq);
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
