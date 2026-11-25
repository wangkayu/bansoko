## 改进

- Bansoko 可直接运行在 Linux x86-64、Linux ARM64 和 Apple 芯片 Mac 上。下载包包含 Web 界面、数据库迁移和 MediaInfo，PostgreSQL 17 与 qBittorrent 仍需单独运行。
- 启动前可用 bansoko-server doctor 检查宿主环境。Linux 的两种架构均支持按需触发应用内更新，macOS 使用手动更新。
- 自定义命名变量 Vars 已退役：命名与通知模板不再支持 vars.*，插件 Webhook 事实包不再包含 vars.*，/api/v1/naming-vars* 接口及变量程序运行时均已移除。
- 业务快照升级为 business-snapshot-v3。仍参与业务的订阅、工作候选、草稿和修订会在升级时自动迁移；历史执行记录的冻结输入与摘要保持原字节。

## 注意

- 请先在 0.2.3 中运行 bansoko-server migrate check-vars（可加 --json），替换所有仍在使用 vars.* 的命名与通知模板，并等待引用这些模板的在途任务完成。升级预检会阻止仍含引用的升级，且不会改写历史冻结证据。
