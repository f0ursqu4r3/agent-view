import http from 'node:http';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {Model} from './model.mjs';
import {Collector} from './collector.mjs';
import {seedDemo} from './demo.mjs';
const args=process.argv.slice(2), demo=args.includes('--demo');
const port=Number(process.env.PORT || 4317);
const watch=args.flatMap((value,index)=>value==='--watch' && args[index+1] ? [path.resolve(args[index+1])] : []);
const model=new Model();
const collector=new Collector(model,{watch});
let stopDemo;
if(demo)stopDemo=seedDemo(model);else await collector.start();
const publicDir=fileURLToPath(new URL('../public/',import.meta.url));
const assets=new Map([['/',['index.html','text/html']],['/app.js',['app.js','text/javascript']],['/commands.js',['commands.js','text/javascript']],['/style.css',['style.css','text/css']]]);
const clients=new Set();
const snapshot=()=>({...model.snapshot(),mode:demo?'demo':'live',host:os.hostname(),now:new Date().toISOString(),coverage:demo?{ready:true,roots:[],watchRoots:[],logsLoaded:0,logsDiscovered:0,malformedRecords:0}:collector.coverage()});
const server=http.createServer(async(req,res)=>{
  if(!['127.0.0.1','localhost'].some(host=>req.headers.host===`${host}:${port}`)) {res.writeHead(403);res.end('Invalid host');return;}
  if(req.headers.origin && ![`http://127.0.0.1:${port}`,`http://localhost:${port}`].includes(req.headers.origin)){res.writeHead(403);res.end('Invalid origin');return;}
  const pathname=new URL(req.url,'http://localhost').pathname;
  res.setHeader('Cache-Control','no-store');res.setHeader('X-Content-Type-Options','nosniff');
  res.setHeader('Content-Security-Policy',"default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'");
  if(req.method!=='GET'){res.writeHead(405);res.end();return;}
  if(pathname==='/api/state'){res.setHeader('Content-Type','application/json');res.end(JSON.stringify(snapshot()));return;}
  if(pathname.startsWith('/api/executions/')) {
    let id;try{id=decodeURIComponent(pathname.slice('/api/executions/'.length));}catch{res.writeHead(400);res.end('Invalid ID');return;}
    const execution=model.executions.get(id);if(!execution){res.writeHead(404);res.end('Execution expired or not found');return;}
    res.setHeader('Content-Type','application/json');res.end(JSON.stringify(execution));return;
  }
  if(pathname==='/api/events') {
    res.writeHead(200,{'Content-Type':'text/event-stream','Connection':'keep-alive'});res.write(`data: ${JSON.stringify(snapshot())}\n\n`);clients.add(res);req.on('close',()=>clients.delete(res));return;
  }
  const asset=assets.get(pathname);if(!asset){res.writeHead(404);res.end('Not found');return;}
  try{res.setHeader('Content-Type',asset[1]);res.end(await fs.readFile(path.join(publicDir,asset[0])));}catch{res.writeHead(500);res.end('Asset unavailable');}
});
const timer=setInterval(()=>{const payload=`data: ${JSON.stringify(snapshot())}\n\n`;for(const client of clients){if(client.writableLength>2*1024*1024){client.destroy();clients.delete(client);}else client.write(payload);}},1000);
server.listen(port,'127.0.0.1',()=>console.log(`Agent View (${demo?'demo':'live'}): http://127.0.0.1:${port}`));
function close(){clearInterval(timer);collector.close();stopDemo?.();for(const c of clients)c.end();server.close();}
process.on('SIGINT',close);process.on('SIGTERM',close);
