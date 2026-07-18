# Docker 与自动发布

## 本地构建

```bash
docker build -t proxycollector:local .
mkdir -p data
docker run --rm \
  -p 27298:27298 \
  -v "$PWD/examples/config.yaml:/etc/proxycollector/config.yaml:ro" \
  -v "$PWD/data:/app/data" \
  proxycollector:local
```

镜像仅发布 `linux/amd64`，进程以 root 运行。镜像包含 CA 证书，但不包含默认
配置；缺少 `/etc/proxycollector/config.yaml` 时应立即失败。

## 自动发布

GitHub Actions 构建并推送：

```text
git.599520.xyz/luofengyuan/proxycollector
```

仓库必须配置以下 GitHub Actions Secrets：

- `GITEA_USER`
- `GITEA_TOKEN`

`main` 发布 `latest` 与 `sha-<短提交>`；`v1.2.3` tag 额外发布 `1.2.3`、
`1.2`、`1`。工作流不把 Registry 凭据写入源码、镜像 layer 或构建参数。
