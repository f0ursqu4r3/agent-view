import type { Envelope, HostStatus } from '../src/shared/schema';

// Browser-safe. The WebSocket constructor and timers can be injected for tests.

/** `lingerMs` is the hub's linger, when the hosts message carries a valid one. */
export type HubHandlers = { onEnvelope(e: Envelope): void; onHosts(h: HostStatus[], lingerMs?: number): void };
export type HubSocket = { send(msg: unknown): void; close(): void };
type SocketLike = {
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: unknown) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  readyState: number;
  send(data: string): void;
  close(): void;
};
type Deps = {
  WebSocket: new (url: string) => SocketLike;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (t: unknown) => void;
};

export const RECONNECT_MIN_MS = 1000;
export const RECONNECT_MAX_MS = 10_000;

/** 1 s, 2 s, 4 s, 8 s, then 10 s. */
export function nextDelay(prev: number): number {
  return Math.min(RECONNECT_MAX_MS, prev * 2);
}

/**
 * Connects to the hub's `/ws`, and reconnects with backoff from 1 to 10 s.
 * After a reconnect it asks for a resnapshot. Envelopes go to `onEnvelope`, `{type:'hosts'}` to `onHosts`.
 */
export function connectHub(url: string, handlers: HubHandlers, deps?: Partial<Deps>): HubSocket {
  const d: Deps = {
    WebSocket: deps?.WebSocket ?? (globalThis.WebSocket as unknown as Deps['WebSocket']),
    setTimeout: deps?.setTimeout ?? ((fn, ms) => globalThis.setTimeout(fn, ms)),
    clearTimeout: deps?.clearTimeout ?? ((t) => globalThis.clearTimeout(t as ReturnType<typeof setTimeout>)),
  };
  let ws: SocketLike | null = null;
  let delay = RECONNECT_MIN_MS;
  let everOpen = false;
  let closed = false;
  let timer: unknown = null;

  const send = (msg: unknown): void => {
    if (ws && ws.readyState === 1) ws.send(JSON.stringify(msg));
  };

  const open = (): void => {
    timer = null;
    if (closed) return;
    const sock = new d.WebSocket(url);
    ws = sock;
    sock.onopen = () => {
      delay = RECONNECT_MIN_MS;
      if (everOpen) send({ type: 'resnapshot' });
      everOpen = true;
    };
    sock.onmessage = (m) => {
      let msg: unknown;
      try {
        msg = JSON.parse(String(m.data));
      } catch {
        return;
      }
      if (typeof msg !== 'object' || msg === null) return;
      const o = msg as { type?: unknown; hosts?: unknown; lingerMs?: unknown; v?: unknown };
      if (o.type === 'hosts') {
        const linger = typeof o.lingerMs === 'number' && Number.isFinite(o.lingerMs) && o.lingerMs > 0 ? o.lingerMs : undefined;
        if (Array.isArray(o.hosts)) handlers.onHosts(o.hosts as HostStatus[], linger);
      } else if (o.v === 1) {
        handlers.onEnvelope(msg as Envelope);
      }
    };
    sock.onerror = () => {
      // `onclose` follows and schedules the reconnect.
    };
    sock.onclose = () => {
      if (ws === sock) ws = null;
      if (closed || timer !== null) return;
      timer = d.setTimeout(open, delay);
      delay = nextDelay(delay);
    };
  };

  open();
  return {
    send,
    close() {
      closed = true;
      if (timer !== null) d.clearTimeout(timer);
      timer = null;
      ws?.close();
      ws = null;
    },
  };
}
