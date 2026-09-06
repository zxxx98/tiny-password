# 0002 · 依赖版本锁定

状态：已接受
日期：2026-09-05
关联：[0001 · V1 边界决策](0001-v1-boundaries.md)

锁定 T01 探针实际验证过的版本。升级任何锁定项都必须重跑对应探针与测试。

## 工具链

| 组件 | 版本 | 完整性依据 |
| --- | --- | --- |
| Go（编译） | 1.25 语言版本（go.mod `go 1.25`），工具链 go1.26.8（GOTOOLCHAIN=auto 自动获取） | Go module proxy 校验；CI 用 `golang:1.26` 镜像 |
| Node（前端构建） | 22.x LTS（本机 22.22.2；镜像 `node:22-alpine`） | `web/package-lock.json` 锁定全部传递依赖 |
| Docker 构建 | Docker 29.x / Compose v5.1.1（开发环境实测） | — |

## 后端关键依赖

| 依赖 | 版本 | 说明与验证入口 |
| --- | --- | --- |
| `modernc.org/sqlite` | v1.58.0 | 纯 Go（无 CGO），amd64/arm64 双架构同一份代码；内嵌 SQLite 3.53.4。暴露在线备份 API（未导出 `*conn.NewBackup`，经接口断言触达）。完整性由 go.sum + sum.golang.org 校验。T03/T21 验证。 |
| `golang.org/x/crypto` | 最新稳定（go.sum 锁定） | Argon2id、XChaCha20-Poly1305。T04 验证。 |
| HTTP 路由 | Go 标准库 `net/http`（Go 1.22+ method patterns） | 不引入第三方框架。 |

## 容器内二进制

| 组件 | 版本 | SHA256（下载产物自算，发布前需对照独立来源复核） |
| --- | --- | --- |
| 7-Zip 官方静态二进制（7zz） | 26.03 | `7z2603-linux-x64.tar.xz`：`dc99eff5008f1ab79bd7084c68513701547a808a89502bf4133683535ab3c695` |
| | | `7z2603-linux-arm64.tar.xz`：`2389ba20e4d8295e8709c20b6263b69bd1ec4972fe38a04ad7a1badbf595b996` |

Dockerfile 在构建期下载上述 tarball 并校验 SHA256，只把 `7zz` 复制进运行镜像。

## 前端关键依赖

| 依赖 | 版本策略 |
| --- | --- |
| React | 19.x（`package-lock.json` 锁定精确版本） |
| Vite | 最新稳定 7.x（同上） |
| TypeScript | 最新稳定 5.x（同上） |
| Tailwind CSS | v4（`@tailwindcss/vite` 插件；token 见 `style.md`） |
| 测试 | Vitest + Testing Library（开发依赖锁定） |

## 对象存储（R2 备份交付）

| 依赖 | 版本/形式 | 说明与验证入口 |
| --- | --- | --- |
| R2 S3 客户端 | 无 SDK：`internal/platform/objectstore` 自实现最小 AWS SigV4 REST 子集（仅标准库） | 2026-09-06 核对 Cloudflare 官方文档（`/r2/api/s3/api/`、`/r2/api/s3/tokens/`）：端点 `https://<account>.r2.cloudflarestorage.com`、region `auto`、PutObject/CopyObject/ListObjectsV2 可用、凭据为 S3 Access Key/Secret。签名正确性由独立参考实现测试 + `TestR2Live` 真实服务验收（待凭据）。T22 验证。 |

## 版本固定规则

1. `go.mod`/`go.sum`、`package-lock.json`、Dockerfile 基础镜像与 7z tarball 摘要是唯一的版本事实来源。
2. 任何升级必须附带：重跑 `scripts/probes/archive-password.sh` 与 `scripts/probes/sqlite-backup.sh`（若涉及）+ 受影响任务的全部测试。
3. 安全升级（漏洞修复）可随时进行，但必须在验证证据文档中记录新旧版本与重跑结果。
