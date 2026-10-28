import { Job, Schedule, Settings } from './types';

export function text(value: unknown, max = 4096): string {
  if (typeof value !== 'string' || value.length > max || /[\x00-\x1f\x7f]/.test(value)) throw new Error('文本格式不正确或超出长度限制');
  return value.trim();
}
export function secureUrl(value: unknown): string {
  const url = new URL(text(value));
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash) throw new Error('请输入不含账号、查询参数或片段的 HTTPS 地址');
  return url.toString().replace(/\/+$/, '');
}
export function relativeDir(value: unknown): string {
  const dir = text(value);
  if (dir.startsWith('/') || dir.includes('\\') || dir.split('/').some(x => x === '.' || x === '..')) throw new Error('远端目录必须是相对目录，不能包含 . 或 ..');
  return dir.split('/').filter(Boolean).join('/');
}
export function integer(value: unknown, min: number, max: number, label: string): number {
  const n = Number(value);
  if (!Number.isInteger(n) || n < min || n > max) throw new Error(`${label}应为 ${min} 到 ${max} 之间的整数`);
  return n;
}
export function settings(input: Partial<Settings>, old: Settings): Settings {
  return { panelUrl: secureUrl(input.panelUrl), webdavUrl: secureUrl(input.webdavUrl), username: text(input.username, 1024),
    password: typeof input.password === 'string' && input.password !== '' ? input.password : old.password,
    baseDir: relativeDir(input.baseDir), concurrency: integer(input.concurrency, 1, 8, '并发数量'),
    timeoutMinutes: integer(input.timeoutMinutes, 5, 1440, '任务超时（分钟）') };
}
export function schedule(value: Schedule): Schedule {
  if (!value || !['daily', 'weekly'].includes(value.kind) || !/^([01]\d|2[0-3]):[0-5]\d$/.test(value.time)) throw new Error('请选择每天或每周的有效执行时间');
  return { kind: value.kind, time: value.time, weekday: integer(value.weekday ?? 0, 0, 6, '星期') };
}
export function job(input: Partial<Job>, id: string, now: number, old?: Job): Job {
  const sources = (Array.isArray(input.sources) ? input.sources : []).map(x => text(x));
  if (!sources.length || sources.length > 128 || sources.some(x => !x.startsWith('/') || x.split('/').includes('..') || x === '/')) throw new Error('请填写 1 到 128 个 Linux 绝对路径；不支持备份整个根目录');
  const excludes = (Array.isArray(input.excludes) ? input.excludes : []).map(x => text(x));
  if (excludes.length > 128 || excludes.some(x => !x || x.startsWith('/') || x.includes('\\') || x.split('/').includes('..'))) throw new Error('排除规则使用相对归档路径，例如 **/*.log 或 opt/app/cache/**');
  const name = text(input.name, 100), nodeId = text(input.nodeId, 100);
  if (!name || !/^[A-Za-z0-9_-]+$/.test(nodeId)) throw new Error('请填写任务名称并选择服务器');
  const changed = !!old && JSON.stringify([sources, excludes, nodeId]) !== JSON.stringify([old.sources, old.excludes, old.nodeId]);
  const lastTestAt = changed ? undefined : old?.lastTestAt;
  if (input.enabled && !lastTestAt) throw new Error('请先完成一次节点连接测试或手动备份，再启用定时任务');
  return { id, name, nodeId, sources: [...new Set(sources)], excludes, taskDir: old?.taskDir || '',
    schedule: schedule(input.schedule!), retentionDays: integer(input.retentionDays ?? 7, 0, 36500, '保留天数'),
    notify: input.notify === true, enabled: input.enabled === true, createdAt: old?.createdAt ?? new Date(now).toISOString(),
    cursor: old?.enabled && input.enabled ? old.cursor : now, lastTestAt };
}

// 只计算最近一个计划点；面板恢复后不会逐个重放历史任务。
export function latestSlot(s: Schedule, now: number): number {
  const d = new Date(now), [hour, minute] = s.time.split(':').map(Number);
  d.setHours(hour, minute, 0, 0);
  if (d.getTime() > now) d.setDate(d.getDate() - 1);
  if (s.kind === 'weekly') d.setDate(d.getDate() - (d.getDay() - s.weekday + 7) % 7);
  return d.getTime();
}
export function nextSlot(s: Schedule, now: number): number {
  const d = new Date(latestSlot(s, now));
  d.setDate(d.getDate() + (s.kind === 'weekly' ? 7 : 1));
  return d.getTime();
}
