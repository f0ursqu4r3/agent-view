#!/usr/bin/env bun
/**
 * Parity check of the gitignore matcher: `bun test/parity/ignore-cases.ts`.
 * For every row of testdata/gitignore-cases.json, creates the row's files in a fresh temporary root and prints
 * `{"name": ..., "ignored": ...}` with the TypeScript `GitignoreMatcher` result (ignore 7.0.16), one line per row.
 * internal/parity compares each line with the Go matcher and with the stored expected value.
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { GitignoreMatcher } from '../../src/collector/sources/fs-changes';

type Row = { name: string; files: Record<string, string>; path: string; ignored: boolean };

const table = path.join(import.meta.dir, '..', '..', 'testdata', 'gitignore-cases.json');
const rows = JSON.parse(fs.readFileSync(table, 'utf8')) as Row[];
const lines: string[] = [];
for (const row of rows) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-view-ignore-'));
  try {
    for (const [rel, text] of Object.entries(row.files)) {
      const file = path.join(root, ...rel.split('/'));
      fs.mkdirSync(path.dirname(file), { recursive: true });
      fs.writeFileSync(file, text);
    }
    const ignored = new GitignoreMatcher(root).isIgnored(path.join(root, ...row.path.split('/')));
    lines.push(JSON.stringify({ name: row.name, ignored }));
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
}
await Bun.write(Bun.stdout, lines.map((l) => `${l}\n`).join(''));
