# 从 AIOPROXY 迁移

ProxyCollector 是全新产品，不兼容 AIOPROXY 的 Git 历史、配置、状态、CLI、API
或二进制名称。

以下能力和配置已经删除：

- `server`、`admin`、`auth`、`scheduler`、`session`
- `validation`、`runtime_failure`、旧 `storage`、`lifecycle`
- `plugins.singbox` 及所有 SINGBOX 节点/订阅转换
- HTTP CONNECT/SOCKS5 代理入口、用户名路由、会话固定和快速池
- `/health`、`/stats`、`/pool`、`/plugins`、`/snapshots`
- 池 JSON、采集快照和验证状态

采集配置移动到 `collectors`。旧配置不会被静默忽略，而是在严格解析阶段直接
失败。迁移时以 `examples/config.yaml` 为基础重新填写 FOFA key、源地址和刷新
周期，不要复制旧顶层区块。
