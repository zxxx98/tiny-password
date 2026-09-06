# Tiny Password V1 Implementation Plan · 开发行动计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. 如用户明确选择代理分工，可使用 superpowers:subagent-driven-development。Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付可通过 Docker 自托管、支持个人与家庭共享保险库、可靠加密备份恢复的 Tiny Password V1。

**Architecture:** Go 模块化单体提供同源 REST API 并嵌入 React 静态资源，SQLite WAL 保存账号、授权元数据和加密负载。授权、加密、归档、调度通过服务接口隔离；Cloudflare R2 和 Tunnel 均为可选适配器，不成为核心运行依赖。

**Tech Stack:** Go 当前稳定版、React 19、TypeScript、Vite、Tailwind CSS、SQLite、Argon2id、XChaCha20-Poly1305、7-Zip CLI、S3/R2、Docker Compose、Vitest/Testing Library、Playwright。

---

## 1. 依据、现状与执行方式

- 产品依据：[完整设计文档](../specs/2026-09-04-tiny-password-design.md)，已覆盖阅读第 1–19 节。
- 视觉依据：[style.md](../../../style.md)。产品设计第 12 节对密码工作区的具体约束优先于风格参考中的营销页示例。
- 编写日期：2026-09-05。
- 仓库现状：只有设计文档与 `style.md`，尚无 Go、React、数据库、容器或测试工程。下文代码路径均为**计划新增**，后续任务对先前建立文件的变更标记为“修改”。
- 本文交付行动拆解、接口边界、测试场景和发布门槛；所有未勾选项均尚未执行，命令是未来验收命令，不代表当前仓库已能运行。
- `style.md` 当前为未跟踪文件，执行者不得覆盖其内容，也不得在无关任务中顺带提交。

按六个里程碑组织一份总行动文档，便于追踪完整 V1。每个任务作为一个可独立评审的变更单元，包含明确输入、输出和验证；备份恢复等高风险任务先验证技术可行性，再接入产品。进入具体实现时，按任务中的行为用例逐个完成“失败测试 → 最小实现 → 通过测试”，单次工作片段控制在约 2–5 分钟；复杂任务可以拆成多次提交，不把整个里程碑压成一次提交。

本计划不加入零知识、离线保险库、附件、第三方格式迁移、TOTP、Passkey、浏览器扩展、多组织或横向扩容。设计第 19 节只作为未来候选，不提前开发。

## 2. 先收敛的实现边界

以下是对设计空白和冲突的**建议落地规则**，不是已完成的技术验证。T01 将其写成决策记录；若需要改变已确认的产品范围，应先修改设计再执行依赖任务。

| 编号 | 问题 | 建议规则与验证入口 |
| --- | --- | --- |
| D01 | 设计要求敏感值不入日志，但初始化令牌必须输出到日志 | 初始化令牌是唯一显式例外，只在未初始化状态下输出；完成后销毁且永不再输出。常规日志扫描允许列表必须只匹配该事件，不能放行任意 token。T05/T30 验证。 |
| D02 | Secure Cookie 与普通局域网 HTTP 不兼容 | 发布部署必须有浏览器可接受的 HTTPS；Tunnel 提供公网 HTTPS，纯内网文档给出已有 TLS 反代接入和可信证书步骤。仅映射 HTTP 端口不足以完成生产登录。开发放宽只能显式启用、默认关闭。T02/T29 验证。 |
| D03 | 只读 Docker Secret 主密钥，恢复不能覆盖挂载文件 | 恢复使用源密钥解密、目标密钥重加密，永不写 Secret。恢复前副本与候选库保留到迁移、检查及提交完成；失败回到原库。T25 验证。 |
| D04 | 7-Zip CLI 能否在指定版本安全接收口令 | T01 在真实容器中验证创建、测试、读取加密文件名归档的 PTY/stdin 行为，关闭终端回显，检查 `/proc` 参数和错误输出。禁止退化为 `-p明文口令`。不通过则阻断 T19–T25 并重新选择兼容方式。 |
| D05 | 防止删光管理员；设计未列角色修改/管理员重置 | V1 不提供角色变更或管理员重置密码接口；拒绝删除/禁用最后一个可用管理员，以事务保证并发安全。删除自己建议禁止，账户页面保留退出和自助改密。T09 验证。 |
| D06 | 收藏、标签是否允许读者修改共享条目 | 收藏和标签属于条目，按“共享创建者唯一写入者”处理；非创建者只读。V1 不新增个人共享收藏映射表。T10/T17 验证。 |
| D07 | 初始化和登录也是写请求，尚无已登录会话 | 发放短期预认证 CSRF 上下文，写请求同时校验 token 与 Origin；登录成功轮换上下文。初始化仍必须验证一次性令牌。T05/T07 验证。 |
| D08 | UI 有个人活动页，API 资源列表未单列 | 增加 `GET /api/v1/auth/activity`，只返回当前操作者自身的脱敏事件；系统审计仅管理员可读。T08/T15 验证。 |
| D09 | 字段大小、锁定调整范围、调度 DST 未量化 | T01 固化可测试上限；建议密码最多 1024 UTF-8 字节、条目 JSON 最多 256 KiB、用户闲置期限 5–30 分钟。DST 跳过时刻在下个有效时刻执行，重复时刻每天只执行一次；停机漏跑恢复后补一次。 |
| D10 | 个人归档的历史/回收站范围与地址引用未定义 | V1 默认导出未删除条目的当前版本，包含本人个人条目及本人创建共享条目，不携带历史/回收站。导入统一重映射引用；未包含的地址引用需用户补全，不携带他人个人数据。UI 明示范围。T20 验证。 |
| D11 | 所有敏感内容不持久化与归档所需临时源文件存在张力 | 含明文个人导出 JSON、主密钥的中间文件放入受限 tmpfs，目录 0700、文件 0600；用大小/并发上限防 OOM，失败不回退普通磁盘。说明主机 swap、内存及运维属于信任边界。T19/T20/T21 验证。 |
| D12 | style.md 在线字体示例与本地独立运行冲突 | 保留指定字体和许可证，在镜像中自托管字体，配置系统字体回退；不引入 Google Fonts 网络依赖。PWA 采用动态内容全禁缓存策略，V1 无头像缓存需求。T14/T28 验证。 |

## 3. 交付顺序与里程碑

| 阶段 | 任务 | 前置 | 可演示产物 | 阶段退出门槛 |
| --- | --- | --- | --- | --- |
| M1 基础与初始化 | T01–T05 | 无 | 单容器空实例与初始化页 | 并发初始化只成功一次，重启后永久关闭入口 |
| M2 认证与用户管理 | T06–T10 | M1 | 登录、改密、成员管理、权限基础 | 会话即时撤销；完整策略矩阵通过；真实条目 API 防越权在 M3 再验 |
| M3 个人保险库 | T11–T16 | M2 | 五类条目与桌面/移动工作区 | 五类完整生命周期，重启可解密，搜索与历史隔离 |
| M4 共享、生成与个人迁移 | T17–T20 | M3；T19 依赖 T01 探针 | 双成员共享、三类生成器、个人归档 | 创建者唯一写入，导入预览与整体事务通过 |
| M5 实例备份与恢复 | T21–T27 | T19、M4 | 本地/R2 备份、调度、恢复命令 | 本地和 R2 各完成新密钥干净实例恢复，故障不破坏原库 |
| M6 PWA、Tunnel 与发布 | T28–T31 | M1–M5 | 安装版 PWA、双架构镜像、运维文档 | 安全、性能、恢复、移动及发布清单全部有证据 |

关键依赖链：`T01 → T02 → T03/T04 → T05 → T06/T07 → T10 → T11 → T12 → T19/T20 → T21 → T25/T26 → T30 → T31`。

可交错推进的工作：T14 视觉基础在 T02 后即可进行；T18 随机生成器在 T04 后即可进行；T19 归档适配器在 T01 探针通过后即可进行；T23 调度纯逻辑可先使用假时钟开发。接口与共享迁移变更必须串行整合。这里描述依赖关系，不自动安排代理或多人同时修改仓库。

## 4. 目录与职责

| 路径 | 职责 |
| --- | --- |
| `cmd/tiny-password/main.go` | 启动 HTTP 服务或离线 restore 子命令，只负责装配 |
| `internal/platform/config/` | Secret 文件、配置加载与启动校验 |
| `internal/platform/sqlite/` | 连接池、迁移、Online Backup、完整性检查 |
| `internal/platform/crypto/` | 负载加密、AAD、主密钥加载 |
| `internal/platform/archive/` | 受限 7-Zip 子进程、临时文件、归档结构校验 |
| `internal/platform/objectstore/` | R2/S3 上传、验证、复制提交、删除与重试 |
| `internal/httpapi/` | 路由、中间件、DTO、稳定错误码；不直接操作 SQL/加密/7z |
| `internal/bootstrap/`、`internal/auth/`、`internal/users/` | 初始化、认证会话、账号生命周期 |
| `internal/vault/` | 授权、五类负载、CRUD、历史、搜索、回收站、健康 |
| `internal/generator/`、`internal/transfer/` | 生成器、个人归档及预览确认 |
| `internal/backup/`、`internal/scheduler/` | 备份目标状态、恢复、调度、保留 |
| `internal/audit/`、`internal/settings/` | 脱敏审计与非敏感设置 |
| `internal/webassets/` | `go:embed` 静态构建产物，SPA fallback 排除 API |
| `migrations/` | 按版本排序的显式 SQL 迁移 |
| `web/src/app/`、`web/src/design-system/` | 路由、内存态会话、错误边界、Newsprint 原语 |
| `web/src/features/{auth,vault,generator,transfer,admin}/` | 按业务组织页面、接口调用及同目录测试 |
| `api/openapi.yaml` | API、错误码、CSRF、revision、幂等与归档协议 |
| `tests/integration/`、`tests/e2e/`、`tests/fixtures/` | API/恢复集成、浏览器流程、纯合成数据 |
| `scripts/`、`deploy/`、`.github/workflows/` | 可复现检查、部署样例和 CI |
| `docs/decisions/`、`docs/operations/`、`docs/releases/` | 决策、部署恢复说明、验收证据 |

服务接口以 `context.Context`、当前主体、明确输入结构为边界；时间、随机源、对象存储和文件系统故障可注入。权限逻辑只在 `internal/vault/policy.go` 定义，数据库先缩小授权候选集，再由服务校验，最后才解密。

基础 API 合同示例（T01 写入 OpenAPI）：

```json
{"code":"REVISION_CONFLICT","message":"条目已更新，请重新加载","request_id":"opaque-id","current_revision":8}
```

创建接口带 `Idempotency-Key`，同用户、同操作、同 key 和相同请求返回同一结果；换用户不得命中，同 key 不同内容返回冲突。若持久化请求指纹，应使用带密钥 HMAC；回放记录只存不透明资源 ID 等非敏感信息，不存明文负载和生成结果。游标采用认证封装并绑定用户、筛选条件、排序及有效期，防篡改与跨用户复用。

## 5. M1：基础与初始化

### T01 · 技术探针、决策与接口合同

**新增：** `docs/decisions/0001-v1-boundaries.md`、`docs/decisions/0002-dependencies.md`、`api/openapi.yaml`、`scripts/probes/archive-password.sh`、`scripts/probes/sqlite-backup.sh`。

- [x] 将 D01–D12 写入决策记录，区分设计原要求、建议默认值和探针结论。（2026-09-05：`docs/decisions/0001-v1-boundaries.md`）
- [x] 锁定 Go、Node、SQLite 驱动和 7-Zip 的版本及校验值；选择的 SQLite 驱动必须暴露可用的一致性备份能力，并能在 amd64/arm64 运行。（`docs/decisions/0002-dependencies.md`：Go 1.25/go1.26.8 工具链、Node 22、modernc.org/sqlite v1.58.0（内嵌 SQLite 3.53.4，暴露备份 API，纯 Go 双架构）、7-Zip 26.03 双架构 SHA256）
- [x] 用合成秘密验证 7z 创建/列目录/解密/校验、文件名加密、错误密码、取消、无 TTY、Unicode 口令及终端不回显；记录进程参数检查结果。测试脚本只读临时文件，不把合成口令拼进命令行。（`scripts/probes/archive-password.sh`：容器内 25/25 通过。关键结论：创建必须带空 `-p`，不带会静默生成未加密归档；列表/解压必须省略 `-p`；口令经 stdin 管道单行传递，不出现在 argv/environ/输出；PTY 方式回显泄漏已弃用）
- [x] 在 SQLite 持续写入期间生成快照，打开副本执行 `PRAGMA integrity_check` 并核对事务一致性。（`scripts/probes/sqlite-backup.sh`：备份 API 与 VACUUM INTO 双方法在 8 写入者活动期间快照，integrity_check=ok、事务不变量成立、0 硬错误。快照继承 WAL 标志，需归一化 journal_mode=DELETE）
- [x] 定义五类负载每个字段的类型、必填、字节/数量上限；定义上传/展开总量、文件数、耗时、并发和 API 错误码。（D09 上限与错误码枚举固化为 `api/openapi.yaml` schemas；个人归档压缩体 64 MiB、展开 128 MiB、512 文件、120 s 超时；实例归档上限显式配置）
- [x] 将所有资源、请求响应、分页、CSRF、权限、幂等和导入两阶段协议写入 OpenAPI；地址引用只能指向当前主体可读取且与共享可见性兼容的 identity 条目。（`api/openapi.yaml`：36 路径、29 schemas、稳定错误码枚举、游标/幂等/CSRF 约定，YAML 与引用校验通过）

**验证：** `bash scripts/probes/archive-password.sh` 与 `bash scripts/probes/sqlite-backup.sh` 均退出 0；决策记录含实际版本与输出摘要。不通过不得进入依赖其能力的任务。

**建议提交：** `docs: define v1 contracts and validate storage primitives`。

### T02 · Go/React 工程与最小容器

**新增：** `go.mod`、`go.sum`、`cmd/tiny-password/main.go`、`internal/httpapi/router.go`、`internal/webassets/embed.go`、`web/package.json`、`web/package-lock.json`、`web/vite.config.ts`、`web/tsconfig.json`、`web/src/main.tsx`、`Dockerfile`、`compose.yaml`、`.dockerignore`、`.gitignore`、`Makefile`、`scripts/test-e2e.sh`、`.github/workflows/ci.yaml`、`tests/integration/health_test.go`。

- [x] 建立 React 19/TypeScript/Vite 与 Go module，安装并锁定测试依赖；定义 `test`、`typecheck`、`build` npm scripts。（React 19.2/Vite 7/Tailwind 4/Vitest 3，锁定于 package-lock.json；go.mod `go 1.25` + toolchain go1.26.8）
- [x] 建立静态资源构建到 `internal/webassets/dist/` 的明确流程；Go 测试前先生成资源，避免 embed 路径不存在。（vite outDir 指向 dist；dist/.keep 入库保证 go:embed 恒可编译，未构建时 SPA 回退提示占位页）
- [x] 先验证 `/healthz` 返回存活响应、未知 API 返回 JSON 404、SPA 路由返回应用入口。（`tests/integration/health_test.go` 4 用例通过；容器级 `scripts/test-e2e.sh` 同样断言通过）
- [x] 构建多阶段镜像，非 root 用户启动；只要求 `/data` 持久化，Secret 只读，敏感临时文件使用受限 tmpfs。（UID 10001 实测；镜像 163MB；运行时基座 debian:bookworm-slim——官方 7zz 26.03 为 glibc 链接，alpine+gcompat 缺 `pthread_attr_setaffinity_np` 不可行，探针已在生产镜像内复跑 25/25）
- [x] 配置 CI 执行前端类型检查/测试/构建与 Go 测试；`make test-e2e` 调用独立测试 Compose，准备合成 Secret 与空库，退出时清理。（.github/workflows/ci.yaml；test-e2e 使用临时空数据卷 + tmpfs，EXIT trap 清理；Secret 挂载在 T04 接入配置后加入测试 compose）

**验证：** `npm --prefix web ci`、`npm --prefix web run build`、`go test ./...`、`docker build -t tiny-password:dev .`；容器 `/healthz` 为 200，进程 UID 非 0，健康路由不读取保险库。

**建议提交：** `build: scaffold go react and container pipeline`。

### T03 · SQLite、迁移和就绪状态

**新增：** `internal/platform/sqlite/{db,migrate,backup}.go`、`internal/platform/sqlite/db_test.go`、`migrations/0001_initial.sql`、`internal/httpapi/health.go`、`tests/integration/migrations_test.go`。

- [x] 先写空库迁移、重复启动、错误迁移回滚、连接级 foreign keys、busy timeout 和一致性快照用例。（db_test.go 5 用例 + migrations_test.go 7 用例；失败迁移事务回滚且版本不记录）
- [x] 建立设计第 9 节十张核心表，CHECK 验证范围/属主组合，UTC 时间，用户名标准化唯一；审计操作者保留不透明 ID，不受用户删除级联清除。（migrations/0001_initial.sql：system_state/users/sessions/login_attempts/vault_items/item_versions/audit_events/app_settings/backup_jobs/backup_runs + schema_migrations + idempotency_keys；测试覆盖 scope/owner 组合、username_norm 唯一、审计事件在用户删除后保留、会话级联撤销）
- [x] 在初始化迁移中增加幂等记录所需表；只存作用域、HMAC 指纹、资源 ID、到期时间，记录该小幅模型补充。（idempotency_keys：PRIMARY KEY(scope,key)，fingerprint/resource_id/status/expires_at）
- [x] 配置 WAL、每条连接的 foreign keys、5000 ms busy timeout；写忙返回可重试 `DATABASE_BUSY`。（DSN 级 _pragma 逐连接生效；`sqlite.IsBusy` 供服务层映射 DATABASE_BUSY 错误码；测试验证 busy 错误分类）
- [x] `/readyz` 检查迁移完成、数据库可用和主密钥校验状态；失败为 503。提供低峰 checkpoint 入口，后续由调度调用。（ReadyChecker 按注册校验器输出 checks；主密钥校验器 T04 接入；DB.Checkpoint/IntegrityCheck 已就绪）

**验证（2026-09-05，实际执行）：** `go test ./internal/platform/sqlite ./tests/integration -run 'Test(Database|Migration|Ready|Snapshot|Migrate|Readyz)' -v` 全部通过；`go test -race` 通过；容器内 /readyz 返回 `{"checks":{"database":true,"migrations":true},"status":"ready"}`；镜像内 /data 属主 app:app 0750（修复：命名卷权限）。

**验证：** `go test ./internal/platform/sqlite ./tests/integration -run 'Test(Database|Migration|Ready|Snapshot)' -v`；覆盖重启和长事务竞争，非法约束写入被拒绝。

**建议提交：** `feat: add sqlite schema migrations and readiness`。

### T04 · 密码哈希、主密钥与负载加密

**新增：** `internal/auth/password.go`、`internal/auth/password_test.go`、`internal/platform/config/secrets.go`、`internal/platform/crypto/{payload,aad}.go`、`internal/platform/crypto/payload_test.go`。

- [x] 先写正确/错误密码、Unicode、12 字符下限、最大字节上限、哈希参数解析和畸形哈希测试。（password_test.go：含 PHC 参数持久化、参数上限拒绝）
- [x] 实现 Argon2id 基线 64 MiB/3/2，逐哈希保存参数；哈希解析也限制恶意超大参数，限制并行哈希工作数量。（解析上限 1 GiB/64 次/16 线程；信号量限制并发 ≤4×64 MiB；本机 arm64 基准 115 ms/op，250–500 ms 调参在 T30）
- [x] 只从挂载文件读取 32-byte 主密钥；缺失、长度错误或与既有库验证标记不匹配时拒绝 ready，不自动生成替代密钥。（config.ReadMasterKeyFile + bootstrap.MasterKeyCheck 容器实测：无密钥 503、合成密钥 ready；marker 为 HMAC-SHA256 域分隔校验值，T05 初始化时写入 system_state）
- [x] 实现版本化 XChaCha20-Poly1305；AAD 采用无歧义编码绑定 ID、scope、owner/creator、payload_version、revision。（wire 格式 ver(2B)+nonce(24B)+ciphertext；AAD 域分隔 + 长度前缀编码）
- [x] 对每个 AAD 字段、nonce、密文逐项篡改，确认解密失败；独立随机 24-byte nonce，历史记录保留其自身 revision/AAD。（payload_test.go 全部通过；100 次加密无 nonce 复用；跨 revision AAD 拒绝）

**验证（2026-09-05，实际执行）：** `go test ./internal/auth ./internal/platform/crypto ./internal/platform/config -v` 全部通过；`go test -race ./internal/...` 通过；`go test ./internal/auth -run '^$' -bench BenchmarkPasswordHash -benchmem` → 115 ms/op 67 MB/op（64 MiB/3/2）；容器级：无密钥 readyz=503、有合成密钥 readyz=200。

**验证：** `go test ./internal/auth ./internal/platform/crypto -v`；`go test ./internal/auth -run '^$' -bench BenchmarkPasswordHash -benchmem` 记录耗时，双架构 250–500 ms 调参在 T30 完成。

**建议提交：** `feat: add password hashing and authenticated encryption`。

### T05 · 一次性初始化闭环

**新增：** `internal/bootstrap/{service,token}.go`、`internal/httpapi/setup.go`、`web/src/features/auth/SetupPage.tsx`、`tests/integration/setup_test.go`、`web/src/features/auth/SetupPage.test.tsx`。

- [x] 先写无令牌、错误令牌、并发 20 请求、完成后重启和数据库错误回滚测试。（setup_test.go 11 用例：缺/错令牌 401、并发 20 恰好 1 成功、重启后 409、用户名冲突事务回滚后可重试、CSRF/Origin/限流、令牌不入审计）
- [x] 空实例生成高熵令牌，只将校验材料留在进程内，按 D01 输出一次；已初始化实例不生成令牌。（32 字节 CSPRNG base64url≈43 字符；进程内存仅存 SHA-256 摘要；测试断言日志恰好 1 次、重启 0 次）
- [x] 一个事务创建管理员并写初始化完成状态，用数据库条件保证只有一个成功；成功销毁令牌校验材料。（`UPDATE system_state SET value='1' WHERE key='initialized' AND value='0'` 的 RowsAffected==1 是唯一胜者；同时写入主密钥 marker 与 setup.success 审计）
- [x] 加入预认证 CSRF、频率/体积限制、安全错误码；完成后 setup 写入口固定关闭。（D07：`POST /api/v1/csrf` 发放匿名上下文，Cookie 与 token 分离；setup/init 10 次/分钟、16 KiB body 上限、稳定错误码枚举）
- [x] 初始化页有持久标签、令牌错误状态、提交中状态与成功后登录引导，禁用浏览器表单中秘密的非必要持久化。（SetupPage.tsx：label/for 关联、role=alert 错误摘要、disabled 提交态、token autocomplete=off、密码 new-password）

**验证（2026-09-05，实际执行）：** `go test ./tests/integration -run TestSetup -v` 11/11 通过；`go test -race ./...` 通过；`npm --prefix web test -- --run` 6/6 通过。容器级（scripts/test-e2e.sh）：日志取令牌→初始化 200→状态 initialized:true→再次初始化 409→容器重启后仍 409 且令牌全程仅输出 1 次→8 个并发初始化恰好 1 个成功。

**建议提交：** `feat: implement one time administrator setup`。

## 6. M2：认证与用户管理

### T06 · 登录、会话、锁定和改密

**新增：** `internal/auth/{service,sessions,limiter}.go`、`internal/httpapi/auth.go`、`tests/integration/auth_test.go`。

- [x] 先写不存在用户/错误密码相同响应、禁用用户、首次改密限制、闲置过期、24 小时绝对过期和全维度限流测试。
- [x] 生成至少 32-byte 随机会话 ID，数据库仅保存哈希；Cookie 限定路径并设置 HttpOnly、Secure、SameSite=Lax。
- [x] 默认闲置 15 分钟，按 D09 限制调整；自动轮询不得无限续期，绝对期限不因活动延长。
- [x] 首次登录只放行本人改密、会话信息和退出；提供当前密码完成自助改密后撤销全部旧会话。
- [x] 成员只能列举和撤销自身会话，管理员主动撤销通过管理服务；用户名、来源、全局限流共同生效，错误消息不枚举账号。

**验证：** `go test ./tests/integration -run 'Test(Auth|Session|Password|RateLimit)' -v`，假时钟跨越全部边界；并发改密与请求不能复活已撤销会话。

**建议提交：** `feat: implement session authentication and password lifecycle`。

**完成记录（2026-09-05）：** `d09ffa5` 实现认证与会话生命周期；`go vet`、完整 race、旧库迁移、并发改密/撤销/闲置偏好回归及容器认证 E2E 均通过。新增当前会话、显式活动续期及 5–30 分钟闲置偏好合同；GET 不续期。详细证据见 `docs/releases/v1-validation.md` T06。

### T07 · HTTP 安全、中间件与幂等基础

**新增：** `internal/httpapi/{middleware,csrf,errors,idempotency,cursor}.go`、`tests/integration/http_security_test.go`。

- [x] 先写无 CSRF、错误 Origin、伪造代理头、超大 body、幂等重放与游标跨用户测试。（失败先行：安全头/HSTS/Origin 后缀伪造/Sec-Fetch-Site/413/日志脱敏/请求 ID 关联测试先于实现编写并确认失败；幂等与游标先写服务级失败测试）
- [x] 设置 CSP、HTTPS HSTS、nosniff、Referrer-Policy、frame 限制；默认关闭 CORS，API/下载统一 `Cache-Control: no-store`。（中间件链全响应生效；HSTS 仅直连 TLS 或可信代理宣告 https 时发送；错误响应统一 no-store + X-Request-ID）
- [x] 只信任配置代理网段；未经信任的 `X-Forwarded-*` 不改变来源地址或协议判断。（`TP_TRUSTED_PROXY_CIDRS`，默认空=不信任；右向左跳数解析上限 64；限流与访问日志均用解析结果）
- [x] 请求关联 ID 贯穿服务；访问日志使用路由模板，不记录查询词、body、Cookie、路径内资源 ID 或底层错误。（`internal/requestid` 贯穿 HTTP→服务→审计；日志仅 method/route/status/duration/remote/request_id）
- [x] 落实创建接口幂等事务、HMAC 指纹、游标认证与到期清理；幂等回放时重新验证当前授权，已撤销权限不能重放数据。（`internal/idempotency`：scope 内嵌操作者、带密钥 HMAC 指纹、pending/completed 与业务同事务、按不透明资源 ID 重放、过期回收；SQL 不入 httpapi——相对本计划文件清单的偏离已记录；已接入 setup/init，游标编解码器就绪待 T08 端点接入）

**验证（2026-09-05，实际执行）：** 安全头/代理/Origin/体积/日志/请求 ID 集成测试与幂等、游标单元测试全部通过；`go vet`、全量 `go test -race`、`git diff --check` 通过。提交 `311b287`。

**建议提交：** `feat: enforce http security and request consistency`。

### T08 · 审计基础与个人活动

**新增：** `internal/audit/{events,service,repository}.go`、`internal/httpapi/audit.go`、`tests/integration/audit_test.go`。

- [x] 先写事件字段允许列表、敏感值拒绝、个人活动隔离、管理员查询和删除成员后事件保留用例。（audit 包单元测试 + 集成测试先行确认失败后实现）
- [x] 为设计第 7.3 节逐项定义稳定事件常量；变更审计尽量与业务事务一同提交，避免成功变更丢审计。（登录成功/退出/会话撤销/改密/初始化与业务同事务；注入审计故障时业务变更一并回滚并有测试证明）
- [x] 失败登录只记录已解析的操作者 ID 或匿名标记，不记录用户名；无资源权限时不泄露目标内容。（专用测试区分“存了内部 ID”与“存了用户名”）
- [x] 接入 T05–T07 事件，提供个人活动和管理员审计游标查询；无修改/删除审计 API。（`GET /auth/activity`、`GET /admin/audit`，游标绑定操作者/筛选/有效期；事件过滤器经允许列表校验）

**验证（2026-09-05，实际执行）：** 审计事件允许列表、同事务完整性、个人隔离、成员禁入系统审计、游标跨用户/篡改拒绝、删除用户后审计保留且无用户名快照、合成秘密（SYNSECRET 标记）不出现在审计与日志、初始化令牌日志例外仅命中专用事件——全部通过；race 通过。提交 `1d98bf9`。

**建议提交：** `feat: add redacted audit trail and activity queries`。

### T09 · 成员生命周期与最后管理员保护

**新增：** `internal/users/{service,repository}.go`、`internal/httpapi/users.go`、`tests/integration/users_test.go`。

- [x] 先写用户名标准化唯一、初始密码强制更换、member 管理接口拒绝、最后管理员保护及并发用例。
- [x] 实现创建、禁用、启用、撤销会话；不提供管理员重置或获取当前密码的入口。（新增 `POST /users/{id}/revoke-sessions`（不改状态只杀会话）并同步 OpenAPI；角色固定 member，无角色变更/重置密码/读密码接口）
- [x] 删除要求精确确认目标展示用户名；事务撤销会话并清除个人/本人创建共享条目及其历史、回收站、相关幂等记录。（精确区分大小写匹配；条件化 DELETE 在写锁内重查最后管理员；item_versions 外键级联；操作者绑定幂等记录按 scope 清除）
- [x] 审计只保留不可逆内部 ID，UI 显示已删除用户；审计表不存用户名快照。（专用测试用“内部 ID ≠ 用户名”的夹具证明）
- [x] 删除页面的数据损失说明由 T15 接入；T11 后补跑真实条目级联测试，覆盖失败回滚。（本轮用合成 vault_items/item_versions/幂等夹具验证级联与审计故障回滚；真实条目 API 回归记入 T11）

**验证（2026-09-05，实际执行）：** 授权矩阵（member 403 全管理端点）、校验/冲突、幂等创建与并发唯一、首登改密限制、禁用即时失效/启用不复活、最后管理员保护（含服务级并发互删恰一成功）、删除确认/级联/审计保留/回滚全部通过；`go vet`、全量 race、`git diff --check` 通过。提交 `70d4669`。

**建议提交：** `feat: implement member administration lifecycle`。

### T10 · 集中权限策略与完整矩阵

**新增：** `internal/vault/policy.go`、`internal/vault/policy_test.go`、`tests/fixtures/permissions.json`。

- [x] 用表驱动测试枚举 `admin/member × personal/shared × 本人/他人 × read/create/update/delete/restore/purge/history/favorite/tag/export`。（80 行动作矩阵 + 8 行 create + 6 行引用 + 4 行不可变归属，全部来自 fixture 并逐行断言）
- [x] 将读权限与写权限分别定义，管理员无个人条目特权，也无他人共享条目写特权。（读规则 `CanReadItem` 独立；显式测试声明管理员对他人个人条目全部动作拒绝）
- [x] 固定条目创建后的 scope 与 owner/creator，不允许通过更新 DTO 转移归属；引用目标也必须授权。（`ValidateImmutableOwnership` 拒绝任何归属变更；`CanReference` 要求目标可读且可见性兼容——共享条目只能引用共享目标）
- [x] 非创建者共享收藏/标签变更、批量接口、历史读取和导出均使用相同策略；记录未来真实 API 回归用例。（D06：收藏/标签属于条目写权限；history_restore 归为写；T11/T17 必补的真实 API 越权回归清单见 `docs/releases/v1-validation.md` M2 节）

**验证（2026-09-05，实际执行）：** `go test ./internal/vault -run TestPolicy -v` 103 个子用例全部通过（矩阵每行一个子测试）。提交 `3f67f9c`。

**建议提交：** `feat: centralize vault authorization policy`。

## 7. M3：个人保险库

### T11 · 五类加密条目 CRUD

**新增：** `internal/vault/{types,payloads,validation,repository,service}.go`、`internal/httpapi/items.go`、`tests/integration/items_test.go`。

- [x] 分别为 login、ssh_key、credit_card、identity、secure_note 建立有效/无效字段 fixture，覆盖设计第 6.2 节所有字段及 T01 上限。（2026-09-05：validPayloadFixtures 五类全字段 + 24 个无效用例；字符串按码点计数，密码按 D09 的 1024 UTF-8 字节）
- [x] 创建 UUIDv7、revision=1 的条目，先授权再解密；标题、网址、标签、备注及全部业务字段仅进入加密 payload。（加密封套 v1：tags + 单一类型分支；明文列仅策略/生命周期元数据，nonce/ciphertext 分列存储；crypto 新增 DecryptColumns 按列组装）
- [x] 实现创建、详情、游标列表和更新；更新使用 `WHERE id=? AND revision=?` 并与历史保存置于同一事务。（客户端 revision 先行比对、CAS 仍作并发闸；无变化更新不 bump 版本不写历史；每次有效更新同事务归档旧版本）
- [x] 校验 owner/scope 组合与地址引用，禁止更新请求偷偷改变所有者、类型版本或范围。（DisallowUnknownFields 拒绝 owner_id/creator_id/vault_scope 走私；类型由存储行固定；引用要求目标 identity、可读且 shared→shared，全部违规统一 REFERENCE_FORBIDDEN 无存在性预言）
- [x] 接入创建幂等与审计，运行管理员读成员个人条目、猜 ID、批量读取和用户删除级联真实 API 回归。（scope 内嵌操作者 + HMAC 指纹，重放先重授权；vault.item.created/updated/viewed 入允许列表；跨用户与管理员读个人条目一律 404，共享写 403）

**验证（2026-09-05，实际执行，worktree `tiny-password-m03` 分支 `m03-personal-vault`）：** `go test ./tests/integration -run 'TestItem|TestUserDeleteCascade|TestUnauthorizedDecrypt' -v` 10/10 通过；`go vet ./...`、全量 `go test ./...`、`go test -race ./...`、`git diff --check` 均通过；DB 与 WAL 文件扫描无 `SYNSECRET` 明文。列表 `tag` 参数属 T13 解密筛选管线，T11 显式 400 拒绝。证据见 `docs/releases/v1-validation.md` T11。

**建议提交：** `feat: add encrypted vault item lifecycle`（已提交 `77c6f1f`，经 `m03-personal-vault` 变基至 M2 审查修复（`4ac83a9`）之上并快进合并回 master，文档 `8a011fc`）。

**建议提交：** `feat: add encrypted vault item lifecycle`。

### T12 · 历史、恢复与回收站

**新增：** `internal/vault/{history,trash}.go`、`tests/integration/item_lifecycle_test.go`。

- [x] 先写连续 12 次有效更新只保留最近 10 个历史、无变化更新策略、历史篡改、跨成员访问及 revision 冲突用例。（2026-09-05：TestHistoryRetentionKeepsLast10 验证 3..12 共 10 版；无变化不 bump；篡改恢复 500 且零写入；跨成员历史 404）
- [x] 有效更新前保存旧加密版本；恢复历史创建新的当前 revision，按新 AAD 重加密，不复制旧密文当新版本。（同事务归档旧版本；TestHistoryRestoreCreatesNewRevision 验证恢复后 rev5 且新 AAD 可解密、可再次回退）
- [x] 普通删除仅标记 deleted_at；恢复与提前永久删除遵守个人属主/共享创建者策略。（TestTrashLifecycleAndPolicy：共享读者 403、管理员 404；purge 仅限回收站条目）
- [x] 实现 30 天到期清理入口，用 UTC 假时钟测试期限前后；关联历史同事务删除，重复执行安全。（PurgeExpiredTrash：29 天不清/31 天恰 1 条/重复 0 条，匿名操作者审计）

**验证：** `go test ./tests/integration -run 'TestHistory|TestTrash' -v`；历史恢复可再次回退，越权恢复不触发解密或写入。

**建议提交：** `feat: add revision history and trash retention`。

### T13 · 搜索、标签、收藏与密码健康

**新增：** `internal/vault/{search,health}.go`、`internal/vault/search_test.go`、`tests/integration/search_test.go`、`tests/fixtures/seed.go`。

- [x] 先写标题/用户名/网址/标签/备注匹配、标签去重、类型与收藏组合筛选、无结果、未授权数据探针。（2026-09-05：单元 + 集成覆盖五字段命中、密码不可搜、他人个人条目不可见、空结果、组合筛选）
- [x] 在 SQL 中先限制授权范围，再分批解密、筛选和分页；按 updated_at 加 ID 稳定倒序，不落地明文索引。（scanMatches 200 条/批；DecryptHook 计数断言解密恰等于授权候选集）
- [x] 搜索可使用 POST JSON 以减少查询词进入 URL 的机会，仍要求 CSRF 和 no-store；响应不泄露未授权总数。（POST /items/search；游标绑定查询词与筛选）
- [x] 按需计算可读 login 的弱/重复/过期密码，明确本地弱密码规则；不存密码指纹，不调用外部服务。（weak=<12 码点；共享条目参与本人结果；TestHealthClassifiesReadableLogins）
- [x] 生成单用户 10,000 条合成记录，建立搜索性能基线和授权前解密调用计数断言。（fixtures 种子直插；基线 2.90s、解密恰 10000 次；P95 由 T30 专测）

**验证：** `go test ./internal/vault ./tests/integration -run 'TestSearch|TestHealth|TestFilter' -v`；性能 P95 由 T30 专用脚本统计，普通 Go benchmark 的均值不能冒充 P95。

**建议提交：** `feat: implement private search and password health`。

### T14 · Newsprint 设计原语与响应式壳层

**新增：** `web/src/design-system/{tokens.css,Button.tsx,Field.tsx,Dialog.tsx,Status.tsx}`、`web/src/app/{AppShell.tsx,router.tsx,api.ts}`、`web/src/design-system/design-system.test.tsx`、`web/public/fonts/LICENSES.md`。

- [x] 固化四色、零圆角、无渐变/模糊/柔和阴影、自托管四套字体及许可、清晰 focus-visible。（2026-09-05：tokens.css + @fontsource 自托管 44 个 woff2，LICENSES.md）
- [x] 建立持久标签字段、错误摘要、图标可访问名称、确认对话框和焦点恢复；触控目标至少 44×44。（Button/Field/ConfirmDialog/Status 全键盘可操作，vitest 覆盖）
- [x] 桌面 12 栏导航/列表/详情，平板详情独立页，<768px 单栏与底部主导航；范围文字和所有者署名同时出现。（AppShell + WorkspaceGrid；创建者署名由后端批量解析）
- [x] 全局 API 客户端处理 CSRF、request_id、401/403/409/503；数据只保留在内存，不做敏感状态持久化。（request 统一入口；409 透传 current_revision；401 清内存会话并广播）

**验证：** `npm --prefix web test -- --run src/design-system`；键盘可操作所有原语，360/768/1280px 无主要操作横向滚动，reduced-motion 下关闭非必要动效。

**建议提交：** `feat: establish newsprint workspace components`。

### T15 · 认证、账户与成员管理页面

**新增：** `web/src/features/auth/{LoginPage,ChangePasswordPage,AccountPage,ActivityPage,SessionBoundary}.tsx`、`web/src/features/admin/UsersPage.tsx`、`web/src/features/auth/session.test.tsx`、`tests/e2e/auth.spec.ts`。

- [x] 连接初始化/登录/首次改密流程，首次改密前无法进入保险库。（2026-09-05：SessionBoundary 强制轮换 + /auth/session 权威刷新 + cookie 会话静默恢复）
- [x] 实现即将过期、锁定、撤销状态；锁定/退出时取消请求并清空条目、搜索、表单、生成结果等内存，防止晚到响应重新填充。（401 → abortInFlightRequests + 内存清空 + 广播；E2E 断言零 storage 写入）
- [x] 实现自助改密、会话列表与撤销、个人活动；管理员页面实现创建/禁用/启用/删除。（AccountPage/ActivityPage/UsersPage；服务端独立校验）
- [x] 删除必须先显示导出提示、影响共享数据的说明，再要求输入目标用户名；服务端仍独立校验。（两步确认对话框；E2E 全流通过）

**验证：** `npm --prefix web test -- --run src/features/auth/session.test.tsx`；`make test-e2e E2E_SPEC=auth.spec.ts`，覆盖初始化→建成员→首次改密→禁用使另一浏览器会话失效。

**建议提交：** `feat: add authentication and account management ui`。

### T16 · 五类表单、敏感字段与工作区

**新增：** `web/src/features/vault/{VaultPage,ItemDetail,ItemEditor,HistoryPage,TrashPage,SensitiveField}.tsx`、`web/src/features/vault/forms/{LoginFields,SshKeyFields,CreditCardFields,IdentityFields,SecureNoteFields}.tsx`、`web/src/features/vault/vault.test.tsx`、`tests/e2e/vault.spec.ts`。

- [x] 五类字段逐项连接验证与错误摘要；完成列表、详情、编辑、标签、收藏、健康提示、历史和回收站。（2026-09-05：forms/ 五组件 + vitest 16 用例；收藏/标签端点走乐观锁更新）
- [x] 敏感字段默认遮蔽，主动显示才审计；30 秒、blur、锁定立即遮蔽，不仅通过 CSS 隐藏仍可聚焦的明文输入。（SensitiveField：display 切换而非 CSS 隐藏，E2E 断言明文不可见）
- [x] 复制时记录字段类别而非值；30 秒后仅在可读且内容未变化时尝试清除，无权限时如实提示，不覆盖用户新复制的内容。（copy 审计 + 剪贴板比对清除 + 拒绝提示，vitest 假时钟覆盖）
- [x] 409 保留当前编辑内容，提供重新加载选择，不自动覆盖；覆盖空库、无结果、网络失败和维护状态。（ItemEditor 冲突面板；VaultPage 空库/无结果/网络失败状态）
- [x] PC/移动完成五类 CRUD、搜索、历史恢复和回收站；重启服务确认仍可解密。（TestItemRestartDecryption + vault.spec 4/4 含移动视口）

**验证：** `npm --prefix web test -- --run src/features/vault`；`make test-e2e E2E_SPEC=vault.spec.ts`；假计时器验证遮蔽及剪贴板分支，浏览器用例验证键盘与焦点恢复。

**建议提交：** `feat: complete personal vault workspace`。

## 8. M4：共享、生成与个人迁移

### T17 · 共享工作区与跨用户权限回归

**修改：** `internal/vault/service.go`、`internal/httpapi/items.go`、`web/src/features/vault/{VaultPage,ItemDetail,ItemEditor}.tsx`。
**新增：** `tests/integration/shared_test.go`、`tests/e2e/shared.spec.ts`。

- [x] 建立管理员 A、成员 B/C 场景，覆盖读取共享与自己创建共享条目的全部写操作。（2026-09-06：TestSharedReadsWriteMatrix）
- [x] 非创建者隐藏编辑、收藏变更、标签变更、删除、恢复、purge；详情显示创建者与只读原因。（服务端 403 全写操作；UI 只读通知 + 控件隐藏，E2E 断言）
- [x] 直接伪造 API、历史恢复、批量操作和地址引用请求，验证服务端拒绝；禁用创建者保留数据，删除创建者清除数据。（TestSharedForgedRequests + 创建者禁用/删除两个测试）

**验证：** `go test ./tests/integration -run TestShared -v`；`make test-e2e E2E_SPEC=shared.spec.ts`。管理员对他人共享条目同样不能写。

**建议提交：** `feat: expose shared vault with creator only writes`。

### T18 · 密码、口令与 SSH 生成器

**新增：** `internal/generator/{password,passphrase,ssh}.go`、`internal/generator/generator_test.go`、`internal/generator/wordlist.txt`、`internal/generator/LICENSE.wordlist`、`internal/httpapi/generators.go`、`web/src/features/generator/GeneratorPage.tsx`、`tests/e2e/generators.spec.ts`。

- [x] 先写密码长度 8/20/128、无字符集拒绝、排除易混淆、各启用类别出现、无模偏差采样的算法测试。（2026-09-06：拒绝采样 + 卡方均匀性）
- [x] 使用 `crypto/rand`；口令默认 5 词、3–10 词、分隔符与首字母选项，固定词表记录来源与许可证。（EFF 短词表 1296 词 + LICENSE.wordlist）
- [x] 生成 Ed25519/RSA-4096，支持可选私钥口令、注释、公钥与指纹；测试解析私钥、算法位数及口令解密。（x/crypto/ssh 编解码 + 口令 KDF）
- [x] UI 默认以保存为 SSH 条目提交，保存前的生成结果仅限请求/内存；取消、断连、超时均不持久化结果。限制 RSA 并发与耗时。（保存后清内存并导航；rsaGate 信号量）
- [x] 生成页面支持将密码/口令填入编辑器，默认遮蔽并适用会话清理；日志和审计不含结果。（刷新即清空 E2E；访问日志仅路由模板）

**验证：** `go test ./internal/generator -v`；`make test-e2e E2E_SPEC=generators.spec.ts`。取消生成后检查数据库、临时目录与日志无秘密残留。

**建议提交：** `feat: add cryptographic credential generators`。

### T19 · 共用加密归档与受限解包

**新增：** `internal/platform/archive/{sevenzip,validate,tempdir}.go`、`internal/platform/archive/archive_test.go`、`tests/integration/archive_test.go`。

- [x] 固化 T01 已验证的 7z 调用方式，口令经受控 stdin，终端关闭回显；子进程 stdout/stderr 不直接写日志。（run() 捕获输出仅用于错误分类）
- [x] 创建 AES-256 且文件名加密归档；操作使用超时、并发限制、受限 tmpfs 和统一清理回调。（-mhe=on + 120s + opGate(2) + 0700 工作区 + cleanup）
- [x] 解包前验证格式、白名单、文件数量、展开总量及路径；拒绝绝对路径、`..`、符号/硬链接、设备文件、重复文件名及不允许的嵌套归档。（ValidateEntries 单元 4 组用例）
- [x] 解包过程再实施资源限制，不能只相信头部声明；错误口令、损坏、压缩炸弹、超时与取消均回收子进程和文件。（2026-09-06：verifyExtracted 磁盘复检；压缩炸弹的 128MiB 硬上限在解包前后双向生效）
- [x] 合成秘密扫查进程参数、普通日志、错误文本与磁盘临时目录；只读归档验证不污染工作目录。（argv 无口令；输出不入日志；错误文本无路径/秘密——T30 终检）

**验证：** `go test ./internal/platform/archive -v`；`go test ./tests/integration -run TestArchive -v`，在含固定 7z 的测试容器内执行真实加解密。

**建议提交：** `feat: add bounded encrypted archive adapter`。

### T20 · 个人导出、导入预览与确认

**新增：** `internal/transfer/{format,export,preview,import}.go`、`internal/httpapi/transfer.go`、`web/src/features/transfer/TransferPage.tsx`、`tests/integration/transfer_test.go`、`tests/e2e/transfer.spec.ts`。

- [x] 建立版本化 JSON 清单、条目文件与 SHA-256；拒绝包含账号哈希、会话、审计或主密钥的个人格式。（2026-09-06：ManifestSelfCheck 类型/范围白名单）
- [x] 导出只选本人个人和本人创建共享条目，用户输入并确认独立归档密码；完成归档后以流式响应下载。（ExportAll 策略复用；handler 流式返回）
- [x] 上传密码放 multipart 受限字段，绝不放 URL；验证后返回类型/数量、冲突与引用摘要，用户确认前数据库零写入。
- [x] 预览结果绑定用户、归档内容与短有效期（HMAC token + 0700 暂存 + 10 分钟）；确认时重新验证身份并在一个事务内写入全部条目。
- [x] 冲突 ID 生成新 UUIDv7；重写归属与内部地址引用；非法类型、字段超限或单条校验失败整体拒绝。（ImportAll 三段式：ID/引用/加密写入）
- [x] 请求中断、预览超时、用户取消和会话失效清理临时数据；重试确认遵守幂等，不能重复导入。（sweepExpiredPreviews + 单次消费 token + E2E 断言二次确认 400）

**验证：** `go test ./tests/integration -run TestTransfer -v`；`make test-e2e E2E_SPEC=transfer.spec.ts`。用新账号导入，现有条目不被覆盖，他人共享条目不进入导出包。

**建议提交：** `feat: implement personal encrypted import and export`。

## 9. M5：实例备份与恢复

### T21 · 一致性快照、本地归档与验证

**新增：** `internal/backup/{manifest,snapshot,runner,local}.go`、`tests/integration/backup_local_test.go`。

- [x] 先写持续并发写入期间备份、空间不足、错误密码、损坏校验、取消与互斥测试。（2026-09-06：backup_local_test.go 7 用例；并发写入以 10 行/事务批写并在快照内断言批一致性，错误密码=空口令、损坏=注入篡改钩子、取消/互斥/空间经 Hooks 注入）
- [x] 获取实例内互斥锁；创建 Online Backup 快照，不直接复制活动 SQLite/WAL 文件，也不持有长写事务压缩。（进程级 instanceMutex（手动/定时/T23 调度共享），重叠返回 BACKUP_BUSY；快照经 sqlite.Snapshot（Online Backup API + journal_mode=DELETE 归一化），归档/验证期间无源库事务）
- [x] manifest 写格式/应用/schema 版本、ID、UTC 时间、实例 ID、架构以及每个文件的大小/SHA-256。（manifest.go：format_version=1、app_version、schema_version、backup_id（UUIDv7）、created_at（UTC RFC3339Nano）、instance_id（system_state 首用生成）、GOOS/GOARCH、每文件 size+SHA256）
- [x] 归档仅含快照、源主密钥和允许的非敏感配置；不含 R2 凭据、Tunnel token、备份密码、TLS 密钥或运行日志。（归档=manifest.json + db/tiny-password.db + secrets/master_key(0600) 三文件白名单，验证时逐项核对提取集与清单、多余/缺失即拒；不存在"允许的非敏感配置"文件）
- [x] 重新打开归档验证全部文件后在本地同文件系统原子发布；失败不生成成功记录，不触发保留清理。（ExtractLimited 复检：白名单/文件数/展开总量/逐文件 SHA256/integrity_check=ok；LocalStore.Publish 复制到目标目录隐藏临时名→fsync→SHA256 复核→同文件系统 rename；失败行 error_code 稳定码且无 succeeded 记录）

**验证：** `go test ./tests/integration -run TestBackupLocal -v`；解出快照完整性为 `ok`，故障后旧备份仍在，tmpfs 工作目录为空。

**完成记录（2026-09-06）：** 7/7 通过（并发写入期间快照批一致性 + integrity ok、空口令拒绝、损坏归档拒绝且旧备份保留、取消清理、进程级互斥 BACKUP_BUSY、空间不足、配置校验）；`go vet ./...`、全量 `go test ./...`（全量包内 items 并发 CAS 用例曾因负载抖动 503 DATABASE_BUSY 一次，隔离与重跑均通过）、`go test -race ./internal/backup ./internal/platform/archive` 与 `-run TestBackup -race` 通过。archive 适配器新增 Limits/ExtractLimited/Timeout（个人迁移默认值不变）；实例归档上限显式配置默认 1 GiB、单次 7z 10 分钟。

**建议提交：** `feat: create verified local instance backups`。

### T22 · R2 目标与独立结果

**新增：** `internal/platform/objectstore/r2.go`、`internal/backup/r2.go`、`tests/integration/backup_targets_test.go`、`tests/integration/r2_live_test.go`。

- [ ] 实现前核对官方 [S3 API 兼容性](https://developers.cloudflare.com/r2/api/s3/api/) 与 [认证方式](https://developers.cloudflare.com/r2/api/s3/tokens/)，将使用的 SDK 版本与访问日期记入依赖决策。
- [ ] R2 access key/secret key 从 Secret 文件获取；数据库只存非敏感 bucket/prefix/启用状态等配置，UI 不回显凭据。
- [ ] 上传唯一临时对象，回读校验归档 SHA-256 后复制到最终 key，再校验最终对象并清理临时对象；S3 不存在文件系统式 rename，不能把 ETag 一概当 SHA-256。
- [ ] 本地/R2 各自维护 pending/running/succeeded/failed 状态；归档生成共同失败影响两者，单目标交付失败不改写另一目标成功。
- [ ] 对网络/限流/服务端暂时故障有限退避重试；认证等永久错误立即报告。中断上传与临时对象有后续清理路径。

**验证：** `go test ./tests/integration -run TestBackupTargets -v`；故障注入双向验证独立成功。真实 R2 验收用专用测试 prefix 执行 `go test ./tests/integration -run TestR2Live -v`，需测试凭据；缺凭据标记未完成，不以 mock 替代真实验收。

**建议提交：** `feat: add verified r2 backup delivery`。

### T23 · 调度、互斥、执行历史与维护任务

**新增：** `internal/scheduler/{scheduler,daily}.go`、`internal/scheduler/scheduler_test.go`、`internal/backup/jobs.go`。

- [ ] 假时钟覆盖 UTC、Asia/Shanghai、America/New_York 的正常/跳过/重复时刻以及重启补跑规则。
- [ ] 每日时间与 IANA 时区持久化；手动与定时共享互斥锁，任务重叠明确返回 busy/skip 结果。
- [ ] 记录 backup_runs 生命周期；进程重启将遗留 running 标成 interrupted，禁止展示永远运行中。
- [ ] 注册回收站清理、过期会话/限流/幂等记录清理和低峰 WAL checkpoint；后台 panic 转为失败状态而非退出 HTTP 进程。

**验证：** `go test ./internal/scheduler -v`；`go test ./tests/integration -run 'TestBackupOverlap|TestJobRestart|TestMaintenanceJobs' -v`；每天最多一次计划执行，失败在管理 API 可见。

**建议提交：** `feat: schedule backups and bounded maintenance jobs`。

### T24 · GFS 保留与可重试清理

**新增：** `internal/backup/retention.go`、`internal/backup/retention_test.go`、`tests/integration/retention_test.go`。

- [ ] 固化日/周/月时间桶规则，默认保留 7 日、4 周、6 月；同一备份落入多桶只保留一份，按目标已验证成功集合求保留并集。
- [ ] 为跨月/跨年/DST、少量备份、数量调整和禁用目标建立纯函数测试。
- [ ] 只在该目标新增已验证成功备份后清理该目标；失败备份不能触发删除最后可用备份。
- [ ] 删除失败保留待清理记录并重试；R2 失败不阻止本地清理，每次删除审计只记录不透明对象标识。

**验证：** `go test ./internal/backup -run TestRetention -v`；`go test ./tests/integration -run TestRetention -v`；明确计算期望保留 ID 集合，不能仅断言数量。

**建议提交：** `feat: apply verified backup retention per target`。

### T25 · 离线恢复与新主密钥重加密

**新增：** `internal/backup/{restore,rekey,recovery_state}.go`、`cmd/tiny-password/restore.go`、`tests/integration/restore_test.go`。

- [ ] 为服务仍运行、错误密码、损坏归档、未知格式/新 schema、空间不足与跨架构恢复先写拒绝/成功用例。
- [ ] HTTP 服务和 restore 共用进程级数据目录独占锁；`tiny-password restore /restore/backup.7z` 必须在正常服务停止后运行。
- [ ] 验证归档白名单、兼容范围、校验值与空间；已有目标先创建可恢复的前置快照，空目标记录为空，禁止凭空假设存在旧库。
- [ ] 在同文件系统准备候选库；归档源密钥仅在受限 tmpfs/内存。重加密全部 vault_items 与 item_versions，保持 ID/revision 并使用新的 nonce/AAD。
- [ ] 清空恢复库旧会话和短期认证/幂等状态；凭据未配置的外部目标不得自动开始上传，页面给出需重新配置状态。
- [ ] 候选库先做兼容迁移、完整性与解密验证；写恢复状态记录后原子切换。若切换后检查失败，利用保留旧库回滚；所有断点重启均能选定完整旧库或完整新库。
- [ ] 清理源密钥及临时文件，目标 Secret 保持只读；失败报告只含阶段/错误码/request_id，不含秘密。

**验证：** `go test ./tests/integration -run TestRestore -v`；在快照、重加密、迁移、切换前后逐阶段注入退出/磁盘故障，原实例仍可启动且数据一致。恢复后新密钥可解密全部当前/历史负载，旧密钥不可解密新库。

**建议提交：** `feat: restore instances safely under a new master key`。

### T26 · 升级前快照、迁移失败回滚

**修改：** `internal/platform/sqlite/migrate.go`、`cmd/tiny-password/main.go`。
**新增：** `tests/fixtures/schema-v1.sql`、`tests/integration/upgrade_test.go`、`docs/operations/upgrade.md`。

- [ ] 初始化空库无需旧库快照；非空库升级前获取数据目录锁并创建已验证的一致性快照，禁止与在线写入并行迁移。
- [ ] SQL 迁移事务化；无法单事务处理的步骤记录可恢复状态；升级失败不进入 ready。
- [ ] 测试旧 schema 升级成功、迁移中断/失败回原库、重复启动和不支持降级的安全拒绝。
- [ ] 写明旧镜像/数据/主密钥配套回滚步骤；数据库副本本身仍含加密负载，不能依赖它替代含密钥的整实例归档。

**验证：** `go test ./tests/integration -run TestUpgrade -v`；失败后旧版本镜像配原库可读，升级前快照存在且可校验。

**建议提交：** `feat: guard database upgrades with recoverable snapshots`。

### T27 · 备份、审计和系统设置管理页

**新增：** `internal/settings/service.go`、`internal/httpapi/{backups,settings}.go`、`web/src/features/admin/{BackupsPage,AuditPage,SettingsPage}.tsx`、`web/src/features/admin/admin.test.tsx`、`tests/e2e/backups.spec.ts`。

- [ ] 管理 API 仅允许 admin；设置只接收显式非敏感字段白名单，秘密通过 Secret 注入，不提供下载秘密配置接口。
- [ ] 页面提供手动执行、每日时间/时区、目标启停/保留设置、逐目标结果、验证与清理失败历史。
- [ ] 覆盖运行中、成功、失败、从未成功、R2 未配置、空间不足、维护状态以及部分目标不可用。
- [ ] 审计页支持事件/结果/时间筛选；系统页展示版本、ready 状态与任务失败，恢复页只说明离线命令，不直接覆盖运行数据库。

**验证：** `npm --prefix web test -- --run src/features/admin`；`make test-e2e E2E_SPEC=backups.spec.ts`，模拟 R2 失败，本地成功仍独立展示。

**建议提交：** `feat: add backup and operations administration ui`。

## 10. M6：PWA、Tunnel 与发布加固

### T28 · PWA 安装、白名单缓存与更新

**修改：** `web/vite.config.ts`、`web/src/app/AppShell.tsx`。
**新增：** `web/src/pwa/register.ts`、`web/public/offline.html`、`web/public/icons/icon-192.png`、`web/public/icons/icon-512.png`、`tests/e2e/pwa.spec.ts`。

- [ ] 配置 Manifest 的名称、图标、主题色、display、start_url；提供独立静态离线说明。
- [ ] 使用显式静态资源预缓存白名单，API、归档、导航动态响应全部 network-only；不注册宽泛 runtime caching 规则。
- [ ] 遍历 Cache Storage 验证只含许可静态资源，检查 IndexedDB/Local Storage 无保险库/密码/归档数据。
- [ ] 更新提示允许用户先保存编辑；不在未保存表单中强制 skipWaiting/刷新。
- [ ] Android 真机验证安装、在线登录与断网说明；断网不展示上次解锁的条目作为离线保险库。

**验证：** `make test-e2e E2E_SPEC=pwa.spec.ts`；Android 手工结果写入发布证据，浏览器模拟不能替代安装验收。

**建议提交：** `feat: add installable pwa with static only caching`。

### T29 · Compose、TLS、Tunnel 与运维说明

**修改：** `Dockerfile`、`compose.yaml`。
**新增：** `deploy/compose.lan.yaml`、`deploy/compose.test.yaml`、`docs/operations/{deploy,secrets,tunnel,backup-restore}.md`、`README.md`。

- [ ] 固化单应用默认 Compose、只读主密钥/备份密码 Secret、非 root、数据卷权限、资源边界和优雅 SIGTERM 停机。
- [ ] 增加可选 `tunnel` profile，cloudflared 通过 Docker 服务名访问应用；核对 [Tunnel 官方文档](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/) 的当前容器与 token 文件配置。
- [ ] 默认主应用不映射宿主公网端口；LAN override 绑定指定局域网地址，并说明 HTTPS 反代与证书信任，验证 Secure Cookie 实际发送。
- [ ] 用实际部署验证可信代理网段、伪造转发头拒绝、安全头、健康检查和 graceful shutdown。
- [ ] 文档覆盖生成/保管 Secret、首次令牌获取、初始改密、遗忘密码限制、删除后果、备份口令遗失后果、本地/R2 恢复与新密钥准备。
- [ ] 找未参与开发者照文档从空目录部署，记录 10 分钟目标的实际耗时与先决条件。

**验证：** `docker compose config --quiet`；`docker compose --profile tunnel config --quiet`；`docker compose -f compose.yaml -f deploy/compose.lan.yaml config --quiet`；Tunnel/LAN HTTPS 各执行登录与安全头检查，不输出 Secret 内容。

**建议提交：** `docs: deliver secure compose and tunnel deployment`。

### T30 · 安全、性能与完整恢复验收

**新增：** `scripts/{check-secrets.sh,check-security.sh,bench-search.sh,restore-drill.sh}`、`tests/e2e/accessibility.spec.ts`、`docs/releases/v1-validation.md`。

- [ ] 完整运行后端/前端/API/E2E；后端 race、静态分析、Go/npm/镜像漏洞扫描；报告包含工具版本、严重级别和处置结论。
- [ ] 用合成敏感标记扫描 SQLite、WAL、普通临时盘、日志与审计；初始化令牌仅允许 D01 事件。解密后的受限工作数据和加密归档内内容按设计单独检查。
- [ ] 完成损坏包、错误密码、旧 schema、磁盘不足、中断上传、归档炸弹、迁移/恢复中断、后台崩溃演练。
- [ ] 在明确 CPU/内存/磁盘和架构环境中，测 20 成员、10,000 条总量；另测单用户 10,000 条搜索 P95 ≤300 ms，记录冷热缓存、关键词、样本数与并发。
- [ ] 双架构测 Argon2id 250–500 ms；空闲内存约 100 MB 是优化目标，记录实际值，不降低加密参数或跳过验证换取数值。
- [ ] 本地与真实 R2 各取一份已验证归档，在干净实例使用新主密钥恢复并跑五类数据/历史/权限校验；再测已有实例失败回滚。
- [ ] Playwright/可访问性检查覆盖 PC/平板/手机和键盘主流程；真机补 Android PWA、剪贴板权限差异与安装更新。

**验证命令：**

```bash
npm --prefix web ci
npm --prefix web run typecheck
npm --prefix web test -- --run
npm --prefix web run build
go vet ./...
go test ./...
go test -race ./...
make test-e2e
bash scripts/check-secrets.sh
bash scripts/check-security.sh
bash scripts/bench-search.sh
bash scripts/restore-drill.sh
```

**预期：** 所有命令退出 0，报告保留脱敏输出、环境、提交 SHA 与日期；无法运行的真实 R2/ARM/Android 检查明确标为未完成。禁止用“mock 通过”替代产品验收。

**建议提交：** `test: verify v1 security performance and recovery`。

### T31 · 双架构镜像与发布交付

**新增：** `.github/workflows/release.yaml`、`docs/releases/v1-checklist.md`、`CHANGELOG.md`。
**修改：** `README.md`、`docs/operations/upgrade.md`。

- [ ] 配置 `linux/amd64`、`linux/arm64` 镜像构建，固定依赖版本，产出镜像摘要、SBOM 与扫描记录。
- [ ] 每个架构实际运行健康、初始化、登录、加密读写、7z 和恢复冒烟；仅交叉编译成功不算可运行验收。
- [ ] 把下节全部验收项链接到实际测试/报告，确认文档版本、镜像版本与 schema 兼容范围一致。
- [ ] 准备版本说明、部署/升级/回滚命令及镜像产物；正式推送镜像、打发布标签或对外发布按届时授权执行。

**验证：** CI 双架构构建及运行任务全部成功；`docs/releases/v1-checklist.md` 无未完成的 V1 必需项，所有证据对应同一候选版本。

**建议提交：** `release: prepare verified v1 multi architecture delivery`。

## 11. 设计覆盖与发布验收映射

### 11.1 完整设计章节覆盖

| 设计章节 | 行动归属 |
| --- | --- |
| §1 文档目的、§2 目标、§3 原则 | 本文 §1–3、T01、T29–T31 |
| §4 范围 | 本文 §1 排除项、全部六个里程碑 |
| §5 用户与权限 | T05–T10、T15、T17 |
| §6 条目与生成 | T04、T10–T13、T16–T18 |
| §7 搜索健康审计 | T08、T13、T15、T27 |
| §8 架构与 API | 本文 §4、T01–T03、T07、T14 |
| §9 数据设计 | T03、T09、T11–T12、T23、T26 |
| §10 安全 | D01–D12、T04–T10、T19、T25、T29–T30 |
| §11 备份恢复 | T19–T27、T30 |
| §12 UI/UX | T05、T14–T18、T20、T27、T30 |
| §13 PWA | T15、T28、T30 |
| §14 Docker/Cloudflare | T02、T22、T29、T31 |
| §15 错误与一致性 | T03、T07、T11–T12、T19–T26 |
| §16 测试 | 每任务验证、T30、下表 |
| §17 路线、§18 验收 | 本文 §3、§11.2 |
| §19 后续候选 | 明确不纳入 V1，无提前实现任务 |

### 11.2 V1 发布门槛

| 状态 | 原设计验收项 | 任务与证据位置 |
| --- | --- | --- |
| [ ] | 初始化需令牌且只能成功一次 | T05；`tests/integration/setup_test.go` |
| [ ] | 管理员/成员完整权限矩阵 | T10/T17；`internal/vault/policy_test.go`、`tests/integration/shared_test.go` |
| [ ] | 管理员 API 无法读他人个人条目 | T11；`tests/integration/items_test.go` |
| [ ] | 共享只有创建者修改、删除、恢复 | T12/T17；`tests/integration/shared_test.go` |
| [ ] | 五类条目、三类生成器及搜索/标签/收藏/历史/回收站 | T11–T18；vault/shared/generators 浏览器测试 |
| [ ] | 敏感数据无非预期明文持久化 | T04/T07/T19/T30；`scripts/check-secrets.sh` 与发布报告 |
| [ ] | 本地/R2 定时备份独立配置验证保留 | T21–T24/T27；backup_targets/retention 集成测试 |
| [ ] | 个人导入导出与实例恢复演练 | T20/T25/T30；transfer/restore 测试和真实 R2 记录 |
| [ ] | 删除成员提示导出、确认用户名、关联数据不可经产品恢复 | T09/T15；users 与 auth 浏览器测试 |
| [ ] | PWA 不缓存 API/明文且 Android 可安装 | T28/T30；pwa 浏览器测试及真机报告 |
| [ ] | Newsprint PC/移动与基本可访问性 | T14–T16/T30；accessibility 测试与断点截图 |
| [ ] | Tunnel 无需主应用开放公网端口 | T29；Compose 配置与真实 HTTPS 检查 |
| [ ] | amd64/arm64 镜像运行、健康通过 | T31；release CI 运行记录 |
| [ ] | 升级前快照与失败回滚 | T25–T26/T30；upgrade/restore 故障注入记录 |

额外成功标准：T29 记录从空目录部署时间；T30 记录规模、搜索 P95、双架构 Argon2id 与空闲内存；T28/T30 记录 PC、移动浏览器和安装 PWA 的核心功能一致性。

## 12. 执行记录规范与首批行动

每完成一个任务，在对应复选框记录结果，并在 `docs/releases/v1-validation.md` 按“任务 ID、提交 SHA、执行环境、命令、结果、证据路径、未解决问题”追加脱敏证据。提交前只暂存该任务实际修改的文件，检查差异；不提交 Secret、真实备份、测试生成秘密或用户既有未跟踪资料。

首批按以下顺序开始：

1. **T01**：完成 7z 密码通道与 SQLite 在线快照探针，固化接口和边界。这两项决定归档与双架构技术选型。
2. **T02–T04**：建立构建/测试闭环、数据库迁移与加密基础。
3. **T05**：交付首个可演示闭环，并以并发与重启测试验收 M1。

每个里程碑必须满足本阶段退出门槛后再标记完成；跨阶段预先开发不等于提前验收。性能未达目标时先测量与优化既有方案，任何明文索引、加密模式或权限规则改变均重新进入设计评审。
