import { expect, test } from 'bun:test';
import { LineSplitter, encodeLine, parseLine } from '../../src/shared/ndjson';

test('LineSplitter returns only complete lines and keeps split UTF-8', () => {
  const s = new LineSplitter(); const b = new TextEncoder().encode('{"p":"日本"}\n{"q":1}');
  const cut = 8; // inside the multi-byte character
  expect(s.push(b.subarray(0, cut))).toEqual([]);
  expect(s.push(b.subarray(cut))).toEqual(['{"p":"日本"}']);
  expect(s.pendingBytes).toBe(7);
  expect(s.push(new TextEncoder().encode('\n'))).toEqual(['{"q":1}']);
});
test('parseLine rejects malformed or wrong-version lines', () => {
  expect(parseLine('{oops')).toBeNull(); expect(parseLine(JSON.stringify({v:2,kind:'hello'}))).toBeNull();
  const e = {v:1,host:'h',seq:1,ts:'2026-11-17T00:00:00Z',kind:'fs.change',path:'/a',op:'change'} as const;
  expect(parseLine(encodeLine(e))).toEqual(e); expect(encodeLine(e).endsWith('\n')).toBe(true);
});
