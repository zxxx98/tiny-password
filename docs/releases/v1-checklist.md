# V1 发布清单

这份清单要求所有结论指向同一个候选版本、同一个 Git SHA 和同一份验证报告。未完成的外部验收不能用 mock 或交叉编译替代。

## 构建与供应链

- [ ] 记录候选版本、Git SHA、Go/Node/SQLite/7-Zip 版本和 Dockerfile digest。
- [ ] `.github/workflows/release.yaml` 的 amd64/arm64 smoke job 均通过；两种架构都实际启动健康检查、初始化、加密读写和 7-Zip。
- [ ] GHCR 多架构 manifest digest、SBOM attestation 和 Trivy 扫描结果已保存；高危/严重漏洞有明确处置。
- [ ] 候选镜像的 `schema_version` 与 `docs/operations/upgrade.md` 的兼容范围一致。

## 安全与数据

- [ ] `scripts/check-secrets.sh` 扫描数据库、WAL、受限临时目录、日志和审计输出；仅 D01 初始化事件可出现一次性 token，其他合成标记为零。
- [ ] `scripts/check-security.sh` 通过安全响应头、API `no-store`、无宽泛 CORS、可信代理和 Secure Cookie 检查。
- [ ] PWA Cache Storage 只有静态白名单；API、归档和导航不缓存；IndexedDB/localStorage/sessionStorage 没有保险库或密码材料。
- [ ] 安全审查确认主密钥/备份口令/Tunnel token 不进命令行、日志、归档 manifest 或 Web 设置响应。

## 功能、性能与恢复

- [ ] Go vet、普通测试、race、前端 typecheck/test/build、容器 API E2E 和 Playwright 主流程全部通过。
- [ ] `scripts/restore-drill.sh` 通过损坏包、错误密码、旧 schema、空间不足、迁移/恢复中断和断点 resume；已有实例失败后原库完整。
- [ ] 固定 CPU/内存/磁盘/架构记录 Argon2id 250–500 ms、空闲内存约 100 MB 目标和单用户 10,000 条搜索 P95；超标项有处置结论。
- [ ] 本地归档和真实 R2 归档各在新主密钥干净实例恢复，并完成五类条目、历史和权限校验；已有实例回滚通过。

## 部署与设备

- [ ] 默认 Compose 无宿主公网端口；LAN override 只绑定指定地址；Tunnel profile 通过 `http://app:8080` 访问且 token 只读文件挂载。
- [ ] 未参与开发的操作者从空目录按文档完成部署，记录先决条件和耗时（目标 ≤10 分钟）。
- [ ] Playwright PC/平板/手机和键盘主流程通过；记录 360/768/1280px 证据。
- [ ] Android 真机完成 PWA 安装、在线登录、断网说明、剪贴板权限差异和更新提示；断网不显示旧保险库。

## 发布决定

- [ ] 发布负责人复核所有未完成项，给出风险接受或阻断结论。
- [ ] 仅在必需项全绿后创建 Git tag、推送镜像和发布说明；回滚命令与对应旧镜像/数据库快照已演练。
