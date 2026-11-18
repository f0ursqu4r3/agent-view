// Throwaway spike: Bun fs.watch recursive vs per-directory, on four path classes.
// For each class and mode: write 1,000 files in 2 s (10 subdirs x 100 files), report events, missed files, p95 latency.
import { watch, mkdirSync, writeFileSync, rmSync, symlinkSync, realpathSync, type FSWatcher } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, dirname, join, resolve } from 'node:path'

const FILES = 1000, SUBDIRS = 10, WRITE_MS = 2000, SETTLE_MS = 1500, RUNS = Number(process.env.RUNS ?? 3)
const repoTmp = resolve(import.meta.dir, '../test/.tmp')
mkdirSync(repoTmp, { recursive: true })
const tag = `probe-${process.pid}`

type Class = { name: string; opts?: Opts; modes?: ('recursive' | 'per-dir')[]; make: () => { watchPath: string; cleanup: () => void } }
const classes: Class[] = [
  { name: 'home', make: () => { const d = join(repoTmp, `${tag}-home`); mkdirSync(d, { recursive: true }); return { watchPath: d, cleanup: () => rmSync(d, { recursive: true, force: true }) } } },
  { name: 'tmpdir', make: () => { const d = join(realpathSync(tmpdir()), `av-${tag}`); mkdirSync(d, { recursive: true }); return { watchPath: d, cleanup: () => rmSync(d, { recursive: true, force: true }) } } },
  { name: 'private-tmp', make: () => { const d = `/private/tmp/av-${tag}`; mkdirSync(d, { recursive: true }); return { watchPath: d, cleanup: () => rmSync(d, { recursive: true, force: true }) } } },
  { name: 'private-tmp+late-subdirs', opts: { lateDirs: true }, make: () => { const d = `/private/tmp/av-${tag}-late`; mkdirSync(d, { recursive: true }); return { watchPath: d, cleanup: () => rmSync(d, { recursive: true, force: true }) } } },
  // Root-watch variant: the watcher sits on /private/tmp itself (shared, busy root); files are written in a sub-directory of it.
  { name: 'private-tmp-ROOT-watch', opts: { watchParent: true }, modes: ['recursive'], make: () => { const d = `/private/tmp/av-${tag}-rootwatch`; mkdirSync(d, { recursive: true }); return { watchPath: d, cleanup: () => rmSync(d, { recursive: true, force: true }) } } },
  { name: 'private-tmp+external-writer', opts: { external: true }, make: () => { const d = `/private/tmp/av-${tag}-ext`; mkdirSync(d, { recursive: true }); return { watchPath: d, cleanup: () => rmSync(d, { recursive: true, force: true }) } } },
  { name: 'tmp-symlinked-path', make: () => { const d = `/tmp/av-${tag}-lnk`; mkdirSync(d, { recursive: true }); return { watchPath: d, cleanup: () => rmSync(d, { recursive: true, force: true }) } } },
  { name: 'home+external-writer', opts: { external: true }, make: () => { const d = join(repoTmp, `${tag}-hext`); mkdirSync(d, { recursive: true }); return { watchPath: d, cleanup: () => rmSync(d, { recursive: true, force: true }) } } },
  { name: 'symlink', make: () => {
    const real = join(repoTmp, `${tag}-real`), link = join(repoTmp, `${tag}-link`)
    mkdirSync(real, { recursive: true }); symlinkSync(real, link)
    return { watchPath: link, cleanup: () => { rmSync(link, { force: true }); rmSync(real, { recursive: true, force: true }) } }
  } },
]

const p95 = (a: number[]) => a.length ? [...a].sort((x, y) => x - y)[Math.min(a.length - 1, Math.floor(a.length * 0.95))] : null

type Opts = { lateDirs?: boolean; external?: boolean; watchParent?: boolean }
async function run(root: string, mode: 'recursive' | 'per-dir', opts: Opts = {}) {
  const subs = Array.from({ length: SUBDIRS }, (_, i) => `d${i}`)
  if (!opts.lateDirs || mode === 'per-dir') for (const s of subs) mkdirSync(join(root, s), { recursive: true })
  const wrote = new Map<string, number>() // rel path -> write time
  const first = new Map<string, number>() // rel path -> first event time
  let events = 0, unknown = 0
  const watchers: FSWatcher[] = []
  const onEvent = (prefix: string) => (_t: string, name: string | Buffer | null) => {
    events++
    let rel = name == null ? null : (prefix ? `${prefix}/${name}` : String(name))
    if (rel && opts.watchParent && rel.startsWith(`${basename(root)}/`)) rel = rel.slice(basename(root).length + 1) // root-watch: strip the sub-directory name
    if (rel && wrote.has(rel)) { if (!first.has(rel)) first.set(rel, performance.now()) } else unknown++
  }
  if (mode === 'recursive') watchers.push(watch(opts.watchParent ? dirname(root) : root, { recursive: true }, onEvent('')))
  else { watchers.push(watch(root, onEvent(''))); for (const s of subs) watchers.push(watch(join(root, s), onEvent(s))) }
  await Bun.sleep(300) // let the watchers arm
  if (opts.lateDirs && mode === 'recursive') for (const s of subs) mkdirSync(join(root, s), { recursive: true })
  const t0 = performance.now()
  if (opts.external) {
    // a separate process does the writing; write times are the parent's clock at the moment the child reports each file
    const child = Bun.spawn(['bun', '-e', `const {writeFileSync}=require('node:fs');const [root,n,ms,subs]=[process.argv[1],+process.argv[2],+process.argv[3],+process.argv[4]];const t0=Date.now();(async()=>{for(let i=0;i<n;i++){const w=t0+i*ms/n-Date.now();if(w>1)await new Promise(r=>setTimeout(r,w));const rel='d'+(i%subs)+'/f'+i+'.txt';console.log(rel);writeFileSync(root+'/'+rel,'x')}})()`, root, String(FILES), String(WRITE_MS), String(SUBDIRS)], { stdout: 'pipe' })
    // stdout line is printed right before the write, so it timestamps (within ~1 ms) the write
    const reader = child.stdout.getReader(); const dec = new TextDecoder(); let buf = ''
    for (;;) { const { done, value } = await reader.read(); if (done) break; buf += dec.decode(value, { stream: true }); let i; while ((i = buf.indexOf('\n')) >= 0) { const rel = buf.slice(0, i); buf = buf.slice(i + 1); if (!wrote.has(rel)) wrote.set(rel, performance.now()) } }
    await child.exited
  } else for (let i = 0; i < FILES; i++) {
    const target = t0 + (i * WRITE_MS) / FILES
    const wait = target - performance.now()
    if (wait > 1) await Bun.sleep(wait)
    const rel = `${subs[i % SUBDIRS]}/f${i}.txt`
    wrote.set(rel, performance.now())
    writeFileSync(join(root, rel), 'x')
  }
  const wrote_ms = performance.now() - t0
  await Bun.sleep(SETTLE_MS)
  for (const w of watchers) w.close()
  const lat: number[] = []
  for (const [rel, t] of first) lat.push(t - wrote.get(rel)!)
  const missed = FILES - first.size
  return { events, unknownEvents: unknown, filesSeen: first.size, missed, missedPct: +((missed / FILES) * 100).toFixed(2), p95Ms: p95(lat) && +p95(lat)!.toFixed(1), writeDurationMs: Math.round(wrote_ms) }
}

const results: any[] = []
for (const cls of classes) {
  if (process.env.ONLY && !cls.name.includes(process.env.ONLY)) continue
  for (const mode of cls.modes ?? (['recursive', 'per-dir'] as const)) {
    for (let r = 0; r < RUNS; r++) {
      const { watchPath, cleanup } = cls.make()
      try { results.push({ class: cls.name, mode, run: r + 1, ...(await run(watchPath, mode, cls.opts)) }) }
      catch (e) { results.push({ class: cls.name, mode, run: r + 1, error: (e as Error).message }) }
      finally { cleanup() }
    }
  }
}
console.log(JSON.stringify({ bun: Bun.version, platform: process.platform, tmpdir: tmpdir(), tmpdirReal: realpathSync(tmpdir()).replace(/\/[^/]+$/, '/<…>'), results }, null, 1))
for (const r of results) console.log(`${r.class.padEnd(28)} ${r.mode.padEnd(9)} run${r.run} seen=${r.filesSeen}/${FILES} missed=${r.missed} (${r.missedPct}%) events=${r.events} p95=${r.p95Ms}ms${r.error ? ' ERROR ' + r.error : ''}`)
