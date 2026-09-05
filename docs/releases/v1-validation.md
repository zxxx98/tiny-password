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

## T06 · 登录、会话与密码生命周期

- 日期：2026-09-05；基于 M1 修复提交 `4d319cd`；执行环境为当前 Linux 工作区及 Docker 单容器。
- 交付：登录/退出、本人会话查询和撤销、自助改密、首次改密访问限制、默认 15 分钟闲置/24 小时绝对期限、5–30 分钟闲置偏好、显式用户活动续期。
- 密码校验使用 Argon2id；不存在用户执行 dummy 验证，错误密码和未知用户名返回相同错误。会话令牌为 32-byte CSPRNG，仅持久化 SHA-256；对外会话管理 ID 与凭据哈希分离。Cookie 默认 Secure/HttpOnly/SameSite=Lax。
- 登录消费预认证 CSRF 并发放会话绑定 token；改密在一个事务中比较旧哈希与当前会话有效性、撤销全部旧会话并发放新会话。GET 不续期，活动请求不能恢复过期/撤销会话。
- SQLite 原子限流覆盖用户名、直接来源地址与实例全局，默认每分钟 5/20/100 次，成功/失败请求均计数，重建服务实例不能清除窗口；改密也限流。转发头不参与来源判断，可信代理配置在 T07。
- `0002_auth_sessions.sql` 增量迁移保留既有账号和会话哈希，增加公开会话 ID、用户闲置偏好及限流索引。原始 `0001` 不变。
- 验证命令与结果：
  - `go test ./... -count=1`、`go vet ./...`：通过。
  - `go test -race -count=1 ./...`：全部通过，覆盖认证/会话/密码 HTTP 集成、假时钟过期、跨用户撤销拒绝、限流三维度及并发准入、旧库迁移。
  - 并发测试覆盖：登录与改密竞争、两次改密仅一次提交、验证期间撤销不能创建会话、轮换失败整体回滚、登录读取并发更新后的闲置偏好。最后一项在修复前明确失败，移入创建事务后通过。
  - `sudo -n docker build -t tiny-password:t06 .`：成功（包含前端构建）。
  - `IMAGE=tiny-password:t06 bash scripts/test-e2e.sh`：PASS；新增 `scripts/test-auth-http.py` 验证两次登录、改密后旧会话立即失效、新凭据登录和退出；M1 重启/令牌与严格并发 1×200 + 7×409 验收继续通过。
  - OpenAPI YAML 与全部本地 `$ref` 解析、`git diff --check`：通过。
- 证据路径：`internal/auth/concurrency_test.go`、`tests/integration/auth_test.go`、`tests/integration/auth_migration_test.go`、`scripts/test-auth-http.py`。
- 后续范围：登录/首次改密页面在 T15；通用 HTTP 安全与幂等在 T07；登录/退出/改密审计接入在 T08。会话列表当前返回全部本人有效会话，GET 查询不延长期限。

## T07 · HTTP 安全、中间件与幂等基础

- 日期：2026-09-05；提交 `311b287`；执行环境为当前 Linux 工作区（linux/arm64）。
- 交付：安全中间件链（CSP、HSTS 仅 TLS/可信代理 https、nosniff、Referrer-Policy、frame 限制、API no-store、panic 恢复 500 信封）、X-Request-ID 关联、路由模板访问日志、严格同源校验（修复旧后缀匹配可被 `https://evil.com//host` 绕过）+ Sec-Fetch-Site、可信代理网段解析（`TP_TRUSTED_PROXY_CIDRS`，默认不信任任何转发头）、413 体积上限、事务化幂等引擎与认证游标编解码器。
- 本轮裁决（合同/计划与实现的冲突）：幂等存储 SQL 放入 `internal/idempotency`（httpapi 不直接操作 SQL 的架构约束优先于 T07 文件清单的 `httpapi/idempotency.go`）；幂等 scope 内嵌操作者 ID 使跨用户命中结构上不可能；失败的创建释放键、成功的创建同事务固定键；回放先经 RequireSession/RequireAdmin 重验再按不透明资源 ID 渲染；游标 HMAC 密钥为进程内随机，重启后游标失效（瞬态分页状态，已文档化）；Origin 接受 http/https 两种 scheme（D02 允许 TLS 反代不经可信代理配置，后端无法可靠判定浏览器 scheme；Secure Cookie 完整性不受影响），主机必须精确匹配。
- 验证命令与结果：
  - `go test ./tests/integration -run 'TestSecurityHeaders|TestHSTS|TestOrigin|TestSecFetch|TestRequestBody|TestAccessLog|TestRequestID' -v`：通过（含 TLS/可信代理/伪造 XFP 的 HSTS 三态、9 组 Origin 用例、Sec-Fetch-Site、413 PAYLOAD_TOO_LARGE、日志含路由模板且无查询词/秘密/路径 ID/UA、请求 ID 头-信封-日志一致）。
  - `go test ./internal/idempotency ./internal/httpapi -v`：通过（fresh/complete/replay/conflict/in-flight/过期回收/跨用户不命中/claim 消失回滚/游标篡改-跨用户-过期-超长拒绝/代理解析多跳与伪造拒绝/panic 恢复不泄密）。
  - `go test ./tests/integration -run 'TestSetupIdempotent|TestSetupRejectsMalformed|TestIdempotentCreate' -v`：通过（setup 幂等重放 200 非 409、同键不同内容 409 IDEMPOTENCY_KEY_CONFLICT、合成路由跨用户隔离、撤销会话后重放 401、并发恰好一个资源）。
  - `go vet ./...`、`go test -race -count=1 ./...`、`git diff --check`：通过。
- 证据路径：`internal/httpapi/{middleware,origin,errors,cursor,idempotency}.go`、`internal/idempotency/service.go`、`internal/requestid/requestid.go`、`tests/integration/{http_security_test,idempotency_test}.go`。
- 原验收遗留问题（已在本文件 M2 审查修复节处理）：幂等指纹曾使用进程内随机密钥，单进程重启也会导致旧请求冲突；当前改由主密钥域隔离派生。游标仍为短期进程内密钥。旧随机密钥创建的记录只能等待原到期时间。

## T08 · 审计基础与个人活动

- 日期：2026-09-05；提交 `1d98bf9`。
- 交付：`internal/audit` 集中事件常量（setup/auth/user 家族，12 个允许列表事件）、字段上限与 fail-closed 校验、可注入故障的写入服务；登录成功/退出/会话撤销/改密与业务同事务提交（注入审计写失败 → 业务变更一并回滚，测试证明无孤儿会话）；登录失败独立记录已解析用户 ID 或 `anonymous`；`GET /auth/activity` 个人活动与 `GET /admin/audit` 系统审计（事件过滤经允许列表校验），均用 T07 认证游标分页（固定宽度 UTC 时间戳保证键集比较可靠）。
- 验证命令与结果：
  - `go test ./internal/audit -v`：允许列表拒绝未知事件名（含 `setup_token_issued` 类日志事件）、结果域限制、字段超限、故障注入、空 actor 归匿名。
  - `go test ./tests/integration -run TestAudit -v`：通过——登录成败/退出/撤销/改密审计与同事务完整性（含审计故障回滚）、个人活动只含本人事件、成员 403 系统审计、游标翻页无重叠且跨用户/篡改拒绝、删除用户后审计保留（用“内部 ID ≠ 用户名”夹具证明无用户名快照）、SYNSECRET 标记不出现在审计表与日志、初始化令牌全程仅出现 1 次且仅命中专用事件。
  - `go vet ./...`、`go test -race -count=1 ./...`、`git diff --check`：通过。
- 证据路径：`internal/audit/{events,service}.go`、`internal/httpapi/audit.go`、`tests/integration/audit_test.go`。
- 未解决问题：`audit_events.created_at` 存在 T08 之前的 RFC3339Nano 整秒行（仅 setup 事件），与新的固定宽度格式混排时键集排序对这些旧行不保证严格单调；仅影响历史少量行，不泄露、不丢数据，暂不迁移。

## T09 · 成员生命周期与最后管理员保护

- 日期：2026-09-05；提交 `70d4669`。
- 交付：`internal/users` 服务（创建/列表/禁用/启用/撤销会话/删除）+ `internal/httpapi/users.go` 管理端点；创建强制 member 角色、must_change_password=1、共享用户名规则与密码策略；最后管理员保护为写锁内的条件化单语句（禁用/删除同款），并发删除/禁用不能移除最后一个活跃管理员；禁止删除自己（先于确认比较）；删除要求精确匹配展示用户名，事务内级联会话（FK）、个人与本人创建共享条目（含 item_versions）、操作者绑定幂等记录，审计保留不透明 ID；新增 `POST /users/{id}/revoke-sessions` 并同步 OpenAPI；不提供角色变更、管理员重置密码或读取当前密码接口。
- 验证命令与结果：
  - `go test ./tests/integration -run 'TestUsers|TestMemberFirstLogin|TestLastAdmin' -v`：通过——member 访问全部管理端点 403、未认证 401、创建校验与大小写归一冲突 409、幂等创建重放同 ID/同键异内容 409/并发同键恰好 1 个成员、首登限制与改密解锁、禁用即时 401/禁用后登录 403/启用旧会话不复活、最后管理员自禁 409 LAST_ADMIN_PROTECTED、自删 403、服务级并发互删恰好 1 成功且剩 1 个活跃管理员、删除确认错误/大小写敏感 409、审计故障注入删除整体回滚、级联精确（被删者条目/历史/幂等清除，他人条目保留，审计保留且无用户名快照）、revoke-sessions 只杀会话不改状态。
  - `go vet ./...`、`go test -race -count=1 ./...`、`git diff --check`：通过。
- 证据路径：`internal/users/service.go`、`internal/httpapi/users.go`、`tests/integration/users_test.go`。
- 后续范围：删除页面的导出提示与用户名确认 UI 在 T15；真实条目 API 级联回归记入 T11（本轮用合成夹具）。

## T10 · 集中权限策略与完整矩阵

- 日期：2026-09-05；提交 `3f67f9c`。
- 交付：`internal/vault/policy.go` 集中策略——读写分离（`CanReadItem`/`Can`），个人条目属主独占（管理员无特权），共享条目全员可读但一切变更（update/trash/restore/purge/favorite/tag/history_restore）与 export 仅创建者（D06/D10）；创建归属声明必须与操作者一致；`ValidateImmutableOwnership` 拒绝更新转移 scope/owner/creator；`CanReference` 要求引用目标可读且可见性兼容（共享源只能引用共享目标）。`tests/fixtures/permissions.json` 为独立于实现推导的期望矩阵：80 行动作 × 角色/范围/关系、8 行创建、6 行引用、4 行不可变归属；`policy_test.go` 逐行断言（103 个子用例），并显式测试管理员对他人个人条目全动作拒绝。
- 验证命令与结果：`go test ./internal/vault -run TestPolicy -v` 全部通过；`go vet ./...`、全量 race 通过。
- 证据路径：`internal/vault/policy.go`、`internal/vault/policy_test.go`、`tests/fixtures/permissions.json`。
- **留给 T11/T17 的真实 API 越权回归（策略单元测试不替代 API 验收）：**
  1. T11：管理员调用 `GET /items/{memberItemId}`（个人）必须 NOT_FOUND；`PUT/DELETE /items/{id}`、history/history_restore、favorite、tags、purge 越权 403/404；猜 ID 批量探测不可区分存在性；创建时伪造 owner/creator/scope 被 `CanCreate`/服务层拒绝；更新 DTO 携带归属变更被拒；地址引用指向他人个人 identity 或个人条目引用不可读目标 → REFERENCE_FORBIDDEN。
  2. T17：成员 B/C 读取共享条目 ✓ 但 update/delete/restore/purge/favorite/tag/history_restore/export 创建者外全 403；管理员对他人共享条目同样不能写；批量操作与禁用创建者（数据保留、不可变更）、删除创建者（级联清除）下的权限一致性。

## M2 · 最终验收

- 日期：2026-09-05；提交序列 `311b287`（T07）→ `1d98bf9`（T08）→ `70d4669`（T09）→ `3f67f9c`（T10）→ 本文档提交。
- 验收项逐条结果：
  1. 从初始化管理员出发经真实 API 创建成员 ✓（`scripts/test-admin-http.py`：POST /users 201）。
  2. 成员首登必须改密 ✓（must_change_password=true；`/auth/activity` 403 PASSWORD_CHANGE_REQUIRED；`/auth/session` 放行；改密后解锁）。
  3. 管理员禁用成员立即使旧会话失效 ✓（disable 后成员会话 401，登录 403 ACCOUNT_DISABLED）。
  4. 启用后旧会话仍失效，必须重新登录 ✓（enable 后旧会话 401，新登录 200）。
  5. 普通成员不能管理用户或读取系统审计 ✓（GET/POST /users、GET /admin/audit 均 403）。
  6. 成员只能读取本人活动 ✓（activity 全部行 actor_id == 本人）。
  7. 成员创建幂等、权限撤销后回放拒绝 ✓（同键重放 201 同 ID；创建会话注销后重放 401；同键异内容 409 IDEMPOTENCY_KEY_CONFLICT）。
  8. 用户删除的确认、级联、审计与失败回滚 ✓（确认不符/大小写 409 CONFIRMATION_MISMATCH；审计故障注入 500 后用户与条目原样保留；正确删除后会话级联失效、审计保留不透明 ID 且无用户名快照）。
  9. 最后管理员并发保护 ✓（自禁 409 LAST_ADMIN_PROTECTED、自删 403；服务级并发互删恰好 1 成功、余 1 个活跃管理员）。
  10. 权限策略矩阵完整通过 ✓（103 子用例）。
  11. 合成秘密检查 ✓（SYNSECRET 标记注入请求体；容器日志、数据库与 WAL 文件、审计行、成员列表响应均无泄漏；初始化令牌仅 1 次专用事件）。
  12. M1/T06 既有测试继续通过 ✓（全量套件包含 setup/auth/migration/health 集成测试与容器 E2E 重启/并发验收）。
- 验证命令与结果（M2 收尾全量）：
  - `go vet ./...`：通过。
  - `go test ./... -count=1`：10 个包全部通过。
  - `go test -race -count=1 ./...`：全部通过。
  - OpenAPI 校验：`api/openapi.yaml` 解析通过；40 路径、30 schemas、195 处 `$ref` 全部可解析（新增 revoke-sessions 端点）。
  - `sudo -n docker build -t tiny-password:m2 .`：成功（包含前端 npm ci/typecheck/test/build 链路，本轮未改动前端源码）。
  - `IMAGE=tiny-password:m2 bash scripts/test-e2e.sh`：PASS——M1 初始化/重启/并发验收 + T06 认证流 + T09/M2 管理流 + 秘密扫描 + 并发初始化 1×200/7×409。
  - `git diff --check`：通过。
- 未运行检查：前端 Vitest/Playwright 未在本轮宿主机单独执行（未触及前端源码；镜像构建内含前端构建链）；真实 R2/浏览器 E2E 属后续里程碑范围。
- M2 退出门槛判定：**满足**——会话即时撤销、完整策略矩阵通过、成员生命周期与最后管理员保护经真实 API 验收；真实条目 API 防越权按计划留待 M3（T11/T17 清单见上）。

## M2 · 审查修复

- 日期：2026-09-05；本节记录 M2 最终验收后的四项修复。
- 成员写操作在事务首条语句获取 SQLite 写锁并重验当前会话、绝对/闲置期限、账号状态、首次改密限制及管理员角色。创建请求和幂等重放在读取请求体期间遭会话撤销均返回 401；失效主体不能创建、启用、禁用、撤销会话或删除成员。
- 重复禁用从当前事务读取完整 User；重复启用也从当前事务读取，避免持锁时另取连接查询。
- 系统审计游标的签名上下文包含 event；跨事件筛选复用游标返回 400。
- 幂等指纹密钥由实例主密钥以独立 HMAC 域派生；关闭并重新打开数据库、重建主密钥与服务后，相同请求仍重放原资源，不同请求仍冲突。缺少主密钥时不生成随机替代密钥，成员创建携带幂等键返回 503。
- 兼容边界：旧随机密钥生成的记录无法恢复指纹匹配，继续返回冲突直到原记录过期（默认成功记录 24 小时、未完成记录 10 分钟）；分页游标仍在进程重启后失效。重启回归覆盖数据库重开与服务重建，未在本轮重新运行容器重启测试。
- 验证：定向回归、`go test ./... -count=1`、`go vet ./...` 、`go test -race -count=1 ./...`、Go 格式检查与 `git diff --check` 全部通过（race 集成测试耗时 376.795 秒）。
- 回归有效性：在 `git archive HEAD` 创建的独立临时副本中运行新增测试，旧实现分别出现跨事件游标返回 200、重复禁用返回空 User、读取请求体期间撤销后仍创建成功（201）；当前修复版本的对应测试全部通过。
- 证据路径：`cmd/tiny-password/idempotency_test.go`、`tests/integration/users_revocation_test.go`、`tests/integration/audit_test.go`、`tests/integration/users_test.go`。
- 本轮未运行：Docker 构建/容器 E2E、前端测试及浏览器测试。
## T11 · 五类加密条目 CRUD

- 日期：2026-09-05
- 提交：`77c6f1f`（变基自 `71290b1`，worktree `tiny-password-m03`，分支 `m03-personal-vault`，基线 master `ddd60bd`）
- 执行环境：Ubuntu（linux/arm64），Go 工具链 go1.26.8
- 实现要点：
  - `internal/vault/`：types（DTO/加密封套/错误）、payloads（五类字段结构 + 上限校验）、validation（payload 分发解码、标签去重、封套 256 KiB 上限、引用提取）、repository（分列密文存取、键集分页、CAS 更新 + 同事务历史归档）、service（策略先行再解密、创建幂等 claim 同事务完成、审计 created/updated/viewed）。
  - 标签存于加密封套内（设计 §6.1），无明文列；列表只返回元数据，`tag` 筛选参数显式 400（属 T13 解密筛选管线）。
  - 越权语义：不可读目标一律 404（读/写皆然，无存在性预言）；可读不可写 403；密文篡改 500 INTERNAL 且响应不含明文。
  - crypto 包新增 `DecryptColumns`（version/nonce/ciphertext 三列组装解密，nonce 长度校验失败关闭）。
- 命令与结果：
  1. `go test ./tests/integration -run 'TestItem|TestUserDeleteCascade|TestUnauthorizedDecrypt' -v` → **10/10 通过**：五类有效 fixture（UUIDv7、revision=1、payload 逐字段往返、DB 明文列形状）与 24 个无效用例；幂等（重放同 ID、异内容 409、跨用户不互撞、8 并发恰 1 成功且 alice 恰 2 条个人条目）；越权（bob/管理员读个人条目 404、共享读 200/写 403、无 CSRF 403）；篡改密文 500 无泄漏；列表分页（默认序 (updated_at,id) 倒序、limit=3 走完 8 条、scope/type/favorite 筛选、跨用户与筛选不匹配游标 400）；更新（stale revision 409 带 current_revision、并发恰一成功、无变化不 bump 不写历史、tags 缺省保留、归属/类型走私 400、历史旧行按旧 AAD 可解密）；地址引用（本人 identity 201、他人个人/共享→个人/未知/空串/非 identity 统一 409、null 清除引用 201）；审计（created→updated→viewed 顺序、actor 为内部 ID、无 SYNSECRET 无用户名）；用户删除级联（真实 DELETE API 后 vault_items/item_versions 清空、他人读 404、审计保留内部 ID 且无用户名快照）。
  2. `go vet ./...` → 通过；`go test ./... -count=1` → 10 包全部通过；`go test -race -count=1 ./...` → 全部通过；`git diff --check` → 通过。
  3. 明文扫查：`SYNSECRET-pw-7f3a`/`-key`/`-note` 标记在 `app.db` 与 `app.db-wal` 原始字节中均不出现；审计表 GROUP_CONCAT 扫查无敏感内容。
- 证据路径：`internal/vault/`、`internal/httpapi/items.go`、`tests/integration/items_test.go`、`internal/audit/events.go`
- 未解决问题：
  - 列表 `tag` 筛选（OpenAPI 已声明）按计划归 T13 解密筛选管线，T11 显式拒绝；T13 接入后移除该限制。
  - 历史保留最近 10 版、回收站/恢复/清理入口属 T12；T11 更新已同事务归档旧版本，T12 的 12 连更用例将在其上验证保留策略。
  - M2 遗留清单（管理员读成员个人条目 404、猜 ID、批量探测、删除级联真实 API）本轮已全部覆盖并关闭。

