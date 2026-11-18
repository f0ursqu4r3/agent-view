import fsp from 'node:fs/promises';
import { LineSplitter } from '../shared/ndjson';

export const TAIL_LIMIT = 4 * 1024 * 1024;

const MIB = 1024 * 1024;

interface Cursor {
  offset: number;
  ino: number;
  splitter: LineSplitter;
  /** Discard the next complete line: it is the rest of a cut or oversized record. */
  skipFirst: boolean;
}

function describeLimit(limit: number): string {
  return limit % MIB === 0 ? `${limit / MIB} MiB` : `${limit} bytes`;
}

/**
 * Reads appended bytes from JSONL logs and emits each parsed record.
 * Keeps one cursor per file. Large files load only the header line and the last `limit` bytes.
 */
export class LogTailer {
  malformed = 0;
  private readonly cursors = new Map<string, Cursor>();
  private readonly limit: number;
  private readonly onWarning: (msg: string) => void;

  constructor(
    private readonly onRecord: (record: unknown, file: string) => void,
    opts: { limit?: number; onWarning?: (msg: string) => void } = {},
  ) {
    this.limit = opts.limit ?? TAIL_LIMIT;
    this.onWarning = opts.onWarning ?? (() => {});
  }

  get openFiles(): number {
    return this.cursors.size;
  }

  forget(file: string): void {
    this.cursors.delete(file);
  }

  async read(file: string): Promise<void> {
    const stat = await fsp.stat(file);
    let cursor = this.cursors.get(file);
    if (!cursor || cursor.ino !== stat.ino || stat.size < cursor.offset) {
      cursor = { offset: 0, ino: stat.ino, splitter: new LineSplitter(), skipFirst: false };
      this.cursors.set(file, cursor);
      if (stat.size > this.limit) {
        await this.readHeader(file);
        cursor.offset = stat.size - this.limit;
        cursor.skipFirst = true;
        this.onWarning('Large logs load only their header and last 4 MiB. Earlier tool calls may be missing.');
      }
    }
    if (stat.size === cursor.offset) return;
    const handle = await fsp.open(file, 'r');
    try {
      while (cursor.offset < stat.size) {
        const data = new Uint8Array(Math.min(this.limit, stat.size - cursor.offset));
        const { bytesRead } = await handle.read(data, 0, data.length, cursor.offset);
        if (bytesRead === 0) break;
        cursor.offset += bytesRead;
        this.feed(file, cursor, data.subarray(0, bytesRead));
      }
    } finally {
      await handle.close();
    }
  }

  private async readHeader(file: string): Promise<void> {
    const handle = await fsp.open(file, 'r');
    try {
      const head = new Uint8Array(this.limit);
      const { bytesRead } = await handle.read(head, 0, head.length, 0);
      const end = head.subarray(0, bytesRead).indexOf(10);
      if (end >= 0) this.parse(file, new TextDecoder().decode(head.subarray(0, end)));
    } finally {
      await handle.close();
    }
  }

  private feed(file: string, cursor: Cursor, chunk: Uint8Array): void {
    for (const line of cursor.splitter.push(chunk)) {
      if (cursor.skipFirst) cursor.skipFirst = false;
      else this.parse(file, line);
    }
    if (cursor.splitter.pendingBytes > this.limit) {
      cursor.splitter.reset();
      // Warn once per record: skipFirst is already set while still inside the same oversized record.
      if (!cursor.skipFirst) this.onWarning(`A log record exceeded ${describeLimit(this.limit)} and was skipped.`);
      cursor.skipFirst = true;
    }
  }

  private parse(file: string, line: string): void {
    if (!line.length) return;
    let record: unknown;
    try {
      record = JSON.parse(line);
    } catch {
      this.malformed++;
      return;
    }
    this.onRecord(record, file);
  }
}
