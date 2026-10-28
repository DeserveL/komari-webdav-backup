import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
const require=createRequire(import.meta.url);
const {pathName,directoryLabel,taskDirectory,runPrefix,upgradeLayout}=require('../.build/layout.cjs');
const {BackupService,defaults}=require('../.build/core.cjs');

test('单层目录支持中文、清理危险字符并按UTF-8限制长度',()=>{
  assert.equal(directoryLabel('东京/主机','配置:每日'),'东京_主机_配置_每日');
  assert.equal(pathName('...','服务器'),'服务器');
  assert.ok(Buffer.byteLength(pathName('中文😀'.repeat(100),'任务'))<=60);
  assert.ok(!pathName('中文😀'.repeat(100),'任务').includes('\ufffd'));
  assert.ok(!/[<>:"/\\|?*\x00-\x1f]/.test(pathName('a<>:"/\\|?*\u0001b','任务')));
});
test('同名任务自动扩展短编号，不能生成重复目录',()=>{
  const occupied=new Set();const a=taskDirectory('东京','配置','12345678aa'+'0'.repeat(22),occupied),b=taskDirectory('东京','配置','12345678bb'+'0'.repeat(22),occupied);
  assert.equal(a,'东京_配置_12345678');assert.equal(b,'东京_配置_12345678bb');
});
test('文件前缀使用面板时间和明确的时区偏移',()=>{
  const previous=process.env.TZ;process.env.TZ='Asia/Shanghai';
  try{assert.equal(runPrefix(Date.parse('2026-10-27T18:00:00Z'),'abcdef0123456789abcdef0123456789'),'20261028_020000+0800_abcdef01');}
  finally{if(previous===undefined)delete process.env.TZ;else process.env.TZ=previous;}
});
test('旧配置只升级本地目录和暂停状态，不丢凭据、历史链接或在途核对数据',()=>{
  const state=defaults();state.version=1;state.settings.password='example';
  state.jobs=[{id:'a'.repeat(32),nodeId:'node',name:'配置',remoteDir:'old',enabled:true,lastTestAt:'tested'}];
  state.runs=[{id:'old',status:'success',remotePath:'https://dav.example/old/backup.tar.gz'},{id:'active',status:'running',config:{protocol:1}},{id:'queued',status:'queued'}];
  assert.equal(upgradeLayout(state,[{uuid:'node',name:'东京'}]),true);
  assert.equal(state.version,2);assert.equal(state.jobs[0].taskDir,'东京_配置_aaaaaaaa');assert.equal(state.jobs[0].enabled,false);assert.equal(state.jobs[0].lastTestAt,undefined);assert.ok(!('remoteDir' in state.jobs[0]));
  assert.equal(state.settings.password,'example');assert.match(state.runs[0].remotePath,/old/);assert.equal(state.runs[1].config.protocol,1);assert.equal(state.runs[2].status,'failed');
  assert.equal(upgradeLayout(state,[]),false);
});
test('批量创建每个节点独立目录，客户端不能指定目录，改名不改变保存位置',async()=>{
  const state=defaults();let seq=0;const nodes=[{uuid:'a',name:'东京'},{uuid:'b',name:'大阪'}];
  const host={now:()=>Date.now(),id:()=>String(++seq).padStart(32,'0'),token:()=>'',hash:()=>'',save:()=>{},binaries:{},call:async m=>m==='admin:listClients'?nodes:{}};
  const service=new BackupService(state,host),input={name:'配置',sources:['/etc/example'],excludes:[],taskDir:'../evil',schedule:{kind:'daily',time:'02:00',weekday:0}};
  const jobs=await service.saveJobs(nodes.map(n=>({...input,nodeId:n.uuid})));
  assert.notEqual(jobs[0].taskDir,jobs[1].taskDir);assert.match(jobs[0].taskDir,/东京_配置/);assert.match(jobs[1].taskDir,/大阪_配置/);
  const saved=jobs[0].taskDir;nodes[0].name='改名';await service.saveJobs([{...jobs[0],name:'新任务名',taskDir:'tampered'}]);
  assert.equal(state.jobs[0].taskDir,saved);assert.equal(service.view().jobs[0].savePath,'komari-backups/'+saved);
});
