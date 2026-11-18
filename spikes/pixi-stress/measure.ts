// Usage: bun spikes/pixi-stress/measure.ts [variant-query ...]   e.g. "glow=filter" "labels=all"
// Headless Playwright Chromium; prints window.__fps (10 s average) and renderer facts per variant.
// UNCAP=1 removes the vsync/frame-rate cap to show headroom.
// GPU=1 adds ANGLE/Metal flags to try for a GPU-backed context.
import { chromium } from 'playwright'

const port = 4398
const server = Bun.serve({ port, hostname: '127.0.0.1', routes: { '/': (await import('./index.html')).default } })
const variants = process.argv.slice(2).length ? process.argv.slice(2) : ['']
const gpuArgs = process.env.GPU === '1' ? ['--use-angle=metal', '--use-gl=angle', '--enable-gpu', '--ignore-gpu-blocklist', '--enable-unsafe-swiftshader=false'] : []
const uncap = process.env.UNCAP === '1' ? ['--disable-gpu-vsync', '--disable-frame-rate-limit'] : []
const browser = await chromium.launch({ headless: true, args: [...gpuArgs, ...uncap] })
for (const v of variants) {
  const ctx = await browser.newContext({ viewport: { width: 2560, height: 1440 }, deviceScaleFactor: 1 })
  const page = await ctx.newPage()
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message)); page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
  await page.goto(`http://127.0.0.1:${port}/?${v}`)
  await page.waitForFunction(() => (window as any).__fpsReady === true, null, { timeout: 60_000 })
  await page.waitForTimeout(2000)
  const r = await page.evaluate(() => { const w = window as any; return { fps: w.__fps, fpsFrames: w.__fpsFrames, ...w.__stress, maxGap: w.__stress.maxGap(), updateMs: w.__stress.updateMs(), hud: document.getElementById('hud')!.textContent } })
  console.log(`[${v || 'default'}] ticker-fps=${r.fps?.toFixed(1)} frames-fps=${r.fpsFrames?.toFixed(1)} maxGap=${r.maxGap.toFixed(1)}ms updateMs=${r.updateMs.toFixed(2)} renderer=${r.rendererType} gl="${r.glRenderer}" software=${r.software} errors=${errors.length}`)
  if (errors.length) console.log(errors.slice(0, 3))
  if (process.env.SHOT) await page.screenshot({ path: process.env.SHOT + `-${v.replace(/[^a-z0-9]/gi, '_') || 'default'}.png` })
  await ctx.close()
}
await browser.close(); server.stop(); process.exit(0)
