// Builds single-file binaries with `bun build --compile`.
// Usage: bun scripts/compile.ts [bun-<os>-<arch>]...
// With no arguments, it builds all targets. The output directory is `dist/`
// (override with AGENT_VIEW_DIST). Cross-target builds can download Bun runtimes.
import fs from 'node:fs';
import path from 'node:path';

const ROOT = path.join(import.meta.dir, '..');
const OUT_DIR = path.resolve(process.env.AGENT_VIEW_DIST ?? path.join(ROOT, 'dist'));

export const TARGETS = ['bun-darwin-arm64', 'bun-darwin-x64', 'bun-linux-x64', 'bun-linux-arm64'] as const;

/** `bun-darwin-arm64` becomes `agent-view-darwin-arm64`. */
export function outputName(target: string): string {
  return `agent-view-${target.replace(/^bun-/, '')}`;
}

async function main(argv: string[]): Promise<number> {
  const unknown = argv.filter((t) => !(TARGETS as readonly string[]).includes(t));
  if (unknown.length > 0) {
    console.error(`Unknown target: ${unknown.join(', ')}. Use: ${TARGETS.join(', ')}`);
    return 2;
  }
  const targets = argv.length > 0 ? TARGETS.filter((t) => argv.includes(t)) : TARGETS;
  fs.mkdirSync(OUT_DIR, { recursive: true });
  for (const target of targets) {
    const outfile = path.join(OUT_DIR, outputName(target));
    const started = performance.now();
    const proc = Bun.spawn(['bun', 'build', '--compile', `--target=${target}`, 'src/cli.ts', '--outfile', outfile], {
      cwd: ROOT,
      stdin: 'ignore',
      stdout: 'pipe',
      stderr: 'pipe',
    });
    const [out, err, code] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
    if (code !== 0) {
      console.error(`FAIL ${target} (exit ${code})\n${out}${err}`.trimEnd());
      return code || 1;
    }
    const mb = (fs.statSync(outfile).size / 1_048_576).toFixed(1);
    console.log(`ok   ${target} -> ${path.relative(ROOT, outfile)} (${mb} MiB, ${((performance.now() - started) / 1000).toFixed(1)}s)`);
  }
  return 0;
}

if (import.meta.main) process.exit(await main(process.argv.slice(2)));
