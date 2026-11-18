import index from '../../viewer/index.html';
import type { Hub } from './hub';

type WsData = { off?: () => void; offHosts?: () => void };

/** A socket whose send buffer grows past this is closed. The viewer reconnects and gets a new snapshot. */
const MAX_BUFFERED_BYTES = 2 * 1024 * 1024;
/** Client messages are tiny (`{type:'resnapshot'}`). */
const MAX_CLIENT_MESSAGE_BYTES = 4096;

const SECURITY_HEADERS = { 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' };

function reply(status: number, body: string, extra: Record<string, string> = {}): Response {
  return new Response(body, { status, headers: { ...SECURITY_HEADERS, ...extra } });
}

/**
 * Serves the viewer, `/api/snapshot`, and `/ws`. Binds 127.0.0.1 only.
 * Data routes check `Host` and `Origin`, which stops DNS rebinding and cross-site reads.
 * The bundled viewer at `/` is a Bun HTML route that cannot run a check. It holds no data.
 */
export function startServer(opts: { hub: Hub; port: number }): { port: number; url: string; stop(): void } {
  const { hub } = opts;
  const sockets = new Set<Bun.ServerWebSocket<WsData>>();

  const send = (ws: Bun.ServerWebSocket<WsData>, msg: unknown): void => {
    if (ws.readyState !== 1) return;
    ws.send(JSON.stringify(msg));
    if (ws.getBufferedAmount() > MAX_BUFFERED_BYTES) ws.close(1013, 'client too slow');
  };
  /** The host list, with the hub's linger so the viewer judges presence the same way. */
  const hostsMessage = (hosts = hub.hosts()) => ({ type: 'hosts', hosts, lingerMs: hub.lingerMs });
  const sendCatchUp = (ws: Bun.ServerWebSocket<WsData>): void => {
    send(ws, hostsMessage());
    for (const env of hub.snapshotAll()) send(ws, env);
  };

  const server = Bun.serve<WsData>({
    hostname: '127.0.0.1',
    port: opts.port,
    routes: { '/': index },
    fetch(req, srv) {
      const url = new URL(req.url);
      if (url.pathname !== '/ws' && url.pathname !== '/api/snapshot') return reply(404, 'not found');

      const port = srv.port;
      const hostOk = [`127.0.0.1:${port}`, `localhost:${port}`].includes(req.headers.get('host') ?? '');
      const origin = req.headers.get('origin');
      const originOk = origin === null || [`http://127.0.0.1:${port}`, `http://localhost:${port}`].includes(origin);
      if (!hostOk || !originOk) return reply(403, 'forbidden');
      if (req.method !== 'GET') return reply(405, 'method not allowed', { Allow: 'GET' });

      if (url.pathname === '/api/snapshot') {
        return new Response(JSON.stringify({ hosts: hub.hosts(), envelopes: hub.snapshotAll() }), {
          headers: { ...SECURITY_HEADERS, 'Content-Type': 'application/json' },
        });
      }
      if (srv.upgrade(req, { data: {}, headers: SECURITY_HEADERS })) return undefined;
      return reply(400, 'websocket upgrade required');
    },
    websocket: {
      maxPayloadLength: MAX_CLIENT_MESSAGE_BYTES,
      open(ws) {
        sockets.add(ws);
        send(ws, hostsMessage());
        ws.data.offHosts = hub.onHostsChange((hosts) => send(ws, hostsMessage(hosts)));
        // `subscribe` delivers hello, snapshot, and coverage for each host first, then live envelopes.
        ws.data.off = hub.subscribe((env) => send(ws, env));
      },
      message(ws, message) {
        if (typeof message !== 'string') return;
        let parsed: unknown;
        try { parsed = JSON.parse(message); } catch { return; }
        if (typeof parsed === 'object' && parsed !== null && (parsed as { type?: unknown }).type === 'resnapshot') sendCatchUp(ws);
      },
      close(ws) {
        sockets.delete(ws);
        ws.data.off?.();
        ws.data.offHosts?.();
      },
    },
  });

  return {
    port: server.port!,
    url: `http://127.0.0.1:${server.port}`,
    stop() {
      for (const ws of sockets) { ws.data.off?.(); ws.data.offHosts?.(); }
      void server.stop(true);
    },
  };
}
