import {test} from 'node:test';
import assert from 'node:assert/strict';
import {Model} from '../src/model.mjs';
const stamp=new Date().toISOString();
function ingest(m,source,payload,type='response_item'){m.ingest('codex',{timestamp:stamp,type,payload},source);}
test('Codex sibling writes use agent-scoped call IDs and preserve shared file receipts',()=>{
 const m=new Model();
 for(const id of ['a','b']){ingest(m,id,{id,parent_thread_id:'main',cwd:'/project'},'session_meta');ingest(m,id,{type:'custom_tool_call',call_id:'same',name:'apply_patch',input:'*** Update File: shared.txt'});}
 for(const id of ['b','a'])ingest(m,id,{type:'custom_tool_call_output',call_id:'same',output:'Success. Updated the following files:\nM shared.txt'});
 assert.deepEqual(new Set(m.files.map(f=>f.agentId)),new Set(['codex:default:a','codex:default:b']));assert.ok(m.files.every(f=>f.path==='/project/shared.txt'));
});
test('Codex failed patch and unpaired result cannot claim a write',()=>{
 const m=new Model();ingest(m,'a',{id:'a',cwd:'/project'},'session_meta');ingest(m,'a',{type:'custom_tool_call',call_id:'p',name:'apply_patch',input:'*** Add File: new.txt'});ingest(m,'a',{type:'custom_tool_call_output',call_id:'p',output:'Error: patch rejected'});ingest(m,'a',{type:'custom_tool_call_output',call_id:'missing',output:'Success. Updated the following files:\nA new.txt'});assert.equal(m.files.length,0);
});
test('Codex resolves relative paths against the latest turn directory',()=>{
 const m=new Model();ingest(m,'a',{id:'a',cwd:'/old'},'session_meta');ingest(m,'a',{cwd:'/new'},'turn_context');ingest(m,'a',{type:'custom_tool_call',call_id:'p',name:'apply_patch',input:'*** Add File: new.txt'});ingest(m,'a',{type:'custom_tool_call_output',call_id:'p',output:'Success. Updated the following files:\nA new.txt'});assert.equal(m.files[0].path,'/new/new.txt');
});
test('history loading cannot push old events ahead of new events',()=>{
 const m=new Model();m.observe('/project/new','change','2026-11-17T12:00:00Z');m.observe('/project/old','change','2026-11-16T12:00:00Z');assert.equal(m.files[0].path,'/project/new');
});
