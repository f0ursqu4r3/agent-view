#!/usr/bin/env bun
/**
 * Parity trace of the demo source: `bun test/parity/demo-trace.ts <normal|stress> <ms>`.
 * Runs `DemoSource` (host `parity`) on a virtual clock that starts at 2026-01-01T00:00:00Z and prints every
 * emitted source event as one JSON line. internal/parity runs the Go demo on `demo.NewFakeScheduler` and compares.
 *
 * Clock rules (the same as the Go FakeScheduler): a timer fires at `now + ms`, the fraction kept; due timers fire
 * in order of fire time, then creation order; the clock reads the fire time while a callback runs; `Date.now()` and
 * `new Date()` without arguments read the virtual time.
 */
import type { SourceEvent } from '../../src/shared/schema';

const START_MS = 1767225600000;

const [scale, msArg] = process.argv.slice(2);
const runMs = Number(msArg);
if ((scale !== 'normal' && scale !== 'stress') || !Number.isFinite(runMs) || runMs < 0) {
  process.stderr.write('usage: bun test/parity/demo-trace.ts <normal|stress> <ms>\n');
  process.exit(2);
}

type Timer = { id: number; at: number; fn: () => void };

let now = START_MS;
let nextId = 1;
const timers = new Map<number, Timer>();

const RealDate = Date;
class VirtualDate extends RealDate {
  constructor(...args: unknown[]) {
    if (args.length === 0) super(now);
    else super(...(args as [string | number | Date]));
  }

  static override now(): number {
    return now;
  }
}

globalThis.Date = VirtualDate as DateConstructor;
globalThis.setTimeout = ((fn: () => void, ms = 0) => {
  const t: Timer = { id: nextId++, at: now + ms, fn };
  timers.set(t.id, t);
  return t.id;
}) as unknown as typeof setTimeout;
globalThis.clearTimeout = ((id: number | undefined) => {
  if (id !== undefined) timers.delete(id);
}) as unknown as typeof clearTimeout;

/** Moves the clock `ms` forward and fires every timer that falls due, including timers created on the way. */
function advance(ms: number): void {
  const target = now + ms;
  for (;;) {
    let next: Timer | undefined;
    for (const t of timers.values()) {
      if (t.at > target) continue;
      if (!next || t.at < next.at || (t.at === next.at && t.id < next.id)) next = t;
    }
    if (!next) {
      now = target;
      return;
    }
    timers.delete(next.id);
    now = next.at;
    next.fn();
  }
}

// Imported after the fakes are in place.
const { DemoSource } = await import('../../src/collector/demo');

const lines: string[] = [];
const source = new DemoSource({
  host: 'parity',
  scale,
  emit: (events: SourceEvent[]) => {
    for (const e of events) lines.push(JSON.stringify(e));
  },
});
source.start();
advance(runMs);
source.stop();
await Bun.write(Bun.stdout, lines.map((l) => `${l}\n`).join(''));
