import { expect, test } from 'bun:test';
import { connectHub, nextDelay } from '../../viewer/socket';

class FakeSocket {
  static all: FakeSocket[] = [];
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: unknown) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  readyState = 0;
  sent: string[] = [];
  constructor(readonly url: string) {
    FakeSocket.all.push(this);
  }
  send(data: string): void {
    this.sent.push(data);
  }
  close(): void {
    this.readyState = 3;
    this.onclose?.({});
  }
  open(): void {
    this.readyState = 1;
    this.onopen?.({});
  }
  drop(): void {
    this.readyState = 3;
    this.onclose?.({});
  }
}

function harness() {
  FakeSocket.all = [];
  const timers: { fn: () => void; ms: number }[] = [];
  const envelopes: unknown[] = [];
  const hosts: unknown[] = [];
  const sock = connectHub('ws://h/ws', { onEnvelope: (e) => envelopes.push(e), onHosts: (h) => hosts.push(h) }, {
    WebSocket: FakeSocket,
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    clearTimeout: () => {},
  });
  return { sock, timers, envelopes, hosts };
}

test('backoff doubles from 1 s and stops at 10 s', () => {
  const seq = [1000];
  while (seq.length < 7) seq.push(nextDelay(seq.at(-1)!));
  expect(seq).toEqual([1000, 2000, 4000, 8000, 10_000, 10_000, 10_000]);
});

test('reconnects with backoff, asks for a resnapshot after a reconnect, and resets the delay on open', () => {
  const { timers } = harness();
  const first = FakeSocket.all[0]!;
  first.open();
  expect(first.sent).toEqual([]); // the first connection gets its snapshot from the hub anyway
  first.drop();
  first.drop(); // a second close event schedules nothing more
  expect(timers.map((t) => t.ms)).toEqual([1000]);
  timers[0]!.fn();
  FakeSocket.all[1]!.drop();
  timers[1]!.fn();
  FakeSocket.all[2]!.drop();
  expect(timers.map((t) => t.ms)).toEqual([1000, 2000, 4000]);
  timers[2]!.fn();
  const fourth = FakeSocket.all[3]!;
  fourth.open();
  expect(fourth.sent.map((s) => JSON.parse(s))).toEqual([{ type: 'resnapshot' }]);
  fourth.drop();
  expect(timers.at(-1)!.ms).toBe(1000);
});

test('routes hosts messages and envelopes, and ignores anything else', () => {
  const { envelopes, hosts } = harness();
  const s = FakeSocket.all[0]!;
  s.open();
  s.onmessage!({ data: JSON.stringify({ type: 'hosts', hosts: [{ label: 'a', kind: 'local', state: 'up' }] }) });
  s.onmessage!({ data: JSON.stringify({ v: 1, host: 'a', seq: 1, kind: 'coverage', ts: 'x' }) });
  s.onmessage!({ data: 'not json' });
  s.onmessage!({ data: JSON.stringify({ v: 2, host: 'a' }) });
  s.onmessage!({ data: 'null' });
  expect(hosts).toEqual([[{ label: 'a', kind: 'local', state: 'up' }]]);
  expect(envelopes).toHaveLength(1);
});

test('close stops reconnecting', () => {
  const { sock, timers } = harness();
  sock.close();
  expect(timers).toHaveLength(0);
  expect(FakeSocket.all).toHaveLength(1);
});
