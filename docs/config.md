# 配置与数据语义

ProxyCollector 只接受严格 YAML。未知字段、旧 AIOPROXY 字段和多个 YAML
document 都会导致 `check` 与 `serve` 失败。

## Web 与输出

```yaml
web:
  listen: "0.0.0.0:27298"
  path_prefix: "/lists"
output:
  directory: "./data"
  filename: "proxies.txt"
```

以上配置发布 `/lists/proxies.txt`。`filename` 必须是以 `.txt` 结尾的纯文件名，
不能包含目录、反斜杠或点路径。输出目录不存在时自动创建。

公开 TXT 由服务完全拥有。每行是 `http://...` 或 `socks5://...`；用户名、
密码和 IPv6 地址使用标准 URL 转义。完整规范 URL 是去重键，不同协议或凭据
不会被合并。输出全局去重、字典序稳定，并以换行结尾。

## 采集网络

```yaml
fetch:
  proxy_url: "http://user:pass@proxy.example:8080"
  timeout: "30s"
```

`proxy_url` 为空时强制直连且不读取 `HTTP_PROXY`/`HTTPS_PROXY`。非空时只接受
HTTP/HTTPS 代理，所有采集器严格使用它；连接失败不会降级直连。

## 验活

```yaml
alive:
  enabled: false
  concurrency: 64
  timeout: "5s"
```

`alive` 默认关闭，开启后发布前对去重后的完整代理集合做基础 TCP 端口连通性
检查：`concurrency` 控制并发拨号数（1–1024，默认 64），`timeout` 为单次拨号
超时（必须为正，默认 5s）。检查是直连目标 `host:port`，不走
`fetch.proxy_url`，只验证端口能否建立 TCP 连接，不验证协议、凭据或延迟。

开启后 TXT 只包含存活节点；来源状态仍保留采集全量，下一轮可重新验证。一轮
验活全部失败时保留上次发布的 TXT，避免临时网络问题导致列表清空；不存在 TXT
时不创建空文件。

## 刷新语义

- 每个采集器启动后立即刷新，随后按自己的 `refresh_interval` 独立运行。
- 每个 FPL source、FOFA query、FreeProxyDB 和 Rola-IP 分别维护来源状态。
- 非零结果即替换该来源，即使该轮被标记为 partial 或伴随后续分页错误。
- 零结果、完全请求失败和进程取消保留该来源上次结果，且不会自动过期。
- 从配置中删除来源时，其状态和代理会在下次启动时删除。
- 不支持热加载、SIGHUP 或手工刷新 API；修改配置后必须重启。

## 采集器

`collectors.rolaip` 默认启用；即使整块省略也会使用
`https://rola-ip.co/proxy-api`。如需关闭，必须明确配置：

```yaml
collectors:
  rolaip:
    enabled: false
```

Rola-IP 按页读取站点免费代理列表。HTTP 与 HTTPS 统一输出为 `http://`；
SOCKS5 输出为 `socks5://`；SOCKS4 和未知协议跳过。同一地址同时支持 HTTP
和 SOCKS5 时会输出两个 URL。分页请求遵守 `request_interval` 与服务端 429
`Retry-After`，单轮最多请求 50 页；达到页数/候选上限或后续页面失败时，保留
本轮已取得的非零结果并标记为 partial。

`collectors.fpl` 支持 `url_list` 和 `host_port`。`host_port` 必须明确指定
`protocol: http` 或 `protocol: socks5`。空的 FPL 配置使用内置 GitHub 源目录。

`collectors.fofa` 需要 `key`。每个 query 必须有唯一名称、协议、查询表达式和
字段列表。默认查询覆盖无认证 SOCKS5 和常见 HTTP proxy banner。

`collectors.freeproxydb` 保留分页、页间隔和 429 Retry-After。只导入 HTTP 与
SOCKS5；VMess、VLESS、Trojan、Shadowsocks、Hysteria、SOCKS4 等记录跳过。

关闭 Rola-IP 后，至少还须配置一个其他采集器。完整字段和边界见
[示例配置](../examples/config.yaml) 及
`proxycollector check -c <file>` 的确定性错误输出。
