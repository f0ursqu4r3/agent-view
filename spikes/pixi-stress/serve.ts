// Usage: bun spikes/pixi-stress/serve.ts [port]
// Query params: glow=sprite|filter  labels=lazy|all  lines=sprite|graphics  zoom=1  w=2560  h=1440  nodes=3000  agents=50  rate=200
import page from './index.html'

const port = Number(process.argv[2] ?? process.env.PORT ?? 4399)
const server = Bun.serve({ port, hostname: '127.0.0.1', routes: { '/': page } })
console.log(`Pixi stress: ${server.url}`)
