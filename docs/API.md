# 插件接口与数据

版本 `1`。所有 JSON 文本使用 UTF-8。管理接口返回 `{ "ok": true, "data": ... }`，失败为 `{ "ok": false, "error": "..." }`。管理接口要求 Komari 管理员身份；修改接口拒绝跨站浏览器请求。

## 管理接口

基础路径：`/api/admin/webdav-backup`。

| 方法 | 路径 | 请求 / 结果 |
| --- | --- | --- |
| GET | `/state` | 公用设置（密码脱敏）、任务、最近 200 条记录、面板时间和时区偏移 |
| GET | `/nodes` | 节点 UUID、名称、在线状态和可用的环境信息；不返回 Agent token |
| POST | `/settings` | `panelUrl, webdavUrl, username, password, baseDir, concurrency, timeoutMinutes`；空密码保留原值 |
| POST | `/test-storage` | 空对象；检查面板到 WebDAV 的读写、列举和删除 |
| POST | `/jobs` | `{jobs: JobInput[]}`；批量保存，更新时带 `id` |
| POST | `/delete-job` | `{id}`；不删除远端数据 |
| POST | `/run` | `{jobId, mode?: "backup" | "test"}`；相同任务在途时返回原记录 |
| POST | `/recover` | `{runId}`；下发只读状态核对命令 |

`JobInput`：`name, nodeId, sources: string[], excludes: string[], remoteDir, schedule: {kind: "daily" | "weekly", time: "HH:mm", weekday: 0..6}, retentionDays, notify, enabled`。

执行状态：`queued → dispatching → running → success | warning | failed`。派发或回传不明确时进入 `pending`；通过状态核对进入运行中或终态。待确认状态占用该节点及并发槽，不允许重复执行。终态历史保留 30 天、最多 500 条，前端展示最近 200 条；在途记录始终保留。错误、警告和通知错误合计限制为 16 KiB UTF-8 文本，超出部分标注截断。

## 执行器接口

基础路径：`/api/clients/webdav-backup`。这些路由不依赖浏览器登录，必须携带各自授权头。选择此路径是为了兼容 Komari 私有站点模式；不会继承其他客户端接口的角色权限。

- `GET /binary?run=<ID>&arch=amd64|arm64`：`Authorization: Bearer <startup-or-recovery-grant>`；返回附带执行器。引导命令另外核对构建摘要。
- `POST /claim`：`Authorization: Bearer <startup-grant>`，正文 `{runId}`；返回原始执行器配置，包含本次凭据和独立 `reportToken`。5 分钟内只能领取一次。
- `POST /report`：`Authorization: Bearer <reportToken>`，正文 `{runId,status,stage,bytes?,sha256?,remotePath?,warnings?,error?}`；只可更新其对应运行。回报授权有效期为任务超时加 7 天，终态回报幂等。

`reportToken` 与状态核对下载授权不能领取 WebDAV 密码。授权只在特定运行和过期时间内有效，不具备 Komari 管理员权限。

运行进度每 15 秒回报。终态同时写入节点本地 `results/<runId>.json` 并以单行 `KWB_RESULT:<JSON>` 输出供 Komari 任务结果核对。文件不保存 WebDAV 或回报授权。

相同阶段的运行心跳只刷新面板内存时间，不触发状态文件写入；阶段变化及终态及时持久化。接口及协议版本保持为 `1`。

## 远端完成清单

`manifest.json` 包含 `owner: "komari-webdav-backup/v1"`、`version: 1`、`nodeId`、`jobId`、`runId`、`completedAt`、`archive: "backup.tar.gz"`、`size`、`sha256`、`sources`、`warnings`。清理同时验证所有权和三层 ID，并严格检查运行目录仅含已知文件；先删除过期归档，再删除仅剩清单的运行目录，最后确认目录不存在。未知内容或不完整列举导致清理警告，不执行集合删除。

清单 SHA-256 用于下载后校验。普通 WebDAV HEAD 只能核对大小，不代表服务端已经重新计算摘要；严格完整性需要下载归档核对。
