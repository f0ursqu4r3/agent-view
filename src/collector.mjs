import fsp from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
const LIMIT = 4 * 1024 * 1024;
const ignored = new Set(['.git','.worktrees','.pytest_cache','__pycache__','.turbo','coverage','node_modules','.next','.cache','.venv','venv','dist','build','.serena']);

export class Tailer {
  constructor(model) {this.model=model;this.cursors=new Map();this.badLines=0;}
  async read(source,provider,scope) {
    const stat=await fsp.stat(source);let cursor=this.cursors.get(source);
    if(!cursor || cursor.ino!==stat.ino || stat.size<cursor.offset) {
      cursor={offset:0,ino:stat.ino,buffer:Buffer.alloc(0)};this.cursors.set(source,cursor);
      if(stat.size>LIMIT) {
        const handle=await fsp.open(source,'r');
        try {const head=Buffer.alloc(LIMIT);const {bytesRead}=await handle.read(head,0,head.length,0);const end=head.subarray(0,bytesRead).indexOf(10);if(end>=0)this.line(head.subarray(0,end),source,provider,scope);} finally {await handle.close();}
        cursor.offset=stat.size-LIMIT;cursor.skipFirst=true;
        this.model.warnings.add('Large logs load only their header and last 4 MiB. Earlier tool calls may be missing.');
      }
    }
    if(stat.size===cursor.offset)return;
    const handle=await fsp.open(source,'r');
    try {
      const data=Buffer.alloc(Math.min(LIMIT,stat.size-cursor.offset));
      const {bytesRead}=await handle.read(data,0,data.length,cursor.offset);cursor.offset+=bytesRead;
      const joined=Buffer.concat([cursor.buffer,data.subarray(0,bytesRead)]);let start=0,end;
      while((end=joined.indexOf(10,start))!==-1) {
        if(cursor.skipFirst)cursor.skipFirst=false;else this.line(joined.subarray(start,end),source,provider,scope);
        start=end+1;
      }
      cursor.buffer=joined.subarray(start);
      if(cursor.buffer.length>LIMIT) {cursor.buffer=Buffer.alloc(0);cursor.skipFirst=true;this.model.warnings.add('A log record exceeded 4 MiB and was skipped.');}
    } finally {await handle.close();}
  }
  line(buffer,source,provider,scope) {
    if(!buffer.length)return;
    let record;try {record=JSON.parse(buffer.toString('utf8'));} catch {this.badLines++;return;}
    this.model.ingest(provider,record,source,scope);
  }
}

async function logsUnder(root) {
  const result=[];
  async function visit(dir,depth=0) {
    if(depth>8)return;
    let entries;try {entries=await fsp.readdir(dir,{withFileTypes:true});}catch(error){if(error.code==='ENOENT')return;throw error;}
    for(const entry of entries) {
      const file=path.join(dir,entry.name);
      if(entry.isDirectory())await visit(file,depth+1);
      else if(entry.isFile() && entry.name.endsWith('.jsonl')) {const stat=await fsp.stat(file).catch(()=>null);if(stat)result.push({file,mtime:stat.mtimeMs});}
    }
  }
  await visit(root);return result;
}

export async function discoverRoots() {
  const roots=[], seen=new Set();
  async function add(provider,folder,scope) {try{const real=await fsp.realpath(folder);if(!seen.has(real)){seen.add(real);roots.push({provider,path:real,scope:real.includes(`${path.sep}.codex-accounts${path.sep}`)?path.basename(path.dirname(real)):scope});}}catch{}}
  await add('codex',path.join(process.env.CODEX_HOME || path.join(os.homedir(),'.codex'),'sessions'),'default');
  const accounts=path.join(os.homedir(),'.codex-accounts');
  for(const entry of await fsp.readdir(accounts,{withFileTypes:true}).catch(()=>[]))if(entry.isDirectory())await add('codex',path.join(accounts,entry.name,'sessions'),entry.name);
  await add('claude',path.join(process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(),'.claude'),'projects'),'default');
  return roots;
}

export class Collector {
  constructor(model,{roots,watch=[],maxLogs=120,maxRoots=24}={}) {
    this.model=model;this.roots=roots;this.explicit=watch;this.maxLogs=maxLogs;this.maxRoots=maxRoots;
    this.tailer=new Tailer(model);this.failedRoots=new Map();this.watchers=new Map();this.debounce=new Map();this.logList=[];this.ticks=0;this.closed=false;this.ready=false;
  }
  async start() {this.roots ||= await discoverRoots();await this.tick();this.ready=true;this.schedule();}
  schedule() {if(!this.closed)this.timer=setTimeout(async()=>{try{await this.tick();}catch(e){this.model.warnings.add(`Collector: ${e.message}`);}this.schedule();},1000);}
  async tick() {
    if(this.ticks++%5===0) {
      const all=[];
      for(const root of this.roots) {
        try {for(const item of await logsUnder(root.path))all.push({...item,...root});}
        catch(e) {this.model.warnings.add(`Cannot scan ${root.path}: ${e.code || e.message}`);}
      }
      all.sort((a,b)=>b.mtime-a.mtime);this.discovered=all.length;
      const providers=[...new Set(all.map(x=>x.provider))];
      const quota=Math.floor(this.maxLogs/Math.max(providers.length,1));
      this.logList=providers.flatMap(p=>all.filter(x=>x.provider===p).slice(0,quota));
      const selected=new Set(this.logList.map(x=>x.file));
      for(const file of this.tailer.cursors.keys())if(!selected.has(file)){this.tailer.cursors.delete(file);this.model.sources.delete(file);}
    }
    for(const source of this.logList)try {await this.tailer.read(source.file,source.provider,source.scope);}catch(e){if(e.code!=='ENOENT')this.model.warnings.add(`Cannot read ${source.file}: ${e.code || e.message}`);}
    const agents=[...this.model.agents.values()].sort((a,b)=>Date.parse(b.lastEvent || 0)-Date.parse(a.lastEvent || 0));
    for(const dir of [...this.explicit,...agents.map(a=>a.cwd).filter(Boolean)])await this.watch(dir);
    for(const [root, previous] of this.watchers) {
      const current=await this.scanFiles(root);
      if(current === null) continue;
      for(const [file, signature] of current) if(previous.get(file)!==signature) this.model.observe(file,previous.has(file)?'change':'create');
      for(const file of previous.keys()) if(!current.has(file)) this.model.observe(file,'delete');
      this.watchers.set(root,current);
    }
  }
  async watch(dir) {
    let root;try{root=await fsp.realpath(dir);}catch{return;}
    if(this.watchers.has(root) || Date.now()-(this.failedRoots.get(root)||0)<60000)return;
    if(root===os.homedir() || root.split(path.sep).length<4){this.model.warnings.add(`Broad directory not watched: ${root}`);return;}
    if([...this.watchers.keys()].some(parent=>root.startsWith(parent+path.sep)))return;
    if(this.watchers.size>=this.maxRoots){this.model.warnings.add(`File monitoring is limited to ${this.maxRoots} project directories.`);return;}
    const baseline=await this.scanFiles(root);
    if(baseline !== null)this.watchers.set(root,baseline);
  }
  async scanFiles(root) {
    const result=new Map();let count=0;
    const visit=async dir=>{
      for(const entry of await fsp.readdir(dir,{withFileTypes:true})) {
        if(ignored.has(entry.name) || entry.isSymbolicLink())continue;
        if(++count>12000)throw new Error('directory exceeds the 12,000-entry prototype limit');
        const file=path.join(dir,entry.name);
        if(entry.isDirectory())await visit(file);
        else if(entry.isFile()) {const s=await fsp.stat(file).catch(()=>null);if(s)result.set(file,`${s.mtimeMs}:${s.ctimeMs}:${s.size}:${s.ino}`);}
      }
    };
    try{await visit(root);return result;}catch(e){this.failedRoots.set(root,Date.now());this.model.warnings.add(`Cannot snapshot ${root}: ${e.code || e.message}`);return null;}
  }
  coverage() {return {ready:this.ready,roots:this.roots,logsLoaded:this.logList.length,logsDiscovered:this.discovered || 0,watchRoots:[...this.watchers.keys()],malformedRecords:this.tailer.badLines,pollMs:1000};}
  close(){this.closed=true;clearTimeout(this.timer);this.watchers.clear();for(const t of this.debounce.values())clearTimeout(t);}
}
