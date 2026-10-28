import type { Job, NodeInfo, State } from './types';

export function pathName(value: string, fallback: string): string {
  const clean = value.normalize('NFC').replace(/[<>:"/\\|?*\x00-\x1f\x7f]/g, '_').replace(/\s+/g, ' ').trim().replace(/[. ]+$/g, '');
  let result = '', bytes = 0;
  for (const char of clean) {
    const cp = char.codePointAt(0)!;
    const size = cp <= 0x7f ? 1 : cp <= 0x7ff ? 2 : cp <= 0xffff ? 3 : 4;
    if (bytes + size > 60) break;
    result += char; bytes += size;
  }
  return result.replace(/[. ]+$/g, '') || fallback;
}
export function directoryLabel(nodeName: string, jobName: string) {
  return pathName(nodeName, '服务器') + '_' + pathName(jobName, '任务');
}
export function taskDirectory(nodeName: string, jobName: string, id: string, occupied: Set<string>) {
  const label = directoryLabel(nodeName, jobName);
  for (let length = 8; length <= Math.max(8, id.length); length += 2) {
    const candidate = label + '_' + id.slice(0, length);
    if (!occupied.has(candidate.toLowerCase())) { occupied.add(candidate.toLowerCase()); return candidate; }
  }
  throw new Error('无法生成唯一保存目录，请重新创建任务');
}
export function runPrefix(now: number, runId: string) {
  const date = new Date(now), pad = (n: number) => String(n).padStart(2, '0');
  const offset = -date.getTimezoneOffset(), absolute = Math.abs(offset);
  return `${date.getFullYear()}${pad(date.getMonth()+1)}${pad(date.getDate())}_${pad(date.getHours())}${pad(date.getMinutes())}${pad(date.getSeconds())}${offset >= 0 ? '+' : '-'}${pad(Math.floor(absolute/60))}${pad(absolute%60)}_${runId.slice(0,8)}`;
}
// 只升级本地配置，不扫描、迁移或删除任何远端文件。
export function upgradeLayout(state: State, nodes: Pick<NodeInfo, 'uuid' | 'name'>[]): boolean {
  if (state.version === 2) return false;
  const occupied = new Set<string>();
  state.jobs = state.jobs.map(job => {
    const old = job as Job & { remoteDir?: string };
    const { remoteDir, ...rest } = old;
    return { ...rest, taskDir: taskDirectory(nodes.find(n => n.uuid === job.nodeId)?.name || '服务器', job.name, job.id, occupied), enabled: false, lastTestAt: undefined };
  });
  // 未执行的排队任务不能在升级后自动改用新布局启动。
  for (const run of state.runs) if (run.status === 'queued') {
    run.status = 'failed'; run.stage = '请测试新的保存位置后重新备份'; run.finishedAt = new Date().toISOString(); run.updatedAt = run.finishedAt;
  }
  state.version = 2;
  return true;
}
