# 0003 · 搜索性能与索引边界决策

状态：已接受
日期：2026-09-07
关联：[行动计划](../superpowers/plans/2026-09-05-tiny-password-action-plan.md) T13/T30；`internal/vault/search.go`

## 1. 目标与不变量

搜索必须继续满足 V1 的原有语义：在调用者有权读取的条目中，对标题、用户名、网址、标签和备注执行大小写不敏感的子串匹配；密码等敏感字段不参与匹配。数据库中不得出现这些字段的明文，也不得因为优化绕过“先授权、后解密”。

## 2. 分段 profile 结论

原实现每次只从 SQL 取 200 个候选。10,000 条数据、单个用户、唯一命中查询会触发 50 次带排序和游标条件的 SQL 查询。基线 profile 的搜索调用链约 2.92s，其中 `repository.listRows` 约 2.81s；`decryptRow` 的 CPU 采样约 40ms，JSON 解析约 20ms，字段匹配未形成主要 CPU 热点。

新增的可选 `SearchProfileHook` 只传递阶段名和耗时，不传递 payload。矩阵基准的代表性结果为：

| 查询类型 | 总耗时 | SQL | 解密 | JSON | 匹配 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 唯一标题/URL/用户名命中或无命中 | 约 220ms | 约 137ms | 约 22ms | 约 47ms | 约 8ms |
| 高命中备注关键词 | 约 94–98ms | 约 49ms | 约 11ms | 约 24ms | 约 6ms |

最终 5 次进程重复矩阵（每个关键词分别采 1 次冷、2 次热）全局 P95 为 227.798ms，最慢分组为 `user05000 / cold`，P95 为 231.476ms；均低于 300ms。这里的 cold 是每次查询前执行 SQLite `PRAGMA shrink_memory` 的数据库页缓存冷样本，不宣称清空操作系统文件缓存；每个进程首个查询也单独记录。

## 3. 采用方案

将搜索扫描批次从 200 提高到 5,000。10,000 条基准因此最多执行两次候选 SQL，仍保持有限工作集；每个候选仍经过现有 AEAD 解密、JSON 解析和精确字段匹配。该方案不新增明文列、明文索引或搜索日志，也不改变授权谓词和分页语义。

隔离实验（同一 arm64 工作区）测得：200 条批次约 2.92s，1,000 条约 674ms，2,000 条约 369ms，3,000 条约 290ms，5,000 条约 215ms。选择 5,000 是为了给 300ms 门槛留下余量，同时避免一次性把全部候选装入内存。

## 4. keyed blind index / 加密搜索索引评估

### keyed blind n-gram index

可行设计是从主密钥通过独立 domain 派生搜索密钥，对可搜索文本的 3-byte n-gram 计算 HMAC-SHA-256，并把 `(item_id, token)` 存入 postings 表；查询先计算 token 找候选，再解密并执行原始子串匹配消除碰撞和前缀误报。短于 3-byte 的查询要么额外存 1/2-byte token（会显著增加高熵文本和长 secure note 的索引体积），要么回退现有扫描。

该设计不泄露明文，但会向能读取数据库的攻击者泄露 token 相等性、出现频率、条目关联和查询访问模式。搜索密钥随主密钥轮换，恢复/重密钥、创建、更新、标签变更、导入和旧库回填都必须原子重建索引；否则新主密钥下索引不可用。当前 10,000 条目标已由 SQL 批处理在 300ms 内达成，因此暂不引入这些泄露和运维复杂度。

### 加密搜索索引

把 n-gram postings 或 Bloom/filter 索引整体加密可以隐藏 token 相等性，但服务器仍需逐项解密索引或维护可搜索的访问结构；这不能消除访问模式泄露，还会增加每次搜索的解密成本和重密钥迁移面。以当前 SQLite 单实例和 V1 子串语义为目标，它的复杂度/收益比低于批处理方案，暂不采用。

若后续受支持环境中 10,000 条搜索 P95 再次超过 300ms，或实际数据规模明显超过当前基准，将先重新 profile；只有 SQL 批处理和排序优化不足时，才按上面的 keyed index 设计进入单独迁移和安全评审。

## 5. 验证入口

- `go test ./internal/vault -run '^TestDecryptRowProfileReportsDecryptAndJSONPhases$' -count=1 -v`
- `go test ./tests/integration -run '^TestSearchPerformanceBaseline$' -count=1 -v`
- `go test ./tests/integration -run '^TestSearchPerformanceMatrix$' -count=1 -v`
- `TP_SEARCH_REPEATS=5 bash scripts/bench-search.sh`

基准脚本分别报告全局、关键词和冷热模式 P95，并对每组执行 `<=300ms` 门禁。
