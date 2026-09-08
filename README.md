# Tiny Password

Tiny Password 是一个面向个人与家庭共享保险库的自托管密码管理器：Go 单体服务提供同源 API，React 前端以 PWA 形式安装，SQLite 保存授权元数据和加密负载。

## 快速开始

```bash
umask 077
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master_key
openssl rand -base64 32 > secrets/backup_passphrase
chmod 0700 secrets
sudo chown "$(id -u)":10001 secrets/master_key secrets/backup_passphrase
chmod 0640 secrets/master_key secrets/backup_passphrase
docker compose build
docker compose up -d
```

默认部署不向宿主机发布端口。生产访问请启用[Cloudflare Tunnel](docs/operations/tunnel.md)或接入已有 HTTPS 反向代理；完整步骤见[部署说明](docs/operations/deploy.md)。

## 运维文档

- [部署、TLS、LAN 和更新](docs/operations/deploy.md)
- [Secret 生成与保管](docs/operations/secrets.md)
- [Cloudflare Tunnel](docs/operations/tunnel.md)
- [备份与恢复](docs/operations/backup-restore.md)
- [升级与回滚](docs/operations/upgrade.md)

## 安全边界

- 浏览器会话和 CSRF 状态只在内存中；PWA 只预缓存明确列出的静态资源，不缓存 API、归档或导航响应。
- 主密钥只从只读 Secret 文件读取，恢复不能覆盖目标 Secret；敏感的归档中间数据只允许进入验证过的 tmpfs。
- 默认 Cookie 为 Secure/HttpOnly/SameSite=Lax；普通 HTTP 放宽必须显式设置 `TP_ALLOW_INSECURE_COOKIES=1`，仅限开发/评估。
- 生产 Tunnel/反代部署必须使用浏览器信任的 HTTPS；可信代理网段通过 `TP_TRUSTED_PROXY_CIDRS` 明确配置，默认不信任转发头。

## 开发验证

```bash
npm --prefix web ci
npm --prefix web run typecheck
npm --prefix web test -- --run
go test ./...
```

构建 Go 二进制前先构建前端：`make build`。发布验收和可重复的容器/浏览器检查见 `docs/releases/v1-validation.md` 与 `docs/superpowers/plans/2026-09-05-tiny-password-action-plan.md`。
