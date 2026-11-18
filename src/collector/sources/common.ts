import type { SourceEvent } from '../../shared/schema';

/** Limits and helpers shared by the log adapters. */
export const MAX_PENDING = 5000;
export const MAX_COMMAND_CHARS = 4096;
export const MAX_CHUNK_BYTES = 4096;
export const MAX_OUTPUT_BYTES = 16 * 1024;

export type Json = Record<string, unknown>;

export const isObject = (v: unknown): v is Json => typeof v === 'object' && v !== null && !Array.isArray(v);
export const str = (v: unknown): string | undefined => (typeof v === 'string' && v.length > 0 ? v : undefined);

/** Split UTF-8 bytes into chunks of at most `size` bytes without cutting a character. */
function chunkBytes(buf: Buffer, size: number): string[] {
  const chunks: string[] = [];
  let start = 0;
  while (start < buf.length) {
    let end = Math.min(start + size, buf.length);
    if (end < buf.length) while (end > start && (buf[end]! & 0xc0) === 0x80) end--;
    if (end === start) end = Math.min(start + size, buf.length);
    chunks.push(buf.subarray(start, end).toString('utf8'));
    start = end;
  }
  return chunks;
}

/** The last `max` bytes of the output, starting on a character boundary. */
function tailBytes(buf: Buffer, max: number): { buf: Buffer; cut: boolean } {
  if (buf.length <= max) return { buf, cut: false };
  let start = buf.length - max;
  while (start < buf.length && (buf[start]! & 0xc0) === 0x80) start++;
  return { buf: buf.subarray(start), cut: true };
}

/**
 * tool.output events for one command result: the last 16 KiB in chunks of at most 4 KiB.
 * The first chunk carries `truncated` when the output was cut. Empty output yields no chunk.
 */
export function outputEvents(ts: string, agentId: string, callId: string, text: string): SourceEvent[] {
  const { buf, cut } = tailBytes(Buffer.from(text, 'utf8'), MAX_OUTPUT_BYTES);
  return chunkBytes(buf, MAX_CHUNK_BYTES).map((chunk, i) => ({
    kind: 'tool.output', ts, agentId, callId, chunk, truncated: cut && i === 0,
  }));
}
