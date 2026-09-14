# Android App UI Design · React Native MVP

状态：提案  
日期：2026-09-11  
目标平台：Android  
客户端技术：React Native  
视觉基线：仓库根目录 `style.md` 的 Newsprint design system

## 1. 目标

Android 第一版只覆盖密码管理器最核心的使用闭环：

1. 用户名 + 密码登录，并在需要时在登录页完成强制改密
2. 查看和搜索个人保险库中的登录密码条目
3. 查看 / 编辑已有密码
4. 新增密码
5. 生成密码并直接填入条目

第一版不增加独立 Settings、底部多 Tab、账号注册、社交、云同步配置等额外页面。移动端应保持界面极简、路径短、信息密度高，并沿用现有 Newsprint 视觉语言。

### 1.1 MVP 数据与接口边界

- 当前服务端支持 `login`、`ssh_key`、`credit_card`、`identity`、`secure_note`、`secret` 六种条目；Android MVP 只展示和编辑 `login`。
- MVP 只访问 `personal` 保险库。列表固定传 `scope=personal&type=login`，搜索请求固定传 `scope: personal, type: login`；新增固定传 `vault_scope: personal, item_type: login`。
- 共享条目、跨保险库移动及其他五种类型保留在 Web 使用，移动端不将它们装入登录密码编辑器。详情加载后仍校验类型、范围和 `owner_id`；不满足范围或归属要求时不提供查看/编辑入口，提示通过 Web 使用。
- 数据由现有服务端存储，移动端通过 API 在线读写。V1 不提供离线保险库、离线搜索或离线保存，也不使用 `LOCAL VAULT` 文案。
- 不新增服务端业务能力；下文列出的接口、字段和错误码以当前实现为基线。

### 1.2 字段映射与保存约定

| UI 字段 | API 字段 | 规则 |
| --- | --- | --- |
| Title | `payload.name` | 必填，最多 256 个 Unicode 字符；列表的 `title` 是服务端派生值 |
| Username | `payload.username` | 可选，最多 256 个 Unicode 字符 |
| Password | `payload.password` | 可选，最多 1024 UTF-8 字节；不同于账号登录密码策略 |
| URLs | `payload.urls[]` | 可选，最多 16 项，每项最多 2048 个 Unicode 字符，保留顺序 |
| Notes | `payload.notes` | 可选，最多 10000 个 Unicode 字符 |

- 编辑前必须通过 `GET /api/v1/items/{itemId}` 获取完整详情。以原始 `payload` 为基础合并页面修改，不能只用可见字段重建 payload。
- V1 不展示 `password_updated_at`、`password_expires_at` 编辑控件，保存时原样保留这两个字段。`tags`、`favorite` 不提供操作，更新请求省略这两个字段以保留服务端原值；不得发送空数组或 `false` 覆盖旧值。
- 新增调用 `POST /api/v1/items`，携带类型、范围和 payload，标签/收藏默认空/否。同一份创建请求的网络重试复用 `Idempotency-Key`；修改请求内容后使用新 key。
- 保存调用 `PUT /api/v1/items/{itemId}`，携带读取时的 `revision` 和完整合并后的 `payload`，省略 `vault_scope`。成功后以服务端返回的详情及新 revision 更新页面。
- `409 REVISION_CONFLICT` 时保留当前编辑，展示“条目已在其他设备更新”，提供继续编辑及重新加载服务端内容的操作；重新加载覆盖草稿前需确认。禁止自动覆盖或盲目更换 revision 重试。

## 2. 信息架构

```text
Sign In
  |-- must_change_password --> Change Password (same screen)
  |
  v  (authenticated, password change completed if required)
Vault
  |-- tap entry --> Entry Detail / Edit
  |-- add entry --> New Entry
                       |
                       +-- Generate Password --> Generator Bottom Sheet
```

已有条目的查看和编辑共用同一页面；新增条目复用同一编辑器，只是初始数据为空。

Android MVP 只需要三个顶层 Screen：

- `SignInScreen`
- `VaultScreen`
- `EntryEditorScreen`

密码生成器实现为 `PasswordGeneratorSheet`，不作为独立导航页面。

## 3. 全局视觉规范

视觉规则直接继承 `style.md`。

### 3.1 色彩

| Token | Value | 用途 |
| --- | --- | --- |
| Background | `#F9F9F7` | 主背景，模拟新闻纸 |
| Foreground | `#111111` | 文本、主边框、主按钮 |
| Muted | `#E5E5E0` | 次级背景、分隔 |
| Accent | `#CC0000` | 危险操作、错误、极少量强调 |

永久浅色模式。Android MVP 不做独立 Dark Mode。

### 3.2 字体

- 标题 / Display：`Playfair Display`
- 正文：`Lora`
- UI 标签 / 按钮：`Inter`
- 密码、账号、URL、技术信息：`JetBrains Mono`

字体按现有项目决策自托管，不依赖运行时 Google Fonts。

### 3.3 几何与层级

- 所有组件 `borderRadius = 0`
- 主结构使用 `1dp` 黑色实线边框
- 重要分区可使用 `2–4dp` 底边
- 不使用模糊、渐变、软阴影、玻璃拟态
- 允许极轻的新闻纸点阵 / 颗粒背景，但必须保证输入区与正文可读性
- 交互元素最小触控区域 `48dp`

### 3.4 基础间距

- 页面左右安全边距：`16dp`
- 大区块垂直间距：`24dp`
- 表单字段间距：`16dp`
- 列表行垂直内边距：`14–16dp`
- 主按钮高度：`52–56dp`

## 4. Screen 01 · Sign In

### 4.1 目的

完成账号认证及必要的强制改密。`SignInScreen` 内包含 `signIn` 和 `changePassword` 两个阶段，不新增顶层 Screen。普通登录阶段提供用户名与密码输入。

### 4.2 页面结构

```text
┌──────────────────────────┐
│        tiny-password     │
│      PRIVATE VAULT       │
├──────────────────────────┤
│                          │
│          SIGN IN         │
│                          │
│ USERNAME                 │
│ [ username             ] │
│                          │
│ PASSWORD                 │
│ [ •••••••••••       👁 ] │
│                          │
│ [ ] REMEMBER PASSWORD    │
│                          │
│ [        SIGN IN       ] │
│                          │
│      error message       │
│                          │
│  PRIVATE / SECURE        │
└──────────────────────────┘
```

### 4.3 组件

- Logo / `tiny-password`
- `UsernameInput`
- `PasswordInput`
- 密码显示 / 隐藏按钮
- `REMEMBER PASSWORD` 复选框
- 主按钮 `SIGN IN`
- Inline error area

### 4.4 交互规则

- 用户名支持系统自动填充，但不主动展示历史账号列表。
- 密码默认隐藏。
- 点击眼睛图标只改变当前输入可见性，不保存明文状态。
- 有已保存凭据时自动回填用户名和密码并勾选 `REMEMBER PASSWORD`；没有已保存凭据时默认不勾选。
- 取消 `REMEMBER PASSWORD` 立即删除已保存的用户名和密码，但不清空当前输入。
- 键盘提交动作在密码字段中等价于点击 `SIGN IN`。
- 登录请求进行中禁用重复提交并显示机械、克制的 loading 状态。
- 登录失败在按钮下方显示红色错误文字，不使用 Toast 作为唯一错误反馈。

### 4.5 强制改密阶段

- 登录响应 `must_change_password=true`，或恢复会话时 `user.must_change_password=true`，必须在同一 `SignInScreen` 切换到改密阶段，不能加载 Vault。业务请求返回 `403 PASSWORD_CHANGE_REQUIRED` 时也进入该阶段。
- 标题为 `CHANGE PASSWORD`，说明“首次登录需要设置新密码，完成后其他设备的旧会话将失效”。表单包含 Current Password、New Password、Confirm New Password，均默认隐藏；不长期保留或自动回填登录时的明文密码。
- 主按钮为 `SAVE AND CONTINUE`，次操作为 `SIGN OUT`。没有跳过入口，Android Back 不得进入 Vault；离开改密阶段需按退出登录流程处理，有未保存输入时先确认。
- 新密码至少 12 个 Unicode 字符、最多 1024 UTF-8 字节；确认密码必须一致。确认字段只在客户端校验，不发送服务端。密码策略失败及限流错误在表单内展示，请求期间禁用重复提交。
- 使用当前受限会话调用 `POST /api/v1/auth/password`，发送 `current_password`、`new_password` 及当前 CSRF token。
- 成功响应为 `204`，接收轮换后的会话 Cookie 和响应头 `X-CSRF-Token`，清空三个密码输入，再调用 `GET /api/v1/auth/session` 确认改密要求已解除后进入 Vault。该操作会撤销所有旧会话。
- 改密失败不进入 Vault；若返回 `401`，清理本地会话并回到普通登录阶段，展示认证失败提示。

### 4.6 服务端连接与会话

- 登录页提供可折叠的 `SERVER` 地址控件，首次无地址时展开。地址为 HTTPS 服务源地址，可由构建配置预填，也可在登录前修改；有效地址会持久化并在下次冷启动回填。服务端须已通过 Web 完成初始化，MVP 不提供 setup 页面。
- 当前认证使用 Cookie，不使用 Bearer token。原生网络层统一管理 Cookie，并遵守域、路径、Secure 和过期约束；更换服务器前清理旧会话上下文，禁止跨服务器携带 Cookie 或 CSRF token。
- 登录前调用 `POST /api/v1/csrf` 获取预认证 Cookie 和 `csrf_token`；登录请求携带该 Cookie 与 `X-CSRF-Token`。登录成功后使用新的会话 Cookie 和响应体中的 `csrf_token`，不复用预认证上下文。
- 已认证写请求均携带会话 Cookie 和 `X-CSRF-Token`。原生请求可省略 Origin；如发送，必须与服务源地址匹配，不发送 `null` 或伪造的跨域 Origin。
- 通过 `GET /api/v1/auth/session` 校验会话及恢复 CSRF 上下文。会话有效性由服务端决定；基于前台真实用户操作节流调用 `POST /api/v1/auth/session/activity`，后台轮询或读请求不能代替续期。
- MVP 会话 Cookie 只在进程内保存，冷启动重新登录。仅当用户勾选 `REMEMBER PASSWORD` 时，用户名和密码才通过 Android Keystore 保护后持久化；不自动登录。条目详情、草稿和 CSRF token 不写入普通本地持久化存储。
- 取消记住密码、切换服务器或退出登录都会清除已记住的用户名和密码，服务器地址仍保留。普通登录成功后保存本次凭据；强制改密在新密码提交并确认会话成功后只保存新密码，不保存旧密码。
- Vault 顶栏提供 `SIGN OUT`。退出时调用 `POST /api/v1/auth/logout`，清理 Cookie、CSRF、敏感内存和已记住凭据并取消在途请求；网络失败仍完成本地退出，同时明确提示服务端会话未确认撤销。`401` 同样清理并回到登录阶段，`403 ACCOUNT_DISABLED` 展示账号停用原因后退出。

### 4.7 生物识别规则（后续扩展，V1 不实现）

**登录页不显示指纹 / 生物识别按钮。**

生物识别不是账号登录方式，而是用户已经成功登录后，用于解除本地 App 锁定状态的便捷方式。

规则如下：

1. 首次使用、主动退出登录、服务端会话失效时，必须回到用户名 + 密码登录。
2. 用户已经完成账号登录，且设备支持并已启用 App 生物识别解锁时：
   - App 冷启动进入“已登录但本地锁定”状态，自动调用 Android `BiometricPrompt`。
   - App 从后台恢复且超过锁定阈值时，自动调用 `BiometricPrompt`。
3. 用户取消或生物识别失败：保持锁定，并提供回到普通用户名 + 密码登录的路径。
4. 生物识别成功后，如果检测到服务端 session 已失效，仍然必须回到登录页。
5. 登录页本身不出现 `Use fingerprint`、`Use biometric` 或类似 CTA。

V1 明确不实现生物识别，不展示启用开关或解锁按钮。上述流程是后续扩展约束；启用前必须补充登录后的 opt-in 入口、独立于敏感内容的锁定遮罩、重试/退出操作、后台锁定阈值，以及受 Android Keystore 保护的会话持久化方案。不能仅调用 BiometricPrompt 就视为完成本地锁定。

## 5. Screen 02 · Vault

### 5.1 目的

作为 App 的主页面，快速完成“找到密码并使用”的高频任务。

### 5.2 页面结构

```text
┌──────────────────────────┐
│ tiny-password   SIGN OUT │
│ PERSONAL VAULT           │
├──────────────────────────┤
│ [ Search passwords...  ] │
├──────────────────────────┤
│ GITHUB                 > │
│ LOGIN / PERSONAL         │
├──────────────────────────┤
│ AMAZON                 > │
│ LOGIN / PERSONAL         │
├──────────────────────────┤
│ NOTION                 > │
│ LOGIN / PERSONAL         │
├──────────────────────────┤
│ [      + ADD ENTRY     ] │
└──────────────────────────┘
```

### 5.3 列表行

列表仅使用当前接口返回的 `Meta`，每条记录展示：

1. Title：Playfair Display / bold
2. Type / Scope：Inter 或 JetBrains Mono，V1 固定显示 `LOGIN / PERSONAL`

整行可点击，不额外堆叠多个小按钮。

列表和搜索接口都不返回 Username、URLs 或完整 payload，V1 不为装饰列表逐条请求详情。只有打开条目后才加载完整详情。分页响应只有 `items`、`next_cursor`，不显示未获知的总条目数；若展示数量，必须标为“已加载 N 条”。

### 5.4 搜索

- 顶部常驻搜索框。
- 输入后防抖约 300ms，调用 `POST /api/v1/items/search`，请求体携带 `query`、`scope: personal`、`type: login`，不只过滤当前已加载数据。
- 匹配名称、用户名、全部 URLs、标签和备注，不搜索密码明文。标签虽然不可编辑，仍可能使既有条目命中搜索。
- 首次列表调用 `GET /api/v1/items?scope=personal&type=login`；列表和搜索都根据 `next_cursor` 提供 `LOAD MORE`，搜索续页在请求体携带 `cursor`。
- 查询变化时重置分页，取消旧请求并忽略迟到响应；清空搜索后重新加载普通列表。搜索中、分页加载中和失败状态独立展示，续页失败保留已有结果并提供重试。
- 没有结果时显示简单空状态，不使用插画。
- 清空按钮只能在存在搜索文本时出现。

### 5.5 新增入口

采用底部完整宽度的 `+ ADD ENTRY` 主操作，而不是永久悬浮的彩色 FAB，以保持 Newsprint 的矩形结构感。

## 6. Screen 03 · Entry Detail / Edit

### 6.1 目的

同一页面完成查看、复制、编辑和删除，避免“详情页 → 编辑页”的多余跳转。

### 6.2 View Mode

```text
┌──────────────────────────┐
│ <          GITHUB   EDIT │
├──────────────────────────┤
│ TITLE                    │
│ GITHUB                   │
├──────────────────────────┤
│ USERNAME                 │
│ you@example.com       ⧉  │
├──────────────────────────┤
│ PASSWORD                 │
│ •••••••••••       👁  ⧉  │
├──────────────────────────┤
│ URLS                     │
│ https://github.com    ⧉  │
├──────────────────────────┤
│ NOTES                    │
│ Personal account...      │
├──────────────────────────┤
│ [    MOVE TO TRASH     ] │
└──────────────────────────┘
```

### 6.3 Edit Mode

点击 `EDIT` 后，当前页面原地切换为可编辑表单：

- 标题变为可输入
- Username / Password / URLs / Notes 可编辑；URLs 逐行输入，支持添加和移除，最多 16 项，保持原顺序
- 顶部操作变为 `CANCEL` + `SAVE`
- 删除仍保留，但放在页面底部危险区域

禁止为了编辑再 push 一个完全相同的 Screen。

查看模式逐项显示和复制所有 URLs；可选字段为空时显示 `—` 并隐藏无效的复制按钮。保存遵循 §1.2 的字段保留和版本冲突规则。

V1 只编辑当前用户的个人条目。后续如开放共享列表，必须根据 `creator_id` 判断写权限：共享条目所有成员可读，但仅创建者能修改或删除，管理员无额外写权限；其他成员只能进入只读详情，隐藏 `EDIT` 和删除操作。

### 6.4 复制行为

- Username、Password、每个 URL 支持一键复制。
- 点击复制后给出短暂、非阻塞的 `COPIED` 状态。
- 密码复制默认不要求先显示明文。

### 6.5 密码显示

- 默认使用掩码。
- 用户主动点击眼睛按钮后临时显示。
- 离开页面后恢复为隐藏状态。

### 6.6 删除

- 主操作文案为 `MOVE TO TRASH`，调用 `DELETE /api/v1/items/{itemId}`，语义是移入回收站，不是永久删除。
- 使用 Accent Red。
- 点击后必须二次确认。
- 确认文案明确包含条目标题，例如“将 GITHUB 移入回收站？可通过 Web 回收站在保留期内恢复。”
- 编辑状态下删除时，确认文案同时说明未保存修改将被丢弃。
- 成功后返回 Vault 并移除该行，显示“已移入回收站”；失败则保留页面和草稿，提供重试。
- V1 不提供回收站、恢复或永久删除页面，也不调用 `/purge`。

## 7. New Entry

新增密码直接复用 `EntryEditorScreen`：

```text
mode = create
entry = empty
```

页面字段：

- Title
- Username
- Password
- URLs（可增删的有序输入列表，初始提供一个空输入行）
- Notes

顶部：`CANCEL` / `SAVE`。

Password 字段右侧增加 `GENERATE` 入口。

新增页面不显示 `MOVE TO TRASH`。Title 必填，其余字段可选；未填写的 URL 输入行不生成数组元素。请求固定创建个人 `login` 条目，保存校验和幂等重试遵循 §1.2。

## 8. Password Generator Bottom Sheet

### 8.1 调用方式

从 New Entry 或 Edit Mode 的 Password 字段调用，不设独立底部 Tab。

### 8.2 页面结构

```text
┌──────────────────────────┐
│ PASSWORD GENERATOR       │
├──────────────────────────┤
│ f9K!2mQ8#vL4pZ7sT6@n ⧉  │
│                          │
│ LENGTH: 20               │
│ 8 ─────────────── 128    │
│                          │
│ [x] Uppercase            │
│ [x] Lowercase            │
│ [x] Numbers              │
│ [x] Symbols              │
│ [ ] Exclude ambiguous    │
│                          │
│ [      REGENERATE      ] │
│ [      USE PASSWORD    ] │
└──────────────────────────┘
```

### 8.3 行为

- 调用现有 `POST /api/v1/generators/password`，显式传 `length`、`uppercase`、`lowercase`、`digits`、`symbols`、`exclude_ambiguous`，从响应的 `value` 读取生成值。该功能需要有效会话和网络。
- 参数与服务端对齐：默认长度 20，可选 8–128；四种字符类型默认全选，排除易混淆字符默认关闭。`Numbers` 对应 API 的 `digits`。
- 打开后立即生成。修改参数后防抖重新请求；提供 `REGENERATE`，允许不改参数再次生成。取消旧请求并忽略迟到响应。
- 生成中或失败时禁用复制和 `USE PASSWORD`，避免使用旧参数对应的密码；错误在 Sheet 内展示并允许重试，失败不覆盖编辑器中原有密码。
- 保留现有“一键复制”能力。
- 点击 `USE PASSWORD`：
  1. 将当前生成值填入 Password 字段；
  2. 关闭 Bottom Sheet；
  3. 不自动保存整个条目。
- 至少保留一种字符类型，不能全部取消。

## 9. 导航与返回行为

```text
Sign In -> Vault
Sign In -> Change Password -> Vault (same SignInScreen, when required)
Vault -> Sign In (sign out / session expired)
Vault -> Entry Detail
Vault -> New Entry
Entry Detail <-> Edit Mode (same screen)
New/Edit -> Generator Sheet
```

Android 系统 Back：

- 强制改密阶段：按 §4.5 处理退出，不允许绕过改密进入 Vault。
- Generator 打开时：先关闭 Sheet。
- 编辑状态且内容已修改：提示是否放弃修改。
- View Mode：返回 Vault。
- Vault：按 Android 默认 App 行为处理。

第一版不使用底部 Navigation Bar，因为只有一个主工作区。

## 10. 状态设计

所有页面至少覆盖：

- Loading
- Empty
- Error
- Offline / network unavailable（涉及服务端请求时）
- Disabled

错误信息优先放在发生问题的控件附近。

Vault 首次加载失败时，应在内容区提供显式 Retry，不能只显示 Toast。

同时覆盖：强制改密、账号停用/会话失效、`409 REVISION_CONFLICT`、详情 `404`（条目已删除或无权访问）、写入 `403 FORBIDDEN`（刷新详情并重新判断权限，不自动重试）。限流遵循 `Retry-After`；断网保存失败保留当前进程内草稿，但明确尚未保存至服务端。

## 11. Android 与可访问性要求

- 遵守系统 Safe Area / 状态栏 / 导航栏 inset。
- 表单页面必须正确响应软键盘，当前输入不能被 IME 遮挡。
- 所有可点击目标至少 `48dp`。
- 图标按钮必须有 accessibility label。
- 不只依赖红色表达错误或危险状态，同时提供文字。
- 支持系统字体缩放；关键按钮和输入不得在常见缩放级别截断。
- 密码字段禁止无意进入普通文本自动学习 / 个性化建议流程。

## 12. React Native 组件建议

```text
src/
├── screens/
│   ├── SignInScreen.tsx
│   ├── VaultScreen.tsx
│   └── EntryEditorScreen.tsx
├── components/
│   ├── NewsprintHeader.tsx
│   ├── NewsprintInput.tsx
│   ├── PrimaryButton.tsx
│   ├── PasswordRow.tsx
│   ├── CopyButton.tsx
│   ├── ChangePasswordForm.tsx
│   ├── UrlListField.tsx
│   └── PasswordGeneratorSheet.tsx
├── theme/
│   ├── colors.ts
│   ├── typography.ts
│   └── spacing.ts
├── api/
│   ├── client.ts
│   └── types.ts
└── auth/
    └── session.ts
```

建议将视觉 token 集中维护，不在 Screen 中散落颜色、字号和边框常量。

## 13. 非目标

以下内容不属于本次 Android UI MVP：

- 独立 Settings 页面
- 独立 Generator Tab
- 底部多 Tab 导航
- 注册页
- 把指纹作为账号登录方式
- 新的云同步产品设计
- 为移动端新增服务端业务能力
- 管理员后台 UI
- 共享保险库及跨保险库移动
- 非 `login` 类型、标签/收藏编辑、密码日期编辑
- 回收站、历史版本、恢复及永久删除页面
- 生物识别解锁、跨冷启动保留会话、离线保险库
- 口令短语和 SSH 密钥生成（服务端已有，移动端 V1 不展示）

如果后续确定需要导入 / 导出、共享、管理等能力，再基于现有 Newsprint 组件扩展，而不是提前占据 MVP 导航结构。

## 14. 验收标准

实现满足以下条件即可视为 UI MVP 结构完成：

- [ ] 用户可用用户名 + 密码完成登录。
- [ ] 登录页可配置服务地址，原生网络层正确处理预认证 CSRF、会话 Cookie 和会话轮换。
- [ ] 必须改密的账号在同一登录页完成当前密码/新密码/确认密码流程；Back 和恢复会话均不能绕过改密。
- [ ] 登录页不存在生物识别按钮。
- [ ] V1 不启用生物识别或持久化会话；服务器地址可持久化，记住密码为显式可选且退出时清除；冷启动、退出和会话失效进入普通登录。
- [ ] 用户可查看和搜索个人 `login` 条目，其他范围/类型不会进入该编辑器。
- [ ] 列表仅使用 Meta；服务端搜索及列表均可分页，旧响应不覆盖新查询，无虚构总数。
- [ ] 用户可查看、复制、显示 / 隐藏已有密码。
- [ ] 用户可在同一详情页进入编辑并保存。
- [ ] 多 URLs 完整展示、复制和编辑；未展示的日期、标签和收藏不会被保存操作清空。
- [ ] 更新携带 revision，冲突保留草稿并提供重新加载；创建重试不会重复创建同一请求。
- [ ] 删除需确认标题，实际移入回收站，并说明 Web 恢复路径。
- [ ] 用户可新增条目。
- [ ] 用户可从密码字段打开生成器。
- [ ] 生成器可复制并将密码回填到条目。
- [ ] 生成器默认 20、范围 8–128，支持排除易混淆字符及重新生成；请求失败不回填旧值。
- [ ] UI 遵守 Newsprint：浅色纸张背景、锐角、明确边框、高对比排版、极少量红色强调。
- [ ] Android Back、键盘、安全区及基本可访问性行为正确。
