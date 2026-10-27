export {};
const API = '/api/admin/webdav-backup';
type Item = Record<string, any>;
const $ = <T extends HTMLElement = HTMLElement>(selector: string) => document.querySelector<T>(selector)!;
const state: { data?: Item; nodes: Item[]; editId?: string; refreshing: boolean } = { nodes: [], refreshing: false };
const labels: Record<string, string> = { queued: '等待执行', dispatching: '正在启动', running: '执行中', pending: '结果待确认', success: '成功', warning: '有警告', failed: '失败' };
const settingsForm = $<HTMLFormElement>('#settings-form'), jobForm = $<HTMLFormElement>('#job-form');
function field(form: HTMLFormElement, name: string) { return form.elements.namedItem(name) as HTMLInputElement; }
function el(tag: string, text = '', className = '') { const node = document.createElement(tag); node.textContent = text; node.className = className; return node; }
function message(text: string, error = false) { const node = $('#message'); node.textContent = text; node.className = 'message'+(error ? ' error' : ''); node.hidden = false; }
async function request(path: string, body?: unknown): Promise<any> {
  const response = await fetch(API+path, { method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin', headers: body === undefined ? {} : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await response.json();
  if (!response.ok || !data.ok) throw new Error(data.error || '请求未完成，请稍后重试');
  return data.data;
}
async function action(button: HTMLButtonElement, fn: () => Promise<void>) { button.disabled = true; try { await fn(); } catch (e) { message(e instanceof Error ? e.message : '操作未完成', true); } finally { button.disabled = false; } }
function button(text: string, fn: (button: HTMLButtonElement) => Promise<void> | void, danger = false) { const b = el('button', text, 'link-button'+(danger ? ' danger' : '')) as HTMLButtonElement; b.type = 'button'; b.onclick = () => { void action(b, async () => { await fn(b); }); }; return b; }
function tab(name: string) { document.querySelectorAll<HTMLElement>('[id^="tab-"]').forEach(x => x.hidden = x.id !== 'tab-'+name); document.querySelectorAll<HTMLElement>('[data-tab]').forEach(x => x.classList.toggle('selected', x.dataset.tab === name)); }
function panelDate(value?: string) {
  if (!value) return '—';
  const offset = Number(state.data?.timezoneOffset ?? 0);
  return new Date(Date.parse(value)-offset*60000).toISOString().replace('T', ' ').slice(0, 19);
}
function size(n: number) { if (!n) return '—'; return n >= 1024**3 ? (n/1024**3).toFixed(2)+' GB' : n >= 1024**2 ? (n/1024**2).toFixed(1)+' MB' : (n/1024).toFixed(1)+' KB'; }
function nodeName(id: string) { return state.nodes.find(n => n.uuid === id)?.name || id; }
function render() {
  const data = state.data!;
  $('#stat-jobs').textContent = String(data.jobs.length);
  $('#stat-enabled').textContent = `已启用 ${data.jobs.filter((j: Item) => j.enabled).length} 个定时任务`;
  $('#stat-active').textContent = String(data.runs.filter((r: Item) => ['queued','dispatching','running','pending'].includes(r.status)).length);
  const last = data.runs.find((r: Item) => r.mode === 'backup');
  $('#stat-last').textContent = last ? labels[last.status] : '尚未备份'; $('#stat-last-time').textContent = last ? panelDate(last.createdAt) : '暂无记录';
  const zone = String(data.timezone).match(/GMT[+-]\d{4}(?: \([^)]+\))?/)?.[0] || `UTC 偏移 ${-Number(data.timezoneOffset)} 分钟`;
  $('#timezone').textContent = `执行时间以面板时区为准：${zone}`;
  const jobs = $('#jobs-body'); jobs.replaceChildren();
  $('#jobs-empty').hidden = data.jobs.length > 0;
  for (const j of data.jobs as Item[]) {
    const row = el('tr'), name = el('td', j.name); name.append(el('span', nodeName(j.nodeId), 'sub')); row.append(name);
    const sources = el('td', j.sources[0]); sources.append(el('span', j.sources.length > 1 ? `共 ${j.sources.length} 个来源` : '文件或目录', 'sub')); row.append(sources);
    const when = el('td', (j.schedule.kind === 'daily' ? '每天 ' : '每周'+'日一二三四五六'[j.schedule.weekday]+' ')+j.schedule.time);
    when.append(el('span', j.enabled ? '下次 '+panelDate(j.nextRun) : '保留 '+(j.retentionDays ? j.retentionDays+' 天' : '全部备份'), 'sub')); row.append(when);
    const status = el('td'); status.append(el('span', j.enabled ? '已启用' : '已暂停', 'badge'+(j.enabled ? ' success' : ''))); row.append(status);
    const actions = el('div', '', 'actions');
    actions.append(button('立即备份', async () => { await request('/run', { jobId: j.id }); message('备份已加入队列'); tab('runs'); await refresh(); }),
      button('测试连接', async () => { await request('/run', { jobId: j.id, mode: 'test' }); message('节点连接测试已加入队列'); tab('runs'); await refresh(); }),
      button(j.enabled ? '暂停' : '启用', async () => { await request('/jobs', { jobs: [{ ...j, enabled: !j.enabled }] }); await refresh(); }),
      button('编辑', () => openJob(j)), button('删除', async () => { if (confirm(`删除“${j.name}”？已有远端备份会保留。`)) { await request('/delete-job', { id: j.id }); await refresh(); } }, true));
    const cell = el('td'); cell.append(actions); row.append(cell); jobs.append(row);
  }
  const runs = $('#runs-body'); runs.replaceChildren(); $('#runs-empty').hidden = data.runs.length > 0;
  for (const r of data.runs as Item[]) {
    const row = el('tr'), name = el('td', r.jobName); name.append(el('span', panelDate(r.createdAt), 'sub')); row.append(name, el('td', r.mode === 'test' ? '连接测试' : '文件备份'));
    const result = el('td'); result.append(el('span', labels[r.status] || r.status, 'badge '+r.status)); row.append(result, el('td', r.stage), el('td', size(r.bytes)));
    const cell = el('td'), actions = el('div', '', 'actions'); actions.append(button('详情', () => detail(r)));
    if (r.status === 'pending') actions.append(button('核对状态', async () => { await request('/recover', { runId: r.id }); message('已请求核对节点状态'); await refresh(); }));
    cell.append(actions); row.append(cell); runs.append(row);
  }
}
function detail(r: Item) {
  const dl = el('dl', '', 'detail-grid');
  for (const [label, value] of [['任务',r.jobName],['服务器',nodeName(r.nodeId)],['结果',labels[r.status]],['阶段',r.stage],['加入队列',panelDate(r.createdAt)],['开始时间',panelDate(r.startedAt)],['结束时间',panelDate(r.finishedAt)],['耗时',r.durationSeconds === undefined ? undefined : r.durationSeconds+' 秒'],['归档大小',size(r.bytes)],['远端位置',r.remotePath],['SHA-256',r.sha256],['警告',(r.warnings || []).join('\n')],['失败原因',r.error],['通知结果',r.notificationError ? '发送失败：'+r.notificationError : r.notify ? '使用 Komari 通知设置' : '未开启'],['运行编号',r.id]]) {
    if (!value) continue; dl.append(el('dt', String(label)), el('dd', String(value)));
  }
  $('#detail-content').replaceChildren(dl); $<HTMLDialogElement>('#detail-dialog').showModal();
}
function fillSettings() {
  const settings = state.data!.settings;
  for (const name of ['panelUrl','webdavUrl','username','baseDir','concurrency','timeoutMinutes']) field(settingsForm, name).value = String(settings[name]);
  field(settingsForm, 'password').value = '';
  $('#password-hint').textContent = settings.passwordSet ? '已保存密码，留空保留原密码。' : '首次配置时填写密码。';
}
function openJob(job?: Item) {
  state.editId = job?.id; jobForm.reset();
  $('#job-title').textContent = job ? '编辑备份任务' : '新建备份任务';
  const list = $('#node-list'); list.replaceChildren();
  for (const node of state.nodes) {
    const label = el('label', '', 'node-option'), input = document.createElement('input'); input.type = 'checkbox'; input.name = 'node'; input.value = node.uuid; input.checked = job?.nodeId === node.uuid;
    if (job) input.disabled = node.uuid !== job.nodeId;
    label.append(input, el('span', node.name), el('small', node.online ? '在线' : '离线')); list.append(label);
  }
  if (!state.nodes.length) list.append(el('span', '暂无可选服务器，请先在 Komari 中添加节点。', 'muted'));
  if (job) {
    for (const name of ['name','remoteDir','retentionDays']) field(jobForm, name).value = String(job[name]);
    field(jobForm, 'sources').value = job.sources.join('\n'); field(jobForm, 'excludes').value = job.excludes.join('\n'); field(jobForm, 'notify').checked = job.notify;
    for (const name of ['kind','time','weekday']) field(jobForm, name).value = String(job.schedule[name]);
  }
  $('#weekday-label').hidden = field(jobForm, 'kind').value !== 'weekly';
  $<HTMLDialogElement>('#job-dialog').showModal();
}
async function refresh() {
  if (state.refreshing) return;
  state.refreshing = true;
  try { const [data, nodes] = await Promise.all([request('/state'), request('/nodes')]); state.data = data; state.nodes = nodes; render(); }
  finally { state.refreshing = false; }
}
document.querySelectorAll<HTMLButtonElement>('[data-tab]').forEach(b => b.onclick = () => tab(b.dataset.tab!));
document.querySelectorAll<HTMLButtonElement>('[data-close]').forEach(b => b.onclick = () => $<HTMLDialogElement>('#'+b.dataset.close).close());
$('#new-job').onclick = () => openJob(); $('#empty-settings').onclick = () => tab('settings');
field(jobForm, 'kind').onchange = () => { $('#weekday-label').hidden = field(jobForm, 'kind').value !== 'weekly'; };
$('#refresh').onclick = () => { void action($<HTMLButtonElement>('#refresh'), refresh); };
settingsForm.onsubmit = event => { event.preventDefault(); void action(settingsForm.querySelector('button[type=submit]')!, async () => {
  const data = Object.fromEntries(new FormData(settingsForm)); data.concurrency = String(Number(data.concurrency)); data.timeoutMinutes = String(Number(data.timeoutMinutes));
  await request('/settings', data); message('公用存储配置已保存，请测试连接'); await refresh(); fillSettings();
}); };
$('#test-storage').onclick = () => { void action($<HTMLButtonElement>('#test-storage'), async () => { const result = await request('/test-storage', {}); message(result.message); }); };
jobForm.onsubmit = event => { event.preventDefault(); void action(jobForm.querySelector('button[type=submit]')!, async () => {
  const selected = [...jobForm.querySelectorAll<HTMLInputElement>('input[name=node]:checked')].map(n => n.value);
  if (!selected.length) throw new Error('请至少选择一台服务器');
  const old = state.data!.jobs.find((j: Item) => j.id === state.editId);
  const lines = (name: string) => field(jobForm, name).value.split('\n').map(x => x.trim()).filter(Boolean);
  await request('/jobs', { jobs: selected.map(nodeId => ({ id: state.editId, nodeId, name: field(jobForm,'name').value, sources: lines('sources'), excludes: lines('excludes'), remoteDir: field(jobForm,'remoteDir').value,
    schedule: { kind: field(jobForm,'kind').value, time: field(jobForm,'time').value, weekday: Number(field(jobForm,'weekday').value) }, retentionDays: Number(field(jobForm,'retentionDays').value), notify: field(jobForm,'notify').checked, enabled: old?.enabled ?? false })) });
  $<HTMLDialogElement>('#job-dialog').close(); message('备份任务已保存'); await refresh();
}); };
void refresh().then(fillSettings).catch(e => message(e.message, true));
setInterval(() => { if (!document.hidden) void refresh().catch(() => message('暂时无法读取备份状态，稍后会自动重试', true)); }, 15000);
