// Browser-safe: no DOM imports. Tested with `bun test`.

/** A failing subsystem logs at most once in this window. */
export const GUARD_LOG_EVERY_MS = 60_000;

export type Guard = (name: string, fn: () => void) => void;

/**
 * Runs one piece of frame work. An exception is caught, so one failing subsystem never stops
 * the others or the frame loop. Each subsystem logs at most once per minute.
 */
export function makeGuard(
  log: (msg: string, err: unknown) => void = (msg, err) => console.error(msg, err),
  now: () => number = Date.now,
): Guard {
  const lastLog = new Map<string, number>();
  return (name, fn) => {
    try {
      fn();
    } catch (err) {
      const t = now();
      const prev = lastLog.get(name);
      if (prev !== undefined && t - prev < GUARD_LOG_EVERY_MS) return;
      lastLog.set(name, t);
      log(`agent-view: ${name} failed`, err);
    }
  };
}
