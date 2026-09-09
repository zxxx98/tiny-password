# Vault Move and Address Removal Design

日期：2026-09-09  
状态：已确认，待实现

## 目标

为现有保险库编辑流程增加个人保险库与共享库之间的双向移动，并按用户确认移除身份地址条目 `IdentityPayload` 及银行卡 `billing_address_item_id` 字段的产品功能、API 契约和数据库类型约束。

## 移动语义

移动沿用 `PUT /api/v1/items/{itemId}`。请求省略 `vault_scope` 时保持普通更新行为；请求中的目标 scope 与当前 scope 不同，则进入移动流程。

移动流程在一个 SQLite 事务内完成：

1. 读取源条目，检查条目未在回收站、revision 匹配、操作者是个人条目所有者或共享条目创建者。
2. 解密当前 payload，并使用本次提交的 payload、标签和收藏状态构建目标内容。
3. 服务端根据操作者和目标 scope 派生目标归属；客户端不能指定 owner/creator。
4. 以新 UUID、目标归属、revision 1 和新 nonce 加密并插入目标条目。目标条目不复制源条目的历史版本；创建时间和更新时间使用移动时刻。
5. 删除源条目。数据库级联删除源条目的历史版本，但审计记录保留。
6. 写入移动审计和目标条目创建审计后提交事务。

移动成功返回新条目的完整详情，因此前端必须切换到新的条目 ID。任何解密、加密、插入、删除或审计失败都会回滚，不能留下目标副本或已删除源条目的半完成状态。

移动银行卡时保留当前 payload 中的普通字段，不再对 `billing_address_item_id` 做任何特殊处理；该字段会从类型、校验、API 和编辑表单中移除。移动不扫描、不复制、不重加密任何历史版本。

## API、审计和权限

`itemUpdateRequest` 与 `vault.UpdateInput` 增加可选 `vault_scope`。非法 scope 返回现有 `VALIDATION_ERROR`；旧客户端省略该字段时行为不变。scope 变化使用新条目的 revision 1，不返回旧条目 revision。

新增 `vault.item.moved` 审计事件。审计记录保存操作者、源条目 ID、目标条目 ID、来源 scope 和目标 scope；scope 是固定枚举，不保存 payload、标题、标签或其他明文。为保持现有审计查询兼容，新增字段使用可空数据库列，既有审计事件不需要填充。

移动权限仍使用原条目的写权限：共享条目的普通成员和管理员不能移动，个人条目的其他用户和管理员不能移动。源条目删除后的旧 ID 按现有不可见条目规则返回 `NOT_FOUND`；目标条目按目标库规则授权。

## 移除身份地址功能

产品当前有效类型从五类调整为四类：`login`、`ssh_key`、`credit_card`、`secure_note`。

后端移除：

- `TypeIdentity`、`IdentityPayload` 及其 envelope、解码、校验、搜索和种子分支。
- `CreditCardPayload.BillingAddressItemID`、`referenceTarget` 以及创建/更新/导入中的账单地址引用校验和重写。
- 导入格式中对 identity 类型的支持。

前端移除身份地址类型、编辑字段、详情渲染、整组复制工具和相关测试；银行卡表单移除账单地址 ID 字段。银行卡其余字段和卡号明文展示行为保持不变。

数据库新增迁移删除已有 identity 条目及其历史，并重建 `vault_items` 的类型 CHECK 约束为四类。之后的 schema、OpenAPI 和测试 fixture 只允许四类。历史设计/发布记录可以保留当时的五类背景，但当前开发文档不再描述身份地址和账单地址能力。

## 前端交互

编辑表单在创建和编辑模式都显示“保存位置”，scope 纳入 dirty snapshot。编辑时改变 scope 后提交，先弹出二次确认：

- 个人 → 共享：说明会创建一个新的共享条目、旧条目会被删除、历史记录不会保留。
- 共享 → 个人：说明会创建一个新的个人条目、旧条目会被删除、历史记录不会保留。

取消确认只关闭确认框并保留草稿；确认后才发送更新请求。保存成功后详情切换为服务端返回的新 ID，并显示“已移至共享”或“已移至个人保险库”。列表刷新失败只显示列表错误，不把已成功的移动报告为失败。

revision 冲突继续保留草稿并显示当前 revision。网络结果不确定时先重新读取源条目；若源条目仍存在则允许重试，若源条目已不存在则读取目标条目或刷新列表确认移动结果，避免重复创建。

## 测试与验证

后端集成测试覆盖：四类条目分别双向移动、内容/标签/收藏保留、新 ID、目标 revision 1、源条目和历史删除、审计保留、权限拒绝、非法 scope、revision 冲突、事务失败回滚以及移动后的新旧 ID 可见性。

前端测试覆盖：编辑位置选择、dirty 状态、双向确认文案、取消保留草稿、确认请求 body、新 ID 保存结果、成功提示、列表刷新失败和网络不确定状态。

移除功能的测试覆盖：四类类型枚举、identity API 请求拒绝、银行卡未知 `billing_address_item_id` 字段拒绝、数据库迁移删除旧 identity 数据、导入/搜索/详情/编辑不再出现身份地址能力。

验证命令：

```bash
go test ./...
npm --prefix web run typecheck
npm --prefix web test -- --run
npm --prefix web run build
```
