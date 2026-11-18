import { categoryFor } from '../shared/schema';
import type { Agent, AgentStatus, Call, Provider, SourceEvent } from '../shared/schema';

type Scale = 'normal' | 'stress';
type Opts = { host: string; emit: (events: SourceEvent[]) => void; scale?: Scale };
type Project = { root: string; files: string[] };
type DemoAgent = {
  agent: Agent;
  project: Project;
  provider: Provider;
  busy: boolean;
  restUntil: number;
  calls: number;
  status: AgentStatus;
};
type Kind = 'read' | 'edit' | 'command' | 'search';

const STRESS_FILES = 3000;
const STRESS_AGENT_SESSIONS = 10;
const STRESS_SUBS_PER_SESSION = 4;
const STRESS_STARTS_PER_SECOND = 78;
const STRESS_TICK_MS = 50;
/** Calls per turn: after this many calls an agent goes idle for a while. */
const CALLS_PER_TURN = 6;
const FILE_STRIDE = 7;

const NORMAL_PROJECTS: Project[] = [
  {
    root: '/demo/agent-view',
    files: ['src/viewer/drawer.ts', 'src/viewer/stage.ts', 'src/viewer/tree.ts', 'src/viewer/colors.ts',
      'src/shared/schema.ts', 'src/collector/state.ts', 'test/viewer/drawer.test.ts', 'public/index.html']
      .map((f) => `/demo/agent-view/${f}`),
  },
  {
    root: '/demo/search-service',
    files: ['src/index.ts', 'src/query/parser.ts', 'src/query/ranking.ts', 'src/store/segments.ts',
      'src/store/merge.ts', 'src/api/routes.ts', 'test/ranking.test.ts', 'README.md']
      .map((f) => `/demo/search-service/${f}`),
  },
];

const STRESS_PROJECTS = ['agent-view', 'search-service', 'billing-api', 'web-client', 'infra-tools'];

function stressProjects(): Project[] {
  const per = STRESS_FILES / STRESS_PROJECTS.length;
  return STRESS_PROJECTS.map((name) => {
    const root = `/demo/${name}`;
    const files: string[] = [];
    for (let i = 0; i < per; i++) files.push(`${root}/src/area${String(i % 30).padStart(2, '0')}/module${String(i).padStart(3, '0')}.ts`);
    return { root, files };
  });
}

/** Every file path the demo can touch. Stress has exactly 3,000. */
export function demoFilePaths(scale: Scale = 'normal'): string[] {
  return (scale === 'stress' ? stressProjects() : NORMAL_PROJECTS).flatMap((p) => p.files);
}

const COMMANDS: { text: string; lines: string[]; failLine: string }[] = [
  { text: 'bun test test/viewer', lines: ['bun test v1.4.4', '  drawer renders open calls (12ms)', '  drawer keeps 16 seconds of history (9ms)', ' 14 pass', ' 0 fail'], failLine: ' 1 fail' },
  { text: 'bun run typecheck', lines: ['$ tsc --noEmit', 'Found 0 errors.'], failLine: 'src/viewer/drawer.ts(41,9): error TS2322' },
  { text: 'git status --short', lines: [' M src/viewer/drawer.ts', '?? test/viewer/drawer.test.ts'], failLine: 'fatal: not a git repository' },
  { text: 'bun run build', lines: ['bundled 128 modules in 84ms', 'dist/index.js  412 KB'], failLine: 'error: could not resolve "./missing"' },
];
const PATTERNS = ['renderDrawer', 'lingerMs', 'SourceEvent', 'rankResults', 'mergeSegments', 'TODO'];

/** Small deterministic generator: the demo looks the same on every run. */
function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/**
 * Simulated agents for `--demo`. Paths are fake and live under `/demo/...`. Every event carries `ts`.
 * Normal scale: one Claude Code session and one Codex session, each with two sub-agents.
 * Stress scale: 50 agents across five projects, 3,000 files, about 200 events per second.
 */
export class DemoSource {
  private readonly scale: Scale;
  private readonly rand = mulberry32(20261117);
  private agents: DemoAgent[] = [];
  private readonly timers = new Set<ReturnType<typeof setTimeout>>();
  private running = false;
  private readonly cursors = new Map<Project, number>();
  private nextAgent = 0;
  private callCount = 0;
  private lastStep = 0;
  private budget = 0;

  constructor(private readonly opts: Opts) {
    this.scale = opts.scale ?? 'normal';
  }

  start(): void {
    if (this.running) return;
    this.running = true;
    this.agents = this.buildAgents();
    const now = Date.now();
    this.lastStep = now;
    const ts = new Date(now).toISOString();
    this.send(this.agents.map((a) => ({ kind: 'agent.upsert', ts, agent: { ...a.agent, lastEventAt: ts } })));
    this.step();
  }

  stop(): void {
    this.running = false;
    for (const t of this.timers) clearTimeout(t);
    this.timers.clear();
  }

  private between(lo: number, hi: number): number {
    return lo + this.rand() * (hi - lo);
  }

  private later(ms: number, fn: () => void): void {
    const t = setTimeout(() => { this.timers.delete(t); if (this.running) fn(); }, ms);
    this.timers.add(t);
  }

  private send(events: SourceEvent[]): void {
    if (!this.running || events.length === 0) return;
    try { this.opts.emit(events); } catch { /* a failing consumer must not stop the demo */ }
  }

  private buildAgents(): DemoAgent[] {
    const host = this.opts.host;
    const projects = this.scale === 'stress' ? stressProjects() : NORMAL_PROJECTS;
    const now = new Date().toISOString();
    const make = (id: string, provider: Provider, name: string, parentId: string | null, project: Project, n: number): DemoAgent => ({
      agent: {
        id, provider, name, role: parentId ? 'sub-agent' : 'main', parentId, link: parentId ? 'confirmed' : 'root',
        cwd: project.root, projectRoot: project.root,
        model: provider === 'claude' ? 'claude-sonnet-5-5' : 'gpt-5-codex',
        status: 'working', lastEventAt: now,
      },
      project, provider, busy: false, restUntil: 0, calls: n % CALLS_PER_TURN, status: 'working',
    });
    const out: DemoAgent[] = [];
    if (this.scale === 'normal') {
      const claude = `${host}/claude:demo:session-1`;
      const codex = `${host}/codex:demo:thread-1`;
      out.push(
        make(claude, 'claude', 'Refactor drawer layout', null, projects[0]!, 0),
        make(`${claude}:review`, 'claude', 'Review drawer styles', claude, projects[0]!, 1),
        make(`${claude}:tests`, 'claude', 'Update drawer tests', claude, projects[0]!, 2),
        make(codex, 'codex', 'Build workspace search', null, projects[1]!, 3),
        make(`${codex}:index`, 'codex', 'Index source files', codex, projects[1]!, 4),
        make(`${codex}:ranking`, 'codex', 'Check ranking results', codex, projects[1]!, 5),
      );
      return out;
    }
    let n = 0;
    for (let s = 0; s < STRESS_AGENT_SESSIONS; s++) {
      const provider: Provider = s % 2 === 0 ? 'claude' : 'codex';
      const project = projects[s % projects.length]!;
      const id = `${host}/${provider}:demo:${provider === 'claude' ? 'session' : 'thread'}-${s + 1}`;
      out.push(make(id, provider, `${project.root.split('/').pop()} lead ${s + 1}`, null, project, n++));
      for (let k = 0; k < STRESS_SUBS_PER_SESSION; k++) {
        out.push(make(`${id}:w${k + 1}`, provider, `Worker ${s + 1}.${k + 1}`, id, project, n++));
      }
    }
    return out;
  }

  /** One scheduling step: start calls on idle agents, then schedule the next step. */
  private step(): void {
    if (!this.running) return;
    const now = Date.now();
    let starts = 1;
    if (this.scale === 'stress') {
      this.budget += STRESS_STARTS_PER_SECOND * (Math.min(now - this.lastStep, 250) / 1000);
      starts = Math.floor(this.budget);
      this.budget -= starts;
    }
    this.lastStep = now;
    const events: SourceEvent[] = [];
    for (let i = 0; i < starts; i++) {
      const a = this.pickIdle(now);
      if (!a) break;
      this.startCall(a, now, events);
    }
    this.send(events);
    const delay = this.scale === 'stress' ? STRESS_TICK_MS : this.between(160, 480);
    this.later(delay, () => this.step());
  }

  private pickIdle(now: number): DemoAgent | null {
    for (let i = 0; i < this.agents.length; i++) {
      const a = this.agents[(this.nextAgent + i) % this.agents.length]!;
      if (a.busy || a.restUntil > now) continue;
      this.nextAgent = (this.nextAgent + i + 1) % this.agents.length;
      return a;
    }
    return null;
  }

  private pickFile(a: DemoAgent): string {
    // One cursor per project: consecutive picks walk the whole pool before any file repeats.
    const cursor = (this.cursors.get(a.project) ?? 0) + FILE_STRIDE;
    this.cursors.set(a.project, cursor);
    return a.project.files[cursor % a.project.files.length]!;
  }

  private startCall(a: DemoAgent, now: number, out: SourceEvent[]): void {
    const ts = new Date(now).toISOString();
    const r = this.rand();
    const kind: Kind = r < 0.35 ? 'read' : r < 0.6 ? 'edit' : r < 0.8 ? 'command' : 'search';
    const speed = this.scale === 'stress' ? 0.25 : 1;
    const duration = speed * (kind === 'read' ? this.between(350, 900)
      : kind === 'edit' ? this.between(700, 1600)
        : kind === 'command' ? this.between(1600, 4200)
          : this.between(450, 1000));
    const callId = `d${++this.callCount}`;
    const claude = a.provider === 'claude';
    const file = kind === 'read' || kind === 'edit' ? this.pickFile(a) : undefined;
    const cmd = COMMANDS[Math.floor(this.rand() * COMMANDS.length)]!;
    const pattern = PATTERNS[Math.floor(this.rand() * PATTERNS.length)]!;

    // Codex has no read or search tools: it reads and searches through shell commands.
    let rawName: string;
    let command: string | undefined;
    let output: string[] = [];
    let fail = false;
    if (kind === 'edit') rawName = claude ? 'Edit' : 'apply_patch';
    else if (kind === 'read') {
      rawName = claude ? 'Read' : 'shell';
      if (!claude) command = `sed -n '1,120p' ${file}`;
    } else if (kind === 'search') {
      rawName = claude ? 'Grep' : 'shell';
      if (!claude) command = `rg -n "${pattern}" src`;
    } else {
      rawName = claude ? 'Bash' : 'shell';
      command = cmd.text;
      fail = this.rand() < 0.12;
      output = fail ? [...cmd.lines.slice(0, 2), cmd.failLine] : cmd.lines;
    }
    const call: Call = { agentId: a.agent.id, callId, startedAt: ts, category: categoryFor(a.provider, rawName), rawName };
    if (file && (claude || kind === 'edit')) call.files = [file];
    if (command !== undefined) call.command = command;
    if (claude && kind === 'search') call.pattern = pattern;

    a.busy = true;
    if (a.status !== 'working') {
      a.status = 'working';
      out.push({ kind: 'agent.status', ts, agentId: a.agent.id, status: 'working' });
    }
    out.push({ kind: 'tool.start', ts, call });
    if (this.callCount % 8 === 0) {
      const path = a.project.files[Math.floor(this.rand() * a.project.files.length)]!;
      out.push({ kind: 'fs.change', ts, path, op: 'change' });
    }

    // Codex streams output while a command runs. Claude Code writes it with the result.
    const streamNow = !claude && output.length > 0;
    if (streamNow) {
      this.later(duration * 0.55, () => this.send([
        { kind: 'tool.output', ts: new Date().toISOString(), agentId: a.agent.id, callId, chunk: output.join('\n') + '\n', truncated: false },
      ]));
    }
    this.later(duration, () => {
      const end = new Date().toISOString();
      const events: SourceEvent[] = [];
      if (!streamNow && output.length > 0) {
        events.push({ kind: 'tool.output', ts: end, agentId: a.agent.id, callId, chunk: output.join('\n') + '\n', truncated: false });
      }
      events.push({
        kind: 'tool.end', ts: end, agentId: a.agent.id, callId, ok: !fail,
        ...(kind === 'command' ? { exitCode: fail ? 1 : 0 } : {}),
        ...(kind === 'edit' && file ? { writes: [{ path: file, op: 'edit' as const }] } : {}),
      });
      a.busy = false;
      a.calls++;
      if (a.calls % CALLS_PER_TURN === 0) {
        a.status = 'idle';
        a.restUntil = Date.now() + (this.scale === 'stress' ? 200 : 2500);
        events.push({ kind: 'agent.status', ts: end, agentId: a.agent.id, status: 'idle' });
      }
      this.send(events);
    });
  }
}
