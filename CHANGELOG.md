# 更新记录

## 0.1.1

首次在 GitHub 公开发布，采用 MIT 许可证。

### 功能

- 在 Komari 后台配置公用 WebDAV，并为 Linux 服务器批量创建或单独管理备份任务。
- 支持文件、目录、排除规则、每天或每周执行、手动备份及保留天数配置。
- 节点直接上传归档，提供完成清单和 SHA-256，按服务器与任务隔离清理。
- 复用 Komari 通知，支持进度记录、离线排队和中断结果核对。
- 提供 Linux amd64 / arm64 执行器、插件安装包、源码包和校验文件。

### 修复

- 修复执行记录详情被压成竖行的问题，长路径、摘要和多行提示正常换行。
- 详情页适配 360、768、1440px 视口及明暗主题。

### 兼容与验证

- 保留 0.1.0 的配置、历史、启停状态和远端目录规则，无需迁移。
- 已验证基线：Komari 1.6.1 / MoeJS；Agent 协议基线为 1.5.11。
- GitHub Linux CI 已通过插件测试、Go 竞态检测、静态检查、打包和官方 Komari 运行时检查。
- 已有一次真实 Linux 节点约 31.5 MiB 备份成功记录；下载恢复、长期故障和更多 WebDAV 服务仍需部署验收。

安装请选择附件中的 **`komari-webdav-backup-0.1.1.zip`**，不要将源码归档作为插件安装包。

[使用说明](https://github.com/DeserveL/komari-webdav-backup#安装配置) · [验证记录](https://github.com/DeserveL/komari-webdav-backup/blob/main/docs/TESTING.md)
