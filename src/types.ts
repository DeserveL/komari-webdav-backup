export const API = '/api/admin/webdav-backup';
export const RUNNER_API = '/api/clients/webdav-backup';
export type Status = 'queued' | 'dispatching' | 'running' | 'pending' | 'success' | 'warning' | 'failed';
export type Schedule = { kind: 'daily' | 'weekly'; time: string; weekday: number };
export type Settings = {
  panelUrl: string; webdavUrl: string; username: string; password: string; baseDir: string;
  concurrency: number; timeoutMinutes: number;
};
export type Job = {
  id: string; nodeId: string; name: string; sources: string[]; excludes: string[]; taskDir: string;
  schedule: Schedule; retentionDays: number; notify: boolean; enabled: boolean;
  createdAt: string; cursor: number; lastTestAt?: string;
};
export type Run = {
  id: string; jobId: string; nodeId: string; jobName: string; mode: 'backup' | 'test';
  status: Status; stage: string; createdAt: string; updatedAt: string; finishedAt?: string;
  startedAt?: string;
  filePrefix?: string;
  taskId?: string; startupHash?: string; startupExpires?: number; claimed?: boolean;
  reportHash?: string; reportExpires?: number; config?: RunnerConfig;
  bytes?: number; sha256?: string; remotePath?: string; manifestPath?: string; warnings: string[]; error?: string;
  requestDiagnostics?: string[];
  notified?: boolean; notificationError?: string; scheduledAt?: number;
  notify?: boolean;
  recoveryHash?: string; recoveryExpires?: number; lastRecoveryAt?: number;
};
export type RunnerConfig = {
  protocol: 2; mode: 'backup' | 'test'; runId: string; jobId: string; nodeId: string; filePrefix: string;
  sources: string[]; excludes: string[]; remoteDir: string; retentionDays: number;
  webdavUrl: string; username: string; password: string; timeoutMinutes: number;
  reportUrl?: string; reportToken?: string;
};
export type State = { version: 1 | 2; settings: Settings; jobs: Job[]; runs: Run[] };
export type NodeInfo = { uuid: string; name: string; os: string; arch: string; online: boolean; version: string };
export type Report = { runId: string; status: Status; stage: string; bytes?: number; sha256?: string; remotePath?: string; manifestPath?: string; warnings?: string[]; error?: string; requestDiagnostics?: string[] };
export const terminal = (status: Status) => ['success', 'warning', 'failed'].includes(status);
export const defaults = (): State => ({ version: 2, settings: {
  panelUrl: '', webdavUrl: '', username: '', password: '', baseDir: 'komari-backups', concurrency: 2, timeoutMinutes: 120,
}, jobs: [], runs: [] });
