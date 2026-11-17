import path from 'node:path';
import {ingestNativeEvent} from './native-events.mjs';

export function parseJSON(value) {
  if (typeof value !== 'string') return value;
  try { return JSON.parse(value); } catch { return null; }
}

function outputText(value) {
  if (typeof value === 'string') return value;
  if (Array.isArray(value)) return value.map(v => v.text || '').join('\n');
  return '';
}

// Only explicit file tools produce attributed writes. Shell text is never executed or guessed.
export function fileOperations(name, input, cwd) {
  const data = parseJSON(input) || {};
  if (['Write', 'Edit', 'MultiEdit', 'NotebookEdit'].includes(name)) {
    const file = data.file_path || data.notebook_path;
    return file && cwd ? [{path: path.resolve(cwd, file), operation: name === 'Write' ? 'write' : 'edit'}] : [];
  }
  if (name?.split('.').at(-1) !== 'apply_patch' || !cwd) return [];
  const patch = typeof input === 'string' ? input : data.patch || data.input || '';
  const result = [];
  for (const line of patch.split('\n')) {
    const match = line.match(/^\*\*\* (Add|Update|Delete|Move to)(?: File)?: (.+)$/);
    if (match) result.push({path: path.resolve(cwd, match[2]), operation: {Add:'create', Update:'edit', Delete:'delete', 'Move to':'move destination'}[match[1]]});
  }
  return result;
}

export class Model {
  constructor() {
    this.agents = new Map(); this.executions = new Map(); this.files = []; this.seen = new Set(); this.pending = new Map();
    this.sources = new Map(); this.sequence = 0; this.warnings = new Set();
  }
  agent(id, fields = {}) {
    const current = this.agents.get(id) || {id, name: id.split(':').at(-1).slice(0, 12), status:'unknown', lastEvent:null, parentId:null, link:'unknown', writes:0};
    Object.assign(current, Object.fromEntries(Object.entries(fields).filter(([,v]) => v !== undefined)));
    this.agents.set(id, current); return current;
  }
  addFile(event, identity) {
    if (identity && this.seen.has(identity)) return;
    if (identity) { this.seen.add(identity); if(this.seen.size > 10000) this.seen.delete(this.seen.values().next().value); }
    const item = {id: ++this.sequence, ...event};
    this.files.push(item); this.files.sort((a,b)=>(Date.parse(b.timestamp)||0)-(Date.parse(a.timestamp)||0) || b.id-a.id); this.files.length = Math.min(this.files.length, 500);
    if(item.agentId && this.agents.has(item.agentId)) this.agents.get(item.agentId).writes++;
  }
  observe(file, operation, timestamp = new Date().toISOString()) {
    // Filesystem events prove a change, not its author. Keep this independent of tool claims.
    this.addFile({path:file, operation, timestamp, agentId:null, attribution:'unknown', evidence:'filesystem'});
  }
  ingest(provider, record, source, scope = 'default') {
    if (!record || typeof record !== 'object') return;
    let state = this.sources.get(source);
    if (!state) {state = {provider, scope, id:null, cwd:null}; this.sources.set(source, state);}
    const prefix = `${provider}:${scope}:`;
    const timestamp = record.timestamp;
    const touch = (id, fields = {}) => this.agent(id, {provider, scope, ...(timestamp ? {lastEvent: timestamp} : {}), ...fields});
    if (provider === 'codex') {
      const p = record.payload || {};
      if (record.type === 'session_meta') {
        const meta = p.source?.subagent?.thread_spawn || {};
        state.id = prefix + (p.id || p.session_id); state.cwd = p.cwd;
        const parent = p.parent_thread_id || meta.parent_thread_id;
        touch(state.id, {cwd:p.cwd, parentId:parent ? prefix + parent : null, link:parent ? 'confirmed' : 'root', name:meta.agent_path?.split('/').at(-1) || (p.thread_source === 'guardian_review' ? 'Approval review' : 'Codex session'), role:meta.agent_role || p.thread_source || 'agent'});
      }
      if (record.type === 'event_msg' && p.type === 'item_completed') ingestNativeEvent(this,state,p,timestamp);
      if (!state.id) return;
      if (record.type === 'turn_context') {state.cwd=p.cwd || state.cwd;touch(state.id, {model:p.model, cwd:state.cwd});}
      if (p.type === 'task_started') touch(state.id, {status:'working'});
      if (['task_complete','task_completed','turn_aborted'].includes(p.type)) touch(state.id, {status:p.type === 'turn_aborted' ? 'interrupted' : 'finished', tool:null});
      if (['function_call','custom_tool_call'].includes(p.type)) {
        touch(state.id,{status:'working',tool:p.name});
        this.startCall(state, p.call_id, p.name, p.arguments ?? p.input, timestamp);
      }
      if (['function_call_output','custom_tool_call_output'].includes(p.type)) {
        const text = outputText(p.output);
        // Codex apply_patch uses this explicit success receipt. Unknown output is not success.
        this.finishCall(state, p.call_id, /Success\. Updated the following files:/.test(text) && !/Failed to|Error:|patch rejected/i.test(text), timestamp);
      }
    } else {
      if (!record.sessionId) return;
      const child = record.agentId || source.match(/[/\\]subagents[/\\]agent-(.+)\.jsonl$/)?.[1];
      const root = prefix + record.sessionId;
      state.id = child ? `${root}:${child}` : root;
      state.cwd = record.cwd || state.cwd;
      const existing = this.agents.get(state.id);
      touch(state.id, {cwd:state.cwd, name:existing?.name || (child ? `Sub-agent ${child.slice(0,8)}` : 'Claude Code session'), parentId:existing?.link === 'confirmed' ? existing.parentId : child ? root : null, link:existing?.link === 'confirmed' ? 'confirmed' : child ? 'session' : 'root', role:child ? 'sub-agent' : 'main'});
      if (record.type === 'custom-title') touch(state.id, {name:record.customTitle});
      if (record.type === 'agent-name') touch(state.id, {name:record.agentName});
      const content = record.message?.content;
      for (const b of Array.isArray(content) ? content : []) {
        if (b.type === 'tool_use') {touch(state.id,{status:'working',tool:b.name,model:record.message.model}); this.startCall(state,b.id,b.name,b.input,timestamp);}
        if (b.type === 'tool_result') {
          const call = this.pending.get(`${state.id}:${b.tool_use_id}`);
          const agentId = record.toolUseResult?.agentId;
          if (call && ['Agent','Task'].includes(call.name) && agentId && !b.is_error) {
            const id = `${root}:${agentId}`;
            this.agent(id,{provider,scope,parentId:state.id,link:'confirmed',name:call.input?.description || `Sub-agent ${agentId.slice(0,8)}`,role:'sub-agent'});
          }
          this.finishCall(state,b.tool_use_id,b.is_error !== true,timestamp);
        }
      }
      if(record.type === 'system' && record.subtype === 'turn_duration') touch(state.id,{status:'finished',tool:null});
      if(record.message?.stop_reason === 'end_turn') touch(state.id,{status:'finished',tool:null});
    }
  }
  startCall(state, id, name, input, timestamp) {
    if (!id) return;
    const parsed = parseJSON(input) || input;
    this.pending.set(`${state.id}:${id}`, {name,input:parsed,cwd:state.cwd, timestamp, operations:fileOperations(name,parsed,state.cwd)});
    if(this.pending.size > 5000) this.pending.delete(this.pending.keys().next().value);
  }
  finishCall(state, id, success, timestamp) {
    const key = `${state.id}:${id}`, call = this.pending.get(key);
    if (!call) return;
    this.pending.delete(key);
    if (!success) return;
    for (const op of call.operations) this.addFile({...op, timestamp:timestamp || call.timestamp, agentId:state.id, attribution:'confirmed', evidence:`${state.provider} tool receipt`, tool:call.name},`${key}:${op.path}:${op.operation}`);
  }
  snapshot() {
    return {agents:[...this.agents.values()].map(a => ({...a, status:a.status === 'working' && Date.now()-Date.parse(a.lastEvent || 0) > 120000 ? 'stale' : a.status})), files:this.files, executions:[...this.executions.values()].sort((a,b)=>(Date.parse(b.timestamp)||0)-(Date.parse(a.timestamp)||0)).map(({output,command,...execution})=>({...execution,commandPreview:command.split('\n')[0].slice(0,180)})), warnings:[...this.warnings]};
  }
}
