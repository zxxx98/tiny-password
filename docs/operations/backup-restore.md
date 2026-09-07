# 备份与恢复操作

实例备份是离线恢复用的整实例归档；个人导入导出不是实例恢复替代品。备份归档含受保护的数据库快照、源主密钥材料和 manifest，不含 R2 credential、Tunnel token、备份口令或 TLS 私钥。

## 配置备份

1. 用 `secrets/backup_passphrase` 提供备份口令。
2. 启动管理员页面的备份设置，分别配置 local 与 R2 目标的启停、时刻、时区和保留数。
3. R2 credential 以 `deploy/compose.r2.yaml` 只读 Secret 挂载，endpoint/bucket/prefix 只从管理员系统页写入；不要把 credential 填进设置表单。
4. 先执行一次手动备份，确认每个启用目标独立成功，再确认 `backup_runs` 和审计页中的脱敏结果。

备份失败不会删除已有成功归档。R2 临时 `incoming` 对象由维护任务单独清扫；最终对象先做内容摘要校验再纳入保留删除。

## 恢复前

- 停止 app 和 Tunnel；restore 会取得数据目录锁，运行服务时拒绝执行。
- 准备目标主密钥。它可以与源密钥不同；恢复会用源密钥解密并以目标密钥重新加密数据库和历史版本。
- 确认目标数据卷至少有数据库、前置快照和临时受限 tmpfs 所需空间。
- 保留归档原件、备份口令和目标主密钥，直到恢复后完成五类条目、历史、权限和登录检查。

## Docker 恢复示例

先在受控主机生成一个新的 32-byte 目标主密钥，并把它挂载到容器的默认 Secret 路径：

```bash
umask 077
head -c 32 /dev/urandom > secrets/restore_master_key
docker compose down
docker run --rm \
  -e TP_DATA_DIR=/data \
  -e TP_MASTER_KEY_FILE=/run/secrets/master_key \
  -e TP_BACKUP_PASSPHRASE_FILE=/run/secrets/backup_passphrase \
  -v tiny-password_app-data:/data \
  -v "$PWD/restore/backup.7z:/restore/backup.7z:ro" \
  -v "$PWD/secrets/restore_master_key:/run/secrets/master_key:ro" \
  -v "$PWD/secrets/backup_passphrase:/run/secrets/backup_passphrase:ro" \
  --tmpfs /tmp:mode=700,uid=10001,gid=10001,size=512m \
  --user 10001:10001 \
  tiny-password:dev restore /restore/backup.7z
```

实际项目名或卷名不同时替换 `tiny-password_app-data`。restore 的临时源密钥和解包目录必须位于验证过的 tmpfs；`TP_RESTORE_WORK_DIR` 若显式设置到普通磁盘会 fail closed。不要把 `TP_RESTORE_WORK_DIR` 指向 `/tmp` 根目录，应用会在其下创建并只清理自己的私有 session 子目录。

恢复输出只包含 stage/code/request_id 等脱敏信息。成功后：

```bash
docker compose up -d
curl -fsS https://password.example.com/readyz
```

登录并检查五类条目、历史版本、共享可见性/创建者写权限、回收站状态和审计记录。已有目标恢复前会留下 `pre-restore-*` 快照；故障应先保留现场，再按输出 code 和升级文档回滚，不要直接删除快照。
