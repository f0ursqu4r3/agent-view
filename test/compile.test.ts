import { afterAll, beforeAll, expect, test } from 'bun:test';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { LineSplitter, parseLine } from '../src/shared/ndjson';

const ROOT = path.join(import.meta.dir, '..');
const SCRIPT = path.join(ROOT, 'scripts', 'compile.ts');
const TMP_PARENT = path.join(import.meta.dir, '.tmp');

// A compiled binary must embed the viewer and run from any directory. These tests build the
// binary for the current platform and run it from a directory outside the repository.
const arch = process.arch === 'arm64' ? 'arm64' : 'x64';
const platform = process.platform === 'darwin' ? 'darwin' : 'linux';
const target = `bun-${platform}-${arch}`;

let outDir = '';
let outsideCwd = '';
let bin = '';
const running: Array<ReturnType<typeof Bun.spawn>> = [];

beforeAll(async () => {
  fs.mkdirSync(TMP_PARENT, { recursive: true });
  outDir = fs.mkdtempSync(path.join(TMP_PARENT, 'compile-'));
  outsideCwd = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-view-cwd-'));
  const build = Bun.spawn(['bun', SCRIPT, target], {
    cwd: ROOT,
    env: { ...process.env, AGENT_VIEW_DIST: outDir },
    stdout: 'pipe',
    stderr: 'pipe',
  });
  const [out, err, code] = await Promise.all([new Response(build.stdout).text(), new Response(build.stderr).text(), build.exited]);
  if (code !== 0) throw new Error(`compile failed (${code})\n${out}\n${err}`);
  bin = path.join(outDir, `agent-view-${platform}-${arch}`);
  if (!fs.existsSync(bin)) throw new Error(`compile did not write ${bin}\n${out}`);
}, 300_000);

afterAll(() => {
  for (const child of running) {
    try { child.kill('SIGKILL'); } catch { /* already gone */ }
  }
  if (outDir) fs.rmSync(outDir, { recursive: true, force: true });
  if (outsideCwd) fs.rmSync(outsideCwd, { recursive: true, force: true });
  try { fs.rmdirSync(TMP_PARENT); } catch { /* other tests may use it */ }
});

function spawnBin(args: string[]) {
  const child = Bun.spawn([bin, ...args], { cwd: outsideCwd, stdin: 'ignore', stdout: 'pipe', stderr: 'pipe' });
  running.push(child);
  return child;
}

/** Reads stdout until `stop` returns a value for a complete line. Rejects after `ms`. */
async function readUntil<T>(child: ReturnType<typeof spawnBin>, stop: (line: string) => T | undefined, ms: number): Promise<T> {
  const reader = (child.stdout as ReadableStream<Uint8Array>).getReader();
  const splitter = new LineSplitter();
  const timer = setTimeout(() => void reader.cancel().catch(() => {}), ms);
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) throw new Error('stdout closed (or timed out) before the expected line');
      for (const line of splitter.push(value)) {
        const hit = stop(line);
        if (hit !== undefined) return hit;
      }
    }
  } finally {
    clearTimeout(timer);
    reader.releaseLock();
  }
}

test('compiled binary: collect --stdio --demo writes a hello line first', async () => {
  const child = spawnBin(['collect', '--stdio', '--demo']);
  try {
    const first = await readUntil(child, (line) => line, 20_000);
    const env = parseLine(first);
    expect(env).not.toBeNull();
    expect(env!.kind).toBe('hello');
    expect((env as { demo?: true }).demo).toBe(true);
  } finally {
    child.kill();
    await child.exited;
  }
}, 60_000);

test('compiled binary: hub serves the embedded viewer and its bundled script, and exits 0 on SIGTERM', async () => {
  const child = spawnBin(['--demo', '--no-open', '--port', '0']);
  try {
    const url = await readUntil(child, (line) => /^Agent View: (http:\/\/\S+)$/.exec(line)?.[1], 20_000);
    const page = await fetch(url);
    expect(page.status).toBe(200);
    expect(page.headers.get('content-type') ?? '').toContain('text/html');
    const html = await page.text();
    const src = /<script[^>]+src="([^"]+)"/.exec(html)?.[1];
    expect(src).toBeTruthy();
    const script = await fetch(new URL(src!, url));
    expect(script.status).toBe(200);
    expect(script.headers.get('content-type') ?? '').toMatch(/javascript/);
    expect((await script.text()).length).toBeGreaterThan(1000);
  } finally {
    child.kill('SIGTERM');
  }
  expect(await child.exited).toBe(0);
}, 60_000);
