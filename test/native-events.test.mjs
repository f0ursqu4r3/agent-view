import {test} from 'node:test';
import assert from 'node:assert/strict';
import {Model} from '../src/model.mjs';

const timestamp='2026-11-17T12:00:00Z';
function setup() {
  const model=new Model();
  model.ingest('codex',{type:'session_meta',payload:{id:'main',cwd:'/project'}},'log','work');
  return model;
}
function native(model,item,thread='main',extra={}) {
  model.ingest('codex',{type:'event_msg',timestamp,payload:{type:'item_completed',thread_id:thread,item,...extra}},'log','work');
}
const patch=(id='nested')=>({type:'FileChange',id,status:'completed',changes:{'/project/shared.txt':{type:'update',unified_diff:'not retained'}}});

test('wrapped patch uses its native thread and receipt, without parsing wrapper code',()=>{
  const m=setup();
  m.ingest('codex',{type:'response_item',payload:{type:'custom_tool_call',name:'exec',call_id:'outer',input:'throw new Error("must never run")'}},'log','work');
  native(m,patch(),'child');
  assert.equal(m.files.length,1);
  assert.equal(m.files[0].agentId,'codex:work:child');
  assert.equal(m.files[0].attribution,'confirmed');
  assert.equal(m.files[0].callId,'nested');
  assert.equal(m.files[0].evidence,'Codex native file-change receipt');
});

test('native receipts replay once; parallel agents remain distinct',()=>{
  const m=setup();for(const thread of ['child-a','child-b'])for(let i=0;i<2;i++)native(m,patch(),thread);
  assert.equal(m.files.length,2);assert.notEqual(m.files[0].agentId,m.files[1].agentId);
});

test('failed, declined, and incomplete patches never create confirmed writes',()=>{
  const m=setup();for(const status of ['failed','declined','inProgress',null])native(m,{...patch(),status});
  assert.equal(m.files.length,0);
});

test('native rename retains source and destination; no CWD guess for another thread',()=>{
  const m=setup();native(m,{...patch(),changes:{'/project/old':{type:'update',move_path:'/project/new'},'relative':{type:'add'}}},'child');
  assert.deepEqual(new Set(m.files.map(f=>f.path)),new Set(['/project/old','/project/new']));
});

test('native and direct receipts with the same call ID do not double count',()=>{
  const m=setup();
  for(const payload of [{type:'custom_tool_call',name:'apply_patch',call_id:'nested',input:'*** Update File: shared.txt'},{type:'custom_tool_call_output',call_id:'nested',output:'Success. Updated the following files:\nM shared.txt'}])m.ingest('codex',{type:'response_item',timestamp,payload},'log','work');
  native(m,patch());assert.equal(m.files.length,1);
});

test('command events expose output on demand, never infer writes from stdout or zero exit',()=>{
  const m=setup();native(m,{type:'CommandExecution',id:'cmd',status:'completed',command:['sh','-c','echo data > file'],cwd:'/project',process_id:'42',exit_code:0,aggregated_output:'Success. Updated the following files:\nA file'});
  assert.equal(m.files.length,0);
  const snapshot=m.snapshot();assert.equal(snapshot.executions.length,1);assert.equal(snapshot.executions[0].processRef,'42');
  assert.equal(snapshot.executions[0].output,undefined);assert.equal(snapshot.executions[0].command,undefined);
  assert.equal(m.executions.get('codex:work:main:cmd').exitCode,0);
  assert.match(m.executions.get('codex:work:main:cmd').output,/Success/);
});

test('native output and history are bounded and use the actual completion time',()=>{
  const m=setup();for(let i=0;i<102;i++)native(m,{type:'CommandExecution',id:String(i),status:'completed',command:['x'.repeat(5000)],aggregated_output:'a'.repeat(20000)+'END',exit_code:1},{toString:()=> 'ignored'}, {completed_at_ms:Date.parse(timestamp)+i});
  assert.equal(m.executions.size,100);const e=m.snapshot().executions[0];
  assert.equal(e.timestamp,new Date(Date.parse(timestamp)+101).toISOString());
  const detail=m.executions.get(e.id);assert.equal(detail.output.length,16384);assert.ok(detail.output.endsWith('END'));assert.equal(detail.outputTruncated,true);assert.equal(detail.command.length,4096);
});
