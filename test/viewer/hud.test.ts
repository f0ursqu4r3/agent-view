import { expect, test } from 'bun:test';
import { sourceSummary } from '../../viewer/hud';
import type { Coverage } from '../../src/shared/schema';

const cov = (sources: Coverage['sources']): Coverage =>
  ({ logRoots: [], logsOpen: 0, logsDiscovered: 0, watchRoots: [], malformedRecords: 0, fsDropped: 0, sources });

test('Minor 5: the host line summarizes sources that are not ok', () => {
  expect(sourceSummary(undefined)).toBeNull();
  expect(sourceSummary(cov({}))).toBeNull();
  expect(sourceSummary(cov({ a: { state: 'ok' } }))).toBeNull();
  expect(sourceSummary(cov({ a: { state: 'degraded', detail: 'x' }, b: { state: 'ok' } }))).toBe('1 source degraded');
  expect(sourceSummary(cov({ a: { state: 'degraded' }, b: { state: 'degraded' }, c: { state: 'down' } }))).toBe('1 source down, 2 sources degraded');
});
