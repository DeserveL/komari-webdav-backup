type Kind = 'progress' | 'success' | 'error';
type Notice = { node: HTMLElement; timer?: number; remaining: number; started: number; hovered: boolean };
const notices = new Map<string, Notice>();
const layer = document.createElement('div');
layer.className = 'feedback-layer';
layer.setAttribute('aria-label', '操作结果');
const popover = typeof layer.showPopover === 'function';
if (popover) layer.setAttribute('popover', 'manual');
document.body.append(layer);

// 原生顶层在新打开的模态框之上重新展示；回退层跟随当前模态框。
function place() {
	const dialogs = [...document.querySelectorAll<HTMLDialogElement>('dialog[open]')];
	const host = dialogs[dialogs.length - 1] || document.body;
  if (popover) {
    if (layer.matches(':popover-open')) layer.hidePopover();
    // 顶层只解决遮挡；挂到当前模态框内才能同时避免其 inert 限制。
    if (layer.parentElement !== host) host.append(layer);
    if (notices.size) layer.showPopover();
  } else {
    if (layer.parentElement !== host) host.append(layer);
    layer.hidden = !notices.size;
  }
}
new MutationObserver(place).observe(document.body, { subtree: true, attributes: true, attributeFilter: ['open'] });

export function clearFeedback(key: string) {
  const notice = notices.get(key);
  if (!notice) return;
  window.clearTimeout(notice.timer);
  notice.node.remove(); notices.delete(key); place();
}

function pause(notice: Notice) {
  if (notice.timer === undefined) return;
  window.clearTimeout(notice.timer); notice.timer = undefined;
  notice.remaining = Math.max(1, notice.remaining - (performance.now() - notice.started));
}
function resume(key: string, notice: Notice) {
  if (notice.remaining <= 0 || notice.hovered || notice.node.contains(document.activeElement) || notice.timer !== undefined) return;
  notice.started = performance.now();
  notice.timer = window.setTimeout(() => clearFeedback(key), notice.remaining);
}

export async function copyText(value: string) {
  if (navigator.clipboard) return navigator.clipboard.writeText(value);
  const input = document.createElement('textarea'); input.value = value;
  const dialogs = [...document.querySelectorAll('dialog[open]')];
  const host = dialogs[dialogs.length - 1] || document.body;
  const focused = document.activeElement as HTMLElement | null;
  input.style.cssText = 'position:fixed;opacity:0;pointer-events:none'; host.append(input); input.select();
  const copied = document.execCommand('copy'); input.remove(); focused?.focus({ preventScroll: true });
  if (!copied) throw new Error('复制未完成，请展开消息后选中文字复制');
}

export function feedback(key: string, text: string, kind: Kind = 'success') {
  const old = notices.get(key);
  // 后台轮询的重复错误不重复播报，也不改变提示顺序。
  if (old?.node.dataset.kind === kind && old.node.dataset.text === text) return;
  const node = document.createElement('article'); node.className = 'feedback '+kind;
  node.dataset.kind = kind; node.dataset.text = text; node.dataset.operation = key;
  node.setAttribute('role', kind === 'error' ? 'alert' : 'status');
  node.setAttribute('aria-atomic', 'true');
  const content = document.createElement('div'); content.className = 'feedback-content';
  const body = document.createElement('div'); body.className = 'feedback-text'; body.textContent = text; content.append(body);
  const controls = document.createElement('div'); controls.className = 'feedback-controls';
  if (text.length > 150 || text.includes('\n')) {
    body.classList.add('collapsed');
    const expand = document.createElement('button'); expand.type = 'button'; expand.textContent = '展开'; expand.setAttribute('aria-expanded', 'false');
    expand.onclick = () => { const expanded = body.classList.toggle('collapsed') === false; expand.textContent = expanded ? '收起' : '展开'; expand.setAttribute('aria-expanded', String(expanded)); };
    controls.append(expand);
  }
  if (kind === 'error') {
    const copy = document.createElement('button'); copy.type = 'button'; copy.textContent = '复制错误';
    copy.onclick = async () => { try { await copyText(text); copy.textContent = '已复制'; } catch { copy.textContent = '请选中文字复制'; body.classList.remove('collapsed'); } };
    controls.append(copy);
  }
  const close = document.createElement('button'); close.type = 'button'; close.className = 'feedback-close'; close.textContent = '×'; close.setAttribute('aria-label', '关闭提示'); close.onclick = () => clearFeedback(key);
  content.append(controls); node.append(content, close);
  const notice: Notice = { node, remaining: kind === 'success' ? 5000 : 0, started: 0, hovered: false };
  node.onmouseenter = () => { notice.hovered = true; pause(notice); };
  node.onmouseleave = () => { notice.hovered = false; resume(key, notice); };
  node.addEventListener('focusin', () => pause(notice));
  node.addEventListener('focusout', () => queueMicrotask(() => resume(key, notice)));
  if (old) { window.clearTimeout(old.timer); old.node.replaceWith(node); } else layer.append(node);
  notices.set(key, notice); place(); resume(key, notice);
}
