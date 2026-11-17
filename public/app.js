import {renderCommands} from './commands.js';
const $=id=>document.getElementById(id);
let state,paused=false,selected=null,detailAgent=null,lastRender='';
const escape=s=>String(s ?? '').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const base=s=>String(s || '').split('/').filter(Boolean).at(-1) || 'Unknown project';
const time=s=>s?new Date(s).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit',second:'2-digit'}):'No time';
const providerName=p=>p==='codex'?'Codex':'Claude Code';
function render(){
 if(!state || paused)return;
 const provider=$('provider').value,search=$('search').value.toLowerCase();
 const matches=a=>(provider==='all'||a.provider===provider)&&(!search||[a.name,a.cwd,a.id].join(' ').toLowerCase().includes(search));
 const all=new Map(state.agents.map(a=>[a.id,a]));
 const agents=state.agents.filter(matches).sort((a,b)=>Date.parse(b.lastEvent || 0)-Date.parse(a.lastEvent || 0));
 // Keep ancestors so search does not turn children into false roots.
 const included=new Map(agents.map(a=>[a.id,a]));for(const a of agents){const seen=new Set([a.id]);let p=all.get(a.parentId);while(p&&!seen.has(p.id)){included.set(p.id,p);seen.add(p.id);p=all.get(p.parentId);}}
 const children=new Map();for(const a of included.values()){const p=included.has(a.parentId)?a.parentId:null;if(!children.has(p))children.set(p,[]);children.get(p).push(a);}
 const rendered=new Set();
 function card(a){return `<button type="button" class="agent ${escape(a.provider)} ${selected===a.id?'selected':''}" data-agent="${escape(a.id)}"><div class="agent-top"><span>${providerName(a.provider)} / ${escape(a.scope)}</span><span class="status ${escape(a.status)}">${escape(a.status)}</span></div><div class="agent-name">${escape(a.name)}</div><div class="agent-project" title="${escape(a.cwd)}">${escape(base(a.cwd))}</div><div class="agent-bottom"><span>${escape(a.tool || a.role || 'Agent')}</span><span>${a.writes} writes · ${time(a.lastEvent)}</span></div></button>`;}
 function tree(a,depth=0){if(rendered.has(a.id)||depth>12)return '';rendered.add(a.id);const kids=children.get(a.id)||[];return `${a.parentId&&!included.has(a.parentId)?'<div class="orphan-note">Parent is outside the loaded history</div>':''}${card(a)}${kids.length?`<div class="children">${kids.map(k=>`<div class="branch ${k.link==='session'?'session':''}">${tree(k,depth+1)}</div>`).join('')}</div>`:''}`;}
 let graph=(children.get(null)||[]).map(a=>`<div class="family">${tree(a)}</div>`).join('');
 for(const a of included.values())if(!rendered.has(a.id))graph+=`<div class="family">${tree(a)}</div>`;
 const files=state.files.filter(f=>(!selected||f.agentId===selected)&&(!$('confirmed').checked||f.attribution==='confirmed')&&(provider==='all'||all.get(f.agentId)?.provider===provider)&&(!search||[f.path,all.get(f.agentId)?.name].join(' ').toLowerCase().includes(search)));
 const signature=JSON.stringify([graph,files,state.executions,state.coverage,state.warnings]);if(signature===lastRender)return;lastRender=signature;
 $('graph').innerHTML=graph||'<p class="empty">No matching agents. Start a Codex or Claude Code session, or clear the filters.</p>';
 $('summary').textContent=`${agents.length} agents · ${agents.filter(a=>a.status==='working').length} recently active · ${state.mode==='demo'?'Simulated events':'Local log records'}`;
 $('files').innerHTML=files.slice(0,150).map(f=>`<article class="file-row"><div class="file-top"><span>${escape(f.operation)} · ${time(f.timestamp)}</span><span class="badge ${f.attribution}">${f.attribution}</span></div><div class="file-name">${escape(base(f.path))}</div><div class="file-path">${escape(f.path)}</div><div class="file-author">${escape(all.get(f.agentId)?.name || 'Author unknown')} · ${escape(f.evidence)}</div></article>`).join('')||'<p class="empty">No matching file changes. New changes appear here while monitoring runs.</p>';
 $('file-count').textContent=files.length;$('file-summary').textContent=selected?`Writes by ${all.get(selected)?.name || 'selected agent'}`:'Tool receipts and observed changes';
 const c=state.coverage;$('coverage-summary').textContent=`Monitoring ${c.logsLoaded} of ${c.logsDiscovered} log files · ${c.watchRoots.length} project directories${state.warnings.length?' · coverage limits':''}`;
 $('coverage').innerHTML=`<p>Recent logs load first. Log discovery runs every 5 scan cycles. File changes use metadata snapshots. Short-lived files and writes between snapshots can be missed.</p><p>Session links do not prove an immediate parent. “Stale” means no agent event for two minutes; it does not prove completion.</p><p>Watched directories:<br>${c.watchRoots.map(escape).join('<br>')||'None'}</p><p>Log sources:<br>${c.roots.map(r=>escape(r.path)).join('<br>')||'Demo mode'}</p><p>Malformed records skipped: ${c.malformedRecords}</p>${state.warnings.map(w=>`<p class="warning">${escape(w)}</p>`).join('')}`;
 $('host').textContent=state.host;
 renderCommands(state,{selected,provider,search});
}
$('graph').addEventListener('click',e=>{const button=e.target.closest('[data-agent]');if(!button)return;detailAgent=state.agents.find(a=>a.id===button.dataset.agent);if(!detailAgent)return;$('detail-title').textContent=detailAgent.name;const a=detailAgent;$('detail-body').innerHTML=Object.entries({Provider:providerName(a.provider),ID:a.id,Parent:a.parentId || 'Main session',Link:a.link,Project:a.cwd || 'Unknown',Model:a.model || 'Not recorded',Status:a.status,Tool:a.tool || 'None',Last:eventTime(a.lastEvent)}).map(([key,value])=>`<dt>${escape(key)}</dt><dd>${escape(value)}</dd>`).join('');$('detail').showModal();});
function eventTime(value){return value?new Date(value).toLocaleString():'Unknown';}
$('close-detail').onclick=()=>$('detail').close();$('filter-agent').onclick=()=>{selected=detailAgent.id;$('detail').close();lastRender='';render();};
$('clear').onclick=()=>{selected=null;lastRender='';render();};
for(const id of ['provider','search','confirmed'])$(id).addEventListener('input',()=>{lastRender='';render();});
$('pause').onclick=()=>{paused=!paused;document.body.classList.toggle('paused',paused);$('pause').textContent=paused?'Resume view':'Pause view';$('connection').textContent=paused?'View paused':'Connected';if(!paused)render();};
const stream=new EventSource('/api/events');stream.onmessage=e=>{state=JSON.parse(e.data);$('connection').textContent=paused?'View paused':state.mode==='demo'?'Demo stream':'Live connection';$('connection-dot').classList.add('live');render();};stream.onerror=()=>{$('connection').textContent='Reconnecting';$('connection-dot').classList.remove('live');};
