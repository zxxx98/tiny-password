# 部署 Tiny Password

Tiny Password 的默认 Compose 配置不向宿主机发布应用端口。生产浏览器应通过 Cloudflare Tunnel 或已有的 HTTPS 反向代理访问；直接使用局域网 HTTP 只适合明确开启开发放宽的临时评估，不应作为生产登录入口。

## 前置条件

- Docker Engine 与 Compose plugin；生产主机至少准备持久化数据盘目录、备份所需空间和一个可接受浏览器信任的 HTTPS 入口。
- 首次部署会在 `${TP_DATA_DIR_HOST:-/mnt/data/tiny-password}/.secrets/` 自动生成主密钥和备份口令。两者丢失都会分别导致数据或备份不可恢复，因此必须在部署成功后立即做受控的离线备份。
- 若使用 Tunnel：Cloudflare 账户、已创建的 remotely-managed Tunnel，以及其 token；应用公共 hostname 的 service URL 设为 `http://app:8080`。

## 首次启动

在仓库目录执行。`init-secrets` 只会输出失败原因，不会把 secret 内容写入日志：

```bash
export TP_DATA_DIR_HOST=/mnt/data/tiny-password
docker compose build
docker compose up -d
docker compose ps
docker compose logs --since=1m app
```

首次部署会先生成：

- `/mnt/data/tiny-password/.secrets/master_key`（32 字节主密钥）
- `/mnt/data/tiny-password/.secrets/backup_passphrase`（备份口令）
- `/mnt/data/tiny-password/backups/`（本地备份目录）

如果数据目录中已经存在 `tiny-password.db`，但任一密钥缺失或格式不正确，初始化服务会失败并阻止 app 启动；它不会生成替代密钥。默认 compose 没有 `ports:` 映射；`docker compose ps` 中 app 应为 healthy，但宿主公网不能直接访问它。首次初始化 token 只在初始化状态的启动日志中出现一次。打开 HTTPS 地址完成初始化和管理员首次改密，然后立即保存 token、主密钥和备份口令的受控副本。

## GHCR / DPanel

DPanel 使用 GHCR 镜像时，导入 [`deploy/compose.ghcr.yaml`](/home/ubuntu/code/personal/tiny-password/deploy/compose.ghcr.yaml)。该文件默认使用 `ghcr.io/zxxx98/tiny-password:latest`、将宿主机 `29998` 映射到容器 `8080`，并把数据写入 `/mnt/data/tiny-password`。R2 凭据只在 DPanel 的环境变量中设置：`CF_R2_ACCESS_KEY` 和 `CF_R2_SECRET_KEY`；不要把真实值写进 YAML 或 Git。

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
TP_LAN_PORT=28888 \
docker compose -f compose.yaml -f deploy/compose.lan.yaml up -d
```

若反向代理在另一台受信任的 LAN 主机，把 `TP_LAN_BIND_ADDRESS` 改成应用主机的具体私网地址，不要改成 `0.0.0.0`。反向代理必须终止 HTTPS、把原始 Host/路径完整转发，并只在代理确实位于指定网络时设置：

```bash
TP_LAN_BIND_ADDRESS=192.168.1.20 \
TP_LAN_PORT=8080 \
TP_TRUSTED_PROXY_CIDRS=192.168.1.10/32 \
docker compose -f compose.yaml -f deploy/compose.lan.yaml up -d
```

应用默认不信任 `X-Forwarded-*`。配置错误的代理网段不会帮助伪造 HTTPS，反而会让浏览器收不到 Secure Cookie；应先从真实 HTTPS URL 登录并检查浏览器请求中带有 session cookie，再进行远程访问测试。不要设置 `TP_ALLOW_INSECURE_COOKIES=1`，除非是隔离的本地开发/评估环境。

若只是临时验证 HTTP 页面，可显式传入 `TP_ALLOW_INSECURE_COOKIES=1` 后重建 app；这会让密码和会话经过明文 HTTP，只能用于短期测试，测试结束应立即关闭并重新部署：

```bash
TP_ALLOW_INSECURE_COOKIES=1 \
TP_LAN_BIND_ADDRESS=0.0.0.0 \
TP_LAN_PORT=28888 \
docker compose -f compose.yaml -f deploy/compose.lan.yaml up -d --force-recreate app
```

## Argon2 密码哈希资源配置

默认密码哈希策略保持为 Argon2id `64 MiB / t=3 / p=2`，但运行时额外限制整个进程最多同时使用 128 MiB Argon2 内存、最多 2 个并发计算。可通过以下环境变量调整：

- `TP_ARGON2_MEMORY_MIB`：新密码 hash 的内存成本，默认 `64`。
- `TP_ARGON2_ITERATIONS`：新密码 hash 的迭代次数，默认 `3`。
- `TP_ARGON2_PARALLELISM`：新密码 hash 的并行度，默认 `2`。
- `TP_ARGON2_MEMORY_BUDGET_MIB`：整个进程允许 Argon2 同时占用的总内存预算，默认 `128`。
- `TP_ARGON2_MAX_CONCURRENCY`：同时执行的 Argon2 计算上限，默认 `2`。
- `TP_ARGON2_VERIFY_MAX_MEMORY_MIB`：允许验证的旧 hash 单次最大内存，默认 `128`。

低内存机器可使用：

```bash
export TP_ARGON2_MEMORY_MIB=19
export TP_ARGON2_ITERATIONS=2
export TP_ARGON2_PARALLELISM=1
export TP_ARGON2_MEMORY_BUDGET_MIB=64
export TP_ARGON2_MAX_CONCURRENCY=2
export TP_ARGON2_VERIFY_MAX_MEMORY_MIB=64
docker compose up -d --force-recreate app
```

如果现有用户仍保存旧的 64 MiB hash，第一次降级时 `TP_ARGON2_MEMORY_BUDGET_MIB` 和 `TP_ARGON2_VERIFY_MAX_MEMORY_MIB` 仍必须至少为 64 MiB。应用启动会扫描已保存的 PHC 参数；配置不足以验证旧 hash 时会拒绝启动并给出明确错误，而不是让用户登录时失败。

用户成功登录后，如果保存的 Argon2 参数与当前目标参数不同，会在同一登录事务中自动重新 hash。这个迁移是双向的：既可把旧的高内存 hash 渐进迁移到低内存配置，也可在以后提高参数时自动升级。错误密码、禁用账户和失败的并发登录不会修改保存的 hash。

支持的新 hash 目标范围为 7–256 MiB、1–10 次迭代、并行度 1–4。低于 19 MiB 会在启动日志中产生提醒；一般优先使用 19 MiB / t=2 / p=1，而不是继续压低内存。

## 更新与停机

```bash
docker compose pull
docker compose up -d
docker compose logs -f app
```

应用捕获 SIGTERM，先停止接收新请求，再在 15 秒服务优雅关闭窗口内清理 transfer worker、scheduler 和 HTTP 连接；Compose 的 30 秒 stop grace period 为其留下余量。更新前必须阅读[升级与回滚说明](upgrade.md)，并确认新旧镜像使用同一主密钥。

## 配置校验

初始化服务会在启动时检查数据目录和密钥。发布前可先检查 Compose 配置：

```bash
docker compose config --quiet
export TP_TUNNEL_TOKEN_SOURCE="$PWD/secrets/tunnel_token"
TP_CHECK_TUNNEL=1 bash scripts/check-compose-secrets.sh
TP_TUNNEL_TOKEN_SOURCE="$PWD/secrets/tunnel_token" docker compose --profile tunnel config --quiet
docker compose -f compose.yaml -f deploy/compose.lan.yaml config --quiet
```

R2 的 endpoint、bucket、prefix 始终从管理员系统页配置。endpoint 必须使用 Cloudflare 显示的、账号专属的 S3 端点（形如 `https://<account>.r2.cloudflarestorage.com`）；应用签名区域固定为 `auto`。

受控的内网部署可以直接通过环境变量配置 R2 凭据。环境变量按字段优先于同名 Secret 文件；值为空或全为空白时才回退到文件。配置后校验并重建 app：

```bash
export TP_R2_ACCESS_KEY='your-access-key-id'
export TP_R2_SECRET_KEY='your-secret-access-key'
docker compose -f compose.yaml config --quiet
docker compose -f compose.yaml up -d --build --force-recreate app
```

环境变量会被传入 app 容器，适合已接受该暴露面的受控内网环境。不要把值提交到 Git 或输出到不受保护的 CI 日志。

如果使用推荐的 Docker Secret 文件方式，准备两份 R2 credential 文件并叠加 `deploy/compose.r2.yaml`；这会继续使用 `/run/secrets/r2_access_key` 和 `/run/secrets/r2_secret_key`，环境变量留空即可：

```bash
export TP_R2_ACCESS_KEY_SOURCE="$PWD/secrets/r2_access_key"
export TP_R2_SECRET_KEY_SOURCE="$PWD/secrets/r2_secret_key"
TP_CHECK_R2=1 bash scripts/check-compose-secrets.sh
docker compose -f compose.yaml -f deploy/compose.r2.yaml config --quiet
```
