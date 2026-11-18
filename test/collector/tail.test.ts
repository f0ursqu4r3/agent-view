import { expect, test } from 'bun:test';
import fs from 'node:fs/promises';
import path from 'node:path';
import { LogTailer } from '../../src/collector/tail';

const TMP = path.join(import.meta.dir, '..', '.tmp');

async function withDir(fn: (dir: string) => Promise<void>): Promise<void> {
  await fs.mkdir(TMP, { recursive: true });
  const dir = await fs.mkdtemp(path.join(TMP, 'tail-'));
  try {
    await fn(dir);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
}

function collect(opts: { limit?: number } = {}) {
  const records: unknown[] = [];
  const warnings: string[] = [];
  const t = new LogTailer((r) => records.push(r), { ...opts, onWarning: (m) => warnings.push(m) });
  return { t, records, warnings };
}

test('tailer waits for complete JSON lines and preserves split UTF-8', async () => {
  await withDir(async (dir) => {
    const file = path.join(dir, 'a.jsonl');
    const { t, records } = collect();
    const line = Buffer.from(JSON.stringify({ type: 'session_meta', payload: { id: 'test', cwd: '/project/日本' } }) + '\n');
    const cut = line.indexOf(Buffer.from('日')) + 1;
    await fs.writeFile(file, line.subarray(0, cut));
    await t.read(file);
    expect(records).toEqual([]);
    await fs.appendFile(file, line.subarray(cut));
    await t.read(file);
    expect(records).toEqual([{ type: 'session_meta', payload: { id: 'test', cwd: '/project/日本' } }]);
    expect(t.malformed).toBe(0);
    await t.read(file);
    expect(records.length).toBe(1);
    expect(t.openFiles).toBe(1);
    t.forget(file);
    expect(t.openFiles).toBe(0);
  });
});

test('replaced file (new inode) and truncated file restart from offset 0', async () => {
  await withDir(async (dir) => {
    const file = path.join(dir, 'a.jsonl');
    const { t, records } = collect();
    await fs.writeFile(file, '{"n":1}\n{"n":2}\n');
    await t.read(file);
    expect(records).toEqual([{ n: 1 }, { n: 2 }]);
    // Replace: write a new file and rename over the old one (new inode, larger size).
    const other = path.join(dir, 'b.jsonl');
    await fs.writeFile(other, '{"n":3}\n{"n":4}\n{"n":5}\n');
    await fs.rename(other, file);
    await t.read(file);
    expect(records.slice(2)).toEqual([{ n: 3 }, { n: 4 }, { n: 5 }]);
    // Truncate in place (same inode, smaller size).
    await fs.truncate(file, 0);
    await fs.writeFile(file, '{"n":6}\n');
    await t.read(file);
    expect(records.slice(5)).toEqual([{ n: 6 }]);
  });
});

test('large file loads first line plus last limit bytes and skips the cut line', async () => {
  await withDir(async (dir) => {
    const file = path.join(dir, 'big.jsonl');
    const { t, records, warnings } = collect({ limit: 1024 });
    const mk = (i: number) => {
      const head = `{"i":${i},"pad":"`;
      const tail = '"}\n';
      return head + 'x'.repeat(100 - head.length - tail.length) + tail;
    };
    const header = JSON.stringify({ header: true }) + '\n';
    const lines = Array.from({ length: 50 }, (_, i) => mk(i));
    for (const l of lines) expect(Buffer.byteLength(l)).toBe(100);
    await fs.writeFile(file, header + lines.join(''));
    const size = Buffer.byteLength(header) + 5000;
    const offset = size - 1024;
    await t.read(file);
    // First line starting at or after offset is the first line after the cut line.
    const starts = lines.map((_, i) => Buffer.byteLength(header) + i * 100);
    const firstFull = starts.findIndex((s) => s > offset); // cut line is skipped even if a boundary
    const expected = lines.slice(firstFull).map((_, k) => ({ i: firstFull + k }));
    expect(records[0]).toEqual({ header: true });
    expect(records.slice(1).map((r) => ({ i: (r as { i: number }).i }))).toEqual(expected);
    expect(records.length).toBe(1 + (50 - firstFull));
    expect(warnings).toEqual(['Large logs load only their header and last 4 MiB. Earlier tool calls may be missing.']);
    // Another read adds nothing and does not warn again.
    await t.read(file);
    expect(warnings.length).toBe(1);
    expect(records.length).toBe(1 + (50 - firstFull));
  });
});

test('malformed lines are counted, not thrown', async () => {
  await withDir(async (dir) => {
    const file = path.join(dir, 'a.jsonl');
    const { t, records } = collect();
    await fs.writeFile(file, '{"a":1}\n{oops\n\n{"a":2}\n');
    await t.read(file);
    expect(records).toEqual([{ a: 1 }, { a: 2 }]);
    expect(t.malformed).toBe(1);
  });
});

test('a record larger than the limit is skipped with one warning', async () => {
  await withDir(async (dir) => {
    const file = path.join(dir, 'a.jsonl');
    const { t, records, warnings } = collect({ limit: 1024 });
    const huge = JSON.stringify({ blob: 'y'.repeat(5000) }) + '\n';
    await fs.writeFile(file, '{"a":1}\n' + huge.slice(0, 900));
    await t.read(file);
    await fs.appendFile(file, huge.slice(900) + '{"a":2}\n');
    await t.read(file);
    expect(records).toEqual([{ a: 1 }, { a: 2 }]);
    expect(warnings).toEqual(['A log record exceeded 1024 bytes and was skipped.']);
    expect(t.malformed).toBe(0);
  });
});
