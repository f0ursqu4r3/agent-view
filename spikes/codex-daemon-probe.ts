// Throwaway spike: attach to the Codex app-server daemon through `codex app-server proxy`.
// SAFETY: only sends initialize, initialized, thread/loaded/list, thread/read (metadata only),
// thread/resume {threadId}, thread/unsubscribe. Never prints or saves strings: shapes only.
// FINDING: the control socket speaks WebSocket (101 Switching Protocols); `codex app-server proxy` passes raw
// bytes, so the client must do the WebSocket handshake and framing itself. Not newline-delimited JSON.
// TRANSPORT=proxy (default) spawns `codex app-server proxy`; TRANSPORT=socket connects to the unix socket directly.
import { createHash, randomBytes } from 'node:crypto'
const TRANSPORT = (process.env.TRANSPORT ?? 'proxy') as 'proxy' | 'socket'
const RTT_SAMPLES = Number(process.env.RTT_SAMPLES ?? 40)

const DURATION_MS = Number(process.env.PROBE_MS ?? 120_000)
const ALLOWED = new Set(['initialize', 'initialized', 'thread/loaded/list', 'thread/read', 'thread/resume', 'thread/unsubscribe'])
const SHAPE_METHODS = ['item/started', 'item/completed', 'item/commandExecution/outputDelta', 'thread/started']

function shape(v: unknown, depth = 0): unknown {
  if (typeof v === 'string') return `<str:${v.length}>`
  if (v === null || typeof v === 'boolean' || typeof v === 'number') return v
  if (depth > 6) return '<deep>'
  if (Array.isArray(v)) return v.length === 0 ? [] : [shape(v[0], depth + 1), `<len:${v.length}>`]
  if (typeof v === 'object') {
    const o: Record<string, unknown> = {}
    for (const [k, val] of Object.entries(v as object)) {
      o[/^[A-Za-z_][A-Za-z0-9_]{0,40}$/.test(k) ? k : '<key>'] = shape(val, depth + 1)
    }
    return o
  }
  return `<${typeof v}>`
}

const enc = new TextEncoder()

// ---- minimal WebSocket client over a raw byte duplex ----
type Duplex = { send(b: Uint8Array): void; close(): void; stderrBytes(): number }
let duplex!: Duplex
let rx = new Uint8Array(0)
let handshakeDone = false
let onHandshake: () => void = () => {}
let fragments: Uint8Array[] = []

function frame(opcode: number, payload: Uint8Array): Uint8Array {
  const len = payload.length
  const hdr = len < 126 ? 2 : len < 65536 ? 4 : 10
  const out = new Uint8Array(hdr + 4 + len)
  out[0] = 0x80 | opcode
  if (len < 126) out[1] = 0x80 | len
  else if (len < 65536) { out[1] = 0x80 | 126; out[2] = len >> 8; out[3] = len & 255 }
  else { out[1] = 0x80 | 127; new DataView(out.buffer).setBigUint64(2, BigInt(len)) }
  const mask = randomBytes(4)
  out.set(mask, hdr)
  for (let i = 0; i < len; i++) out[hdr + 4 + i] = payload[i] ^ mask[i % 4]
  return out
}
function onBytes(chunk: Uint8Array) {
  const n = new Uint8Array(rx.length + chunk.length); n.set(rx); n.set(chunk, rx.length); rx = n
  if (!handshakeDone) {
    const i = Buffer.from(rx).indexOf('\r\n\r\n')
    if (i < 0) return
    const head = Buffer.from(rx.subarray(0, i)).toString('latin1')
    rx = rx.subarray(i + 4)
    if (!/^HTTP\/1\.1 101/.test(head)) throw new Error('websocket handshake refused')
    handshakeDone = true
    onHandshake()
  }
  for (;;) {
    if (rx.length < 2) return
    const fin = (rx[0] & 0x80) !== 0, op = rx[0] & 15
    let len = rx[1] & 127, off = 2
    if (len === 126) { if (rx.length < 4) return; len = (rx[2] << 8) | rx[3]; off = 4 }
    else if (len === 127) { if (rx.length < 10) return; len = Number(new DataView(rx.buffer, rx.byteOffset).getBigUint64(2)); off = 10 }
    if (rx.length < off + len) return
    const payload = rx.slice(off, off + len)
    rx = rx.subarray(off + len)
    if (op === 0x9) duplex.send(frame(0xa, payload))
    else if (op === 0x8) { counts['<ws-close>'] = (counts['<ws-close>'] ?? 0) + 1 }
    else if (op === 0x1 || op === 0x0) {
      fragments.push(payload)
      if (fin) {
        const all = Buffer.concat(fragments).toString('utf8'); fragments = []
        try { handle(JSON.parse(all)) } catch { counts['<unparseable>'] = (counts['<unparseable>'] ?? 0) + 1 }
      }
    }
  }
}
async function connect(): Promise<void> {
  const key = randomBytes(16).toString('base64')
  const req = `GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: ${key}\r\nSec-WebSocket-Version: 13\r\n\r\n`
  const ready = new Promise<void>((res) => { onHandshake = res })
  if (TRANSPORT === 'proxy') {
    const proc = Bun.spawn(['codex', 'app-server', 'proxy'], { stdin: 'pipe', stdout: 'pipe', stderr: 'pipe' })
    let errBytes = 0
    ;(async () => { const r = proc.stdout.getReader(); for (;;) { const { done, value } = await r.read(); if (done) return; onBytes(value) } })()
    ;(async () => { const r = proc.stderr.getReader(); for (;;) { const { done, value } = await r.read(); if (done) return; errBytes += value.length } })()
    duplex = {
      send: (b) => { proc.stdin.write(b); proc.stdin.flush() },
      close: () => { try { proc.stdin.end() } catch {} try { proc.kill() } catch {} },
      stderrBytes: () => errBytes,
    }
  } else {
    const v = JSON.parse(await Bun.$`codex app-server daemon version`.text())
    const sock = await Bun.connect({ unix: v.socketPath, socket: {
      data: (_s, d) => onBytes(new Uint8Array(d)),
      open() {}, close() {}, error() {},
    } })
    duplex = { send: (b) => { sock.write(b) }, close: () => { try { sock.end() } catch {} }, stderrBytes: () => 0 }
  }
  duplex.send(new TextEncoder().encode(req))
  await Promise.race([ready, Bun.sleep(10_000).then(() => { throw new Error('handshake timeout') })])
}
function sendJson(obj: unknown) { duplex.send(frame(0x1, enc.encode(JSON.stringify(obj)))) }

let nextId = 1
const pending = new Map<number, { method: string; sent: number; resolve: (m: any) => void }>()
const counts: Record<string, number> = {}
const shapes: Record<string, unknown[]> = {}
const latencies: number[] = []
const rtts: Record<string, number[]> = {}
const resumed: string[] = []
const loadedAtResume = new Set<string>()
const flags = { turnStartedNotFromProbe: 0, settingsUpdated: 0, threadStarted: 0, serverRequests: 0 }
let unsubscribed = false

function write(obj: any) {
  const method: string = obj.method
  if (!ALLOWED.has(method)) throw new Error(`refusing to send ${method}`)
  sendJson(obj)
}
function request(method: string, params?: unknown): Promise<any> {
  const id = nextId++
  return new Promise((resolve) => {
    pending.set(id, { method, sent: performance.now(), resolve })
    write({ jsonrpc: '2.0', id, method, ...(params === undefined ? {} : { params }) })
  })
}
function note(method: string, params?: unknown) { write({ jsonrpc: '2.0', method, ...(params === undefined ? {} : { params }) }) }

function handle(msg: any) {
  if (msg.id !== undefined && msg.method === undefined) {
    const p = pending.get(msg.id)
    if (p) {
      pending.delete(msg.id)
      ;(rtts[p.method] ??= []).push(performance.now() - p.sent)
      p.resolve(msg)
    }
    return
  }
  if (msg.method) {
    if (msg.id !== undefined) { flags.serverRequests++; counts[`REQ:${msg.method}`] = (counts[`REQ:${msg.method}`] ?? 0) + 1; return } // never answered
    counts[msg.method] = (counts[msg.method] ?? 0) + 1
    const m: string = msg.method
    if (m === 'thread/started') flags.threadStarted++
    if (m === 'turn/started') flags.turnStartedNotFromProbe++
    if (m === 'thread/settings/updated') flags.settingsUpdated++
    if (m === 'item/started' && typeof msg.params?.startedAtMs === 'number') latencies.push(Date.now() - msg.params.startedAtMs)
    if (SHAPE_METHODS.includes(m) || m.startsWith('turn/')) {
      const key = m.startsWith('turn/') ? 'turn/*' : m
      const list = (shapes[key] ??= [])
      if (list.length < 3) list.push({ method: m, params: shape(msg.params) })
    }
  }
}

async function cleanup() {
  if (unsubscribed) return
  unsubscribed = true
  for (const id of resumed) {
    try { await Promise.race([request('thread/unsubscribe', { threadId: id }), Bun.sleep(3000)]) } catch {}
  }
  try { duplex.send(frame(0x8, new Uint8Array([0x03, 0xe8]))) } catch {}
  await Bun.sleep(100)
  duplex.close()
}
for (const sig of ['SIGINT', 'SIGTERM'] as const) process.on(sig, async () => { await cleanup(); process.exit(130) })
process.on('uncaughtException', async (e) => { console.log('ERROR', (e as Error).name); await cleanup(); process.exit(1) })

// thread/read: reduce to booleans/short non-text settings fingerprint (never prints strings)
function settingsFingerprint(resp: any) {
  const t = resp?.result?.thread
  if (!t) return null
  const h = new Bun.CryptoHasher('sha256')
  h.update(JSON.stringify([t.model, t.modelProvider, t.reasoningEffort, t.cwd, t.source, t.ephemeral, t.name, t.path]))
  return { fp: h.digest('hex').slice(0, 12), status: t.status?.type ?? typeof t.status, model: t.model, reasoningEffort: t.reasoningEffort }
}

const out: any = { startedAt: new Date().toISOString(), durationMs: DURATION_MS }
try {
  await connect()
  const init = await Promise.race([
    request('initialize', { clientInfo: { name: 'agent-view-probe', version: '0.0.0' } }),
    Bun.sleep(10_000).then(() => null),
  ])
  out.initialize = init ? { ok: !init.error, resultShape: shape(init.result), error: init.error ? shape(init.error) : undefined } : 'timeout'
  if (!init || init.error) throw new Error('initialize failed')
  note('initialized')
  // RTT sampling with the one read-only request, to compare transports
  const rttList: number[] = []
  for (let i = 0; i < RTT_SAMPLES; i++) { const t = performance.now(); await request('thread/loaded/list', {}); rttList.push(performance.now() - t) }
  out.transport = TRANSPORT
  out.rttSamples = (() => { const q = [...rttList].sort((a, b) => a - b); return { n: q.length, minMs: +q[0].toFixed(3), p50Ms: +q[Math.floor(q.length / 2)].toFixed(3), p95Ms: +q[Math.floor(q.length * 0.95)].toFixed(3), maxMs: +q[q.length - 1].toFixed(3) } })()
  rtts['thread/loaded/list'] = []
  const list = await request('thread/loaded/list', {})
  const ids: string[] = list.result?.data ?? []
  out.loadedCount = ids.length
  out.loadedListShape = shape(list.result)
  out.loadedListRttMs = rtts['thread/loaded/list']?.[0]
  const before: Record<string, any> = {}
  const after: Record<string, any> = {}
  out.threads = []
  for (const id of ids) {
    loadedAtResume.add(id)
    before[id] = settingsFingerprint(await request('thread/read', { threadId: id }))
    const r = await request('thread/resume', { threadId: id })
    resumed.push(id)
    after[id] = settingsFingerprint(await request('thread/read', { threadId: id }))
    out.threads.push({
      resumeOk: !r.error,
      resumeErr: r.error ? shape(r.error) : undefined,
      resumeResultShape: shape({ ...r.result, thread: { ...(r.result?.thread ?? {}), turns: undefined } }),
      settingsBeforeEqualsAfter: !!before[id] && !!after[id] && before[id].fp === after[id].fp,
      before: before[id] && { status: before[id].status, model: before[id].model, reasoningEffort: before[id].reasoningEffort },
      after: after[id] && { status: after[id].status, model: after[id].model, reasoningEffort: after[id].reasoningEffort },
    })
  }
  const t0 = performance.now()
  await Bun.sleep(ids.length ? DURATION_MS : Math.min(DURATION_MS, 20_000))
  out.listenedMs = Math.round(performance.now() - t0)
  // re-list once to see whether new threads appeared (visibility)
  const list2 = await request('thread/loaded/list', {})
  const ids2: string[] = list2.result?.data ?? []
  out.loadedCountAfter = ids2.length
  out.newLoadedSinceStart = ids2.filter((i) => !loadedAtResume.has(i)).length
} catch (e) {
  out.error = (e as Error).message
} finally {
  await cleanup()
}
out.counts = counts
out.flags = flags
out.shapes = shapes
const stats = (a: number[]) => {
  if (!a.length) return null
  const s = [...a].sort((x, y) => x - y)
  const q = (p: number) => s[Math.min(s.length - 1, Math.floor(p * s.length))]
  return { n: s.length, minMs: s[0], p50Ms: q(0.5), p95Ms: q(0.95), maxMs: s[s.length - 1] }
}
out.itemStartedLatency = stats(latencies)
out.rttMs = Object.fromEntries(Object.entries(rtts).map(([k, v]) => [k, stats(v)]))
out.proxyStderrBytes = duplex?.stderrBytes?.() ?? 0
console.log(JSON.stringify(out, null, 2))
process.exit(0)
