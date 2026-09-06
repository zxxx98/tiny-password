# 升级与回滚（T26）

本文说明 Tiny Password V1 的数据库升级保护、失败回滚步骤，以及必须成对验证的镜像/数据/主密钥配套关系。

## 升级前检查

1. **停止旧容器**。升级（启动新版本）前实例必须停止服务；启动时的数据目录锁保证升级不与在线写入并行。
2. **确认主密钥配套**。数据库中的负载由挂载的主密钥加密（`TP_MASTER_KEY_FILE`）。升级只改 schema、不改密钥；新旧镜像必须挂载**同一把**主密钥。
3. **确认磁盘余量**。启动守卫会在升级前为非空数据库生成一份一致性快照（约等于当前数据库大小），请保证数据目录余量 ≥ 2×数据库体积。

## 升级流程

```bash
docker compose pull          # 或 docker compose up -d 使用新镜像
docker compose up -d
docker compose logs -f       # 观察 "tiny-password listening" 与 /readyz
```

启动时自动执行：

1. 获取数据目录锁（旧实例仍在运行会直接失败退出）。
2. 检查 `schema_migrations`：目标库版本比本二进制**更新**时拒绝启动（不支持降级）。
3. 非空数据库先在数据目录生成 `pre-upgrade-<UTC时间>.db` 快照并通过 `integrity_check`；空数据库（全新实例）不需要快照。
4. 逐个以独立事务应用未执行的迁移；失败即回滚该迁移并中止启动，服务不会进入 ready。
5. 全部通过后正常启动。

## 升级失败回滚

迁移是事务化的：失败的迁移不留半成品，数据库保持在上一版本；快照是额外的完整副本。

1. 确认启动日志中的失败迁移版本（`apply migrations:` 行）。
2. **回滚方式 A（首选）**：直接用旧镜像重启（挂载同一数据卷、同一主密钥）。数据库本身停在旧版本，旧镜像可正常打开。

   ```bash
   docker compose down
   # 编辑 compose.yaml 镜像 tag 回旧版本，然后：
   docker compose up -d
   ```

3. **回滚方式 B（数据库损坏时）**：停止容器后，用数据目录中的 `pre-upgrade-<UTC时间>.db` 覆盖 `tiny-password.db`（同时删除可能存在的 `-wal`/`-shm` 文件），再用旧镜像启动：

   ```bash
   docker compose down
   cp pre-upgrade-<UTC时间>.db tiny-password.db
   rm -f tiny-password.db-wal tiny-password.db-shm
   # 旧镜像启动
   ```

## 重要限制

- **数据库副本 ≠ 备份**。任何 `tiny-password.db` 文件（含升级前快照）只包含加密负载，不含主密钥；它不能替代含密钥、可独立恢复的整实例加密归档（M5 备份/恢复流程）。
- **主密钥丢失不可恢复**。升级或回滚都不会重建或替换主密钥。
- **跨架构**：数据库文件与架构无关；amd64/arm64 镜像可互换升级。整实例迁移请使用实例备份/恢复流程（`tiny-password restore`）。

## 相关文档

- 部署与 Secret 准备：`docs/operations/deploy.md`（T29 交付）
- 备份与恢复：`docs/operations/backup-restore.md`（T29 交付）
- 验收证据：`docs/releases/v1-validation.md`
