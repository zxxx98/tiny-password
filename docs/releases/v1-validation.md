# V1 验证证据

按行动计划 §12 规范追加：任务 ID、提交 SHA、执行环境、命令、结果、证据路径、未解决问题。所有输出脱敏。

## T01 · 技术探针、决策与接口合同

- 日期：2026-09-05
- 执行环境：Ubuntu（linux/arm64，内核 6.17.0-oracle），Go 1.24.13 + 自动获取的 go1.26.8 工具链，Docker 29.3.1 / Compose v5.1.1，Node 22.22.2
- 命令与结果：
  1. `sudo docker run --rm -v $PWD:/repo -v /tmp/tp-probe:/probe -w /repo -e SEVENZIP_BIN=/probe/7zz debian:bookworm-slim bash scripts/probes/archive-password.sh` → **25/25 通过，exit 0**（真实容器内）
  2. `bash scripts/probes/archive-password.sh`（宿主机，验证下载 + SHA256 校验路径）→ **25/25 通过，exit 0**
  3. `bash scripts/probes/sqlite-backup.sh`（内部执行 `go vet .` + `go run .`）→ **全部断言通过，exit 0**：modernc.org/sqlite v1.58.0 / SQLite 3.53.4；备份 API 与 VACUUM INTO 两种快照均在 8 个并发写入者活动期间生成（备份 API 10.2 ms / VACUUM INTO 9.2 ms，rows=2000）；integrity_check=ok；事务不变量成立；0 硬错误、0 SQLITE_BUSY
  4. OpenAPI 校验：YAML 解析通过；36 路径、29 schemas；41 处 `$ref` 全部可解析
- 证据路径：`docs/decisions/0001-v1-boundaries.md`（§13 探针记录）、`docs/decisions/0002-dependencies.md`、`api/openapi.yaml`、`scripts/probes/`
- 未解决问题：
  - 7-Zip 发布页无权威 SHA256 文件，0002 中校验值由本次下载产物计算，T30/T31 需对照独立来源复核。
  - 备份 API 快照继承源库 WAL 头标志，需以 `journal_mode=DELETE` 归一化（探针已验证可行）；超大库上该步骤耗时待 T21 实测。
  - debian:bookworm-slim 无 curl/xz，容器内探针用 `SEVENZIP_BIN` 注入宿主下载的官方二进制（与生产镜像供给方式一致）；Dockerfile 构建期下载校验在 T02 实现。
