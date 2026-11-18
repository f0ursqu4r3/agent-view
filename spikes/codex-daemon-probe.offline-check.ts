// Offline check for the probe's unsubscribe-on-exit guarantee. No Codex daemon involved.
// A fake WebSocket JSON-RPC server (unix socket) reports one loaded thread and NEVER answers thread/resume.
// Scenario "sigint": send SIGINT while the resume is in flight.  Scenario "timeout": let the 10 s request timeout fire.
// Pass: the fake receives thread/unsubscribe for the thread, in both scenarios. Both set AGENT_VIEW_PROBE_RESUME=1.
// Scenario "no-opt-in": without AGENT_VIEW_PROBE_RESUME=1 the probe prints why, exits 0, and sends nothing.
// Usage: bun spikes/codex-daemon-probe.offline-check.ts
import { createHash } from 'node:crypto'
import { rmSync } from 'node:fs'

const sockPath = `/private/tmp/av-fake-codex-${process.pid}.sock`
type Seen = { method: string; params?: any }[]

function startFake(seen: Seen) {
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
        for (;;) {
          const b = s.data.buf; if (b.length < 2) return
          const op = b[0] & 15; let len = b[1] & 127, off = 2
          if (len === 126) { if (b.length < 4) return; len = b.readUInt16BE(2); off = 4 } else if (len === 127) { if (b.length < 10) return; len = Number(b.readBigUInt64BE(2)); off = 10 }
          if (b.length < off + 4 + len) return
          const mask = b.subarray(off, off + 4), p = Buffer.from(b.subarray(off + 4, off + 4 + len))
          for (let k = 0; k < len; k++) p[k] ^= mask[k % 4]
          s.data.buf = b.subarray(off + 4 + len)
          if (op !== 1) continue
          const m = JSON.parse(p.toString()); seen.push({ method: m.method, params: m.params })
          const reply = (result: unknown) => { const body = Buffer.from(JSON.stringify({ jsonrpc: '2.0', id: m.id, result })); s.write(Buffer.concat([Buffer.from([0x81, body.length < 126 ? body.length : 126, ...(body.length < 126 ? [] : [body.length >> 8, body.length & 255])]), body])) }
          if (m.method === 'initialize') reply({ userAgent: 'fake', codexHome: 'x', platformFamily: 'unix', platformOs: 'macos' })
          else if (m.method === 'thread/loaded/list') reply({ data: ['fake-thread-1'], nextCursor: null })
          else if (m.method === 'thread/read') reply({ thread: { model: 'm', modelProvider: 'p', reasoningEffort: 'low', status: { type: 'idle' } } })
          else if (m.method === 'thread/unsubscribe') reply({ status: 'unsubscribed' })
          // thread/resume: deliberately no reply
        }
      },
    },
  })
}

async function scenario(name: 'sigint' | 'timeout') {
  const seen: Seen = []
  const server = startFake(seen)
  const proc = Bun.spawn(['bun', 'spikes/codex-daemon-probe.ts'], {
    env: { ...process.env, AGENT_VIEW_PROBE_RESUME: '1', TRANSPORT: 'socket', SOCKET_PATH: sockPath, RTT_SAMPLES: '1', PROBE_MS: '5000', REQUEST_TIMEOUT_MS: name === 'timeout' ? '1500' : '10000' },
    stdout: 'pipe', stderr: 'pipe',
  })
  const t0 = Date.now()
  while (!seen.some((m) => m.method === 'thread/resume')) { if (Date.now() - t0 > 8000) break; await Bun.sleep(20) }
  const resumeSeen = seen.some((m) => m.method === 'thread/resume')
  if (name === 'sigint') { await Bun.sleep(200); proc.kill('SIGINT') }
  const code = await Promise.race([proc.exited, Bun.sleep(15_000).then(() => 'hung')])
  if (code === 'hung') proc.kill('SIGKILL')
  await Bun.sleep(100)
  const unsub = seen.filter((m) => m.method === 'thread/unsubscribe')
  const ok = resumeSeen && unsub.length >= 1 && unsub.every((m) => m.params?.threadId === 'fake-thread-1')
  const methods = [...new Set(seen.map((m) => m.method))]
  console.log(`${name.padEnd(8)} exit=${code} resumeSeen=${resumeSeen} unsubscribeCount=${unsub.length} methods=[${methods.join(', ')}] -> ${ok ? 'PASS' : 'FAIL'}`)
  server.stop(true); rmSync(sockPath, { force: true })
  return ok
}
async function noOptIn() {
  const seen: Seen = []
  const server = startFake(seen)
  const env: Record<string, string | undefined> = { ...process.env, TRANSPORT: 'socket', SOCKET_PATH: sockPath, RTT_SAMPLES: '1', PROBE_MS: '1000' }
  delete env.AGENT_VIEW_PROBE_RESUME
  const proc = Bun.spawn(['bun', 'spikes/codex-daemon-probe.ts'], { env, stdout: 'pipe', stderr: 'pipe' })
  const code = await Promise.race([proc.exited, Bun.sleep(15_000).then(() => 'hung')])
  if (code === 'hung') proc.kill('SIGKILL')
  const text = await new Response(proc.stdout).text()
  const ok = code === 0 && seen.length === 0 && text.includes('AGENT_VIEW_PROBE_RESUME=1')
  console.log(`${'no-opt-in'.padEnd(8)} exit=${code} messagesSent=${seen.length} explains=${text.includes('AGENT_VIEW_PROBE_RESUME=1')} -> ${ok ? 'PASS' : 'FAIL'}`)
  server.stop(true); rmSync(sockPath, { force: true })
  return ok
}

const g = await noOptIn(), a = await scenario('sigint'), b = await scenario('timeout')
process.exit(g && a && b ? 0 : 1)
