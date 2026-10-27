// 仅启动本脚本创建的本机隔离实例，不连接现有面板或生产节点。
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {createServer} from 'node:net';
import {mkdir,mkdtemp,cp,readFile,writeFile,open} from 'node:fs/promises';
import {resolve,join} from 'node:path';
import {randomBytes,createDecipheriv,createHash} from 'node:crypto';
import assert from 'node:assert/strict';
const binary=process.env.KOMARI_EXE;
if(!binary)throw Error('Set KOMARI_EXE to an official Komari 1.6.1 binary');
await mkdir('.build',{recursive:true});
const root=await mkdtemp(resolve('.build/komari-smoke-'));
await cp('dist',join(root,'data/plugin/webdav-backup'),{recursive:true});
const socket=createServer();socket.listen(0,'127.0.0.1');await once(socket,'listening');const port=socket.address().port;await new Promise(r=>socket.close(r));
const base='http://127.0.0.1:'+port;
const username='local-test',password='LocalA1-'+randomBytes(16).toString('hex'),apiKey=randomBytes(24).toString('hex');
const checks=[];let child,ws;const log=await open(join(root,'server.log'),'a');
function start(){child=spawn(resolve(binary),['server','-l','127.0.0.1:'+port],{cwd:root,stdio:['ignore',log.fd,log.fd],windowsHide:true});}
async function stop(){if(!child||child.exitCode!==null)return;const exited=once(child,'exit');child.kill();await exited;}
async function waitFor(path){const deadline=Date.now()+60000;while(Date.now()<deadline){if(child.exitCode!==null)throw Error('Komari test process exited: '+await readFile(join(root,'server.log'),'utf8'));try{const r=await fetch(base+path);if(r.ok)return;}catch{}await new Promise(r=>setTimeout(r,250));}throw Error('Local Komari startup timed out');}
async function request(path,body,auth=true,extra={}){
  const res=await fetch(base+path,{method:body===undefined?'GET':'POST',headers:{...(auth?{Authorization:'Bearer '+apiKey}:{}),'Content-Type':'application/json',...extra},body:body===undefined?undefined:JSON.stringify(body)});
  const data=await res.json();return {status:res.status,data};
}
async function rpc(method,params){const r=await request('/api/rpc2',{jsonrpc:'2.0',method,params,id:1});if(r.data.error)throw Error(r.data.error.message);return r.data.result;}
async function installPackage(file){
  const data=await readFile(file);
  const initialized=await request('/api/admin/upload/init',{purpose:'plugin',filename:'webdav-backup.zip',size:data.length});assert.equal(initialized.status,200);
  const {upload_id,chunk_size}=initialized.data.data;
  for(let offset=0,index=0;offset<data.length;offset+=chunk_size,index++){
    const body=new FormData();body.set('upload_id',upload_id);body.set('chunk_index',String(index));body.set('chunk_data',new Blob([data.subarray(offset,offset+chunk_size)]),'chunk.bin');
    const response=await fetch(base+'/api/admin/upload/chunk',{method:'POST',headers:{Authorization:'Bearer '+apiKey},body});assert.equal(response.status,200);
  }
  const merged=await request('/api/admin/upload/merge',{upload_id});assert.equal(merged.status,200,JSON.stringify(merged.data));
}
const plugin='/api/admin/webdav-backup';
try{
  start();await waitFor('/api/install/status');
  let r=await request('/api/install/complete',{username,password,sitename:'Isolated backup test',metric_dsn:'./data/metrics.db'},false);assert.equal(r.status,200);
  await waitFor('/api/version');
  r=await request('/api/login',{username,password},false);assert.equal(r.status,200);const session=r.data.data['set-cookie'].session_token;
  r=await request('/api/rpc2',{jsonrpc:'2.0',method:'admin:editSettings',params:{api_key:apiKey},id:1},false,{Cookie:'session_token='+session});assert.ok(!r.data.error);
  if(process.env.KOMARI_PREVIOUS_PACKAGE)await installPackage(process.env.KOMARI_PREVIOUS_PACKAGE);
  r=await request('/api/admin/plugin/enabled',{short:'webdav-backup',enabled:true,approved:true});assert.equal(r.status,200);checks.push('official runtime load');
  r=await request(plugin+'/settings',{panelUrl:'https://panel.invalid',webdavUrl:'https://dav.invalid/dav',username:'test-backup',password:'Local-Secret-测试',baseDir:'服务器备份',concurrency:2,timeoutMinutes:120});assert.equal(r.status,200);checks.push('MoeJS settings and AES-GCM write');
  const a=await rpc('admin:addClient',{name:'隔离测试 A'}),b=await rpc('admin:addClient',{name:'隔离测试 B'});
  const input=nodeId=>({name:'中文配置',nodeId,sources:['/opt/服务/config.json','/opt/服务/data'],excludes:['**/*.log'],remoteDir:'生产',schedule:{kind:'daily',time:'02:00',weekday:1},retentionDays:7,notify:false,enabled:false});
  r=await request(plugin+'/jobs',{jobs:[input(a.uuid),input(b.uuid)]});assert.equal(r.status,200);const jobs=r.data.data;assert.equal(jobs.length,2);checks.push('real client RPC and batch jobs');
  r=await request(plugin+'/nodes');assert.equal(r.data.data.length,2);assert.equal(r.data.data[0].online,false);checks.push('real node status RPC');
  r=await request(plugin+'/run',{jobId:jobs[0].id,mode:'test'});assert.equal(r.status,200);const runId=r.data.data.id;
  r=await request(plugin+'/run',{jobId:jobs[0].id,mode:'test'});assert.equal(r.data.data.id,runId);checks.push('offline queue and duplicate suppression');
  const dir=join(root,'data/plugin-data/webdav-backup'),encrypted=JSON.parse(await readFile(join(dir,'state.enc.json'),'utf8')),key=await readFile(join(dir,'master.key'));
  assert.ok(!JSON.stringify(encrypted).includes('Local-Secret'));
  const decipher=createDecipheriv('aes-256-gcm',key,Buffer.from(encrypted.iv,'hex'));decipher.setAuthTag(Buffer.from(encrypted.tag,'hex'));
  const plain=JSON.parse(Buffer.concat([decipher.update(Buffer.from(encrypted.data,'hex')),decipher.final()]).toString());assert.equal(plain.jobs.length,2);checks.push('Node authenticates MoeJS encrypted state');
  for(let i=0;i<3;i++)for(const enabled of [false,true]){r=await request('/api/admin/plugin/enabled',{short:'webdav-backup',enabled,approved:true});assert.equal(r.status,200);}
  r=await request(plugin+'/state');assert.equal(r.data.data.jobs.length,2);assert.equal(r.data.data.settings.password,'');assert.equal(r.data.data.settings.passwordSet,true);assert.equal(r.data.data.runs.length,1);checks.push('three reload cycles preserve state and redact secrets');
  r=await request(plugin+'/settings',{},true,{'sec-fetch-site':'cross-site'});assert.equal(r.status,403);checks.push('cross-site mutation rejected');
  await rpc('admin:editSettings',{private_site:true});
  r=await request('/api/clients/webdav-backup/claim',{runId:'missing'},false);assert.equal(r.status,403);assert.match(r.data.error,/授权/);checks.push('private-site runner route reaches token validation');
  r=await request(plugin+'/state',undefined,false);assert.equal(r.status,403);checks.push('anonymous admin request rejected');
  await stop();start();await waitFor('/api/version');
  r=await request(plugin+'/state');assert.equal(r.status,200);assert.equal(r.data.data.jobs.length,2);assert.equal(r.data.data.runs.length,1);checks.push('full server restart preserves configuration and queue');
  const messages=[];
  ws=new WebSocket(base.replace(/^http/,'ws')+'/api/clients/v2/rpc?token='+encodeURIComponent(a.token));
  ws.addEventListener('message',event=>messages.push(JSON.parse(event.data)));
  await once(ws,'open');
  ws.send(JSON.stringify({jsonrpc:'2.0',method:'agent.report',params:{report:{cpu:{usage:0},ram:{total:1024,used:0},uptime:1,updated_at:new Date().toISOString()}},id:'report-1'}));
  const waitUntil=async(fn)=>{const end=Date.now()+25000;while(Date.now()<end){if(await fn())return;await new Promise(r=>setTimeout(r,100));}throw Error('Agent protocol fixture timed out');};
  await waitUntil(async()=>{const r=await request(plugin+'/nodes');return r.data.data.find(n=>n.uuid===a.uuid)?.online;});
  await request(plugin+'/run',{jobId:jobs[0].id,mode:'test'});
  await waitUntil(()=>messages.some(m=>m.method==='agent.exec'));
  const exec=messages.find(m=>m.method==='agent.exec');assert.ok(!exec.params.command.includes('Local-Secret'));
  const grant=exec.params.command.match(/Authorization: Bearer ([a-f0-9]+)/)[1];checks.push('real internal admin:exec reaches virtual v2 agent without credentials in command');
  const downloaded=await fetch(base+'/api/clients/webdav-backup/binary?run='+runId+'&arch=amd64',{headers:{Authorization:'Bearer '+grant}});assert.equal(downloaded.status,200);
  const manifest=JSON.parse(await readFile(join(root,'data/plugin/webdav-backup/runners/manifest.json'),'utf8'));assert.equal(createHash('sha256').update(Buffer.from(await downloaded.arrayBuffer())).digest('hex'),manifest.amd64.sha256);checks.push('real runtime binary response preserves installed-version SHA-256');
  r=await request('/api/clients/webdav-backup/claim',{runId},false,{Authorization:'Bearer '+grant});assert.equal(r.status,200);assert.equal(r.data.password,'Local-Secret-测试');const reportToken=r.data.reportToken;
  r=await request('/api/clients/webdav-backup/claim',{runId},false,{Authorization:'Bearer '+grant});assert.equal(r.status,403);checks.push('single-use claim on real runtime');
  const result={runId,status:'success',stage:'协议检查完成（模拟执行器）',warnings:[]};r=await request('/api/clients/webdav-backup/report',result,false,{Authorization:'Bearer '+reportToken});assert.equal(r.status,200);
  r=await request(plugin+'/state');assert.equal(r.data.data.runs[0].status,'success');assert.ok(!JSON.stringify(r.data).includes('Local-Secret'));checks.push('real runtime accepts scoped completion and redacts secrets');
  const agentResult=await fetch(base+'/api/clients/v2/rpc?token='+encodeURIComponent(a.token),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({jsonrpc:'2.0',method:'agent.taskResult',params:{task_id:exec.params.task_id,result:'KWB_RESULT:'+JSON.stringify(result),exit_code:0,finished_at:new Date().toISOString()},id:'task-result'})});assert.equal(agentResult.status,200);
  await rpc('admin:sendNotification',{event:{event:'备份协议检查',message:'本机隔离测试，未启用外部通知通道',clients:[{uuid:a.uuid}]}});checks.push('real task-result and notification RPC signatures');
  ws.close();await once(ws,'close');ws=undefined;
  const before=(await request(plugin+'/state')).data.data;
  const keyBefore=await readFile(join(dir,'master.key'));
  const version=JSON.parse(await readFile('package.json','utf8')).version;
  await installPackage('release/komari-webdav-backup-'+version+'.zip');
  const after=(await request(plugin+'/state')).data.data;
  assert.equal(after.version,version);assert.deepEqual(after.jobs,before.jobs);assert.deepEqual(after.runs,before.runs);assert.deepEqual(after.settings,before.settings);
  assert.deepEqual(JSON.parse(await readFile(join(root,'data/plugin/webdav-backup/runners/manifest.json'),'utf8')),JSON.parse(await readFile('dist/runners/manifest.json','utf8')));
  assert.deepEqual(await readFile(join(dir,'master.key')),keyBefore);
  const stored=JSON.parse(await readFile(join(dir,'state.enc.json'),'utf8')),decrypt=createDecipheriv('aes-256-gcm',keyBefore,Buffer.from(stored.iv,'hex'));decrypt.setAuthTag(Buffer.from(stored.tag,'hex'));
  const restored=JSON.parse(Buffer.concat([decrypt.update(Buffer.from(stored.data,'hex')),decrypt.final()]).toString());assert.equal(restored.settings.password,'Local-Secret-测试');assert.ok(restored.jobs.every(j=>!j.enabled));
  checks.push('official ZIP '+(process.env.KOMARI_PREVIOUS_PACKAGE?'upgrade from '+before.version:'reinstall')+' preserves tasks, credentials, history and disabled schedules');
  const report={runtime:'Komari 1.6.1',host:process.platform,checks,passed:checks.length,limitations:['No production Linux Agent or real cloud WebDAV provider was connected.']};
  await writeFile('.build/komari-smoke-result.json',JSON.stringify(report,null,2)+'\n');console.log(JSON.stringify(report,null,2));
}finally{ws?.close();await stop();await log.close();}
