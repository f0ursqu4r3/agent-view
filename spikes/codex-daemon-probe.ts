// Throwaway spike: attach to the Codex app-server daemon through `codex app-server proxy`.
// SAFETY: only sends initialize, initialized, thread/loaded/list, thread/read (metadata only),
// thread/resume {threadId}, thread/unsubscribe. Never prints or saves strings: shapes, booleans, counts and hashes only.
// FINDING: the control socket speaks WebSocket (101 Switching Protocols); `codex app-server proxy` passes raw
// bytes, so the client must do the WebSocket handshake and framing itself. Not newline-delimited JSON.
// TRANSPORT=proxy (default) spawns `codex app-server proxy`; TRANSPORT=socket connects to the unix socket directly.
// OPT-IN: the probe sends thread/resume to the owner's loaded threads. It runs only with AGENT_VIEW_PROBE_RESUME=1,
// after the preconditions in docs/superpowers/spikes/2026-11-17-m0-results.md ("Before the next probe run") are met.
// WARNING (run of 2026-11-18): `initialize` itself is NOT free of side effects on the shared daemon. The first client
// after a daemon start sets the daemon-wide originator (written into every thread the daemon creates later, and sent
// upstream); every client's initialize sets the daemon-wide User-Agent suffix "(<clientInfo.name>; <version>)".
// Only `codex app-server daemon restart` clears them. Do not run this probe again without the owner's consent.
//
// Preconditions met in this file:
// m1  model and reasoning effort are printed only as "unchanged" booleans and hashed fingerprints.
// m2  every turn/started is kept with its time after the resume and whether the resume result already listed the turn.
//     A turn the resume did not list, on a resumed thread, inside TURN_WINDOW_MS after the resume, counts as suspect.
// m3  the settings fingerprint of the thread/resume result includes the approval policy and the sandbox; the resume
//     result is compared with thread/read; thread/settings/updated payloads are compared with the resume result.
// m4  checkOutgoing() refuses any method outside the allow-list, any thread call whose params are not exactly
//     {threadId}, and any threadId that is not in the first thread/loaded/list reply. It also refuses every message
//     without a method, so the probe can never answer a server request.
// m6  the run is short (PROBE_MS, default 45 s), a warning goes to stderr first, and the probe stops listening and
//     unsubscribes at the first server request (for example an approval prompt), so it never holds one open for long.
import { createHash, randomBytes } from 'node:crypto'

const TRANSPORT = (process.env.TRANSPORT ?? 'proxy') as 'proxy' | 'socket'
const RTT_SAMPLES = Number(process.env.RTT_SAMPLES ?? 40)
const DURATION_MS = Number(process.env.PROBE_MS ?? 45_000)
const TURN_WINDOW_MS = Number(process.env.TURN_WINDOW_MS ?? 10_000)
const REQUEST_TIMEOUT_MS = Number(process.env.REQUEST_TIMEOUT_MS ?? 10_000)
const RESUME_OPT_IN = process.env.AGENT_VIEW_PROBE_RESUME === '1'
const CLIENT_NAME = process.env.PROBE_CLIENT_NAME ?? 'agent-view-probe'

export const ALLOWED: ReadonlySet<string> = new Set(['initialize', 'initialized', 'thread/loaded/list', 'thread/read', 'thread/resume', 'thread/unsubscribe'])

/** m4: the one gate for every outgoing message. Throws instead of sending anything outside the read-only rules. */
export function checkOutgoing(msg: { method?: unknown; params?: unknown }, ctx: { loaded: ReadonlySet<string>; resumeOptIn: boolean }): void {
  const method = msg.method
  if (typeof method !== 'string' || !ALLOWED.has(method)) throw new Error(`refusing to send ${typeof method === 'string' ? method : '<no method>'}`)
  const p = msg.params
  const keys = p !== null && typeof p === 'object' && !Array.isArray(p) ? Object.keys(p) : null
  switch (method) {
    case 'initialize':
      if (!keys || keys.length !== 1 || keys[0] !== 'clientInfo') throw new Error('refusing initialize: params must be exactly {clientInfo}')
      return
    case 'initialized':
      if (p !== undefined) throw new Error('refusing initialized: it takes no params')
      return
    case 'thread/loaded/list':
      if (!keys || keys.length !== 0) throw new Error('refusing thread/loaded/list: params must be {}')
      return
    default: {
      if (method === 'thread/resume' && !ctx.resumeOptIn) throw new Error('refusing to send thread/resume without AGENT_VIEW_PROBE_RESUME=1')
      if (!keys || keys.length !== 1 || keys[0] !== 'threadId') throw new Error(`refusing ${method}: params must be exactly {threadId}`)
      const id = (p as { threadId: unknown }).threadId
      if (typeof id !== 'string' || !ctx.loaded.has(id)) throw new Error(`refusing ${method}: the thread is not in thread/loaded/list`)
    }
  }
}

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
const hash = (v: unknown, n = 12) => createHash('sha256').update(JSON.stringify(v) ?? 'undefined').digest('hex').slice(0, n)
/** Protocol enum values only (for example `idle`, `commandExecution`); anything else becomes `<other>`. */
const enumOf = (v: unknown) => (typeof v === 'string' && /^[A-Za-z][A-Za-z_]{0,30}$/.test(v) ? v : typeof v === 'string' ? '<other>' : `<${v === null ? 'null' : typeof v}>`)
/** A server error message with ids, paths, quoted text and numbers removed: only the fixed template is left. */
const errorTemplate = (m: unknown) => (typeof m === 'string'
  ? m.replace(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi, '<id>').replace(/\/\S+/g, '<path>')
    .replace(/(["'`]).*?\1/g, '<quoted>').replace(/\d+/g, '<n>').slice(0, 120)
  : `<${typeof m}>`)
const SOURCE_KINDS = new Set(['cli', 'vscode', 'exec', 'appServer', 'unknown'])
function sourceKind(s: unknown): string {
  if (typeof s === 'string') return SOURCE_KINDS.has(s) ? s : '<other>'
  if (s && typeof s === 'object') return `{${Object.keys(s).filter((k) => /^[A-Za-z_]{1,20}$/.test(k)).join(',')}}`
  return `<${s === null ? 'null' : typeof s}>`
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
    const socketPath = process.env.SOCKET_PATH ?? JSON.parse(await Bun.$`codex app-server daemon version`.text()).socketPath // SOCKET_PATH only for the offline fake-server check
    const sock = await Bun.connect({ unix: socketPath, socket: {
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
const latencies = { itemStarted: [] as number[], itemCompleted: [] as number[] }
const rtts: Record<string, number[]> = {}
const loaded = new Set<string>() // m4: filled once, from the first thread/loaded/list reply
const resumed: string[] = []
const loadedAtResume = new Set<string>()
type ThreadInfo = { label: string; resumeSentAt?: number; knownTurns: Set<string>; statusBefore: string; resumeFp?: string }
const threads = new Map<string, ThreadInfo>()
const turnEvents: { event: 'started' | 'completed'; threadId: unknown; turnId: unknown; at: number }[] = []
const settingsUpdates: { threadId: unknown; fp: string; at: number }[] = []
const threadStarted: { sourceKind: string; wasLoaded: boolean; hasParent: boolean }[] = []
const flags = { turnStartedSuspect: 0, turnStartedOther: 0, settingsUpdated: 0, threadStarted: 0, serverRequests: 0, stoppedEarly: false }
let unsubscribed = false
let stopEarly = false
let wake: () => void = () => {}
const labels = new Map<string, string>()
const label = (id: unknown) => {
  if (typeof id !== 'string') return '<none>'
  let l = labels.get(id)
  if (!l) { l = `T${labels.size + 1}`; labels.set(id, l) }
  return l
}

function write(obj: { jsonrpc: '2.0'; id?: number; method: string; params?: unknown }) {
  checkOutgoing(obj, { loaded, resumeOptIn: RESUME_OPT_IN })
  sendJson(obj)
}
function request(method: string, params?: unknown): Promise<any> {
  const id = nextId++
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { pending.delete(id); reject(new Error(`request timeout: ${method}`)) }, REQUEST_TIMEOUT_MS)
    pending.set(id, { method, sent: performance.now(), resolve: (m) => { clearTimeout(timer); resolve(m) } })
    try { write({ jsonrpc: '2.0', id, method, ...(params === undefined ? {} : { params }) }) }
    catch (e) { clearTimeout(timer); pending.delete(id); reject(e) }
  })
}
function note(method: string, params?: unknown) { write({ jsonrpc: '2.0', method, ...(params === undefined ? {} : { params }) }) }

function keepShape(key: string, max: number, method: string, params: unknown) {
  const list = (shapes[key] ??= [])
  if (list.length < max) list.push({ method, params: shape(params) })
}
const bump = (k: string) => { counts[k] = (counts[k] ?? 0) + 1 }

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
  if (typeof msg.method !== 'string') return
  const m: string = msg.method
  if (msg.id !== undefined) {
    // m6: a server request (for example an approval prompt). Never answered; stop listening and unsubscribe soon.
    flags.serverRequests++
    bump(`REQ:${m}`)
    stopEarly = true
    wake()
    return
  }
  bump(m)
  const params = msg.params ?? {}
  if (m === 'thread/started') {
    flags.threadStarted++
    const t = params.thread
    threadStarted.push({ sourceKind: sourceKind(t?.source), wasLoaded: loaded.has(t?.id), hasParent: typeof t?.parentThreadId === 'string' })
    keepShape(m, 2, m, { ...params, thread: { ...(t ?? {}), turns: undefined } })
    return
  }
  if (m === 'turn/started' || m === 'turn/completed') {
    turnEvents.push({ event: m === 'turn/started' ? 'started' : 'completed', threadId: params.threadId, turnId: params.turn?.id, at: Date.now() })
    keepShape('turn/*', 3, m, { ...params, turn: { ...(params.turn ?? {}), items: shape(params.turn?.items) } })
    return
  }
  if (m === 'thread/settings/updated') {
    flags.settingsUpdated++
    settingsUpdates.push({ threadId: params.threadId, fp: settingsFp(params.threadSettings), at: Date.now() })
    return
  }
  if (m === 'item/started' || m === 'item/completed') {
    const type = enumOf(params.item?.type)
    bump(`${m}:${type}`)
    if (m === 'item/started' && typeof params.startedAtMs === 'number') latencies.itemStarted.push(Date.now() - params.startedAtMs)
    if (m === 'item/completed' && typeof params.completedAtMs === 'number') latencies.itemCompleted.push(Date.now() - params.completedAtMs)
    keepShape(`${m}:${type}`, 2, m, params)
    return
  }
  if (m === 'item/commandExecution/outputDelta' || m === 'item/fileChange/patchUpdated' || m === 'thread/status/changed' || m === 'thread/closed' || m === 'thread/archived') {
    keepShape(m, 2, m, params)
  }
}

async function cleanup() {
  if (unsubscribed) return
  unsubscribed = true
  // Unsubscribe every thread the probe began to handle (resume may be in flight or its reply lost).
  for (const id of new Set([...resumed, ...loadedAtResume])) {
    try { await Promise.race([request('thread/unsubscribe', { threadId: id }), Bun.sleep(3000)]) } catch {}
  }
  try { duplex.send(frame(0x8, new Uint8Array([0x03, 0xe8]))) } catch {}
  await Bun.sleep(100)
  duplex?.close()
}

// m1 + m3: fingerprints. Strings never leave this process; only the 12-hex hash and booleans are printed.
/** thread/read: the settings a Thread carries. Status is left out: it changes when the owner works. */
const readFp = (t: any) => (t ? hash([t.model, t.modelProvider, t.reasoningEffort, t.cwd, t.ephemeral, t.name, t.path, t.source, t.parentThreadId, t.forkedFromId]) : null)
/** thread/resume result and thread/settings/updated payload: same field order, so the two hashes compare. Includes approval policy and sandbox. */
const resumeFp = (r: any) => (r ? hash([r.model, r.modelProvider, r.serviceTier, r.cwd, r.approvalPolicy, r.approvalsReviewer, r.sandbox, r.reasoningEffort, r.collaborationMode]) : '<none>')
const settingsFp = (s: any) => (s ? hash([s.model, s.modelProvider, s.serviceTier, s.cwd, s.approvalPolicy, s.approvalsReviewer, s.sandboxPolicy, s.effort, s.collaborationMode]) : '<none>')
const statusOf = (t: any) => enumOf(t?.status?.type)

const stats = (a: number[]) => {
  if (!a.length) return null
  const s = [...a].sort((x, y) => x - y)
  const q = (p: number) => s[Math.min(s.length - 1, Math.floor(p * s.length))]
  return { n: s.length, minMs: s[0], p50Ms: q(0.5), p95Ms: q(0.95), maxMs: s[s.length - 1] }
}

async function main() {
  if (!RESUME_OPT_IN) {
    console.log([
      'codex-daemon-probe: not run. The probe sends thread/resume to loaded Codex threads, so it needs AGENT_VIEW_PROBE_RESUME=1.',
      'Before you set it, meet the preconditions in docs/superpowers/spikes/2026-11-17-m0-results.md ("Before the next probe run"),',
      'and warn the owner of the loaded threads: the probe does not answer server requests, which can stall their turn.',
      'initialize also changes daemon-wide state (originator, User-Agent suffix) until `codex app-server daemon restart`.',
    ].join('\n'))
    process.exit(0)
  }
  for (const sig of ['SIGINT', 'SIGTERM'] as const) process.on(sig, async () => { await cleanup(); process.exit(130) })
  process.on('uncaughtException', async (e) => { console.log('ERROR', (e as Error).name); await cleanup(); process.exit(1) })
  process.on('unhandledRejection', async (e) => { console.log('ERROR', (e as Error)?.name); await cleanup(); process.exit(1) })
  process.stderr.write(`codex-daemon-probe: resumes every loaded Codex thread for up to ${Math.round(DURATION_MS / 1000)} s. ` +
    'It never answers approval prompts; at the first one it stops and unsubscribes. Do not start a new turn in the first ' +
    `${Math.round(TURN_WINDOW_MS / 1000)} s.\n`)

  const out: any = { startedAt: new Date().toISOString(), durationMs: DURATION_MS, turnWindowMs: TURN_WINDOW_MS }
  try {
    await connect()
    const init = await Promise.race([
      request('initialize', { clientInfo: { name: CLIENT_NAME, version: '0.0.0' } }),
      Bun.sleep(10_000).then(() => null),
    ])
    const ua = typeof init?.result?.userAgent === 'string' ? init.result.userAgent : ''
    out.initialize = init ? {
      ok: !init.error, resultShape: shape(init.result), error: init.error ? shape(init.error) : undefined,
      // Daemon-global state that initialize may set: the originator (first client wins) and the User-Agent suffix.
      userAgentStartsWithProbeName: ua.startsWith(`${CLIENT_NAME}/`),
      userAgentHasProbeSuffix: ua.includes(`(${CLIENT_NAME}; `),
    } : 'timeout'
    if (!init || init.error) throw new Error('initialize failed')
    note('initialized')
    // RTT sampling with the one read-only request, to compare transports
    const rttList: number[] = []
    for (let i = 0; i < RTT_SAMPLES; i++) { const t = performance.now(); await request('thread/loaded/list', {}); rttList.push(performance.now() - t) }
    out.transport = TRANSPORT
    out.rttSamples = (() => { const q = [...rttList].sort((a, b) => a - b); return { n: q.length, minMs: +q[0].toFixed(3), p50Ms: +q[Math.floor(q.length / 2)].toFixed(3), p95Ms: +q[Math.floor(q.length * 0.95)].toFixed(3), maxMs: +q[q.length - 1].toFixed(3) } })()
    rtts['thread/loaded/list'] = []
    const list = await request('thread/loaded/list', {})
    const ids: string[] = (list.result?.data ?? []).filter((x: unknown): x is string => typeof x === 'string')
    for (const id of ids) loaded.add(id)
    out.loadedCount = ids.length
    out.loadedListShape = shape(list.result)
    out.loadedListRttMs = rtts['thread/loaded/list']?.[0]
    const perThread: any[] = (out.threads = [])
    const reads = new Map<string, { before: any; afterResume: any; resume: any }>()
    for (const id of ids) {
      if (stopEarly) break
      const info: ThreadInfo = { label: label(id), knownTurns: new Set(), statusBefore: '<unknown>' }
      threads.set(id, info)
      loadedAtResume.add(id)
      const before = (await request('thread/read', { threadId: id })).result?.thread
      info.statusBefore = statusOf(before)
      resumed.push(id) // before sending: cleanup must unsubscribe even if the reply never arrives
      info.resumeSentAt = Date.now()
      const r = await request('thread/resume', { threadId: id })
      for (const t of r.result?.thread?.turns ?? []) if (typeof t?.id === 'string') info.knownTurns.add(t.id)
      info.resumeFp = resumeFp(r.result)
      const afterResume = (await request('thread/read', { threadId: id })).result?.thread
      reads.set(id, { before, afterResume, resume: r })
    }
    const t0 = performance.now()
    if (!stopEarly) await Promise.race([Bun.sleep(ids.length ? DURATION_MS : Math.min(DURATION_MS, 20_000)), new Promise<void>((res) => { wake = res })])
    out.listenedMs = Math.round(performance.now() - t0)
    for (const id of ids) {
      const rd = reads.get(id)
      if (!rd) continue
      const info = threads.get(id)!
      const afterWindow = stopEarly ? undefined : (await request('thread/read', { threadId: id })).result?.thread
      const { before, afterResume, resume: r } = rd
      const res = r.result
      const turns: any[] = res?.thread?.turns ?? []
      perThread.push({
        thread: info.label,
        sourceKind: sourceKind(before?.source),
        isSubAgent: typeof before?.parentThreadId === 'string',
        statusBefore: info.statusBefore,
        statusAfterResume: statusOf(afterResume),
        statusAfterWindow: afterWindow ? statusOf(afterWindow) : '<skipped>',
        resumeOk: !r.error,
        resumeErr: r.error ? shape(r.error) : undefined,
        resumeErrTemplate: r.error ? errorTemplate(r.error.message) : undefined,
        resumeResultShape: shape({ ...res, thread: { ...(res?.thread ?? {}), turns: `<len:${turns.length}>` } }),
        turnsAtResume: turns.length,
        inProgressTurnsAtResume: turns.filter((t) => t?.status === 'inProgress').length,
        // m1: booleans, never the values
        modelUnchanged: !!before && !!afterResume && before.model === afterResume.model,
        reasoningEffortUnchanged: !!before && !!afterResume && before.reasoningEffort === afterResume.reasoningEffort,
        readSettingsUnchangedAfterResume: !!before && readFp(before) === readFp(afterResume),
        readSettingsUnchangedAfterWindow: afterWindow ? readFp(before) === readFp(afterWindow) : null,
        // m3: the resume result carries approval policy and sandbox; it must agree with thread/read on the shared fields
        resumeHasApprovalPolicy: res?.approvalPolicy !== undefined,
        resumeHasSandbox: res?.sandbox !== undefined,
        resumeAgreesWithRead: !!before && !!res && res.model === before.model && res.modelProvider === before.modelProvider &&
          (res.reasoningEffort ?? null) === (before.reasoningEffort ?? null) && res.cwd === before.cwd,
        resumeSettingsFp: info.resumeFp,
      })
    }
    // re-list once to see whether new threads appeared (visibility)
    if (!stopEarly) {
      const list2 = await request('thread/loaded/list', {})
      const ids2: string[] = list2.result?.data ?? []
      out.loadedCountAfter = ids2.length
      out.newLoadedSinceStart = ids2.filter((i) => !loaded.has(i)).length
    }
  } catch (e) {
    out.error = (e as Error).message
  } finally {
    await cleanup()
  }
  flags.stoppedEarly = stopEarly
  // m2: attribute each turn event after the fact, so a notification that raced the resume reply is still matched.
  out.turns = turnEvents.slice(0, 50).map((ev) => {
    const info = typeof ev.threadId === 'string' ? threads.get(ev.threadId) : undefined
    const msAfterResume = info?.resumeSentAt !== undefined ? ev.at - info.resumeSentAt : null
    const knownAtResume = !!info && typeof ev.turnId === 'string' && info.knownTurns.has(ev.turnId)
    const suspect = ev.event === 'started' && !!info && !knownAtResume && msAfterResume !== null && msAfterResume >= 0 && msAfterResume <= TURN_WINDOW_MS
    return { event: ev.event, thread: label(ev.threadId), turn: typeof ev.turnId === 'string' ? hash(ev.turnId, 8) : '<none>', msAfterResume, knownAtResume, threadStatusBeforeResume: info?.statusBefore ?? null, suspect }
  })
  for (const t of out.turns) if (t.event === 'started') t.suspect ? flags.turnStartedSuspect++ : flags.turnStartedOther++
  out.settingsUpdates = settingsUpdates.map((u) => {
    const info = typeof u.threadId === 'string' ? threads.get(u.threadId) : undefined
    return { thread: label(u.threadId), msAfterResume: info?.resumeSentAt !== undefined ? u.at - info.resumeSentAt : null, matchesResumeSettings: !!info && u.fp === info.resumeFp }
  })
  out.threadStarted = threadStarted
  out.counts = counts
  out.flags = flags
  out.shapes = shapes
  out.itemStartedLatency = stats(latencies.itemStarted)
  out.itemCompletedLatency = stats(latencies.itemCompleted)
  out.rttMs = Object.fromEntries(Object.entries(rtts).map(([k, v]) => [k, stats(v)]))
  out.proxyStderrBytes = duplex?.stderrBytes?.() ?? 0
  console.log(JSON.stringify(out, null, 2))
  process.exit(0)
}

if (import.meta.main) await main()
