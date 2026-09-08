# Secret 生成与保管

Tiny Password 不生成替代主密钥，也不把 secret 写入数据库、备份 manifest、审计或 API 响应。凭据通过 Docker Secret 文件或受控环境变量注入，应用在响应中最多暴露“凭据是否已配置”的布尔状态。

## 文件

默认 Compose 需要：

| 文件 | 用途 | 丢失后果 |
| --- | --- | --- |
| `secrets/master_key` | 解密保险库、实例幂等指纹和恢复目标密钥 | 加密负载不可解密；不能重置替代 |
| `secrets/backup_passphrase` | 创建/读取加密实例归档 | 该口令保护的归档不可读取 |
| `secrets/tunnel_token` | 仅启用 Tunnel profile 时给 cloudflared | Tunnel 无法连接；不会影响本地 app |

R2 需要额外的 `secrets/r2_access_key` 和 `secrets/r2_secret_key`，通过 `deploy/compose.r2.yaml` 挂载；它们不进入应用设置 API 和备份归档。

## R2 环境变量方式

受控的内网部署也可以设置 `TP_R2_ACCESS_KEY` 和 `TP_R2_SECRET_KEY`。两个值都必须是非空凭据；每个字段都会优先使用非空环境变量，空白值才回退到对应的 Secret 文件。环境变量方式不会改变凭据不入库、不入日志、不入审计和不入备份归档的约束。

该方式接受更大的运行时暴露面：环境变量可能通过 `docker inspect`、`/proc`、进程转储或部署诊断信息暴露。因此不要把它们写入 Git、shell 历史、未保护的 CI 输出或 issue；如果环境边界变化，应改用上面的 Docker Secret 文件方式并轮换凭据。

即使使用环境变量，R2 的 endpoint、bucket 和 prefix 仍需在管理员系统页配置；endpoint 使用 Cloudflare 账号专属的 R2 S3 endpoint，应用签名区域为 `auto`。

## 生成

在受控主机执行并保持 umask：

```bash
umask 077
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master_key
openssl rand -base64 32 > secrets/backup_passphrase
chmod 0600 secrets/master_key secrets/backup_passphrase
```

主密钥必须是恰好 32 个原始字节，不要用文本口令、十六进制字符串或带换行的编码替代。备份口令可以是文本；创建归档后应使用独立的密码管理器保存，而不是只留在 Compose 项目目录。

本项目的普通 Docker Compose `secrets: file:` 在 Docker Engine 上由宿主文件 bind mount 实现，不会像 Swarm Secret 一样自动改成容器用户可读。应用以 UID/GID `10001` 的非 root 用户运行，因此在使用本地 Compose 时，还要保持 `secrets/` 目录为 `0700`，并让容器组读取文件：

```bash
chmod 0700 secrets
sudo chown "$(id -u)":10001 secrets/master_key secrets/backup_passphrase
chmod 0640 secrets/master_key secrets/backup_passphrase
```

这不会让其他宿主用户穿过 `secrets/` 目录；若使用支持原生 Secret 对象的编排环境，应使用该环境的 root-owned、只读 Secret 投影方式，不要把宿主文件权限规则直接照搬过去。

Tunnel token 从 Cloudflare Dashboard 创建 remotely-managed Tunnel 后复制到受限文件：

```bash
umask 077
printf '%s' "$CLOUDFLARE_TUNNEL_TOKEN" > secrets/tunnel_token
chmod 0600 secrets/tunnel_token
```

不要把 token 作为 Compose `command` 参数、shell 历史、CI 输出或 issue 内容提交。变更 token 后先替换受限文件，再重建/重启 Tunnel service。

## 备份策略

- 主密钥、备份口令和 Tunnel token 分开保存，并至少有一份离线副本；副本不放在同一数据卷。
- 备份归档包含源主密钥的加密归档材料，因此备份口令与归档必须拥有相同或更严格的访问控制。
- 忘记备份口令不能通过管理员 UI、数据库或应用日志恢复；忘记主密钥不能通过 `restore` 猜测或重建。
- 任何 secret 误进入日志、shell history、聊天或 CI artifact，都应立即轮换对应 token/credential；主密钥不能轮换来挽救已经泄漏的明文暴露，需按事件响应处理。
