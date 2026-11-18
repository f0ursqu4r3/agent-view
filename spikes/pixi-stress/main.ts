// Throwaway spike: PixiJS stage stress test.
// 3,000 file circles (+ labels), 50 agent sprites with additive glow, 200 effect lines per second.
import { Application, BitmapText, Container, Graphics, Sprite, Texture, BlurFilter } from 'pixi.js'

const q = new URLSearchParams(location.search)
const cfg = {
  glow: (q.get('glow') ?? 'sprite') as 'sprite' | 'filter',
  labels: (q.get('labels') ?? 'lazy') as 'lazy' | 'all',
  lines: (q.get('lines') ?? 'sprite') as 'sprite' | 'graphics',
  zoom: Number(q.get('zoom') ?? 1),
  w: Number(q.get('w') ?? 2560),
  h: Number(q.get('h') ?? 1440),
  nodes: Number(q.get('nodes') ?? 3000),
  agents: Number(q.get('agents') ?? 50),
  rate: Number(q.get('rate') ?? 200), // effect lines per second
}
const LABEL_MS = 3000, LABEL_ZOOM = 1.5, LINE_MS = 450, WINDOW_MS = 10_000
const CATS = [0x8f9bff, 0xff5fa8, 0x6dff9e, 0xffd166, 0xb9a6ff]
const AGENT_COLORS = [0xff8a4c, 0x36c6ff]
const hud = document.getElementById('hud')!
const w = window as any

const app = new Application()
await app.init({ width: cfg.w, height: cfg.h, background: 0x05070d, antialias: false, resolution: 1, autoDensity: false, preference: 'webgl', powerPreference: 'high-performance' })
document.getElementById('stage')!.appendChild(app.canvas)

// renderer facts
const gl = (app.renderer as any).gl as WebGL2RenderingContext | undefined
let glRenderer = 'n/a'
if (gl) {
  const ext = gl.getExtension('WEBGL_debug_renderer_info')
  glRenderer = ext ? String(gl.getParameter(ext.UNMASKED_RENDERER_WEBGL)) : String(gl.getParameter(gl.RENDERER))
}
const rendererType = String(app.renderer.name ?? (app.renderer as any).type)
const software = /swiftshader|llvmpipe|software|basic render/i.test(glRenderer)

// textures: circle for file nodes, radial gradient glow for agents (pre-rendered, no filters)
function canvasTex(size: number, draw: (g: CanvasRenderingContext2D, s: number) => void) {
  const c = document.createElement('canvas'); c.width = c.height = size
  draw(c.getContext('2d')!, size)
  return Texture.from(c)
}
const circleTex = canvasTex(16, (g, s) => { g.fillStyle = '#fff'; g.beginPath(); g.arc(s / 2, s / 2, s / 2 - 1, 0, Math.PI * 2); g.fill() })
const glowTex = canvasTex(128, (g, s) => {
  const grd = g.createRadialGradient(s / 2, s / 2, 0, s / 2, s / 2, s / 2)
  grd.addColorStop(0, 'rgba(255,255,255,0.95)'); grd.addColorStop(0.25, 'rgba(255,255,255,0.45)'); grd.addColorStop(1, 'rgba(255,255,255,0)')
  g.fillStyle = grd; g.fillRect(0, 0, s, s)
})

const world = new Container()
app.stage.addChild(world)
world.scale.set(cfg.zoom)
world.position.set((cfg.w - cfg.w * cfg.zoom) / 2, (cfg.h - cfg.h * cfg.zoom) / 2) // zoom about the centre

// file nodes: phyllotaxis layout so the spread is even
type Node = { s: Sprite; x: number; y: number; touched: number; label: BitmapText | null; name: string; pulse: number }
const nodeLayer = new Container(), labelLayer = new Container(), lineLayer = new Container(), agentLayer = new Container()
world.addChild(nodeLayer, lineLayer, agentLayer, labelLayer)
const nodes: Node[] = []
const R = Math.min(cfg.w, cfg.h) * 0.47
for (let i = 0; i < cfg.nodes; i++) {
  const r = R * Math.sqrt((i + 0.5) / cfg.nodes), a = i * 2.399963
  const x = cfg.w / 2 + Math.cos(a) * r * (cfg.w / cfg.h) * 0.9, y = cfg.h / 2 + Math.sin(a) * r
  const s = new Sprite(circleTex); s.anchor.set(0.5); s.tint = CATS[i % CATS.length]; s.alpha = 0.55; s.scale.set(0.4); s.position.set(x, y)
  nodeLayer.addChild(s)
  nodes.push({ s, x, y, touched: -1e9, label: null, name: `src/mod${i % 97}/file-${i}.ts`, pulse: 0 })
}
const labelPool: BitmapText[] = []
function labelFor(n: Node): BitmapText {
  if (n.label) return n.label
  const t = labelPool.pop() ?? new BitmapText({ text: '', style: { fontFamily: 'Menlo, monospace', fontSize: 11, fill: 0xdfe8ff } })
  t.text = n.name; t.position.set(n.x + 7, n.y - 6); t.scale.set(1 / Math.max(1, cfg.zoom) * 1) // keep screen size roughly constant
  labelLayer.addChild(t); n.label = t
  return t
}
function dropLabel(n: Node) { if (n.label) { labelLayer.removeChild(n.label); labelPool.push(n.label); n.label = null } }

// agents with additive glow
type Agent = { c: Container; ph: number; sx: number; sy: number; rx: number; ry: number; sp: number; color: number }
const agents: Agent[] = []
for (let i = 0; i < cfg.agents; i++) {
  const color = AGENT_COLORS[i % 2]
  const c = new Container()
  if (cfg.glow === 'sprite') {
    const g = new Sprite(glowTex); g.anchor.set(0.5); g.tint = color; g.blendMode = 'add'; g.scale.set(1.1); c.addChild(g)
  } else {
    const g = new Graphics().circle(0, 0, 14).fill({ color, alpha: 0.9 }); g.blendMode = 'add'
    g.filters = [new BlurFilter({ strength: 10, quality: 3 })]; c.addChild(g)
  }
  const core = new Sprite(circleTex); core.anchor.set(0.5); core.tint = color; core.scale.set(0.9); c.addChild(core)
  agentLayer.addChild(c)
  agents.push({ c, ph: Math.random() * 6.28, sx: cfg.w / 2, sy: cfg.h / 2, rx: 200 + Math.random() * (cfg.w * 0.35), ry: 100 + Math.random() * (cfg.h * 0.35), sp: 0.15 + Math.random() * 0.35, color })
}

// effect lines
type Line = { s: Sprite | null; x0: number; y0: number; x1: number; y1: number; color: number; born: number }
const live: Line[] = [], spritePool: Sprite[] = []
const gfx = new Graphics(); if (cfg.lines === 'graphics') lineLayer.addChild(gfx)
function fire(now: number) {
  const a = agents[(Math.random() * agents.length) | 0], n = nodes[(Math.random() * nodes.length) | 0]
  const ln: Line = { s: null, x0: a.c.x, y0: a.c.y, x1: n.x, y1: n.y, color: a.color, born: now }
  if (cfg.lines === 'sprite') {
    const s = spritePool.pop() ?? Object.assign(new Sprite(Texture.WHITE), {}); s.anchor.set(0, 0.5); s.height = 2; s.tint = ln.color
    const dx = ln.x1 - ln.x0, dy = ln.y1 - ln.y0; s.position.set(ln.x0, ln.y0); s.rotation = Math.atan2(dy, dx); s.width = Math.hypot(dx, dy)
    lineLayer.addChild(s); ln.s = s
  }
  live.push(ln)
  n.touched = now; n.pulse = 1
  if (cfg.labels === 'lazy' && n.s.visible) labelFor(n).visible = true
}

// per-node style: touched nodes pulse
const pulsing = new Set<Node>()
function labelRule(now: number) {
  // lazy: label exists only if touched within LABEL_MS, or zoom > LABEL_ZOOM and inside the viewport
  const zoomed = cfg.zoom > LABEL_ZOOM
  for (const n of nodes) {
    const hot = now - n.touched < LABEL_MS
    let show = hot
    if (!show && zoomed) {
      const sx = n.x * cfg.zoom + world.x, sy = n.y * cfg.zoom + world.y
      show = sx > -40 && sx < cfg.w + 40 && sy > -20 && sy < cfg.h + 20
    }
    if (show) labelFor(n); else dropLabel(n)
  }
}
if (cfg.labels === 'all') for (const n of nodes) labelFor(n) // worst case: every label alive and drawn

// FPS: average of app.ticker.FPS over the last 10 s, plus frame-count FPS
const samples: { t: number; fps: number }[] = []
const frames: number[] = []
let acc = 0, fireAcc = 0, labelAcc = 0, lastHud = 0, minFrameGap = 1e9, maxFrameGap = 0, lastT = performance.now()
const t0 = performance.now()
w.__stress = { cfg, rendererType, glRenderer, software, ready: false }
w.__fps = null; w.__fpsFrames = null; w.__fpsReady = false

let cpuSum = 0, cpuN = 0
app.ticker.add((tk) => {
  const now = performance.now(), dtMs = tk.deltaMS
  const gap = now - lastT; lastT = now
  if (now - t0 > 1000) { minFrameGap = Math.min(minFrameGap, gap); maxFrameGap = Math.max(maxFrameGap, gap) }
  acc += dtMs / 1000
  for (const a of agents) {
    const t = acc * a.sp + a.ph
    a.c.position.set(a.sx + Math.cos(t) * a.rx, a.sy + Math.sin(t * 1.3) * a.ry)
  }
  // fire effect lines at cfg.rate per second
  fireAcc += (dtMs / 1000) * cfg.rate
  while (fireAcc >= 1) { fireAcc -= 1; fire(now); }
  // expire lines
  let w2 = 0
  if (cfg.lines === 'graphics') gfx.clear()
  for (let i = 0; i < live.length; i++) {
    const l = live[i], age = now - l.born
    if (age >= LINE_MS) { if (l.s) { lineLayer.removeChild(l.s); spritePool.push(l.s) } continue }
    const a = 1 - age / LINE_MS
    if (l.s) l.s.alpha = a
    else gfx.moveTo(l.x0, l.y0).lineTo(l.x1, l.y1).stroke({ width: 2, color: l.color, alpha: a })
    live[w2++] = l
  }
  live.length = w2
  // node pulses + lazy labels
  for (const n of nodes) {
    if (n.pulse > 0) { n.pulse = Math.max(0, n.pulse - dtMs / 600); n.s.scale.set(0.4 + n.pulse * 0.5); n.s.alpha = 0.55 + n.pulse * 0.45; if (n.pulse === 0) pulsing.delete(n) }
  }
  if (cfg.labels === 'lazy') { labelAcc += dtMs; if (labelAcc > 250) { labelAcc = 0; labelRule(now) } }
  cpuSum += performance.now() - now; cpuN++
  // fps
  samples.push({ t: now, fps: app.ticker.FPS }); frames.push(now)
  while (samples.length && now - samples[0].t > WINDOW_MS) samples.shift()
  while (frames.length && now - frames[0] > WINDOW_MS) frames.shift()
  const span = now - frames[0]
  if (span >= WINDOW_MS - 100) {
    w.__fpsReady = true
    w.__fps = samples.reduce((s, x) => s + x.fps, 0) / samples.length
    w.__fpsFrames = ((frames.length - 1) / span) * 1000
  }
  if (now - lastHud > 500) {
    lastHud = now
    const shown = labelLayer.children.length
    hud.textContent =
      `FPS (ticker, 10s avg): ${w.__fps == null ? '... warming up' : w.__fps.toFixed(1)}   frames/s: ${w.__fpsFrames == null ? '...' : w.__fpsFrames.toFixed(1)}   now: ${app.ticker.FPS.toFixed(1)}\n` +
      `renderer: ${rendererType} / ${glRenderer}${software ? '  [SOFTWARE]' : ''}\n` +
      `canvas ${cfg.w}x${cfg.h}  dpr ${devicePixelRatio}  zoom ${cfg.zoom}\n` +
      `nodes ${cfg.nodes}  labels shown ${shown}  agents ${cfg.agents}  lines/s ${cfg.rate} (live ${live.length})\n` +
      `glow=${cfg.glow} labels=${cfg.labels} lines=${cfg.lines}  max frame gap ${maxFrameGap.toFixed(1)}ms  update ${(cpuSum / Math.max(1, cpuN)).toFixed(2)}ms`
  }
})
w.__stress.ready = true
w.__stress.maxGap = () => maxFrameGap
w.__stress.updateMs = () => cpuSum / Math.max(1, cpuN)
