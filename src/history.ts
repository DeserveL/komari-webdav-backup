import { Run } from './types';

export const HISTORY_DAYS = 30;
export const HISTORY_LIMIT = 500;
const DIAGNOSTIC_BYTES = 16 * 1024;
const marker = '…[已截断]';

// 按 UTF-8 字节截断，避免中文被截成无效字符；优先保留错误原因。
export function limitDiagnostics(run: Pick<Run, 'error' | 'warnings' | 'notificationError' | 'requestDiagnostics'>) {
  const warnings = (run.warnings ?? []).slice(0, 100);
  let requestBytes = 4096;
  const requests = (run.requestDiagnostics ?? []).slice(-16).map(value => {
    const bytes = Buffer.from(value, 'utf8');
    if (bytes.length <= requestBytes) { requestBytes -= bytes.length; return value; }
    const suffix = Buffer.from(marker, 'utf8').length;
    if (requestBytes < suffix) return '';
    let end = requestBytes - suffix;
    while (end > 0 && (bytes[end] & 0xc0) === 0x80) end--;
    requestBytes = 0; return bytes.subarray(0, end).toString('utf8') + marker;
  }).filter(Boolean);
  const values = [run.error || '', run.notificationError || '', ...warnings, ...requests];
  const truncated = values.reduce((n, value) => n + Buffer.from(value, 'utf8').length, 0) > DIAGNOSTIC_BYTES;
  let remaining = DIAGNOSTIC_BYTES - (truncated ? Buffer.from(marker, 'utf8').length : 0);
  const take = (value: string): string => {
    const bytes = Buffer.from(value, 'utf8');
    if (bytes.length <= remaining) { remaining -= bytes.length; return value; }
    let end = remaining;
    while (end > 0 && (bytes[end] & 0xc0) === 0x80) end--;
    const text = bytes.subarray(0, end).toString('utf8');
    remaining = 0;
    return text;
  };
  const limited = values.map(take);
  if (truncated) {
    let last = limited.length - 1;
    while (last > 0 && !limited[last]) last--;
    limited[last] += marker;
  }
  run.error = limited[0] || undefined;
  run.notificationError = limited[1] || undefined;
  run.warnings = limited.slice(2, 2 + warnings.length).filter(Boolean);
  const diagnostics = limited.slice(2 + warnings.length).filter(Boolean);
  if (run.requestDiagnostics !== undefined) run.requestDiagnostics = diagnostics;
}
