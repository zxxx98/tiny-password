# KV 密钥条目设计

## 目标

新增独立的 `secret` 保险库条目类型，用于在一个条目中按录入顺序保存多组 key/value 数据。密钥值默认隐藏，用户可以单独复制某个 value，也可以一键复制全部 KV；查看和复制不产生审计事件。

## 范围

本次包含：

- 新建、查看、编辑、搜索筛选、历史、移动、回收站和导入导出流程对 `secret` 类型的支持。
- 多组 KV 的增删、排序保持、校验、显示/隐藏、单项复制与复制全部。
- 数据库约束、Go 类型与校验、OpenAPI、Web 类型与界面、自动化测试同步更新。

本次不包含：

- 拖拽排序、KV 批量文本导入、`.env` 语法解析或 shell 转义。
- 针对 KV key/value 的搜索或明文索引。
- KV 模板、自动生成 value、字段级权限和独立的查看/复制审计。

## 方案选择

采用独立的 `secret` 条目类型，payload 使用有序数组：

```json
{
  "name": "生产环境 API",
  "entries": [
    { "key": "API_KEY", "value": "example-key" },
    { "key": "API_SECRET", "value": "example-secret" }
  ],
  "notes": "仅供部署使用"
}
```

数组能够明确保留用户的录入顺序，并让重复 key 校验和逐行编辑具有稳定语义。直接使用 JSON 对象会弱化顺序语义；复用安全笔记并解析 `KEY=VALUE` 则无法提供可靠的字段校验和逐项复制，均不采用。

## 数据模型和约束

后端新增：

- 条目类型常量 `TypeSecret = "secret"`，并加入 `ItemTypes`、加密 envelope 和 payload 分派。
- `SecretPayload`：`name string`、`entries []SecretEntry`、`notes string`。
- `SecretEntry`：`key string`、`value string`。

校验规则：

- `name` 必填，最多 256 个 Unicode code point，沿用其他条目标题限制。
- `entries` 至少 1 组、最多 128 组。
- 每个 `key` 去除首尾空白后必须非空，最多 256 个 Unicode code point；存储时保留原始输入，不自动改写。
- 同一条目内的 key 必须唯一。唯一性按原始字符串精确比较，大小写敏感；`FOO` 与 `foo` 可同时存在。前后空白参与唯一性比较，但仅由空白组成的 key 无效。
- `value` 允许为空，最多 16,384 个 Unicode code point。
- `notes` 可选，最多 10,000 个 Unicode code point。
- 整个明文 envelope 继续受现有 256 KiB 上限约束。
- 未知字段继续由严格 JSON 解码拒绝。

payload schema version 保持为 1：这是在 envelope 中增加新的可选类型分支，不改变已有条目的表示。数据库通过新迁移重建 `vault_items` 的 `item_type` CHECK 约束，将 `secret` 加入允许集合，并保留所有现有列、索引和数据。

## 后端与 API

`decodePayload`、`buildEnvelope`、`typedPayloadOf` 和 `TitleOf` 增加 `secret` 分支。条目生命周期继续复用现有 Service、Repository、加密、权限、乐观锁、历史、移动与回收站逻辑，不新增专用 endpoint。

OpenAPI 的 `ItemType` enum 加入 `secret`，新增 `SecretEntry` 与 `SecretPayload` schema，并把 `SecretPayload` 纳入条目 payload 的联合类型。创建和更新仍使用现有 `/api/v1/items` 接口。

搜索只匹配现有可搜索元数据（标题和标签），不搜索 key 或 value。列表摘要由客户端显示为“密钥 · N 个键值”，列表响应不解密或返回 entries。

通用加密归档导入导出允许 `secret` 类型并保持 payload 原样往返。Bitwarden 导入不映射为 `secret`，既有映射规则不变。

## 前端交互

类型选择器增加“密钥”。`SecretFields` 负责编辑：

- 标题和备注使用现有表单样式。
- 初始提供一行 KV；每行包含 key、value、“显示/隐藏”和“删除”。
- value 使用密码输入框，默认隐藏。新增行同样默认隐藏。
- “添加一组”追加到末尾；删除后其余项相对顺序不变；最后一行不可删除，以满足至少一组的约束。
- 提交前显示空 key、重复 key、数量及长度错误，并由服务端再次执行同样校验。

`SecretValueField` 负责详情页每个 value 的默认隐藏、显示/隐藏和复制。它只在本地改变可见状态或调用 Clipboard API，不调用 `/audit/reveal` 或 `/audit/copy`。这与现有需要审计的 `SensitiveField` 明确分离，避免误发审计请求。

详情页按存储顺序展示所有 key：

- 每行可一键复制该行原始 value，包括空字符串。
- “复制全部”生成 `key=value` 行，并用 `\n` 连接，末尾不额外添加换行。
- key 和 value 不加引号、不转义；value 中的换行原样保留。因此输出是便捷文本格式，不保证能被 `.env` 或 shell 直接解析。
- 复制成功或失败均显示 `role="status"` 状态消息；成功后复用现有 `scheduleClipboardCleanup` 尝试定时清空剪贴板。
- 显示或复制 value 均不记录审计事件。

## 数据流与错误处理

创建或编辑时，React 将有序 `entries` 数组随现有条目请求发送。HTTP 层严格解码 payload，Vault Service 完成类型与限制校验，将带 `secret` 分支的 envelope 加密后通过现有事务保存。读取时 Service 解密并返回同一顺序的数组，详情组件再按行渲染。

客户端校验用于即时反馈，服务端校验是最终约束。无效 payload 使用现有 `PAYLOAD_INVALID` 错误响应；超过 envelope 总限制使用现有 payload-too-large 响应；版本冲突、权限和归档错误均沿用现有处理。Clipboard API 拒绝或不可用时不改变 value，仅展示“复制失败，请重试或手动复制”。

## 测试策略

后端测试覆盖：

- `SecretPayload` 的有效数据、空 value、空白 key、重复 key、大小写敏感唯一性、条目数和字段长度边界。
- envelope 构建、解密、标题提取和顺序保持。
- API 创建、读取、更新、历史、个人/共享权限、移动与回收站。
- 新迁移从旧 schema 升级后保留已有数据，并同时接受 `secret`、拒绝未知类型。
- 通用加密归档导出、预览和导入对 `secret` 的无损往返。

前端测试覆盖：

- 新类型、空 payload、校验和列表摘要。
- KV 新增、删除、顺序保持、空 key 与重复 key 的行内错误。
- value 初始隐藏、逐行显示/隐藏、复制单项、复制空值、复制全部的精确文本。
- Clipboard API 失败提示、成功后的清理调度，以及所有查看/复制路径都没有审计请求。
- E2E 完成新建多组 KV、查看、复制全部、编辑后再次读取并确认顺序和值。

验证依次运行相关 Go 单元/集成测试、Web Vitest、相关 Playwright 场景，最后执行项目的全量检查命令。

## 验收标准

- 用户能创建包含 1–128 组 KV 的“密钥”条目，保存和再次打开后顺序及内容不变。
- 同一条目不接受空 key 或完全相同的重复 key；value 可以为空。
- 所有 value 在编辑页和详情页初始均隐藏。
- 用户能复制单个 value，或以精确的多行 `key=value` 格式复制全部。
- 查看和复制不产生审计 API 请求或审计事件，复制成功后触发现有剪贴板清理机制。
- 新类型适用于现有生命周期、权限、历史、移动、回收站和通用加密归档流程，旧数据无需重加密且行为不回归。
