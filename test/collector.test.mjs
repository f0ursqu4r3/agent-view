import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {Model} from '../src/model.mjs';
import {Tailer,Collector} from '../src/collector.mjs';

test('tailer waits for complete JSON lines and preserves split UTF-8',async()=>{
 const dir=await fs.mkdtemp(path.join(os.tmpdir(),'agent-view-tail-'));try {
 const file=path.join(dir,'a.jsonl'),m=new Model(),t=new Tailer(m);
 const line=Buffer.from(JSON.stringify({type:'session_meta',payload:{id:'test',cwd:'/project/日本'}})+'\n');const cut=line.indexOf(Buffer.from('日'))+1;
 await fs.writeFile(file,line.subarray(0,cut));await t.read(file,'codex','test');assert.equal(m.agents.size,0);
 await fs.appendFile(file,line.subarray(cut));await t.read(file,'codex','test');assert.equal(m.agents.get('codex:test:test').cwd,'/project/日本');assert.equal(t.badLines,0);
 await t.read(file,'codex','test');assert.equal(m.agents.size,1);
 await fs.writeFile(file,'{}\n');await t.read(file,'codex','test');assert.equal(t.cursors.get(file).offset,3);
 }finally{await fs.rm(dir,{recursive:true,force:true});}
});
test('real filesystem write is observed with unknown attribution',async()=>{
 const dir=await fs.mkdtemp(path.join(os.tmpdir(),'agent-view-watch-'));const m=new Model(),c=new Collector(m,{roots:[],watch:[dir]});
 try{await c.start();await fs.writeFile(path.join(dir,'observed.txt'),'hello');
 const deadline=Date.now()+3000;while(!m.files.length && Date.now()<deadline)await new Promise(r=>setTimeout(r,50));
 assert.equal(m.files.length,1);assert.equal(m.files[0].attribution,'unknown');assert.equal(m.files[0].path,path.join(await fs.realpath(dir),'observed.txt'));
 }finally{c.close();await fs.rm(dir,{recursive:true,force:true});}
});
