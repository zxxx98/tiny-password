# Cloudflare Tunnel

Tunnel profile 使用 Docker 服务名访问 app，不需要向宿主机打开 Tiny Password 端口。当前配置采用 Cloudflare 的 remotely-managed Tunnel token 文件模式；官方参数文档说明了 `cloudflared tunnel run --token-file <PATH>` 和 `--no-autoupdate`：

<https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/run-parameters/>

## Cloudflare 侧

1. 在 Cloudflare Zero Trust 中创建一个 remotely-managed Tunnel。
2. 为其添加 Public Hostname，例如 `password.example.com`。
3. Service 类型选择 HTTP，URL 填 `http://app:8080`。Tunnel 容器与 app 在同一个 Compose 网络，`app` 是 Compose 服务名。
4. 复制安装 token 到本机 `secrets/tunnel_token`；token 是秘密，不要写入本文件或日志。

## 启动

```bash
TP_TUNNEL_TOKEN_SOURCE="$PWD/secrets/tunnel_token" \
docker compose --profile tunnel up -d
docker compose ps
docker compose logs --since=1m tunnel
```

Compose 中 Tunnel service：

- 只从 `/run/secrets/tunnel_token` 读取 token；命令行中没有 token 明文。
- 使用 `--no-autoupdate`，镜像更新由运维明确执行，避免运行中的 cloudflared 自行替换进程。
- 等待 app healthcheck 后再启动；app 没有 host port，外部请求只能经 Tunnel 网络到达。
- 只声明 `tunnel` profile 才会启动，不启用 profile 时本地部署不需要 cloudflared 镜像或 token 文件。

验证时不要打印 token：

```bash
docker compose --profile tunnel ps
docker compose --profile tunnel logs --since=1m tunnel | rg -i 'connected|registered|error|warn'
```

如果日志包含 token 或完整 secret，应立即停止复制、清理暴露位置并轮换 token。Cloudflare Tunnel 的 HTTPS 终止发生在边缘；应用仍保持默认 Secure Cookie 和严格同源安全策略。

## 不使用 Tunnel

使用已有反向代理时，参阅[部署说明](deploy.md)的 LAN override。代理必须使用可信证书提供 HTTPS，且只有代理网段才可列入 `TP_TRUSTED_PROXY_CIDRS`。不要为了绕过 Secure Cookie 在生产设置 `TP_ALLOW_INSECURE_COOKIES=1`。
