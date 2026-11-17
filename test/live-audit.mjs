// Run separately: node test/live-audit.mjs. Reads local data, prints counts only.
import assert from 'node:assert/strict';
const response=await fetch('http://127.0.0.1:4317/api/state');assert.equal(response.status,200);
const state=await response.json();const agents=new Map(state.agents.map(a=>[a.id,a]));
const counts={};for(const provider of ['codex','claude']){
 const list=state.agents.filter(a=>a.provider===provider);assert.ok(list.length>0,`${provider} discovery`);
 const linked=list.filter(a=>a.parentId);assert.ok(linked.length>0,`${provider} parent links`);
 for(const a of linked){assert.notEqual(a.id,a.parentId);const seen=new Set([a.id]);let p=agents.get(a.parentId);while(p){assert.ok(!seen.has(p.id),'parent cycle');seen.add(p.id);p=agents.get(p.parentId);}}
 counts[provider]={agents:list.length,confirmedParents:linked.filter(a=>a.link==='confirmed').length,sessionLinks:linked.filter(a=>a.link==='session').length,loadedParents:linked.filter(a=>agents.has(a.parentId)).length,confirmedWrites:state.files.filter(f=>f.attribution==='confirmed' && agents.get(f.agentId)?.provider===provider).length};
}
for(const f of state.files)if(f.attribution==='confirmed')assert.ok(agents.has(f.agentId));
console.log(JSON.stringify({counts,loadedLogs:state.coverage.logsLoaded,malformedRecords:state.coverage.malformedRecords,warnings:state.warnings.length},null,2));
