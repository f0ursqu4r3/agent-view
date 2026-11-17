import path from 'node:path';

const OUTPUT_LIMIT = 16384;
const COMMAND_LIMIT = 4096;
const operationNames = {add:'create',update:'edit',delete:'delete'};

// Native events describe the nested tool, even when its outer response item is functions.exec.
// Never evaluate wrapper JavaScript, shell text, or stdout to infer file writes.
export function ingestNativeEvent(model, state, payload, fallbackTimestamp) {
  const item = payload.item;
  if (!item || !['FileChange','CommandExecution'].includes(item.type) || typeof item.id !== 'string') return;
  const thread = typeof payload.thread_id === 'string' && payload.thread_id;
  const agentId = thread ? `codex:${state.scope}:${thread}` : state.id;
  if (!agentId) return;
  const milliseconds = payload.completed_at_ms;
  const timestamp = Number.isFinite(milliseconds) && Math.abs(milliseconds) < 8.64e15
    ? new Date(milliseconds).toISOString() : fallbackTimestamp;
  const cwd = typeof item.cwd === 'string' ? item.cwd : (agentId === state.id ? state.cwd : model.agents.get(agentId)?.cwd);
  model.agent(agentId, {provider:'codex',scope:state.scope,cwd});
  if (item.type === 'FileChange') {
    if (item.status !== 'completed' || !item.changes || Array.isArray(item.changes)) return;
    for (const [file, change] of Object.entries(item.changes)) {
      if (!change || !Object.hasOwn(operationNames,change.type)) continue;
      if (!path.isAbsolute(file) && !cwd) continue;
      const absolute = path.isAbsolute(file) ? path.normalize(file) : path.resolve(cwd,file);
      const operation = operationNames[change.type];
      model.addFile({path:absolute,operation,timestamp,agentId,attribution:'confirmed',evidence:'Codex native file-change receipt',tool:'apply_patch',callId:item.id},`${agentId}:${item.id}:${absolute}:${operation}`);
      if (typeof change.move_path === 'string' && change.move_path && (path.isAbsolute(change.move_path) || cwd)) {
        const destination = path.isAbsolute(change.move_path) ? path.normalize(change.move_path) : path.resolve(cwd,change.move_path);
        model.addFile({path:destination,operation:'move destination',timestamp,agentId,attribution:'confirmed',evidence:'Codex native file-change receipt',tool:'apply_patch',callId:item.id},`${agentId}:${item.id}:${destination}:move destination`);
      }
    }
    return;
  }
  const id = `${agentId}:${item.id}`;
  const command = Array.isArray(item.command) ? item.command.filter(x=>typeof x === 'string').join(' ') : typeof item.command === 'string' ? item.command : '';
  const output = typeof item.aggregated_output === 'string' ? item.aggregated_output : '';
  model.executions.set(id, {
    id,agentId,callId:item.id,provider:'codex',timestamp,cwd,
    status:typeof item.status === 'string' ? item.status : 'unknown',
    exitCode:Number.isInteger(item.exit_code) ? item.exit_code : null,
    // This is a Codex execution-session handle. It is NOT a verified operating-system PID.
    processRef:typeof item.process_id === 'string' ? item.process_id : null,
    command:command.slice(0,COMMAND_LIMIT),commandTruncated:command.length>COMMAND_LIMIT,
    output:output.slice(-OUTPUT_LIMIT),outputTruncated:output.length>OUTPUT_LIMIT,
    evidence:'Codex native command event'
  });
  if (model.executions.size > 100) {
    const oldest = [...model.executions.values()].sort((a,b)=>(Date.parse(a.timestamp)||0)-(Date.parse(b.timestamp)||0))[0];
    model.executions.delete(oldest.id);
  }
}
