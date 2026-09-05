# V1 验证证据

按行动计划 §12 规范追加：任务 ID、提交 SHA、执行环境、命令、结果、证据路径、未解决问题。所有输出脱敏。

## T01 · 技术探针、决策与接口合同

- 日期：2026-09-05
- 提交：`0c5c26c`
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

## T02 · Go/React 工程与最小容器

- 日期：2026-09-05
- 提交：`a4f345a`
- 命令与结果：
  1. `npm --prefix web ci` → 0 vulnerabilities；`npm run typecheck` → 通过；`npm test -- --run` → 1/1 通过；`npm run build` → 产物写入 `internal/webassets/dist/`
  2. `go vet ./... && go test ./...` → 通过（`tests/integration/health_test.go`：/healthz 200、未知 API JSON 404 含 request_id、SPA 路由 200 text/html、无存储依赖时 /healthz 存活）
  3. `docker build -t tiny-password:dev .` → 成功；镜像 163MB；`id -u` = 10001（非 root）；镜像内 `7zz` 为官方 26.03
  4. **`bash scripts/probes/archive-password.sh` 在生产镜像内复跑（SEVENZIP_BIN=/usr/local/bin/7zz）→ 25/25 通过**
  5. `bash scripts/test-e2e.sh` → PASS（/healthz 就绪、未知 API JSON 404、SPA 路由服务应用、退出清理）
  6. `docker compose config --quiet` 与 `TP_TEST_DATA_DIR=… compose -f deploy/compose.test.yaml config --quiet` → 通过
- 证据路径：`Dockerfile`、`compose.yaml`、`deploy/compose.test.yaml`、`Makefile`、`.github/workflows/ci.yaml`、`scripts/test-e2e.sh`
- 未解决问题：无（alpine 运行时与官方 7zz 二进制不兼容一事已在 0001/0002 记录并决策为 debian-slim 基座）

## T03 · SQLite、迁移和就绪状态

- 日期：2026-09-05
- 执行环境：同 T02；容器级验证使用 tiny-password:dev 镜像
- 命令与结果：
  1. `go test ./internal/platform/sqlite ./tests/integration -run 'Test(Database|Migration|Ready|Snapshot|Migrate|Readyz)' -v` → 全部通过（迁移幂等、失败迁移事务回滚且版本不记录、foreign keys 逐连接生效、busy 错误分类、并发写入下快照一致性、scope/owner CHECK 组合、username_norm 唯一、审计事件在用户删除后保留、会话级联、readyz 三态）
  2. `go test -race ./internal/platform/sqlite ./tests/integration` → 通过
  3. 容器级：`/readyz` 返回 `{"checks":{"database":true,"migrations":true},"status":"ready"}`；`/data` 属主 app:app（镜像内预建 + chown，命名卷继承属主）
- 证据路径：`internal/platform/sqlite/`、`migrations/0001_initial.sql`、`internal/httpapi/health.go`、`tests/integration/migrations_test.go`
- 未解决问题：无（修复过程：/data 卷属主导致 SQLite 无法建库 → Dockerfile 预建目录并 chown；/readyz 曾落入 SPA 分流 → 顶层路由补 /readyz）

## T04 · 密码哈希、主密钥与负载加密

- 日期：2026-09-05
- 命令与结果：
  1. `go test ./internal/auth ./internal/platform/crypto ./internal/platform/config -v` → 全部通过（正确/错误/Unicode 密码、12 字符下限、1024 字节上限、PHC 参数持久化、畸形哈希与超限参数拒绝、主密钥 32 字节校验与 Docker Secrets 0444 模式兼容、AAD 五字段逐项篡改失败、密文/nonce/版本篡改失败、100 次加密无 nonce 复用、跨 revision AAD 拒绝、marker 换钥检测）
  2. `go test ./internal/auth -run '^$' -bench BenchmarkPasswordHash -benchmem` → 115 ms/op、67 MB/op（64 MiB/3/2，本机 arm64；250–500 ms 双架构调参在 T30）
  3. 容器级：无密钥 `/readyz`=503（master_key:false，日志出现 master key unavailable）；有合成密钥（只读挂载）`/readyz`=200
- 证据路径：`internal/auth/password.go`、`internal/platform/config/secrets.go`、`internal/platform/crypto/{payload,aad}.go`、`internal/bootstrap/keys.go`
- 未解决问题：曾实现"拒绝组/其他可读的密钥文件"，与 Docker Secrets 标准 0444 挂载冲突 → 已撤销该检查（属主/权限属运维责任），以 0444 兼容性测试固化

## T05 · 一次性初始化闭环

- 日期：2026-09-05
- 命令与结果：
  1. `go test ./tests/integration -run TestSetup -v` → 11/11 通过：状态查询与令牌仅记录 1 次、缺/错令牌 401 SETUP_TOKEN_INVALID、并发 20 恰好 1 成功且库里恰好 1 个管理员、重启后 setup 保持关闭且不再发令牌、用户名冲突事务回滚后可换名重试、用户名/密码校验 400、无 CSRF 403、跨域 Origin 403、限流 429、令牌值不入审计
  2. `npm --prefix web test -- --run` → 6/6 通过（SetupPage：持久标签、autocomplete 策略、已初始化提示、成功后登录引导、令牌错误状态、密码不一致拦截）
  3. `go test -race ./...` → 全部通过
  4. **容器级 M1 验收（scripts/test-e2e.sh）→ PASS**：日志提取 43 字符令牌 → 初始化 200 → 状态 initialized:true → 二次初始化 409 → 容器重启后初始化仍 409 且令牌全程仅输出 1 次 → 8 个并发初始化恰好 1 个成功
- 证据路径：`internal/bootstrap/{service,token,keys}.go`、`internal/httpapi/{setup,csrf}.go`、`web/src/features/auth/SetupPage.tsx`、`tests/integration/setup_test.go`、`scripts/test-e2e.sh`
- 未解决问题：
  - `/api/v1/csrf` 与 setup 共用限流器曾导致并发验收被 429 干扰 → 已拆分为独立限流器（csrf 30/min、setup 10/min，后者可经 `TP_SETUP_RATE_LIMIT_PER_MIN` 调整）。
  - 预认证 CSRF 上下文存于进程内存，多进程部署不适用（V1 单进程单容器，与设计一致）。

## M1 review 修复 · 主密钥、Cookie 与并发验收

- 日期：2026-09-05；基于 `b68b7fd` 的工作区修复，尚未提交。
- 主密钥缺失时初始化返回 `503 MAINTENANCE`，不创建用户、不写成功审计、不关闭入口；挂载有效密钥重启后可正常初始化。已初始化但缺少 marker 的实例拒绝 ready，不自动补写 marker 或接受替代密钥。
- 预认证 Cookie 默认带 `Secure`，包括 TLS 反代后的 HTTP 后端连接；仅 `TP_ALLOW_INSECURE_COOKIES=1` 允许开发/评估模式关闭。容器 HTTP 测试显式开启此开关，生产保持未设置。
- 容器并发请求各自使用受限临时目录保存 Cookie 和响应，结果不再共用全局文件；严格要求 1 个 200 和 7 个 409，任何 403/429/500 均使验收失败。
- 回归证据：新增测试在修复前复现无密钥初始化成功、缺 marker 仍接受密钥、HTTP Cookie 不带 Secure；修复后全部通过。
- 验证命令与结果：
  - `go vet ./...`、`go test -race -count=1 ./...`：全部通过。
  - 前端 `typecheck`、`test -- --run`（6/6）、`build`：全部通过。
  - `bash -n scripts/test-e2e.sh`、`git diff --check`：通过。
  - `sudo -n docker build -t tiny-password:review-fixes .`：成功。
  - `IMAGE=tiny-password:review-fixes bash scripts/test-e2e.sh`：PASS；初始化成功，重启后仍返回 409 且不重发令牌，并发结果恰好 1 个 200、7 个 409。
- 证据路径：`tests/integration/setup_key_test.go`、`internal/httpapi/csrf_test.go`、`internal/platform/config/http_test.go`、`scripts/test-e2e.sh`。
