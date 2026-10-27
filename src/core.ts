import { RUNNER_API, defaults, Job, NodeInfo, Report, Run, RunnerConfig, Settings, State, terminal } from './types';
import * as validate from './validation';
import { Binaries, bootstrap } from './bootstrap';

export type Host = {
  now(): number; id(): string; token(): string; hash(value: string): string;
  save(state: State): void; call(method: string, params?: unknown): Promise<any>;
  binaries: Binaries;
};
export class BackupService {
  private ticking = false;
  private recovering = new Set<string>();
  constructor(public state: State, private host: Host) {
    for (const run of state.runs) if (run.status === 'running' || run.status === 'dispatching') { run.status = 'pending'; run.stage = '等待核对节点执行结果'; }
    this.persist();
  }
  private persist() { this.host.save(this.state); }
  private iso() { return new Date(this.host.now()).toISOString(); }
  private redact(value: unknown): string {
    let s = String(value ?? '').slice(0, 6000);
    for (const secret of [this.state.settings.password, ...this.state.runs.map(r => r.config?.password)].filter(Boolean) as string[]) s = s.split(secret).join('[已隐藏]');
    return s;
  }
  view() {
    return { settings: { ...this.state.settings, password: '', passwordSet: !!this.state.settings.password },
      jobs: this.state.jobs.map(j => ({ ...j, nextRun: new Date(validate.nextSlot(j.schedule, this.host.now())).toISOString() })),
      runs: [...this.state.runs].reverse().slice(0, 200).map(r => this.publicRun(r)),
      time: this.iso(), timezone: new Date(this.host.now()).toString(), timezoneOffset: new Date(this.host.now()).getTimezoneOffset(), version: '0.1.1' };
  }
  publicRun(r: Run) {
    const { config, startupHash, startupExpires, reportHash, reportExpires, recoveryHash, recoveryExpires, ...safe } = r;
    return { ...safe, durationSeconds: r.startedAt ? Math.max(0, Math.round(((r.finishedAt ? Date.parse(r.finishedAt) : this.host.now()) - Date.parse(r.startedAt)) / 1000)) : undefined };
  }
  saveSettings(input: Partial<Settings>) {
    const next = validate.settings(input, this.state.settings);
    if (!next.password || next.password.length > 4096) throw new Error('请填写 WebDAV 密码（最多 4096 字符）');
    if (JSON.stringify(next) !== JSON.stringify(this.state.settings)) for (const j of this.state.jobs) { j.enabled = false; j.lastTestAt = undefined; }
    this.state.settings = next; this.persist();
    return this.view().settings;
  }
  async nodes(): Promise<NodeInfo[]> {
    const [raw, latest] = await Promise.all([this.host.call('admin:listClients'), this.host.call('common:getNodesLatestStatus')]);
    return (Array.isArray(raw) ? raw : Object.values(raw ?? {})).map((n: any) => ({ uuid: n.uuid, name: n.name || n.uuid,
      os: String(n.os ?? ''), arch: String(n.arch ?? ''), version: String(n.version ?? ''), online: latest?.[n.uuid]?.online === true }));
  }
  async saveJobs(inputs: Partial<Job>[]) {
    if (!Array.isArray(inputs) || !inputs.length || inputs.length > 128) throw new Error('请选择 1 到 128 台服务器');
    const nodes = await this.nodes();
    const updates = inputs.map(input => {
      if (!nodes.some(n => n.uuid === input.nodeId)) throw new Error('所选服务器不存在');
      const old = input.id ? this.state.jobs.find(j => j.id === input.id) : undefined;
      if (input.id && !old) throw new Error('任务不存在');
      return validate.job(input, old?.id ?? this.host.id(), this.host.now(), old);
    });
    if (this.state.jobs.length + updates.filter(j => !this.state.jobs.some(old => old.id === j.id)).length > 1000) throw new Error('最多保存 1000 个备份任务');
    for (const j of updates) { const index = this.state.jobs.findIndex(old => old.id === j.id); if (index < 0) this.state.jobs.push(j); else this.state.jobs[index] = j; }
    this.persist(); return updates;
  }
  deleteJob(id: string) {
    if (!this.state.jobs.some(j => j.id === id)) throw new Error('任务不存在');
    this.state.jobs = this.state.jobs.filter(j => j.id !== id);
    for (const r of this.state.runs) if (r.jobId === id && r.status === 'queued') this.finish(r, { status: 'failed', stage: '任务已删除', error: '任务已删除，未开始备份', runId: r.id });
    this.persist();
  }
  enqueue(jobId: string, mode: 'backup' | 'test' = 'backup', scheduledAt?: number): Run {
    const j = this.state.jobs.find(j => j.id === jobId);
    if (!j) throw new Error('任务不存在');
    const s = this.state.settings;
    if (!s.panelUrl || !s.webdavUrl || !s.password) throw new Error('请先保存公用存储配置');
    const active = this.state.runs.find(r => r.jobId === jobId && !terminal(r.status));
    if (active) return active;
    const r: Run = { id: this.host.id(), jobId, nodeId: j.nodeId, jobName: j.name, mode, status: 'queued', stage: '等待服务器可用',
      createdAt: this.iso(), updatedAt: this.iso(), warnings: [], notify: j.notify, scheduledAt };
    this.state.runs.push(r);
    this.trim(); this.persist(); return r;
  }
  private trim() {
    const done = this.state.runs.filter(r => terminal(r.status)).slice(-500);
    this.state.runs = [...this.state.runs.filter(r => !terminal(r.status)), ...done].sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  }
  private config(j: Job, r: Run): RunnerConfig {
    const s = this.state.settings;
    return { protocol: 1, mode: r.mode, runId: r.id, jobId: j.id, nodeId: j.nodeId, sources: [...j.sources], excludes: [...j.excludes],
      remoteDir: [s.baseDir, j.remoteDir, j.nodeId, j.id].filter(Boolean).join('/'), retentionDays: j.retentionDays,
      webdavUrl: s.webdavUrl, username: s.username, password: s.password, timeoutMinutes: s.timeoutMinutes,
      reportUrl: s.panelUrl + RUNNER_API + '/report' };
  }
  async tick() {
    if (this.ticking) return;
    this.ticking = true;
    try {
      const now = this.host.now();
      for (const j of this.state.jobs.filter(j => j.enabled)) {
        const slot = validate.latestSlot(j.schedule, now);
        const active = this.state.runs.find(r => r.jobId === j.id && !terminal(r.status));
        if (slot > j.cursor && !active) { this.enqueue(j.id, 'backup', slot); j.cursor = slot; }
        else if (slot > j.cursor && active?.status === 'queued' && active.scheduledAt !== undefined) { active.scheduledAt = slot; j.cursor = slot; }
      }
      for (const r of this.state.runs.filter(r => !terminal(r.status) && r.status !== 'queued')) await this.reconcile(r);
      let count = this.state.runs.filter(r => !terminal(r.status) && r.status !== 'queued').length;
      const busy = new Set(this.state.runs.filter(r => !terminal(r.status) && r.status !== 'queued').map(r => r.nodeId));
      const queued = this.state.runs.filter(r => r.status === 'queued');
      if (queued.length && count < this.state.settings.concurrency) {
        const nodes = await this.nodes();
        for (const r of queued) {
          if (count >= this.state.settings.concurrency) break;
          const node = nodes.find(n => n.uuid === r.nodeId);
          if (!node) { this.finish(r, { runId: r.id, status: 'failed', stage: '服务器不存在', error: '服务器已从 Komari 删除' }); continue; }
          if (!node.online || busy.has(r.nodeId)) continue;
          if (node.os && /windows|darwin|freebsd/i.test(node.os)) { this.finish(r, { runId: r.id, status: 'failed', stage: '环境不支持', error: '首版仅支持 Linux' }); continue; }
          busy.add(r.nodeId); count++; await this.dispatch(r);
        }
      }
      for (const r of this.state.runs.filter(r => r.status === 'pending' && now - Date.parse(r.updatedAt) > 5 * 60_000 && now - (r.lastRecoveryAt ?? 0) > 5 * 60_000)) {
        const nodes = await this.nodes();
        if (nodes.some(n => n.uuid === r.nodeId && n.online)) { try { await this.recover(r.id); } catch { /* 离线和暂时无法核对时继续等待。 */ } }
      }
      this.persist();
      for (const r of this.state.runs.filter(r => terminal(r.status) && r.notify && !r.notified)) await this.notify(r);
    } finally { this.ticking = false; }
  }
  private async dispatch(r: Run) {
    const j = this.state.jobs.find(j => j.id === r.jobId);
    if (!j) { this.finish(r, { runId: r.id, status: 'failed', stage: '任务不存在' }); return; }
    const grant = this.host.token();
    r.config = this.config(j, r); r.startupHash = this.host.hash(grant); r.startupExpires = this.host.now() + 5 * 60_000;
    r.status = 'dispatching'; r.stage = '部署执行器并检查环境'; r.updatedAt = this.iso(); this.persist();
    try {
      const result = await this.host.call('admin:exec', { clients: [r.nodeId], command: bootstrap(this.state.settings.panelUrl, r.id, grant, this.host.binaries) });
      r.taskId = result.task_id;
    } catch (e) {
      // RPC 出错可能发生在命令已送达之后，必须核对，不能直接重新派发。
      r.status = 'pending'; r.stage = '派发结果待确认'; r.error = this.redact(e instanceof Error ? e.message : e);
    }
    this.persist();
  }
  authorize(runId: string, token: string, report = false): Run {
    const r = this.state.runs.find(r => r.id === runId);
    const expected = report ? r?.reportHash : r?.startupHash;
    const expires = report ? r?.reportExpires : r?.startupExpires;
    if (!r || !expected || !token || this.host.hash(token) !== expected || !expires || this.host.now() > expires || (!report && r.claimed)) throw new Error('授权无效或已过期');
    return r;
  }
  authorizeDownload(runId: string, token: string): Run {
    try { return this.authorize(runId, token); } catch {
      const r = this.state.runs.find(r => r.id === runId);
      if (r?.recoveryHash && this.host.hash(token) === r.recoveryHash && this.host.now() < (r.recoveryExpires ?? 0)) return r;
      throw new Error('授权无效或已过期');
    }
  }
  claim(runId: string, token: string) {
    const r = this.authorize(runId, token);
    if (!r.config || terminal(r.status)) throw new Error('任务已结束');
    const reportToken = this.host.token();
    r.claimed = true; r.reportHash = this.host.hash(reportToken); r.reportExpires = this.host.now() + (r.config.timeoutMinutes + 7 * 1440) * 60_000;
    r.status = 'running'; r.stage = '检查备份来源'; r.updatedAt = this.iso(); r.startedAt = this.iso(); this.persist();
    return { ...r.config, reportToken };
  }
  report(report: Report, token: string) {
    const r = this.authorize(report.runId, token, true);
    if (terminal(r.status)) return this.publicRun(r);
    if (!['running', 'success', 'warning', 'failed'].includes(report.status)) throw new Error('执行状态不正确');
    if (typeof report.stage !== 'string' || report.stage.length > 4096 || (report.bytes !== undefined && (!Number.isSafeInteger(report.bytes) || report.bytes < 0)) ||
      (report.sha256 !== undefined && !/^[a-f0-9]{64}$/.test(report.sha256)) || (report.warnings !== undefined && (!Array.isArray(report.warnings) || report.warnings.some(x => typeof x !== 'string')))) throw new Error('执行结果格式不正确');
    if (terminal(report.status)) this.finish(r, report);
    else { r.status = 'running'; r.stage = this.redact(report.stage); r.updatedAt = this.iso(); }
    this.persist(); return this.publicRun(r);
  }
  private finish(r: Run, data: Report) {
    r.status = data.status; r.stage = this.redact(data.stage); r.updatedAt = this.iso(); r.finishedAt = this.iso();
    r.bytes = data.bytes; r.sha256 = data.sha256; r.remotePath = this.redact(data.remotePath);
    r.warnings = (data.warnings ?? []).slice(0, 100).map(x => this.redact(x)); r.error = data.error ? this.redact(data.error) : undefined;
    const j = this.state.jobs.find(j => j.id === r.jobId);
    if (j && r.config && ['success', 'warning'].includes(r.status) && JSON.stringify(r.config) === JSON.stringify(this.config(j, r))) j.lastTestAt = this.iso();
    delete r.config; delete r.startupHash; delete r.recoveryHash;
  }
  private async reconcile(r: Run) {
    if (r.taskId) {
      try {
        const result = await this.host.call('admin:getSpecificTaskResult', { task_id: r.taskId, uuid: r.nodeId });
        if (result?.exit_code !== null && result?.exit_code !== undefined) {
          const raw = String(result.result ?? ''), line = raw.split('\n').find(x => x.startsWith('KWB_RESULT:'));
          if (line) {
            const report = JSON.parse(line.slice('KWB_RESULT:'.length));
            if (report.runId === r.id && terminal(report.status)) { this.finish(r, report); return; }
            if (report.runId === r.id && report.status === 'running') { r.status = 'running'; r.stage = this.redact(report.stage); r.updatedAt = this.iso(); delete r.taskId; return; }
          }
          if (result.exit_code !== 0 && !r.claimed) { this.finish(r, { runId: r.id, status: 'failed', stage: '执行器未启动', error: this.redact(raw) || '请检查节点远程控制、下载工具及目录权限' }); return; }
          r.status = 'pending'; r.stage = '等待核对节点执行结果';
        }
      } catch { /* 查询失败不代表远端备份失败。 */ }
    }
    if (this.host.now() - Date.parse(r.updatedAt) > 5 * 60_000) { r.status = 'pending'; r.stage = '等待核对节点执行结果'; }
  }
  async recover(runId: string) {
    const r = this.state.runs.find(r => r.id === runId);
    if (!r || terminal(r.status) || r.status === 'queued') throw new Error('此记录无需核对');
    if (this.recovering.has(runId)) return this.publicRun(r);
    this.recovering.add(runId);
    try {
      const token = this.host.token();
      r.recoveryHash = this.host.hash(token); r.recoveryExpires = this.host.now() + 5 * 60_000; r.lastRecoveryAt = this.host.now(); this.persist();
      // 恢复授权只允许下载执行器，不能再次领取 WebDAV 配置。
      const result = await this.host.call('admin:exec', { clients: [r.nodeId], command: bootstrap(this.state.settings.panelUrl, r.id, token, this.host.binaries, true) });
      if (!terminal(r.status)) { r.taskId = result.task_id; r.stage = '正在核对节点结果'; r.updatedAt = this.iso(); this.persist(); }
      return this.publicRun(r);
    } finally { this.recovering.delete(runId); }
  }
  private async notify(r: Run) {
    r.notified = true; this.persist();
    try {
      await this.host.call('admin:sendNotification', { event: { event: r.mode === 'test' ? '备份连接测试' : 'WebDAV 备份', clients: [{ uuid: r.nodeId }],
        message: `${r.jobName}：${r.status === 'success' ? '完成' : r.status === 'warning' ? '完成，有警告' : '失败'}\n${r.error || r.remotePath || r.stage}${r.warnings.length ? '\n' + r.warnings.join('\n') : ''}`, time: r.finishedAt } });
    } catch (e) { r.notificationError = this.redact(e instanceof Error ? e.message : e); }
    this.persist();
  }
}
export { defaults };
