# 插件接口与数据

管理接口位于 `/api/admin/webdav-backup`，要求管理员身份；修改请求检查浏览器来源。JSON 使用 UTF-8，返回 `{ok:true,data:...}` 或 `{ok:false,error:...}`。

| 方法 | 路径 | 内容 |
| --- | --- | --- |
| GET | `/state` | 脱敏设置、任务、最近 200 条记录、面板时间和时区 |
| GET | `/nodes` | 节点 UUID、名称、在线状态；不含 Agent token |
| POST | `/settings` | `panelUrl, webdavUrl, username, password, baseDir, concurrency, timeoutMinutes`；空密码保留原值 |
| POST | `/test-storage` | 面板到 WebDAV 的读写、大小、列举和文件删除检查 |
| POST | `/jobs` | `{jobs: JobInput[]}`，多节点分别创建独立任务 |
| POST | `/delete-job` | `{id}`，不删除远端文件 |
| POST | `/run` | `{jobId,mode?:"backup"|"test"}`，在途请求合并 |
| POST | `/recover` | `{runId}`，只核对节点结果 |

`JobInput`：`name,nodeId,sources,excludes,schedule:{kind,time,weekday},retentionDays,notify,enabled`；更新携带 `id`。不再提供用户指定的 `remoteDir`。后端生成并持久化 `taskDir`；`GET /state` 为每个任务附带相对 WebDAV 地址的只读 `savePath`。客户端指定的目录字段不会改变实际目录。

执行状态包括 `queued,dispatching,running,pending,success,warning,failed`。在途与待确认记录保留；终态保留 30 天、最多 500 条。诊断文本合计最多 16 KiB，超出标注截断。相同阶段的心跳只更新内存联系时间，关键变化及时保存。

## 执行器协议 2

节点路径为 `/api/clients/webdav-backup`，须使用独立受限授权：

- `GET /binary?run=<ID>&arch=amd64|arm64`：启动或核对下载授权；另外校验执行器 SHA-256。
- `POST /claim`：启动授权，正文 `{runId}`，5 分钟内只能领取一次。返回 `protocol:2`、完整运行/任务/节点 ID、来源和排除、任务目录 `remoteDir`、文件前缀 `filePrefix`、保留天数、超时、WebDAV 凭据及 `reportUrl,reportToken`。
- `POST /report`：回报授权，正文 `{runId,status,stage,bytes?,sha256?,remotePath?,manifestPath?,warnings?,error?}`。终态幂等，回报授权不能领取配置。

`filePrefix` 来自面板派发时间，格式为 `YYYYMMDD_HHmmss±HHmm_<运行短编号>`。节点短名冲突时使用完整运行 ID。回报中的 `remotePath`、`manifestPath` 为实际文件地址，包括回退后的文件名。进度每 15 秒回报，本地结果不含凭据，并以 `KWB_RESULT:<JSON>` 输出供宿主核对。

## 远端文件与清单 2

每个任务只有一个目录，每次运行保存 `<prefix>.tar.gz` 和 `<prefix>.manifest.json`，不创建运行目录。

清单字段为 `owner:"komari-webdav-backup/v2",version:2,nodeId,jobId,runId,completedAt,archive,size,sha256,sources,warnings`。完整 ID 用于归属验证，`archive` 必须是与清单前缀匹配的安全文件名。保留清理以 `completedAt` 为准，不通过解析文件名时间判断是否过期；只删除确认归属的文件对，不删除集合、未知文件或旧版备份。

状态内部版本升为 2，可从版本 1 升级本地任务信息并暂停旧任务；不兼容扫描旧版远端归档。旧运行历史链接及结果核对保留。

HEAD 只校验远端大小；归档恢复仍须下载并核对 SHA-256、解压和检查文件内容。
