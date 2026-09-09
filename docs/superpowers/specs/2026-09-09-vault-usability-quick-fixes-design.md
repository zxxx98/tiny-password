# Vault usability quick fixes design

日期：2026-09-09  
范围：先交付保险库易用性文档中的四项低耦合改动；个人保险库与共享库之间的移动另行设计和实现。

## 目标

完成危险操作的红色文字、共享文案统一、身份地址整组复制，以及银行卡号明文输入和详情显示。现有会话、权限、API 数据结构、加密规则和其他敏感字段行为保持不变。

## 方案

### 危险操作样式和文案

在 `web/src/design-system/Button.tsx` 增加 `danger` 变体。该变体使用 `text-accent` 和浅色背景，hover 保持红色文字，沿用现有的键盘 focus、最小触控尺寸和禁用透明度规则。`ConfirmDialog` 在 `danger` 为真时将确认按钮使用该变体；取消按钮仍使用 `secondary`。

详情弹窗、详情操作区、回收站列表和回收站彻底删除确认层全部使用 `danger`。回收站的按钮、确认标题和确认按钮统一使用“彻底删除”；说明可以继续使用“永久清除”。成员管理等其他业务中的“永久删除”不属于保险库本次范围。

### 共享文案

新建条目表单中的保存位置选项从“家庭共享”改为“共享”。编辑表单当前没有保存位置选择；它将在后续移动功能中开放，因此本阶段不伪造编辑移动能力。

### 身份地址复制

新增 `web/src/features/vault/identityClipboard.ts`，导出纯函数 `formatIdentityClipboard(payload)`。格式化规则如下：

- 姓名使用 `full_name`；地址字段按 `state`、`city`、`district`、`address_line`、`postal_code` 顺序处理，不包含 `country`。
- 每个字段先把连续的 CR/LF 换成一个空格，再去掉首尾空白；空值不参与地址拼接，其他值用一个空格连接。
- 姓名、地址、电话始终输出三行，标签使用全角冒号，行尾不增加空行。
- 电话使用 `phone`，保留加号、前导零和分机内容。

`ItemDetail` 在身份条目详情中增加“复制身份地址”按钮，所有能读取详情的用户都可以使用。点击处理只调用浏览器剪贴板 API；成功显示“已复制身份地址”，失败显示“复制失败，请重试或手动复制”。成功后复用 `scheduleClipboardCleanup` 做尽力清理，不新增审计事件，也不写日志或持久化存储。

### 银行卡号显示

`CreditCardFields` 将卡号输入改为 `type="text"`、`inputMode="numeric"`、`autoComplete="off"`，输入值继续以字符串保存，避免前导零和长卡号精度丢失；删除遮蔽提示。

为 `SensitiveField` 增加局部 `displayMode` 选项，默认仍为 masked。银行卡号使用 plain 模式，始终显示完整字符串，只保留复制能力；plain 模式不显示“显示/遮蔽”按钮，也不伪造 reveal 审计。CVV、PIN、密码、私钥和私钥口令继续使用默认遮蔽模式及原有 reveal/copy 审计。

## 文件边界

- 修改 `web/src/design-system/Button.tsx`：新增危险操作按钮变体。
- 修改 `web/src/design-system/Dialog.tsx`：危险确认层的确认按钮复用危险变体。
- 修改 `web/src/features/vault/ItemDialog.tsx`、`ItemDetail.tsx`、`TrashPage.tsx`：接入危险按钮、删除文案和身份复制。
- 修改 `web/src/features/vault/ItemEditor.tsx`：更新新建表单的共享文案。
- 修改 `web/src/features/vault/forms/CreditCardFields.tsx`：改卡号输入属性和提示。
- 修改 `web/src/features/vault/SensitiveField.tsx`：增加局部明文展示模式。
- 新增 `web/src/features/vault/identityClipboard.ts`：承载身份地址纯格式化函数。
- 仅更新现有 UI 测试中受文案影响的断言；本阶段不新增 UI 测试用例。

## 验证

先运行 `npm run typecheck` 和 `npm test -- --run`。再运行 `npm run build` 验证前端产物，检查构建后的危险按钮样式仍由 `danger` 变体提供。最后按仓库现有 `scripts/test-browser-e2e.sh` 流程执行浏览器回归；如果环境不具备浏览器依赖，记录具体阻断信息。

个人/共享移动、保存位置确认、服务端 scope 更新、AAD 重加密、历史版本迁移以及银行卡与身份地址引用一致性不在本阶段实现或验收。
