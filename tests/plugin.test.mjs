import {test} from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import crypto from 'node:crypto';

const code=fs.readFileSync('dist/script.js','utf8');
async function fixture(t, buffer = Buffer){
  const root=fs.mkdtempSync(path.join(os.tmpdir(),'kwb-plugin-'));t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
  const storage=path.join(root,'storage');fs.mkdirSync(path.join(root,'runners'));
  const bin=Buffer.from('test binary'),sha256=crypto.createHash('sha256').update(bin).digest('hex');
  fs.writeFileSync(path.join(root,'runners','manifest.json'),JSON.stringify({amd64:{file:'runner',sha256},arm64:{file:'runner',sha256}}));fs.writeFileSync(path.join(root,'runners','runner'),bin);
  const routes=new Map(),calls=[],timers=[];
  const server={route:(method,url,handler)=>routes.set(method+' '+url,handler),cron:(expr,fn)=>timers.push(fn),call:async(method,params)=>{calls.push({method,params});if(method==='admin:listClients')return [{uuid:'node-a',name:'测试节点',os:'Linux'}];if(method==='common:getNodesLatestStatus')return {'node-a':{online:true}};if(method==='admin:exec')return {task_id:'task-a'};return {exit_code:null};}};
  function context(){return vm.createContext({require:name=>name==='server'?server:({fs,path,crypto}[name]),__storageDir__:storage,__dirname:root,Buffer:buffer,URL,fetch,console:{log(){},error(){}},setTimeout,clearTimeout});}
  let ctx=context();vm.runInContext(code,ctx);await ctx.load();
  async function request(method,url,body,headers={},roles=['admin']){
    const route=routes.get(method+' '+url.split('?')[0]);assert.ok(route,'route exists: '+url);
    const query=Object.fromEntries(new URL('http://localhost'+url).searchParams),res={statusCode:200,headers:{},setHeader(k,v){this.headers[k]=v;},write(data){this.binary=data;},end(data){this.text=data;}};
    await route({method,body:JSON.stringify(body??{}),headers,query,context:{principal:{roles}}},res);
    return {...res,data:res.text?JSON.parse(res.text):null};
  }
  return {root,storage,routes,calls,timers,request,reload:async()=>{ctx=context();vm.runInContext(code,ctx);await ctx.load();}};
}
test('插件适配器拒绝访客和跨站修改，页面读取密码脱敏',async t=>{
  const f=await fixture(t);
  assert.equal((await f.request('GET','/api/admin/webdav-backup/state',null,{},[])).statusCode,403);
  assert.equal((await f.request('POST','/api/admin/webdav-backup/settings',{}, {'sec-fetch-site':'cross-site'})).statusCode,403);
  const response=await f.request('POST','/api/admin/webdav-backup/settings',{panelUrl:'https://panel.example',webdavUrl:'https://dav.example',username:'backup',password:'Secret-123',baseDir:'backup',concurrency:2,timeoutMinutes:120});assert.equal(response.statusCode,200);
  const view=await f.request('GET','/api/admin/webdav-backup/state');assert.ok(!JSON.stringify(view.data).includes('Secret-123'));assert.equal(view.data.data.settings.passwordSet,true);
  assert.ok(!fs.readFileSync(path.join(f.storage,'state.enc.json'),'utf8').includes('Secret-123'));
  await f.reload();assert.equal((await f.request('GET','/api/admin/webdav-backup/state')).data.data.settings.passwordSet,true);
});
test('真实入口路由完成保存、派发、单次领取、二进制下载和结果回报',async t=>{
  const f=await fixture(t);
  await f.request('POST','/api/admin/webdav-backup/settings',{panelUrl:'https://panel.example',webdavUrl:'https://dav.example',username:'backup',password:'Secret-123',baseDir:'backup',concurrency:2,timeoutMinutes:120});
  const saved=await f.request('POST','/api/admin/webdav-backup/jobs',{jobs:[{name:'配置',nodeId:'node-a',sources:['/opt/app'],excludes:[],remoteDir:'',schedule:{kind:'daily',time:'02:00',weekday:0},retentionDays:7}]});
  const job=saved.data.data[0];const runResponse=await f.request('POST','/api/admin/webdav-backup/run',{jobId:job.id});
  await new Promise(resolve=>setTimeout(resolve,30));const run=runResponse.data.data;
  const command=f.calls.find(x=>x.method==='admin:exec').params.command;const grant=command.match(/Authorization: Bearer ([a-f0-9]+)/)[1];
  assert.ok(!command.includes('Secret-123'));
  const headers={authorization:'Bearer '+grant};
  const binary=await f.request('GET','/api/clients/webdav-backup/binary?run='+run.id+'&arch=amd64',null,headers,[]);assert.equal(binary.binary.toString(),'test binary');
  const cfg=await f.request('POST','/api/clients/webdav-backup/claim',{runId:run.id},headers,[]);assert.equal(cfg.data.password,'Secret-123');
  assert.equal((await f.request('POST','/api/clients/webdav-backup/claim',{runId:run.id},headers,[])).statusCode,403);
  const report=await f.request('POST','/api/clients/webdav-backup/report',{runId:run.id,status:'success',stage:'备份完成',warnings:[]},{authorization:'Bearer '+cfg.data.reportToken},[]);assert.equal(report.statusCode,200);
  assert.equal((await f.request('GET','/api/admin/webdav-backup/state')).data.data.runs[0].status,'success');
});
test('损坏或丢失密钥的已有状态不被静默重置',async t=>{
  const f=await fixture(t);fs.writeFileSync(path.join(f.storage,'master.key'),crypto.randomBytes(32));await assert.rejects(f.reload());
});
test('MoeJS Buffer.concat 异常时加密状态仍能正确重载',async t=>{
  const broken=new Proxy(Buffer,{get(target,key){if(key==='concat')return list=>Buffer.alloc(list.reduce((n,b)=>n+b.length,0));return Reflect.get(target,key);}});
  const f=await fixture(t,broken);await f.reload();assert.equal((await f.request('GET','/api/admin/webdav-backup/state')).data.ok,true);
});
