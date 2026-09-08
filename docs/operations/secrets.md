# Secret 生成与保管

应用不会生成替代主密钥，也不把 secret 写入数据库、备份 manifest、审计或 API 响应。默认 Compose 的一次性 `init-secrets` 服务只在还没有数据库且对应文件不存在时生成初始主密钥和备份口令；应用本身仍只从文件读取凭据。

## 文件

默认 Compose 需要：

| 文件 | 用途 | 丢失后果 |
| --- | --- | --- |
| `${TP_DATA_DIR_HOST:-/mnt/data/tiny-password}/.secrets/master_key` | 解密保险库、实例幂等指纹和恢复目标密钥 | 加密负载不可解密；不能重置替代 |
| `${TP_DATA_DIR_HOST:-/mnt/data/tiny-password}/.secrets/backup_passphrase` | 创建/读取加密实例归档 | 该口令保护的归档不可读取 |
| `secrets/tunnel_token` | 仅启用 Tunnel profile 时给 cloudflared | Tunnel 无法连接；不会影响本地 app |

R2 需要额外的 `secrets/r2_access_key` 和 `secrets/r2_secret_key`，通过 `deploy/compose.r2.yaml` 挂载；它们不进入应用设置 API 和备份归档。

## R2 环境变量方式

受控的内网部署也可以设置 `TP_R2_ACCESS_KEY` 和 `TP_R2_SECRET_KEY`。两个值都必须是非空凭据；每个字段都会优先使用非空环境变量，空白值才回退到对应的 Secret 文件。环境变量方式不会改变凭据不入库、不入日志、不入审计和不入备份归档的约束。

该方式接受更大的运行时暴露面：环境变量可能通过 `docker inspect`、`/proc`、进程转储或部署诊断信息暴露。因此不要把它们写入 Git、shell 历史、未保护的 CI 输出或 issue；如果环境边界变化，应改用上面的 Docker Secret 文件方式并轮换凭据。

即使使用环境变量，R2 的 endpoint、bucket 和 prefix 仍需在管理员系统页配置；endpoint 使用 Cloudflare 账号专属的 R2 S3 endpoint，应用签名区域为 `auto`。

## 首次生成与保管

首次执行 `docker compose up -d` 时，`init-secrets` 会在数据盘的 `.secrets/` 目录中生成文件，并设置为 `0600`、归属应用 UID/GID `10001:10001`。主密钥必须是恰好 32 个原始字节，不要用文本口令、十六进制字符串或带换行的编码替代。

如果 `tiny-password.db` 已经存在而任一密钥缺失、为空或格式不正确，初始化会 fail closed；不要删除数据库或让 Compose 重新生成密钥，应从受控离线副本恢复原密钥。重启和升级会复用现有密钥，不会覆盖它们。

部署成功后，至少把 `.secrets/master_key` 和 `.secrets/backup_passphrase` 各保存一份不在该数据盘上的受控副本。备份口令可以是文本；创建归档后也应使用独立的密码管理器保存。

使用支持原生 Secret 对象的编排环境时，应使用该环境的 root-owned、只读 Secret 投影方式，不要把 Compose 数据盘初始化方式直接照搬过去。

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
