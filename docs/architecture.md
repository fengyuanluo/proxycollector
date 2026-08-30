# 架构与持久化

```text
Rola-IP / FPL / FOFA / FreeProxyDB
          |
          v
 shared strict fetch client
          |
          v
 normalize HTTP/SOCKS5 URLs
          |
          v
 per-source state -> global dedupe
          |
          v
 optional alive TCP check (direct dial, no fetch proxy)
          |
          v
 sort -> atomic TXT
                                      |
                                      v
                               GET/HEAD Web endpoint
```

公开 TXT 旁固定保存 `.proxycollector-state.json`，权限为 `0600`。它记录每个
子来源最近接受的 URL 集合和更新时间，用于重启后的来源级替换。TXT 权限为
`0644`。

更新时先将状态写入同目录临时文件，执行同步并原子重命名，再以相同方式发布
TXT。若状态已更新而进程在 TXT 重命名前退出，下次启动会从状态重建 TXT。

若 TXT 存在但状态缺失或损坏，旧 TXT 继续服务；损坏状态会改名隔离。只有当
当前配置中的所有子来源至少完整成功一次后，才会用重建状态替换旧 TXT。

产品中没有代理 listener、代理池、会话、运行时剔除、SINGBOX 或管理 API。验活
启用时存在一个验证 worker pool：它只做 TCP 端口连通性拨号（不走采集代理），
在发布前过滤死节点；全部失败时保留上次 TXT。常驻任务只包括 Web server、每个
启用采集器的刷新循环，以及可选的验活 worker pool。
