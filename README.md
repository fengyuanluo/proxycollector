# ProxyCollector

ProxyCollector 是一个只做代理数据采集的常驻服务：从 Rola-IP、FPL、FOFA 和
FreeProxyDB 收集 HTTP/SOCKS5 代理，规范化、全局去重并按字典序输出为
TXT，同时通过一个只读 HTTP 端点托管该文件。

它不提供代理转发、延迟测试、调度、会话、SINGBOX、Admin API 或 Web UI。
可选启用基础 TCP 端口验活（默认关闭）：发布前只保留端口可连通的节点。

## 快速开始

```bash
cp examples/config.yaml config.yaml
go run ./cmd/proxycollector check -c config.yaml
go run ./cmd/proxycollector serve -c config.yaml
curl http://127.0.0.1:27298/proxies.txt
```

默认输出到 `./data/proxies.txt`。每行是一个可直接消费的规范 URL：

```text
http://host:port
http://user:pass@host:port
socks5://host:port
```

首次采集完成前，列表端点返回 `503 proxy list not ready`。其他路径返回
404；目标路径只允许 GET 和 HEAD。

## Docker

镜像不会内置运行配置，必须挂载 YAML：

```bash
mkdir -p data
docker run --rm \
  -p 27298:27298 \
  -v "$PWD/examples/config.yaml:/etc/proxycollector/config.yaml:ro" \
  -v "$PWD/data:/app/data" \
  git.599520.xyz/luofengyuan/proxycollector:latest
```

## 文档

- [配置与数据语义](docs/config.md)
- [架构与持久化](docs/architecture.md)
- [Docker 与发布](docs/deployment.md)
- [从 AIOPROXY 迁移](docs/migration.md)

## License

MIT
