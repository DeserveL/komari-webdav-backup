# 验证记录

## 0.2.1 慢上传与代理容错

日期：2026-10-28。环境：中文 Windows、Node.js 24.19.0、Go 1.27.3、官方 Komari 1.6.1 / MoeJS 隔离实例。

- TypeScript 严格检查和 30 项插件测试通过，覆盖请求诊断脱敏、4 KiB / 16 条及 16 KiB 总限制、警告通知和重复心跳不写盘。
- Go 测试及 `go vet` 通过，实际等待 61 秒模拟上传后慢响应，另测请求体发送时间不计入响应头等待；明确未建立连接时可重试，可能送达的上传不重复 PUT。
- 隔离 WebDAV 覆盖归档落盘但返回 502 / 524、客户端超时后后端继续持锁、两次连续大小核对及中间异常重置、大小缺失、403 / 413 / 423 / 412、取消及 Retry-After。
- 清单丢失响应后逐项核对；缺失或不一致时失败并保留归档。不 GET 远端归档、不重传或删除未确认写入，大小恢复路径保留警告并跳过旧备份清理。相同大小但内容不同的测试也只显示大小核对警告，不声称远端摘要正确。
- 从实际下载且校验摘要的 0.2.0 包覆盖升级，官方运行时 19 项检查通过：凭据、密钥、任务、历史、固定目录、启停和测试批准状态保留；真实路由接收新诊断字段。连续至少 90 秒空闲，状态文件内容、mtime、inode 不变。
- 360px / 1440px、明暗主题的详情诊断区域无横向溢出，关闭操作正常。截图：[桌面暗色](upload-details-1440-dark.png)、[手机暗色](upload-details-360-dark.png)，均使用本地示例数据。

Linux 竞态检测、双架构构建和运行时检查以本版 [CI](https://github.com/DeserveL/komari-webdav-backup/actions/workflows/ci.yml) 与 [Release](https://github.com/DeserveL/komari-webdav-backup/actions/workflows/release.yml) 为准。本轮未连接生产 WebDAV、Cloudflare 或真实 Linux Agent；模拟代理错误不等同于真实 Cloudflare 联调，也不作为网盘持久化或真实恢复验收。

## 0.2.0 单层任务目录

日期：2026-10-28。环境：中文 Windows、Node.js 24.19.0、Go 1.27.3，以及官方 Komari 1.6.1 / MoeJS 隔离实例。

- TypeScript 严格检查及 29 项插件测试通过，覆盖批量任务、同名目录扩展、中文和危险字符、UTF-8 字节限制、面板时间偏移、固定目录及配置升级。
- Go 测试和 `go vet` 通过，覆盖多次备份平铺、目录归属检查、短名冲突回退与完整名冲突拒绝、条件上传不覆盖、实际 HTTP 下载后摘要与解压内容比较。
- 保留清理测试覆盖文件对、未知内容与跨任务隔离、危险清单路径、文件删除 202 / 207 / 403、失败后保留清单及后续重试；不执行集合删除。
- 本地官方 Komari 19 项检查通过，从 GitHub 实际下载的 0.1.2 插件升级，凭据、任务及历史链接保留；目录重新生成、旧任务暂停、测试批准清除，协议为 2。
- 新版实际路由验证阶段心跳、UTF-8 诊断与新任务保存位置；连续至少 90 秒，状态文件内容、mtime、inode 均不变。
- 浏览器验证 360px 和 1440px、明暗主题下的创建、保存位置预览、批量保存、改名时路径固定、复制成功提示及恢复说明。手机详情值列宽约 169px，长文件 URL 和摘要无横向溢出。

新版截图：[批量创建桌面暗色](flat-create-1440-dark.png)、[批量创建手机明色](flat-create-360-light.png)、[手机暗色详情](flat-details-360-dark.png)。截图及预览使用本地示例数据。

本轮不连接生产节点或生产 WebDAV。更多 WebDAV 服务对条件上传的兼容性、真实生产恢复与 arm64 硬件仍待部署验收。Linux 的竞态检测、运行锁、双架构及官方运行时结果以本次 [CI](https://github.com/DeserveL/komari-webdav-backup/actions/workflows/ci.yml) 和 [Release](https://github.com/DeserveL/komari-webdav-backup/actions/workflows/release.yml) 为准。

## 0.1.2 轻量优化

日期：2026-10-27。环境：中文 Windows，Node.js 24.19.0、Go 1.27.3，官方 Komari 1.6.1 / MoeJS 隔离实例。

- TypeScript 严格检查和 24 项插件测试通过，覆盖空闲与重复心跳不保存、阶段和终态保存、保存失败重试、历史数量与年龄边界、非终态保护和 UTF-8 诊断限制。
- Go 测试和 `go vet` 通过；模拟 WebDAV 实际执行文件及目录删除，检查未知文件保护、删除后再次列举、异常 XML、跨任务隔离、202 / 207 / 403 处理和失败后重试。
- 节点维护测试覆盖 30 天、500 条、孤立锁、当前记录保护、执行器缓存和重复维护；Linux CI 另执行真实 flock 保护与竞态检测。
- 本地官方 Komari 19 项检查通过：从 0.1.1 ZIP 覆盖升级，任务、公用凭据、保留期内历史、密钥和暂停状态保留；新版本实际路由验证重复心跳不写盘及中文诊断截断。
- 在升级后的官方实例中连续观察至少 90 秒，跨过至少六次真实调度，并持续读取管理状态；`state.enc.json` 的字节内容、修改时间和 inode 均不变。
- 插件仍按需启动执行器，不新增常驻服务、存储依赖或配置项；API、执行器协议、加密状态及归档格式保持兼容。

本轮不连接生产节点或生产 WebDAV，不修改线上插件和任务。真实 WebDAV 的目录删除兼容性、生产恢复验收、性能峰值及 arm64 硬件运行仍需实际部署验证。Linux 结果以本次提交的 [CI](https://github.com/DeserveL/komari-webdav-backup/actions/workflows/ci.yml) 和 [Release](https://github.com/DeserveL/komari-webdav-backup/actions/workflows/release.yml) 为准，不将本地 Windows 测试当作 Linux 验收。

## GitHub 开源发布验证

日期：2026-10-27。版本：0.1.1。

- [Linux CI](https://github.com/DeserveL/komari-webdav-backup/actions/runs/40433493585) 已通过：TypeScript 严格检查、19 项插件测试、`go test -race -count=1 ./...`、`go vet ./...`、Linux amd64 / arm64 打包，以及官方 Komari 1.6.1 运行时检查。
- 本地真实 Komari 的 17 项检查通过，包含旧版 0.1.0 ZIP 覆盖升级；GitHub Linux 流程同时检查当前版本重新安装。
- 发布源码仅包含项目文件和示例截图，排除构建缓存、运行配置与密钥；提交使用 GitHub noreply 邮箱。
- 真实环境的下载恢复、长期断网、arm64 硬件运行及更多 WebDAV 服务仍需部署验收。CI 和模拟服务检查不替代这些验收。

后续提交和版本发布以 [Actions](https://github.com/DeserveL/komari-webdav-backup/actions) 与 [Releases](https://github.com/DeserveL/komari-webdav-backup/releases) 中的结果为准。

## 0.1.1 布局修复与覆盖安装

日期：2026-10-26。构建环境沿用下方首版环境。本次只修复详情网格和版本展示，执行器、API 数据结构、状态格式及远端目录规则保持一致。

- `npm test`：19 项插件测试和 TypeScript 严格检查通过。
- 浏览器完成以下六组检查，详情弹窗及所有值列均无横向溢出，普通任务名称保持一行，64 位摘要和长地址自动换行，多行警告保留换行。

| 视口宽度 | 主题 | 标签列 | 值列实际宽度 |
| --- | --- | --- | --- |
| 360px | 明 / 暗 | 80px | 169px |
| 768px | 明 / 暗 | 80px | 约 579.9px |
| 1440px | 明 / 暗 | 100px | 578px |

- 另外检查失败原因、缺少摘要和远端地址时的显示，以及详情关闭、任务列表、编辑弹窗和取消操作。
- 本地官方 Komari 1.6.1：17 项运行时检查通过，包含通过实际分片上传和合并安装接口将 0.1.0 升级为 0.1.1；任务、公用凭据、加密密钥、历史记录及暂停状态全部保留。
- 通过用户提供的测试结果及前一轮只读排查，确认已有一次真实 Linux 节点约 31.5 MiB 备份成功记录，无警告。额外读取校验返回 HTTP 403，尚未完成该归档的下载摘要与恢复验收。
- 本次未更新线上插件，也未启用任何线上定时任务。

截图使用本地示例数据：[桌面暗色详情](details-1440-dark.png)、[手机明色详情](details-360-light.png)、[手机失败详情](details-failure-360-dark.png)。其他宽度和主题截图位于同一目录。

复现覆盖安装检查时，在构建后设置 `KOMARI_EXE` 为官方 Komari 二进制路径、`KOMARI_PREVIOUS_PACKAGE` 为旧版 0.1.0 ZIP 的本地路径，再运行 `node scripts/smoke-komari.mjs`。未提供旧版包时验证当前版本重新安装。

## 0.1.0 历史验证

日期：2026-10-26。版本：0.1.0。构建环境：中文 Windows，Node.js 24.19.0、Go 1.27.3。

## 已完成

| 检查 | 结果 |
| --- | --- |
| TypeScript 严格类型检查 | 通过 |
| 插件逻辑及入口测试 | 19 项通过 |
| Go 执行器测试 | 15 个顶层用例，14 项通过，1 项因 Windows 符号链接创建权限跳过；另含上传失败子场景 |
| Go 格式和静态检查 | `gofmt`、`go vet ./...` 通过 |
| 首次部署脚本 | Git Bash `bash -n` 语法检查通过 |
| Linux amd64 / arm64 静态执行器 | 交叉编译通过 |
| 插件 ZIP / 源码 ZIP | 根目录结构、逐文件 CRC、执行器摘要与敏感文件排除检查 |
| 管理页面 | 浏览器验证页面布局、每周计划切换、中文多路径录入及任务保存；见 dashboard.png |

Go 自动测试使用本机模拟 WebDAV 服务，实际执行了归档、上传、清单、过期清理、下载、摘要比较和读取归档内容。覆盖单文件、中文路径、排除规则、重复来源、认证失败、取消、上传失败、大小不一致、暂时错误重试、远端所有权隔离、最近成功备份保护、XML 命名空间及危险 href 过滤。

## 官方 Komari 1.6.1 运行时检查

使用官方 Windows amd64 二进制启动脚本自己创建的隔离实例，16 项检查通过：

1. 真实 MoeJS 加载插件。
2. 公用配置和 AES-GCM 加密写入。
3. 真实节点 RPC 与批量任务保存。
4. 节点在线状态 RPC。
5. 离线排队和重复操作合并。
6. Node.js 成功认证、解密 MoeJS 产生的状态文件。
7. 连续三次重载后任务、凭据和队列保持一致，读取接口脱敏。
8. 跨站修改被拒绝。
9. 私有站点下节点接口可达，并校验短效授权。
10. 未登录请求不能读取管理数据。
11. 完整重启服务后配置和队列恢复。
12. 实际 `admin:exec` 派发到虚拟 v2 Agent，命令不包含 WebDAV 密码。
13. 实际二进制下载路由的 SHA-256 与构建清单一致。
14. 实际配置授权只能领取一次。
15. 实际结果回报接口接受受限授权且管理输出不暴露密码。
16. Agent 任务结果和 Komari 通知 RPC 的调用参数可用。

重载测试发现 MoeJS 1.6.1 的 `Buffer.concat` 会将字节变为零；插件改用加密接口编码输出，并加入模拟该差异的回归测试。未修改 Komari 本体。

虚拟 Agent 只验证协议，不执行 Linux 引导命令。通知检查没有开启外部通道，未向 Telegram、邮箱或其他第三方发送消息。

## 首版尚未验证的范围

以下保留 0.1.0 阶段的验证边界；当前 Linux CI 情况见本页顶部。

- Linux Agent 的系统性故障验收，包括文件权限、`flock`、空间检查、进程中断及恢复；单次真实备份成功记录补充于上方 0.1.1 验证。
- Linux 符号链接用例和竞态检测；GitHub Actions 已配置 Linux 测试，但本地未运行该工作流。
- arm64 真实硬件运行。
- AList / OpenList、Nextcloud 或用户实际使用的 WebDAV 服务。
- 生产网络中的大文件吞吐、长时间故障和真实通知通道。

因此当前交付为可安装的首版候选，不能把模拟服务或协议检查当作生产部署验收。

## 复现

```sh
npm ci
npm test
cd runner
go test -v ./...
go vet ./...
cd ..
npm run build
# KOMARI_EXE 指向官方 Komari 1.6.1 二进制；脚本要求 Node.js 22+
node scripts/smoke-komari.mjs
```

项目含 GitHub Actions 自动检查和标签发布工作流。最新 Linux 检查见 [CI](https://github.com/DeserveL/komari-webdav-backup/actions/workflows/ci.yml)，可安装产物见 [Releases](https://github.com/DeserveL/komari-webdav-backup/releases)。
