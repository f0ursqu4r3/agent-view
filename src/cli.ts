#!/usr/bin/env bun
import os from 'node:os';
import path from 'node:path';
import { Collector } from './collector/collector';
import { encodeLine } from './shared/ndjson';

export type CliArgs = {
  mode: 'hub' | 'collect';
  remotes: string[];
  demo: boolean;
  demoScale: 'normal' | 'stress';
  port: number;
  open: boolean;
  watch: string[];
  lingerMs: number;
  /** `null` means "use the host name" (resolved when the collector runs). */
  label: string | null;
};

export class UsageError extends Error {}

export const USAGE = `Usage:
  agent-view [--port <n>] [--no-open] [--demo] [--demo-scale normal|stress]
             [--watch <dir>]... [--remote <ssh-host>]... [--linger <minutes>]
  agent-view collect --stdio [--label <name>] [--demo] [--demo-scale normal|stress] [--watch <dir>]... [--linger <minutes>]`;

const FLAGS_WITH_VALUE = new Set(['--port', '--demo-scale', '--watch', '--remote', '--linger', '--label']);
const BOOLEAN_FLAGS = new Set(['--no-open', '--demo', '--stdio']);

/** Parses the command line. Throws `UsageError` for anything it does not accept. */
export function parseArgs(argv: string[]): CliArgs {
  const args: CliArgs = {
    mode: 'hub', remotes: [], demo: false, demoScale: 'normal', port: 4317, open: true, watch: [], lingerMs: 600_000, label: null,
  };
  const rest = [...argv];
  if (rest[0] === 'collect') { args.mode = 'collect'; rest.shift(); }
  let stdio = false;
  while (rest.length > 0) {
    const token = rest.shift()!;
    if (!token.startsWith('--')) throw new UsageError(`Unexpected argument: ${token}`);
    const eq = token.indexOf('=');
    const flag = eq === -1 ? token : token.slice(0, eq);
    let value: string | undefined;
    if (FLAGS_WITH_VALUE.has(flag)) {
      value = eq === -1 ? rest.shift() : token.slice(eq + 1);
      if (value === undefined) throw new UsageError(`Missing value for ${flag}`);
    } else if (!BOOLEAN_FLAGS.has(flag) || eq !== -1) {
      throw new UsageError(`Unknown option: ${token}`);
    }
    switch (flag) {
      case '--port': {
        const n = Number(value);
        if (!/^\d+$/.test(value!) || n > 65535) throw new UsageError(`Invalid port: ${value}`);
        args.port = n;
        break;
      }
      case '--no-open': args.open = false; break;
      case '--demo': args.demo = true; break;
      case '--demo-scale':
        if (value !== 'normal' && value !== 'stress') throw new UsageError(`Invalid --demo-scale: ${value} (use normal or stress)`);
        args.demoScale = value;
        break;
      case '--watch': args.watch.push(path.resolve(value!)); break;
      case '--remote': args.remotes.push(value!); break;
      case '--linger': {
        const minutes = Number(value);
        if (value!.trim() === '' || !Number.isFinite(minutes) || minutes <= 0) throw new UsageError(`Invalid --linger: ${value} (minutes, above 0)`);
        args.lingerMs = Math.round(minutes * 60_000);
        break;
      }
      case '--label':
        if (args.mode !== 'collect') throw new UsageError('--label is only valid with collect');
        if (value === '') throw new UsageError('--label needs a name');
        args.label = value!;
        break;
      case '--stdio':
        if (args.mode !== 'collect') throw new UsageError('--stdio is only valid with collect');
        stdio = true;
        break;
    }
  }
  if (args.mode === 'collect' && !stdio) throw new UsageError('collect needs --stdio');
  return args;
}

/** Runs a collector that writes one NDJSON envelope per line to stdout, until a signal or a closed pipe. */
async function collectStdio(args: CliArgs): Promise<void> {
  const collector = new Collector({
    host: args.label ?? os.hostname(),
    demo: args.demo,
    demoScale: args.demoScale,
    watch: args.watch,
    lifecycle: { lingerMs: args.lingerMs },
  });
  let closing = false;
  const shutdown = (): void => {
    if (closing) return;
    closing = true;
    void collector.stop().finally(() => process.exit(0));
  };
  for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP'] as const) process.on(signal, shutdown);
  process.stdout.on('error', shutdown);
  process.stdout.on('close', shutdown);
  collector.subscribe((env) => {
    if (closing) return;
    process.stdout.write(encodeLine(env));
  });
  await collector.start();
}

export async function main(argv: string[]): Promise<void> {
  let args: CliArgs;
  try {
    args = parseArgs(argv);
  } catch (e) {
    if (!(e instanceof UsageError)) throw e;
    process.stderr.write(`agent-view: ${e.message}\n${USAGE}\n`);
    process.exit(2);
  }
  if (args.mode === 'collect') return collectStdio(args);
  process.stderr.write('agent-view: hub mode not available yet\n');
  process.exit(1);
}

if (import.meta.main) await main(process.argv.slice(2));
