import { SCHEMA_VERSION, type Envelope } from './schema';

const NEWLINE = 10;

/** Splits a byte stream into complete lines. Decodes only complete lines, so split UTF-8 is safe. */
export class LineSplitter {
  private buf = new Uint8Array(0);
  private readonly decoder = new TextDecoder();

  push(chunk: Uint8Array): string[] {
    const merged = new Uint8Array(this.buf.length + chunk.length);
    merged.set(this.buf, 0);
    merged.set(chunk, this.buf.length);
    const lines: string[] = [];
    let start = 0;
    for (let i = merged.indexOf(NEWLINE); i !== -1; i = merged.indexOf(NEWLINE, start)) {
      lines.push(this.decoder.decode(merged.subarray(start, i)));
      start = i + 1;
    }
    this.buf = merged.slice(start);
    return lines;
  }

  reset(): void {
    this.buf = new Uint8Array(0);
  }

  get pendingBytes(): number {
    return this.buf.length;
  }
}

export function encodeLine(e: Envelope): string {
  return JSON.stringify(e) + '\n';
}

export function parseLine(line: string): Envelope | null {
  let value: unknown;
  try {
    value = JSON.parse(line);
  } catch {
    return null;
  }
  if (typeof value !== 'object' || value === null) return null;
  return (value as { v?: unknown }).v === SCHEMA_VERSION ? (value as Envelope) : null;
}
