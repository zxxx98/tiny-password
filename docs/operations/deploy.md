# 部署 Tiny Password

Tiny Password 的默认 Compose 配置不向宿主机发布应用端口。生产浏览器应通过 Cloudflare Tunnel 或已有的 HTTPS 反向代理访问；直接使用局域网 HTTP 只适合明确开启开发放宽的临时评估，不应作为生产登录入口。

## 前置条件

- Docker Engine 与 Compose plugin；生产主机至少准备持久化 `/data` 卷、备份所需空间和一个可接受浏览器信任的 HTTPS 入口。
- 一份只读挂载的主密钥和一份备份口令。两者丢失都会分别导致数据或备份不可恢复。
- 若使用 Tunnel：Cloudflare 账户、已创建的 remotely-managed Tunnel，以及其 token；应用公共 hostname 的 service URL 设为 `http://app:8080`。

## 首次启动

在仓库目录执行。命令不会把 secret 内容写入日志：

```bash
umask 077
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master_key
openssl rand -base64 32 > secrets/backup_passphrase

docker compose build
docker compose up -d
docker compose ps
docker compose logs --since=1m app
```

默认 compose 没有 `ports:` 映射；`docker compose ps` 中 app 应为 healthy，但宿主公网不能直接访问它。首次初始化 token 只在初始化状态的启动日志中出现一次。打开 HTTPS 地址完成初始化和管理员首次改密，然后立即保存 token、主密钥和备份口令的受控副本。

健康检查：

```bash
curl -fsS https://password.example.com/healthz
curl -fsS https://password.example.com/readyz
```

`/healthz` 只表示进程存活；`/readyz` 还会检查数据库、迁移和主密钥。任何输出都不要复制到公开工单或聊天中。

## 已有 HTTPS 反向代理 / LAN

只在需要让本机或指定 LAN 地址上的反向代理访问时叠加 override：

```bash
TP_LAN_BIND_ADDRESS=127.0.0.1 \
docker compose -f compose.yaml -f deploy/compose.lan.yaml up -d
```

若反向代理在另一台受信任的 LAN 主机，把 `TP_LAN_BIND_ADDRESS` 改成应用主机的具体私网地址，不要改成 `0.0.0.0`。反向代理必须终止 HTTPS、把原始 Host/路径完整转发，并只在代理确实位于指定网络时设置：

```bash
TP_LAN_BIND_ADDRESS=192.168.1.20 \
TP_TRUSTED_PROXY_CIDRS=192.168.1.10/32 \
docker compose -f compose.yaml -f deploy/compose.lan.yaml up -d
```

应用默认不信任 `X-Forwarded-*`。配置错误的代理网段不会帮助伪造 HTTPS，反而会让浏览器收不到 Secure Cookie；应先从真实 HTTPS URL 登录并检查浏览器请求中带有 session cookie，再进行远程访问测试。不要设置 `TP_ALLOW_INSECURE_COOKIES=1`，除非是隔离的本地开发/评估环境。

## 更新与停机

```bash
docker compose pull
docker compose up -d
docker compose logs -f app
```

应用捕获 SIGTERM，先停止接收新请求，再在 15 秒服务优雅关闭窗口内清理 transfer worker、scheduler 和 HTTP 连接；Compose 的 30 秒 stop grace period 为其留下余量。更新前必须阅读[升级与回滚说明](upgrade.md)，并确认新旧镜像使用同一主密钥。

## 配置校验

准备好本地 secret 文件后，发布前运行：

```bash
export TP_MASTER_KEY_SOURCE="$PWD/secrets/master_key"
export TP_BACKUP_PASSPHRASE_SOURCE="$PWD/secrets/backup_passphrase"
bash scripts/check-compose-secrets.sh
docker compose config --quiet
export TP_TUNNEL_TOKEN_SOURCE="$PWD/secrets/tunnel_token"
TP_CHECK_TUNNEL=1 bash scripts/check-compose-secrets.sh
TP_TUNNEL_TOKEN_SOURCE="$PWD/secrets/tunnel_token" docker compose --profile tunnel config --quiet
docker compose -f compose.yaml -f deploy/compose.lan.yaml config --quiet
```

R2 只在已准备两份 R2 credential 文件时叠加 `deploy/compose.r2.yaml`；R2 endpoint、bucket、prefix 从管理员系统页配置，credential 只通过 Secret 文件提供。启用 R2 前先执行：

```bash
export TP_R2_ACCESS_KEY_SOURCE="$PWD/secrets/r2_access_key"
export TP_R2_SECRET_KEY_SOURCE="$PWD/secrets/r2_secret_key"
TP_CHECK_R2=1 bash scripts/check-compose-secrets.sh
docker compose -f compose.yaml -f deploy/compose.r2.yaml config --quiet
```
