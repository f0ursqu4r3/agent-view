const $=id=>document.getElementById(id);
const escape=s=>String(s ?? '').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
let mode='files', latest, request=0;
for(const [id,value] of [['show-files','files'],['show-commands','commands']])$(id).onclick=()=>{mode=value;updateMode();};
function updateMode(){
 const commands=mode==='commands';$('files').hidden=commands;$('commands').hidden=!commands;$('file-filter').hidden=commands;
 $('show-files').setAttribute('aria-pressed',String(!commands));$('show-commands').setAttribute('aria-pressed',String(commands));
 $('files-title').textContent=commands?'Commands':'File activity';
 if(commands&&latest){$('file-count').textContent=latest.count;$('file-summary').textContent='Codex output, as recorded in local logs';}
 else if(latest){$('file-count').textContent=latest.fileCount;$('file-summary').textContent=latest.fileSummary;}
}
export function renderCommands(state,{selected,provider,search}){
 const agents=new Map(state.agents.map(a=>[a.id,a]));
 const executions=(state.executions||[]).filter(e=>(!selected||e.agentId===selected)&&(provider==='all'||provider===e.provider)&&(!search||[e.commandPreview,e.cwd,agents.get(e.agentId)?.name].join(' ').toLowerCase().includes(search)));
 latest={count:executions.length,fileCount:$('file-count').textContent,fileSummary:$('file-summary').textContent};
 $('commands').innerHTML=executions.map(e=>`<button class="command-row" type="button" data-execution="${escape(e.id)}"><span class="file-top"><span>${escape(e.status)} · ${e.exitCode===null?'exit unknown':`exit ${escape(e.exitCode)}`}</span><span>${e.timestamp?escape(new Date(e.timestamp).toLocaleTimeString()):''}</span></span><span class="command-preview">${escape(e.commandPreview||'Command')}</span><span class="file-author">${escape(agents.get(e.agentId)?.name||e.agentId)}</span></button>`).join('')||'<p class="empty">No matching Codex command records. Output appears when Codex records an execution event.</p>';
 updateMode();
}
$('commands').addEventListener('click',async event=>{
 const button=event.target.closest('[data-execution]');if(!button)return;
 const current=++request;$('command-meta').textContent='Loading recorded output…';$('command-text').textContent='';$('command-output').textContent='';$('command-detail').showModal();
 try{
  const response=await fetch(`/api/executions/${encodeURIComponent(button.dataset.execution)}`);
  if(!response.ok)throw new Error(response.status===404?'This command has left the retained history.':'Could not load command output.');
  const e=await response.json();if(current!==request)return;
  $('command-meta').textContent=`${e.status} · exit ${e.exitCode??'unknown'} · ${e.cwd||'directory unknown'}${e.processRef?` · Codex execution handle ${e.processRef} (not an OS PID)`:''}`;
  $('command-text').textContent=e.command+(e.commandTruncated?'\n[command truncated]':'');
  $('command-output').textContent=(e.outputTruncated?'[earlier output omitted]\n':'')+(e.output||'[No recorded output]');
 }catch(error){if(current===request)$('command-meta').textContent=error.message;}
});
$('close-command').onclick=()=>{$('command-detail').close();request++;};
$('command-detail').addEventListener('cancel',()=>{request++;});
