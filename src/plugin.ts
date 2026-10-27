import { API, RUNNER_API, defaults, State } from './types';
import { BackupService } from './core';
import { Binaries } from './bootstrap';

declare const __storageDir__: string;
const fs = require('fs') as typeof import('fs');
const path = require('path') as typeof import('path');
const crypto = require('crypto') as typeof import('crypto');
const server = require('server');
let service: BackupService;
let stopped = false;

type Req = { method: string; headers: Record<string, string>; query: Record<string, string>; body: string; context?: { principal?: { roles?: string[] } } };
type Res = { statusCode: number; setHeader(k: string, v: string): void; write(data: unknown): void; end(data?: string): void };
function json(res: Res, data: unknown, code = 200) { res.statusCode = code; res.setHeader('Content-Type', 'application/json; charset=utf-8'); res.setHeader('Cache-Control', 'no-store'); res.end(JSON.stringify(data)); }
function parse(req: Req): any { if (req.body.length > 256*1024) throw new Error('请求内容过大'); return JSON.parse(req.body || '{}'); }
function bearer(req: Req) { const h = req.headers.authorization || ''; return h.startsWith('Bearer ') ? h.slice(7) : ''; }
function admin(req: Req) {
  if (!req.context?.principal?.roles?.includes('admin')) throw new Error('需要管理员登录');
  if (req.method !== 'GET' && req.headers['sec-fetch-site'] === 'cross-site') throw new Error('不允许跨站请求');
  const origin = req.headers.origin;
  if (req.method !== 'GET' && origin && origin !== new URL(service.state.settings.panelUrl || origin).origin) throw new Error('请求来源与面板地址不一致');
}
function route(method: string, name: string, fn: (req: Req, res: Res) => unknown | Promise<unknown>, requireAdmin = true) {
  server.route(method, (requireAdmin ? API : RUNNER_API)+name, async (req: Req, res: Res) => {
    try { if (requireAdmin) admin(req); await fn(req, res); }
    catch (error) {
      const message = error instanceof Error ? error.message : '操作未完成';
      json(res, { ok: false, error: message }, /授权|管理员|跨站|来源/.test(message) ? 403 : 400);
    }
  });
}

function storage() {
  const dir = __storageDir__;
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const keyFile = path.join(dir, 'master.key');
  const file = path.join(dir, 'state.enc.json');
  let key: Buffer;
  if (fs.existsSync(keyFile)) key = fs.readFileSync(keyFile);
  else { if (fs.existsSync(file)) throw new Error('已有备份配置但缺少 master.key，请恢复原密钥'); key = crypto.randomBytes(32); fs.writeFileSync(keyFile, key, { mode: 0o600, flag: 'wx' }); }
  if (key.length !== 32) throw new Error('备份配置密钥无效，请恢复 master.key');
  function save(state: State) {
    const iv = crypto.randomBytes(12), cipher = crypto.createCipheriv('aes-256-gcm', key, iv);
    const data = cipher.update(JSON.stringify(state), 'utf8', 'hex') + cipher.final('hex');
    const serialized = JSON.stringify({ version: 1, iv: iv.toString('hex'), tag: cipher.getAuthTag().toString('hex'), data });
    fs.writeFileSync(file+'.tmp', serialized, { mode: 0o600 }); fs.renameSync(file+'.tmp', file);
  }
  return {
    read(): State {
      if (!fs.existsSync(file)) { const state = defaults(); save(state); return state; }
      const data = JSON.parse(fs.readFileSync(file, 'utf8'));
      const decipher = crypto.createDecipheriv('aes-256-gcm', key, Buffer.from(data.iv, 'hex'));
      decipher.setAuthTag(Buffer.from(data.tag, 'hex'));
      // MoeJS 1.6.1 的 Buffer.concat 会损坏字节，使用加密接口的编码输出。
      const state: State = JSON.parse(decipher.update(data.data, 'hex', 'utf8') + decipher.final('utf8'));
      if (state.version !== 1 || !Array.isArray(state.jobs) || !Array.isArray(state.runs)) throw new Error('备份配置版本不支持');
      return state;
    },
    save,
  };
}

async function testStorage() {
  const s = service.state.settings;
  if (!s.webdavUrl || !s.password) throw new Error('请先保存公用存储配置');
  const authorization = 'Basic '+Buffer.from(s.username+':'+s.password).toString('base64');
  const base = new URL(s.webdavUrl+'/');
  const target = (relative: string) => { const u = new URL(base.toString()); u.pathname = base.pathname+relative; return u.toString(); };
  const request = async (method: string, relative: string, body?: string) => {
    const response = await fetch(target(relative), { method, body, redirect: 'manual', headers: { Authorization: authorization, Depth: '1' } });
    if (response.status >= 300 && response.status !== 405) throw new Error(`WebDAV ${method} HTTP ${response.status}`);
    return response;
  };
  let current = '';
  for (const part of s.baseDir.split('/').filter(Boolean)) { current += part+'/'; await request('MKCOL', current); }
  const probe = current+'.probe-panel-'+crypto.randomBytes(12).toString('hex');
  await request('PUT', probe, 'komari-webdav-backup');
  try {
    const head = await request('HEAD', probe);
    if (head.headers.get('content-length') !== '20') throw new Error('WebDAV 文件大小核验失败');
    await request('PROPFIND', current);
  } finally { await request('DELETE', probe); }
  return { message: 'WebDAV 连接及读写检查通过' };
}

export async function load() {
  stopped = false;
  const store = storage();
  const binaries: Binaries = JSON.parse(fs.readFileSync(path.join(__dirname, 'runners', 'manifest.json'), 'utf8'));
  service = new BackupService(store.read(), { now: () => Date.now(), id: () => crypto.randomBytes(16).toString('hex'),
    token: () => crypto.randomBytes(32).toString('hex'), hash: value => crypto.createHash('sha256').update(value).digest('hex'),
    save: state => store.save(state), call: (method, params) => server.call(method, params), binaries });
  route('GET', '/state', (_req, res) => json(res, { ok: true, data: service.view() }));
  route('GET', '/nodes', async (_req, res) => json(res, { ok: true, data: await service.nodes() }));
  route('POST', '/settings', (req, res) => json(res, { ok: true, data: service.saveSettings(parse(req)) }));
  route('POST', '/test-storage', async (_req, res) => json(res, { ok: true, data: await testStorage() }));
  route('POST', '/jobs', async (req, res) => json(res, { ok: true, data: await service.saveJobs(parse(req).jobs) }));
  route('POST', '/delete-job', (req, res) => { service.deleteJob(parse(req).id); json(res, { ok: true }); });
  route('POST', '/run', (req, res) => { const input = parse(req); if (input.mode && !['backup', 'test'].includes(input.mode)) throw new Error('操作类型无效'); const run = service.enqueue(input.jobId, input.mode || 'backup'); json(res, { ok: true, data: service.publicRun(run) }); void tick(); });
  route('POST', '/recover', async (req, res) => json(res, { ok: true, data: await service.recover(parse(req).runId) }));
  route('POST', '/claim', (req, res) => json(res, service.claim(parse(req).runId, bearer(req))), false);
  route('POST', '/report', (req, res) => { service.report(parse(req), bearer(req)); json(res, { ok: true }); }, false);
  route('GET', '/binary', (req, res) => {
    service.authorizeDownload(req.query.run, bearer(req));
    const binary = binaries[req.query.arch];
    if (!binary || !['amd64', 'arm64'].includes(req.query.arch)) throw new Error('不支持的处理器架构');
    const data = fs.readFileSync(path.join(__dirname, 'runners', binary.file));
    if (crypto.createHash('sha256').update(data).digest('hex') !== binary.sha256) throw new Error('执行器文件校验失败');
    res.setHeader('Content-Type', 'application/octet-stream'); res.setHeader('Cache-Control', 'no-store'); res.write(data); res.end();
  }, false);
  server.cron('@every 15s', () => { void tick(); });
  console.log('WebDAV 备份插件已加载');
}
async function tick() { if (stopped) return; try { await service.tick(); } catch { console.error('备份调度暂未完成，将在下次检查时重试'); } }
export function unload() { stopped = true; }
