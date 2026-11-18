export const SCHEMA_VERSION = 1 as const;
export type Provider = 'claude' | 'codex';
export type AgentStatus = 'working' | 'idle' | 'finished' | 'interrupted';
export type Category = 'read' | 'edit' | 'write' | 'command' | 'search' | 'agent' | 'other';
export type WriteOp = 'create' | 'edit' | 'delete' | 'move';
export type Agent = { id: string; provider: Provider; name: string; role: 'main' | 'sub-agent';
  parentId: string | null; link: 'confirmed' | 'session' | 'root'; cwd: string | null;
  projectRoot: string | null; model?: string; status: AgentStatus; lastEventAt: string };
export type Call = { agentId: string; callId: string; startedAt: string; category: Category;
  rawName: string; files?: string[]; command?: string; pattern?: string };
export type FileTouch = { path: string; lastTouchAt: string; attribution: 'confirmed' | 'unattributed'; agentId?: string };
export type SourceState = { state: 'ok' | 'degraded' | 'down'; detail?: string };
export type Coverage = { logRoots: string[]; logsOpen: number; logsDiscovered: number; watchRoots: string[];
  malformedRecords: number; fsDropped: number; sources: Record<string, SourceState> };
export type Event =
  | { kind: 'hello'; collectorVersion: string; platform: string; now: string; demo?: true }
  | { kind: 'snapshot'; agents: Agent[]; openCalls: Call[]; files: FileTouch[] }
  | { kind: 'agent.upsert'; agent: Agent }
  | { kind: 'agent.status'; agentId: string; status: AgentStatus }
  | { kind: 'tool.start'; call: Call }
  | { kind: 'tool.update'; agentId: string; callId: string; files: string[] }
  | { kind: 'tool.output'; agentId: string; callId: string; chunk: string; truncated: boolean }
  | { kind: 'tool.end'; agentId: string; callId: string; ok: boolean | null; exitCode?: number; writes?: { path: string; op: WriteOp }[] }
  | { kind: 'fs.change'; path: string; op: 'create' | 'change' | 'delete' | 'rename' }
  | { kind: 'source.status'; source: string } & SourceState
  | { kind: 'coverage'; coverage: Coverage };
export type SourceEvent = Event & { ts: string };
export type HostStatus = { label: string; kind: 'local' | 'remote'; state: 'connecting' | 'up' | 'down'; detail?: string };
export type Envelope = SourceEvent & { v: typeof SCHEMA_VERSION; host: string; seq: number };

const CLAUDE_CATEGORIES: Record<string, Category> = {
  Read: 'read',
  Edit: 'edit', MultiEdit: 'edit', NotebookEdit: 'edit',
  Write: 'write',
  Bash: 'command',
  Grep: 'search', Glob: 'search',
  Agent: 'agent', Task: 'agent',
};

const CODEX_CATEGORIES: Record<string, Category> = {
  apply_patch: 'edit', FileChange: 'edit', fileChange: 'edit',
  shell: 'command', exec_command: 'command', local_shell: 'command',
  CommandExecution: 'command', commandExecution: 'command',
  spawn_agent: 'agent', CollabAgentToolCall: 'agent', collabAgentToolCall: 'agent',
  webSearch: 'search',
};

const CATEGORY_TABLES: Record<Provider, Record<string, Category>> = {
  claude: CLAUDE_CATEGORIES,
  codex: CODEX_CATEGORIES,
};

export function categoryFor(provider: Provider, rawName: string): Category {
  const table = CATEGORY_TABLES[provider];
  return Object.hasOwn(table, rawName) ? table[rawName]! : 'other';
}

/** Index 0 is the main agent; later entries are sub-agents. */
export const PROVIDER_COLORS: Record<Provider, readonly string[]> = {
  claude: ['#ff8a4c', '#ffad78', '#ffcda1'],
  codex: ['#36c6ff', '#79dbff', '#b0ecff'],
};

export const CATEGORY_COLORS: Record<Category, string> = {
  read: '#8f9bff',
  edit: '#ff5fa8',
  write: '#ff5fa8',
  command: '#6dff9e',
  search: '#ffd166',
  agent: '#b9a6ff',
  other: '#b9a6ff',
};
