import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {createHash} from 'node:crypto';
const require = createRequire(import.meta.url);
const {BackupService, defaults} = require('../.build/core.cjs');
const {latestSlot, nextSlot, relativeDir, secureUrl} = require('../.build/validation.cjs');
const {bootstrap} = require('../.build/bootstrap.cjs');
const binaries = {amd64:{sha256:'a'.repeat(64), file:'runner-linux-amd64'}, arm64:{sha256:'b'.repeat(64), file:'runner-linux-arm64'}};
function fixture() {
  let seq = 0, now = new Date(2026, 9, 26, 3).getTime();
  const calls = [], nodes = [{uuid:'node-a',name:'上海',os:'Linux'}, {uuid:'node-b',name:'东京',os:'Linux'}];
  const online = {'node-a':{online:true},'node-b':{online:true}};
  const results = {};
  const host = {now:()=>now,id:()=>`id-${++seq}`,token:()=>`grant-${++seq}`,hash:s=>createHash('sha256').update(s).digest('hex'),save:()=>{},binaries,
    call:async(method,params)=>{calls.push({method,params}); if(method==='admin:listClients')return nodes;if(method==='common:getNodesLatestStatus')return online;if(method==='admin:exec')return {task_id:`task-${++seq}`};if(method==='admin:getSpecificTaskResult')return results[params.task_id] ?? {exit_code:null};if(method==='admin:sendNotification')return null;throw Error('unknown RPC '+method);}};
  const state = defaults(); state.settings = {panelUrl:'https://panel.example',webdavUrl:'https://dav.example/dav',username:'backup',password:'super-secret-password',baseDir:'备份',concurrency:2,timeoutMinutes:120};
  const service = new BackupService(state, host);
  const makeJob = async(nodeId='node-a', extra={})=>(await service.saveJobs([{name:'配置',nodeId,sources:['/opt/app'],excludes:[],remoteDir:'prod',schedule:{kind:'daily',time:'02:00',weekday:0},retentionDays:7,notify:true,enabled:false,...extra}]))[0];
  return {service,host,state,calls,online,results,makeJob,advance:ms=>{now+=ms;}};
}
test('HTTPS 和远端路径校验阻止账号嵌入、路径穿越',()=>{
  for(const url of ['http://dav.example','https://u:p@dav.example','https://dav.example?token=1']) assert.throws(()=>secureUrl(url));
  for(const dir of ['../other','/root','a/../b','a\\b']) assert.throws(()=>relativeDir(dir));
  assert.equal(relativeDir('备份/生产/'),'备份/生产');
});
test('日计划与周计划只返回最近一次；下次计划严格晚于当前',()=>{
  const now = new Date(2026,9,26,3).getTime(), daily={kind:'daily',time:'02:00',weekday:0};
  assert.equal(new Date(latestSlot(daily,now)).getHours(),2); assert.ok(nextSlot(daily,now)>now);
  const weekly={kind:'weekly',time:'02:00',weekday:1};assert.equal(new Date(latestSlot(weekly,now)).getDay(),1);
  assert.equal(new Date(nextSlot(weekly,now)).getDate(),2);
});
test('任务默认停用，必须测试后才能启用；修改来源清除测试结果',async()=>{
  const f=fixture(),j=await f.makeJob();assert.equal(j.enabled,false);
  await assert.rejects(f.service.saveJobs([{...j,enabled:true}]),/先完成/);
  j.lastTestAt=new Date().toISOString();await f.service.saveJobs([{...j,enabled:true}]);
  await assert.rejects(f.service.saveJobs([{...j,sources:['/new'],enabled:true}]),/先完成/);
});
test('短效授权仅能领取一次，持久密码不出现在命令或管理 API 中',async()=>{
  const f=fixture(),j=await f.makeJob(),r=f.service.enqueue(j.id);await f.service.tick();
  const command=f.calls.find(c=>c.method==='admin:exec').params.command;
  assert.ok(!command.includes('super-secret-password'));assert.ok(!command.includes('/opt/app'));
  assert.ok(!JSON.stringify(f.service.view()).includes('super-secret-password'));
  const token=command.match(/Authorization: Bearer (grant-\d+)/)[1];
  const cfg=f.service.claim(r.id,token);assert.equal(cfg.password,'super-secret-password');
  assert.throws(()=>f.service.claim(r.id,token),/授权/);
  f.service.report({runId:r.id,status:'success',stage:'done',warnings:[],remotePath:'https://dav.example/file'},cfg.reportToken);
  assert.equal(j.lastTestAt.length>0,true);assert.equal(r.config,undefined);
  await f.service.tick();assert.equal(f.calls.filter(c=>c.method==='admin:sendNotification').length,1);
});
test('过期授权、伪造回报都被拒绝',async()=>{
  const f=fixture(),j=await f.makeJob(),r=f.service.enqueue(j.id);await f.service.tick();
  assert.throws(()=>f.service.report({runId:r.id,status:'success',stage:'done'},'bad'),/授权/);
  f.advance(6*60000);assert.throws(()=>f.service.claim(r.id,'grant-3'),/授权/);
});
test('两台服务器并行，同一服务器串行；重复点击复用同一运行',async()=>{
  const f=fixture(),a=await f.makeJob(),a2=await f.makeJob(),b=await f.makeJob('node-b');
  const r=f.service.enqueue(a.id);assert.equal(f.service.enqueue(a.id).id,r.id);f.service.enqueue(a2.id);f.service.enqueue(b.id);
  await f.service.tick();assert.equal(f.calls.filter(c=>c.method==='admin:exec').length,2);assert.equal(f.state.runs.filter(r=>r.status==='queued').length,1);
});
test('离线任务等待恢复，历史计划合并成一次补跑',async()=>{
  const f=fixture(),j=await f.makeJob();j.lastTestAt=new Date().toISOString();await f.service.saveJobs([{...j,enabled:true}]);
  f.online['node-a'].online=false;f.advance(4*86400000);await f.service.tick();assert.equal(f.state.runs.length,1);assert.equal(f.state.runs[0].status,'queued');
  f.advance(86400000);await f.service.tick();assert.equal(f.state.runs.length,1);
  f.online['node-a'].online=true;await f.service.tick();assert.equal(f.calls.filter(c=>c.method==='admin:exec').length,1);
});
test('回传失败后通过 Komari 任务结果核对，面板重启不重复备份',async()=>{
  const f=fixture(),j=await f.makeJob(),r=f.service.enqueue(j.id);await f.service.tick();
  const restored=new BackupService(f.state,f.host);assert.equal(r.status,'pending');
  f.results[r.taskId]={exit_code:0,result:'KWB_RESULT:'+JSON.stringify({runId:r.id,status:'success',stage:'备份完成',warnings:[],bytes:123})};
  await restored.tick();assert.equal(r.status,'success');assert.equal(f.calls.filter(c=>c.method==='admin:exec').length,1);
});
test('派发 RPC 错误保留待确认状态，禁止盲目重派',async()=>{
  const f=fixture();const call=f.host.call;f.host.call=async(m,p)=>{if(m==='admin:exec')throw Error('network timeout');return call(m,p);};
  const j=await f.makeJob(),r=f.service.enqueue(j.id);await f.service.tick();assert.equal(r.status,'pending');await f.service.tick();assert.equal(r.status,'pending');
});
test('修改公用配置暂停任务并要求重新测试；留空保留原密码',async()=>{
  const f=fixture(),j=await f.makeJob();j.lastTestAt='tested';j.enabled=true;
  f.service.saveSettings({...f.state.settings,baseDir:'new',password:''});assert.equal(j.enabled,false);assert.equal(j.lastTestAt,undefined);assert.equal(f.state.settings.password,'super-secret-password');
});
test('删除任务保留运行记录；排队中的任务结束且不执行远端删除',async()=>{
  const f=fixture(),j=await f.makeJob(),r=f.service.enqueue(j.id);f.service.deleteJob(j.id);assert.equal(r.status,'failed');assert.equal(f.state.runs.length,1);assert.equal(f.calls.filter(c=>c.method==='admin:exec').length,0);
});
test('bootstrap 通过校验摘要安装，状态核对命令不重新执行备份',()=>{
  const cmd=bootstrap('https://panel.example','run-1','token',binaries,true);assert.match(cmd,/SHA256 mismatch/);assert.match(cmd,/status --state-dir/);assert.ok(!cmd.includes('--claim-url'));assert.match(cmd,/Only Linux/);
});
test('离线数日的排队任务合并到最新计划，恢复完成后当天不重复补跑',async()=>{
  const f=fixture(),j=await f.makeJob();j.lastTestAt='tested';await f.service.saveJobs([{...j,enabled:true}]);f.online['node-a'].online=false;
  f.advance(4*86400000);await f.service.tick();f.advance(2*86400000);await f.service.tick();f.online['node-a'].online=true;await f.service.tick();
  const r=f.state.runs[0];f.results[r.taskId]={exit_code:0,result:'KWB_RESULT:'+JSON.stringify({runId:r.id,status:'success',stage:'done',warnings:[]})};
  await f.service.tick();await f.service.tick();assert.equal(f.state.runs.length,1);
});
test('状态核对授权不能再次领取备份密码',async()=>{
  const f=fixture(),j=await f.makeJob(),r=f.service.enqueue(j.id);await f.service.tick();const command=f.calls.find(c=>c.method==='admin:exec').params.command;
  const startup=command.match(/Authorization: Bearer (grant-\d+)/)[1];f.service.claim(r.id,startup);r.status='pending';await f.service.recover(r.id);
  const recovery=f.calls.filter(c=>c.method==='admin:exec').at(-1).params.command.match(/Authorization: Bearer (grant-\d+)/)[1];
  assert.doesNotThrow(()=>f.service.authorizeDownload(r.id,recovery));assert.throws(()=>f.service.claim(r.id,recovery),/授权/);assert.throws(()=>f.service.claim(r.id,startup),/授权/);
});
test('运行期间修改来源后，旧备份成功不能批准新配置',async()=>{
  const f=fixture(),j=await f.makeJob(),r=f.service.enqueue(j.id);await f.service.tick();const command=f.calls.find(c=>c.method==='admin:exec').params.command;
  const token=command.match(/Authorization: Bearer (grant-\d+)/)[1],cfg=f.service.claim(r.id,token);
  const updated=(await f.service.saveJobs([{...j,sources:['/new/source']}]))[0];f.service.report({runId:r.id,status:'success',stage:'done',warnings:[]},cfg.reportToken);assert.equal(updated.lastTestAt,undefined);
});

test('空闲调度、读取、重复心跳不保存；阶段变化与终态及时保存',async()=>{
  const f=fixture();let saves=0;f.host.save=()=>saves++;
  for(let i=0;i<8;i++){f.advance(15000);await f.service.tick();f.service.view();}
  assert.equal(saves,0);
  const j=await f.makeJob(),r=f.service.enqueue(j.id);await f.service.tick();
  const grant=f.calls.find(c=>c.method==='admin:exec').params.command.match(/Authorization: Bearer (grant-\d+)/)[1];
  const cfg=f.service.claim(r.id,grant);const before=saves;
  for(let i=0;i<24;i++){f.advance(15000);f.service.report({runId:r.id,status:'running',stage:'检查备份来源'},cfg.reportToken);await f.service.tick();}
  assert.equal(saves,before);assert.equal(r.status,'running');
  f.service.report({runId:r.id,status:'running',stage:'正在上传'},cfg.reportToken);assert.equal(saves,before+1);
  f.service.report({runId:r.id,status:'success',stage:'完成',warnings:[]},cfg.reportToken);assert.equal(saves,before+2);
});

test('保存失败保持待保存状态，后续空闲调度重试且不反复写盘',async()=>{
  const f=fixture();let saves=0;f.host.save=()=>{saves++;if(saves===1)throw Error('disk unavailable');};
  assert.throws(()=>f.service.saveSettings({...f.state.settings,baseDir:'changed'}),/disk unavailable/);
  await f.service.tick();assert.equal(saves,2);await f.service.tick();assert.equal(saves,2);
  f.service.saveSettings({...f.state.settings});assert.equal(saves,2);
});

test('历史按完成时间保留30天和500条，保护所有非终态且重复维护不保存',async()=>{
  const f=fixture(),now=f.host.now(),iso=age=>new Date(now-age).toISOString();
  f.online['node-a'].online=false;
  const run=(id,status,age)=>({id,jobId:'removed',nodeId:'node-a',jobName:'test',mode:'backup',status,stage:'done',createdAt:iso(age),updatedAt:iso(age),finishedAt:iso(age),warnings:[]});
  f.state.runs=Array.from({length:505},(_,i)=>run('recent-'+i,'success',i*1000));
  f.state.runs.push(run('expired','failed',31*86400000),run('boundary','success',30*86400000),run('queued','queued',90*86400000),run('pending','pending',90*86400000));
  f.state.runs.find(r=>r.id==='pending').stage='等待核对节点执行结果';
  let saves=0;f.host.save=()=>saves++;
  const restored=new BackupService(f.state,f.host);assert.equal(restored.state.runs.length,502);assert.equal(saves,1);
  assert.ok(restored.state.runs.some(r=>r.id==='queued'));assert.ok(restored.state.runs.some(r=>r.id==='pending'));
  assert.ok(!restored.state.runs.some(r=>r.id==='recent-504'));assert.ok(!restored.state.runs.some(r=>r.id==='expired'));
  await restored.tick();assert.equal(saves,1);
  f.advance(31*86400000);await restored.tick();assert.equal(restored.state.runs.length,2);assert.equal(saves,2);
  await restored.tick();assert.equal(saves,2);
});

test('30天整的历史保留，超期在加载时裁剪；长诊断总计不超过16KiB',()=>{
  const f=fixture(),now=f.host.now();
  const run=(id,age)=>({id,jobId:'job',nodeId:'node-a',jobName:'test',mode:'backup',status:'failed',stage:'done',createdAt:new Date(now-age).toISOString(),updatedAt:new Date(now-age).toISOString(),finishedAt:new Date(now-age).toISOString(),warnings:Array(100).fill('中文错误'.repeat(2000)),error:'失败'.repeat(2000),notificationError:'通知失败'.repeat(2000)});
  f.state.runs=[run('boundary',30*86400000),run('old',30*86400000+1)];
  const restored=new BackupService(f.state,f.host);assert.equal(restored.state.runs.length,1);
  const r=restored.state.runs[0],text=[r.error,r.notificationError,...r.warnings].filter(Boolean).join('');
  assert.ok(Buffer.byteLength(text)<=16384);assert.match(text,/已截断/);assert.ok(!text.includes('\ufffd'));
  let saves=0;f.host.save=()=>saves++;new BackupService(restored.state,f.host);assert.equal(saves,0);
});


test('请求诊断脱敏、有界，警告和通知保留；同阶段心跳不写盘',async()=>{
  const f=fixture(),j=await f.makeJob(),r=f.service.enqueue(j.id);await f.service.tick();
  const grant=f.calls.find(c=>c.method==='admin:exec').params.command.match(/Authorization: Bearer (grant-\d+)/)[1],cfg=f.service.claim(r.id,grant);
  let saves=0;f.host.save=()=>saves++;
  f.service.report({runId:r.id,status:'running',stage:'检查备份来源',requestDiagnostics:['HEAD HTTP 423']},cfg.reportToken);assert.equal(saves,0);
  assert.throws(()=>f.service.report({runId:r.id,status:'running',stage:'检查备份来源',requestDiagnostics:[42]},cfg.reportToken),/格式/);
  const warning='上传响应异常，仅核对文件大小，未核验远端内容摘要';
  f.service.report({runId:r.id,status:'warning',stage:'备份完成，有警告',warnings:[warning],requestDiagnostics:Array(30).fill('PUT 502 super-secret-password https://user:pass@dav.example/file?token=private Bearer private '+'中文'.repeat(150))},cfg.reportToken);
  const text=r.requestDiagnostics.join('');assert.ok(r.requestDiagnostics.length<=16);assert.ok(Buffer.byteLength(text)<=4096);assert.ok(!text.includes('super-secret-password'));assert.ok(!text.includes('private'));assert.ok(!text.includes('user:pass'));assert.ok(!text.includes('\ufffd'));
  assert.equal(r.status,'warning');assert.equal(r.warnings[0],warning);assert.equal(f.service.view().runs[0].requestDiagnostics.length,r.requestDiagnostics.length);
  await f.service.tick();assert.ok(f.calls.find(c=>c.method==='admin:sendNotification').params.event.message.includes(warning));
  saves=0;new BackupService(f.state,f.host);assert.equal(saves,0);
});
