import {test} from 'node:test';
import assert from 'node:assert/strict';
import {Model} from '../src/model.mjs';
const time = new Date().toISOString();
function codex(m,id,parent) {m.ingest('codex',{type:'session_meta',timestamp:time,payload:{id,parent_thread_id:parent,cwd:'/project',source:{subagent:{thread_spawn:{parent_thread_id:parent}}}}},id);}
function cx(m,id,p) {m.ingest('codex',{type:'response_item',timestamp:time,payload:p},id);}
function cl(m,agent,content,extra={}) {m.ingest('claude',{sessionId:'root',agentId:agent,cwd:'/project',timestamp:time,type:'assistant',message:{content},...extra},agent || 'main');}

test('Codex links children before parents and preserves nested parent identity',()=>{
 const m=new Model();codex(m,'grandchild','child');codex(m,'child','main');codex(m,'main');
 assert.equal(m.agents.get('codex:default:grandchild').parentId,'codex:default:child');
 assert.equal(m.agents.get('codex:default:child').link,'confirmed');
});
test('Claude session membership does not claim an immediate parent; Agent result confirms nested links',()=>{
 const m=new Model();cl(m,'nested',[]);assert.equal(m.agents.get('claude:default:root:nested').link,'session');
 cl(m,'child',[{type:'tool_use',id:'spawn',name:'Agent',input:{description:'Nested test'}}]);
 cl(m,'child',[{type:'tool_result',tool_use_id:'spawn',content:[]}],{toolUseResult:{agentId:'nested'}});
 cl(m,'nested',[]);assert.equal(m.agents.get('claude:default:root:nested').parentId,'claude:default:root:child');
 assert.equal(m.agents.get('claude:default:root:nested').link,'confirmed');
});
test('two agents writing the same file retain separate confirmed receipts',()=>{
 const m=new Model();
 for(const agent of ['a','b']) {cl(m,agent,[{type:'tool_use',id:'write',name:'Write',input:{file_path:'same.txt',content:'x'}}]);cl(m,agent,[{type:'tool_result',tool_use_id:'write',content:'ok'}]);}
 assert.equal(m.files.length,2);assert.notEqual(m.files[0].agentId,m.files[1].agentId);assert.equal(m.files[0].path,'/project/same.txt');
 m.observe('/project/same.txt','change');assert.equal(m.files[0].attribution,'unknown');assert.equal(m.files[0].agentId,null);
});
test('failed and unfinished file tools do not claim writes',()=>{
 const m=new Model();cl(m,'a',[{type:'tool_use',id:'a',name:'Edit',input:{file_path:'a.txt'}}]);
 cl(m,'a',[{type:'tool_result',tool_use_id:'a',is_error:true,content:'failed'}]);
 cl(m,'a',[{type:'tool_use',id:'b',name:'Write',input:{file_path:'b.txt'}}]);assert.equal(m.files.length,0);
});
test('Codex patch success requires a paired receipt; replay is deduplicated',()=>{
 const m=new Model();codex(m,'a','main');
 for(let i=0;i<2;i++) {cx(m,'a',{type:'custom_tool_call',call_id:'patch',name:'apply_patch',input:'*** Begin Patch\n*** Add File: hello.txt\n+hi\n*** End Patch'});cx(m,'a',{type:'custom_tool_call_output',call_id:'patch',output:'Success. Updated the following files:\nA hello.txt'});}
 assert.equal(m.files.length,1);assert.equal(m.files[0].agentId,'codex:default:a');
});
test('shell commands and opaque wrapper tools cannot invent file attribution',()=>{
 const m=new Model();cl(m,null,[{type:'tool_use',id:'s',name:'Bash',input:{command:'echo x > file'}}]);cl(m,null,[{type:'tool_result',tool_use_id:'s',content:'ok'}]);assert.equal(m.files.length,0);
});
test('provider and account namespaces prevent identity collisions',()=>{
 const m=new Model();for(const scope of ['work','personal'])m.ingest('codex',{type:'session_meta',payload:{id:'same',cwd:'/project'}},scope,scope);
 cl(m,null,[]);assert.equal(m.agents.size,3);
});
