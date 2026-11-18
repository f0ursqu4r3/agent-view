// Offline checks for the probe's safety rules. No Codex daemon involved.
// A fake WebSocket JSON-RPC server (unix socket) reports one loaded thread, `fake-thread-1`.
// Scenario "no-opt-in": without AGENT_VIEW_PROBE_RESUME=1 the probe prints why, exits 0, and sends nothing.
// Scenario "sigint":    the fake never answers thread/resume; SIGINT during the resume. Pass: thread/unsubscribe arrives.
// Scenario "timeout":   the fake never answers thread/resume; the request timeout fires. Pass: thread/unsubscribe arrives.
// Scenario "attach":    the fake answers and then sends turn, settings and item notifications, plus thread/started for an
//                       unloaded thread. Pass: thread params are exactly {threadId: fake-thread-1} (m4), no secret string
//                       reaches stdout (m1), turn attribution counts are right (m2), settings checks are right (m3).
// Scenario "server-request": the fake sends an approval request after the resume. Pass: the probe never answers it,
//                       stops listening early, and unsubscribes (m6).
// Scenario "guard":     checkOutgoing() refuses extra params, unknown thread ids, unknown methods, and replies (m4).
// Usage: bun spikes/codex-daemon-probe.offline-check.ts
import { createHash } from 'node:crypto'
import { rmSync } from 'node:fs'
import { checkOutgoing } from './codex-daemon-probe.ts'

const sockPath = `/private/tmp/av-fake-codex-${process.pid}.sock`
type Seen = { method?: string; id?: unknown; params?: any }[]
type Send = (obj: unknown) => void
type FakeOpts = { answerResume: boolean; afterResume?: (send: Send) => void }

const SECRET = 'SECRET-VALUE-MUST-NOT-PRINT'
const T1 = 'fake-thread-1'
const thread = (id: string) => ({ id, model: `${SECRET}-model`, modelProvider: 'p', reasoningEffort: `${SECRET}-effort`, cwd: `/${SECRET}/cwd`, source: 'cli', ephemeral: false, name: null, path: null, parentThreadId: null, forkedFromId: null, status: { type: 'idle' }, turns: [] })
const settings = { model: `${SECRET}-model`, modelProvider: 'p', serviceTier: null, cwd: `/${SECRET}/cwd`, approvalPolicy: 'on-request', approvalsReviewer: 'user', reasoningEffort: `${SECRET}-effort`, collaborationMode: null }
const sandbox = { type: 'workspaceWrite', writableRoots: [`/${SECRET}/cwd`], networkAccess: false, excludeTmpdirEnvVar: false, excludeSlashTmp: false }

function wsText(obj: unknown): Buffer {
  const body = Buffer.from(JSON.stringify(obj))
  const len = body.length
  const head = len < 126 ? Buffer.from([0x81, len]) : len < 65536 ? Buffer.from([0x81, 126, len >> 8, len & 255]) : (() => { const b = Buffer.alloc(10); b[0] = 0x81; b[1] = 127; b.writeBigUInt64BE(BigInt(len), 2); return b })()
  return Buffer.concat([head, body])
}

function startFake(seen: Seen, opts: FakeOpts) {
  return Bun.listen<{ buf: Buffer; up: boolean }>({
    unix: sockPath,
    socket: {
      open(s) { s.data = { buf: Buffer.alloc(0), up: false } },
      data(s, d) {
        s.data.buf = Buffer.concat([s.data.buf, Buffer.from(d)])
        if (!s.data.up) {
          const i = s.data.buf.indexOf('\r\n\r\n'); if (i < 0) return
          const key = /Sec-WebSocket-Key: (.+)\r\n/i.exec(s.data.buf.subarray(0, i).toString())![1].trim()
          const acc = createHash('sha1').update(key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64')
          s.write(`HTTP/1.1 101 Switching Protocols\r\nconnection: Upgrade\r\nupgrade: websocket\r\nsec-websocket-accept: ${acc}\r\n\r\n`)
          s.data.buf = s.data.buf.subarray(i + 4); s.data.up = true
        }
        const send: Send = (obj) => { try { s.write(wsText(obj)) } catch {} }
        for (;;) {
          const b = s.data.buf; if (b.length < 2) return
          const op = b[0] & 15; let len = b[1] & 127, off = 2
          if (len === 126) { if (b.length < 4) return; len = b.readUInt16BE(2); off = 4 } else if (len === 127) { if (b.length < 10) return; len = Number(b.readBigUInt64BE(2)); off = 10 }
          if (b.length < off + 4 + len) return
          const mask = b.subarray(off, off + 4), p = Buffer.from(b.subarray(off + 4, off + 4 + len))
          for (let k = 0; k < len; k++) p[k] ^= mask[k % 4]
          s.data.buf = b.subarray(off + 4 + len)
          if (op !== 1) continue
          const m = JSON.parse(p.toString()); seen.push({ method: m.method, id: m.id, params: m.params })
          const reply = (result: unknown) => send({ jsonrpc: '2.0', id: m.id, result })
          if (m.method === 'initialize') reply({ userAgent: 'fake', codexHome: 'x', platformFamily: 'unix', platformOs: 'macos' })
          else if (m.method === 'thread/loaded/list') reply({ data: [T1], nextCursor: null })
          else if (m.method === 'thread/read') reply({ thread: thread(T1) })
          else if (m.method === 'thread/unsubscribe') reply({ status: 'unsubscribed' })
          else if (m.method === 'thread/resume' && opts.answerResume) {
            reply({ ...settings, sandbox, thread: { ...thread(T1), turns: [{ id: 'turn-old', status: 'inProgress', items: [] }] }, disabledPluginIds: [], instructionSources: [], turnsBackwardsCursor: null, itemsBackwardsCursor: null })
            opts.afterResume?.(send)
          }
        }
      },
    },
  })
}

function spawnProbe(env: Record<string, string>) {
  return Bun.spawn(['bun', 'spikes/codex-daemon-probe.ts'], {
    env: { ...process.env, AGENT_VIEW_PROBE_RESUME: '1', TRANSPORT: 'socket', SOCKET_PATH: sockPath, RTT_SAMPLES: '1', ...env },
    stdout: 'pipe', stderr: 'pipe',
  })
}
async function exitOf(proc: ReturnType<typeof spawnProbe>, ms = 15_000) {
  const code = await Promise.race([proc.exited, Bun.sleep(ms).then(() => 'hung' as const)])
  if (code === 'hung') proc.kill('SIGKILL')
  return code
}
const threadCalls = (seen: Seen) => seen.filter((m) => m.method === 'thread/read' || m.method === 'thread/resume' || m.method === 'thread/unsubscribe')
const exactlyT1 = (seen: Seen) => threadCalls(seen).every((m) => JSON.stringify(m.params) === JSON.stringify({ threadId: T1 }))

async function scenario(name: 'sigint' | 'timeout') {
  const seen: Seen = []
  const server = startFake(seen, { answerResume: false })
  const proc = spawnProbe({ PROBE_MS: '5000', REQUEST_TIMEOUT_MS: name === 'timeout' ? '1500' : '10000' })
  const t0 = Date.now()
  while (!seen.some((m) => m.method === 'thread/resume')) { if (Date.now() - t0 > 8000) break; await Bun.sleep(20) }
  const resumeSeen = seen.some((m) => m.method === 'thread/resume')
  if (name === 'sigint') { await Bun.sleep(200); proc.kill('SIGINT') }
  const code = await exitOf(proc)
  await Bun.sleep(100)
  const unsub = seen.filter((m) => m.method === 'thread/unsubscribe')
  const ok = resumeSeen && unsub.length >= 1 && exactlyT1(seen)
  const methods = [...new Set(seen.map((m) => m.method))]
  console.log(`${name.padEnd(14)} exit=${code} resumeSeen=${resumeSeen} unsubscribeCount=${unsub.length} methods=[${methods.join(', ')}] -> ${ok ? 'PASS' : 'FAIL'}`)
  server.stop(true); rmSync(sockPath, { force: true })
  return ok
}

async function noOptIn() {
  const seen: Seen = []
  const server = startFake(seen, { answerResume: true })
  const env: Record<string, string | undefined> = { ...process.env, TRANSPORT: 'socket', SOCKET_PATH: sockPath, RTT_SAMPLES: '1', PROBE_MS: '1000' }
  delete env.AGENT_VIEW_PROBE_RESUME
  const proc = Bun.spawn(['bun', 'spikes/codex-daemon-probe.ts'], { env, stdout: 'pipe', stderr: 'pipe' })
  const code = await exitOf(proc)
  const text = await new Response(proc.stdout).text()
  const ok = code === 0 && seen.length === 0 && text.includes('AGENT_VIEW_PROBE_RESUME=1')
  console.log(`${'no-opt-in'.padEnd(14)} exit=${code} messagesSent=${seen.length} explains=${text.includes('AGENT_VIEW_PROBE_RESUME=1')} -> ${ok ? 'PASS' : 'FAIL'}`)
  server.stop(true); rmSync(sockPath, { force: true })
  return ok
}

async function attach() {
  const seen: Seen = []
  const at = (ms: number, send: Send, obj: unknown) => setTimeout(() => send(obj), ms)
  const server = startFake(seen, {
    answerResume: true,
    afterResume: (send) => {
      at(50, send, { jsonrpc: '2.0', method: 'thread/started', params: { thread: { ...thread('fake-thread-2'), source: 'vscode' } } })
      at(100, send, { jsonrpc: '2.0', method: 'turn/started', params: { threadId: T1, turn: { id: 'turn-old', status: 'inProgress', items: [] } } }) // known at resume
      at(200, send, { jsonrpc: '2.0', method: 'turn/started', params: { threadId: T1, turn: { id: 'turn-new', status: 'inProgress', items: [] } } }) // suspect
      at(250, send, { jsonrpc: '2.0', method: 'thread/settings/updated', params: { threadId: T1, threadSettings: { ...settings, sandboxPolicy: sandbox, effort: settings.reasoningEffort, disabledPluginIds: [], activePermissionProfile: null, summary: null, personality: null } } })
      at(300, send, { jsonrpc: '2.0', method: 'item/started', params: { threadId: T1, turnId: 'turn-old', startedAtMs: Date.now(), item: { type: 'commandExecution', id: 'i1', command: `${SECRET} cmd`, cwd: '/x', status: 'inProgress' } } })
      at(1600, send, { jsonrpc: '2.0', method: 'turn/started', params: { threadId: T1, turn: { id: 'turn-late', status: 'inProgress', items: [] } } }) // outside window
    },
  })
  const proc = spawnProbe({ PROBE_MS: '2500', TURN_WINDOW_MS: '1000' })
  const code = await exitOf(proc)
  const text = await new Response(proc.stdout).text()
  let out: any = {}
  try { out = JSON.parse(text) } catch {}
  const t = out.threads?.[0] ?? {}
  const checks: Record<string, boolean> = {
    exit0: code === 0,
    m4ParamsExactlyThreadId: threadCalls(seen).length >= 4 && exactlyT1(seen),
    m4NoUnloadedThread: !JSON.stringify(seen).includes('fake-thread-2'),
    m1NoSecretPrinted: text.length > 0 && !text.includes('SECRET'),
    m2Suspect1: out.flags?.turnStartedSuspect === 1,
    m2Other2: out.flags?.turnStartedOther === 2,
    m3ApprovalAndSandbox: t.resumeHasApprovalPolicy === true && t.resumeHasSandbox === true,
    m3ResumeAgreesWithRead: t.resumeAgreesWithRead === true && t.modelUnchanged === true && t.readSettingsUnchangedAfterWindow === true,
    m3SettingsUpdateMatches: out.settingsUpdates?.[0]?.matchesResumeSettings === true,
    visibility: out.threadStarted?.[0]?.sourceKind === 'vscode' && out.threadStarted?.[0]?.wasLoaded === false,
    latency: out.itemStartedLatency?.n === 1,
  }
  const ok = Object.values(checks).every(Boolean)
  console.log(`${'attach'.padEnd(14)} exit=${code} ${Object.entries(checks).filter(([, v]) => !v).map(([k]) => `FAILED:${k}`).join(' ')} -> ${ok ? 'PASS' : 'FAIL'}`)
  server.stop(true); rmSync(sockPath, { force: true })
  return ok
}

async function serverRequest() {
  const seen: Seen = []
  const server = startFake(seen, {
    answerResume: true,
    afterResume: (send) => { setTimeout(() => send({ jsonrpc: '2.0', id: 9001, method: 'item/commandExecution/requestApproval', params: { threadId: T1, turnId: 'turn-old', itemId: 'i1' } }), 300) },
  })
  const t0 = Date.now()
  const proc = spawnProbe({ PROBE_MS: '20000' })
  const code = await exitOf(proc)
  const ms = Date.now() - t0
  const text = await new Response(proc.stdout).text()
  let out: any = {}
  try { out = JSON.parse(text) } catch {}
  const answered = seen.some((m) => m.method === undefined || m.id === 9001)
  const unsub = seen.filter((m) => m.method === 'thread/unsubscribe').length
  const ok = code === 0 && ms < 8000 && !answered && unsub >= 1 && out.flags?.stoppedEarly === true && out.flags?.serverRequests === 1
  console.log(`${'server-request'.padEnd(14)} exit=${code} ms=${ms} answered=${answered} unsubscribeCount=${unsub} stoppedEarly=${out.flags?.stoppedEarly} -> ${ok ? 'PASS' : 'FAIL'}`)
  server.stop(true); rmSync(sockPath, { force: true })
  return ok
}

function guard() {
  const loaded = new Set(['a'])
  const on = { loaded, resumeOptIn: true }
  const cases: [string, unknown, typeof on, boolean][] = [
    ['resume {threadId} of a loaded thread', { method: 'thread/resume', params: { threadId: 'a' } }, on, true],
    ['resume with an override', { method: 'thread/resume', params: { threadId: 'a', model: 'x' } }, on, false],
    ['resume with excludeTurns', { method: 'thread/resume', params: { threadId: 'a', excludeTurns: true } }, on, false],
    ['resume of an unloaded thread', { method: 'thread/resume', params: { threadId: 'b' } }, on, false],
    ['resume without opt-in', { method: 'thread/resume', params: { threadId: 'a' } }, { loaded, resumeOptIn: false }, false],
    ['read {threadId}', { method: 'thread/read', params: { threadId: 'a' } }, on, true],
    ['read with includeTurns', { method: 'thread/read', params: { threadId: 'a', includeTurns: true } }, on, false],
    ['unsubscribe of an unloaded thread', { method: 'thread/unsubscribe', params: { threadId: 'b' } }, on, false],
    ['turn/start', { method: 'turn/start', params: { threadId: 'a' } }, on, false],
    ['a reply to a server request', { id: 9001, result: { decision: 'accept' } }, on, false],
    ['loaded/list with a cursor', { method: 'thread/loaded/list', params: { cursor: 'x' } }, on, false],
    ['initialize with capabilities', { method: 'initialize', params: { clientInfo: {}, capabilities: {} } }, on, false],
  ]
  const failed: string[] = []
  for (const [name, msg, ctx, allowed] of cases) {
    let threw = false
    try { checkOutgoing(msg as any, ctx) } catch { threw = true }
    if (threw === allowed) failed.push(name)
  }
  console.log(`${'guard'.padEnd(14)} cases=${cases.length} ${failed.map((f) => `FAILED:"${f}"`).join(' ')} -> ${failed.length ? 'FAIL' : 'PASS'}`)
  return failed.length === 0
}

const results = [guard(), await noOptIn(), await scenario('sigint'), await scenario('timeout'), await attach(), await serverRequest()]
process.exit(results.every(Boolean) ? 0 : 1)
