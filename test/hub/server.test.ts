import { afterEach, expect, test } from 'bun:test';
import { Hub } from '../../src/hub/hub';
import { startServer } from '../../src/hub/server';
import type { Envelope, HostStatus } from '../../src/shared/schema';
import { FakeSource, upsert } from './helpers';

type Msg = Envelope | { type: 'hosts'; hosts: HostStatus[] };

const cleanups: (() => void)[] = [];
afterEach(() => { while (cleanups.length) cleanups.pop()!(); });

function setup(...labels: string[]) {
  const hub = new Hub();
  const sources = labels.map((l) => new FakeSource(l));
  for (const s of sources) hub.addSource(s);
  const server = startServer({ hub, port: 0 });
  cleanups.push(() => server.stop());
  return { hub, sources, server };
}

/** Opens a WebSocket and records its messages. */
async function connect(url: string, headers?: Record<string, string>) {
  const ws = new WebSocket(url.replace('http', 'ws') + '/ws', headers ? { headers } as never : undefined);
  const msgs: Msg[] = [];
  const waiters: (() => void)[] = [];
  ws.onmessage = (m) => { msgs.push(JSON.parse(String(m.data))); for (const w of [...waiters]) w(); };
  cleanups.push(() => ws.close());
  await new Promise<void>((resolve, reject) => {
    ws.onopen = () => resolve();
    ws.onerror = () => reject(new Error('websocket error'));
  });
  const until = (ok: () => boolean, ms = 3000): Promise<void> => new Promise((resolve, reject) => {
    if (ok()) return resolve();
    const timer = setTimeout(() => reject(new Error(`timeout; got ${JSON.stringify(msgs.map((x) => 'type' in x ? x.type : x.kind))}`)), ms);
    const w = () => { if (ok()) { clearTimeout(timer); waiters.splice(waiters.indexOf(w), 1); resolve(); } };
    waiters.push(w);
  });
  const label = (m: Msg) => ('type' in m ? 'hosts' : `${m.host}:${m.kind}`);
  return { ws, msgs, until, labels: () => msgs.map(label) };
}

test('server serves the bundled viewer at / and streams envelopes over /ws', async () => {
  const { server, sources } = setup('alpha');
  const page = await fetch(server.url + '/');
  expect(page.status).toBe(200);
  expect(page.headers.get('content-type')).toContain('text/html');
  expect(await page.text()).toContain('Agent View');

  const c = await connect(server.url);
  await c.until(() => c.msgs.length >= 3);
  expect(c.labels()).toEqual(['hosts', 'alpha:hello', 'alpha:snapshot']);
  expect(c.msgs[0]).toMatchObject({ type: 'hosts', hosts: [{ label: 'alpha', kind: 'remote', state: 'up' }] });

  sources[0]!.emit(upsert('a1'));
  await c.until(() => c.msgs.length >= 4);
  expect(c.msgs[3]).toMatchObject({ kind: 'agent.upsert', host: 'alpha', agent: { id: 'a1' } });
});

test('server sends hosts again when host status changes', async () => {
  const { sources, server } = setup('alpha');
  const c = await connect(server.url);
  await c.until(() => c.msgs.length >= 3);
  sources[0]!.setState('down');
  await c.until(() => c.msgs.length >= 4);
  expect(c.msgs[3]).toMatchObject({ type: 'hosts', hosts: [{ label: 'alpha', state: 'down' }] });
});

test('resnapshot message resends hello + snapshot; other messages are ignored', async () => {
  const { sources, server } = setup('alpha');
  const c = await connect(server.url);
  await c.until(() => c.msgs.length >= 3);
  sources[0]!.emit(upsert('a1'));
  await c.until(() => c.msgs.length >= 4);

  c.ws.send('not json');
  c.ws.send(JSON.stringify({ type: 'other' }));
  c.ws.send(JSON.stringify({ type: 'resnapshot' }));
  await c.until(() => c.msgs.length >= 7);
  expect(c.labels().slice(4)).toEqual(['hosts', 'alpha:hello', 'alpha:snapshot']);
  const snap = c.msgs[6] as Extract<Envelope, { kind: 'snapshot' }>;
  expect(snap.agents.map((a) => a.id)).toEqual(['a1']);
  expect(c.msgs.length).toBe(7);
});

test('/api/snapshot returns hosts and envelopes with no-store and nosniff', async () => {
  const { server, sources } = setup('alpha', 'beta');
  sources[1]!.emit(upsert('b1'));
  const res = await fetch(server.url + '/api/snapshot');
  expect(res.status).toBe(200);
  expect(res.headers.get('cache-control')).toBe('no-store');
  expect(res.headers.get('x-content-type-options')).toBe('nosniff');
  expect(res.headers.get('content-type')).toContain('application/json');
  const body = await res.json() as { hosts: HostStatus[]; envelopes: Envelope[] };
  expect(body.hosts.map((h) => h.label)).toEqual(['alpha', 'beta']);
  expect(body.envelopes.map((e) => `${e.host}:${e.kind}`)).toEqual(['alpha:hello', 'alpha:snapshot', 'beta:hello', 'beta:snapshot']);
});

test('server rejects /ws and /api/snapshot with a foreign Host or Origin (403)', async () => {
  const { server } = setup('alpha');
  const port = server.port;
  for (const path of ['/api/snapshot', '/ws']) {
    const upgrade: Record<string, string> = path === '/ws' ? { Upgrade: 'websocket', Connection: 'Upgrade', 'Sec-WebSocket-Version': '13', 'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ==' } : {};
    const foreignHost = await fetch(server.url + path, { headers: { Host: `evil.test:${port}`, ...upgrade } });
    expect(foreignHost.status).toBe(403);
    const wrongPort = await fetch(server.url + path, { headers: { Host: 'localhost:1', ...upgrade } });
    expect(wrongPort.status).toBe(403);
    const foreignOrigin = await fetch(server.url + path, { headers: { Origin: 'http://evil.test', ...upgrade } });
    expect(foreignOrigin.status).toBe(403);
    expect(foreignOrigin.headers.get('cache-control')).toBe('no-store');
    expect(foreignOrigin.headers.get('x-content-type-options')).toBe('nosniff');
  }
  // A real WebSocket client with a foreign Origin never opens.
  await expect(connect(server.url, { Origin: 'http://evil.test' })).rejects.toThrow();
  // Allowed Host and Origin pass.
  for (const host of ['127.0.0.1', 'localhost']) {
    const ok = await fetch(server.url + '/api/snapshot', { headers: { Host: `${host}:${port}`, Origin: `http://${host}:${port}` } });
    expect(ok.status).toBe(200);
  }
  const c = await connect(server.url, { Origin: `http://localhost:${port}`, Host: `localhost:${port}` });
  await c.until(() => c.msgs.length >= 1);
});

test('non-GET requests to data routes get 405', async () => {
  const { server } = setup('alpha');
  for (const method of ['POST', 'PUT', 'DELETE']) {
    const res = await fetch(server.url + '/api/snapshot', { method });
    expect(res.status).toBe(405);
    expect(res.headers.get('allow')).toBe('GET');
  }
  expect((await fetch(server.url + '/ws', { method: 'POST' })).status).toBe(405);
});

test('unknown paths get 404 and the server binds 127.0.0.1 only', async () => {
  const { server } = setup('alpha');
  expect((await fetch(server.url + '/nope')).status).toBe(404);
  expect((await fetch(server.url + '/api/other')).status).toBe(404);
  expect(server.url).toBe(`http://127.0.0.1:${server.port}`);
  expect(server.port).toBeGreaterThan(0);
});

test('stop closes sockets and releases the hub subscription', async () => {
  const hub = new Hub();
  const src = new FakeSource('alpha');
  hub.addSource(src);
  const server = startServer({ hub, port: 0 });
  const c = await connect(server.url);
  await c.until(() => c.msgs.length >= 3);
  const closed = new Promise<void>((resolve) => { c.ws.onclose = () => resolve(); });
  server.stop();
  await closed;
  const n = c.msgs.length;
  src.emit(upsert('late'));
  await Bun.sleep(30);
  expect(c.msgs.length).toBe(n);
  await expect(fetch(server.url + '/api/snapshot')).rejects.toThrow();
});

test('I2: every hosts message carries the hub linger', async () => {
  const hub = new Hub({ lifecycle: { lingerMs: 120_000 } });
  const src = new FakeSource('alpha');
  hub.addSource(src);
  const server = startServer({ hub, port: 0 });
  cleanups.push(() => server.stop());
  const c = await connect(server.url);
  await c.until(() => c.msgs.length >= 3);
  src.setState('down');
  await c.until(() => c.msgs.length >= 4);
  c.ws.send(JSON.stringify({ type: 'resnapshot' }));
  await c.until(() => c.msgs.length >= 7);
  const hosts = c.msgs.filter((m) => 'type' in m);
  expect(hosts).toHaveLength(3);
  for (const m of hosts) expect(m).toMatchObject({ type: 'hosts', lingerMs: 120_000 });
});
