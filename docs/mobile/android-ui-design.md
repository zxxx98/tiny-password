# Android App UI Design · React Native MVP

状态：提案  
日期：2026-09-11  
目标平台：Android  
客户端技术：React Native  
视觉基线：仓库根目录 `style.md` 的 Newsprint design system

## 1. 目标

Android 第一版只覆盖密码管理器最核心的使用闭环：

1. 用户名 + 密码登录
2. 查看和搜索密码条目
3. 查看 / 编辑已有密码
4. 新增密码
5. 生成密码并直接填入条目

第一版不增加独立 Settings、底部多 Tab、账号注册、社交、云同步配置等额外页面。移动端应保持界面极简、路径短、信息密度高，并沿用现有 Newsprint 视觉语言。

## 2. 信息架构

```text
Sign In
  |
  v
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

完成账号认证。当前服务端使用用户名 + 密码，因此登录页只提供这两个核心输入。

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
- 主按钮 `SIGN IN`
- Inline error area

### 4.4 交互规则

- 用户名支持系统自动填充，但不主动展示历史账号列表。
- 密码默认隐藏。
- 点击眼睛图标只改变当前输入可见性，不保存明文状态。
- 键盘提交动作在密码字段中等价于点击 `SIGN IN`。
- 登录请求进行中禁用重复提交并显示机械、克制的 loading 状态。
- 登录失败在按钮下方显示红色错误文字，不使用 Toast 作为唯一错误反馈。

### 4.5 生物识别规则

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

如 V1 暂不实现生物识别，本规则不会改变任何页面结构，只需省略自动解锁流程。

## 5. Screen 02 · Vault

### 5.1 目的

作为 App 的主页面，快速完成“找到密码并使用”的高频任务。

### 5.2 页面结构

```text
┌──────────────────────────┐
│ tiny-password            │
│ LOCAL VAULT / 128        │
├──────────────────────────┤
│ [ Search passwords...  ] │
├──────────────────────────┤
│ GITHUB                 > │
│ github.com               │
│ you@example.com          │
├──────────────────────────┤
│ AMAZON                 > │
│ amazon.com               │
│ you@example.com          │
├──────────────────────────┤
│ NOTION                 > │
│ notion.so                │
│ you@example.com          │
├──────────────────────────┤
│ [      + ADD ENTRY     ] │
└──────────────────────────┘
```

### 5.3 列表行

每条记录最多展示三层信息：

1. Title：Playfair Display / bold
2. URL / domain：Lora 或 Inter
3. Username：JetBrains Mono

整行可点击，不额外堆叠多个小按钮。

### 5.4 搜索

- 顶部常驻搜索框。
- 输入后即时过滤。
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
│ URL                      │
│ https://github.com    ⧉  │
├──────────────────────────┤
│ NOTES                    │
│ Personal account...      │
├──────────────────────────┤
│ [        DELETE        ] │
└──────────────────────────┘
```

### 6.3 Edit Mode

点击 `EDIT` 后，当前页面原地切换为可编辑表单：

- 标题变为可输入
- Username / Password / URL / Notes 可编辑
- 顶部操作变为 `CANCEL` + `SAVE`
- 删除仍保留，但放在页面底部危险区域

禁止为了编辑再 push 一个完全相同的 Screen。

### 6.4 复制行为

- Username、Password、URL 支持一键复制。
- 点击复制后给出短暂、非阻塞的 `COPIED` 状态。
- 密码复制默认不要求先显示明文。

### 6.5 密码显示

- 默认使用掩码。
- 用户主动点击眼睛按钮后临时显示。
- 离开页面后恢复为隐藏状态。

### 6.6 删除

- 使用 Accent Red。
- 点击后必须二次确认。
- 确认文案必须明确说明删除的是哪一个条目。

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
- URL
- Notes

顶部：`CANCEL` / `SAVE`。

Password 字段右侧增加 `GENERATE` 入口。

新增页面不显示 `DELETE`。

## 8. Password Generator Bottom Sheet

### 8.1 调用方式

从 New Entry 或 Edit Mode 的 Password 字段调用，不设独立底部 Tab。

### 8.2 页面结构

```text
┌──────────────────────────┐
│ PASSWORD GENERATOR       │
├──────────────────────────┤
│ f9K!2mQ8#vL4pZ7s     ⧉   │
│                          │
│ LENGTH: 16               │
│ 8 ─────────────── 32     │
│                          │
│ [x] Uppercase            │
│ [x] Lowercase            │
│ [x] Numbers              │
│ [x] Symbols              │
│                          │
│ [      USE PASSWORD    ] │
└──────────────────────────┘
```

### 8.3 行为

- 打开后立即生成一个符合当前参数的密码。
- 修改长度或字符类型后即时重新生成。
- 保留现有“一键复制”能力。
- 点击 `USE PASSWORD`：
  1. 将当前生成值填入 Password 字段；
  2. 关闭 Bottom Sheet；
  3. 不自动保存整个条目。
- 至少保留一种字符类型，不能全部取消。

## 9. 导航与返回行为

```text
Sign In -> Vault
Vault -> Entry Detail
Vault -> New Entry
Entry Detail <-> Edit Mode (same screen)
New/Edit -> Generator Sheet
```

Android 系统 Back：

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
│   └── PasswordGeneratorSheet.tsx
├── theme/
│   ├── colors.ts
│   ├── typography.ts
│   └── spacing.ts
└── auth/
    └── biometricUnlock.ts
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

如果后续确定需要导入 / 导出、共享、管理等能力，再基于现有 Newsprint 组件扩展，而不是提前占据 MVP 导航结构。

## 14. 验收标准

实现满足以下条件即可视为 UI MVP 结构完成：

- [ ] 用户可用用户名 + 密码完成登录。
- [ ] 登录页不存在生物识别按钮。
- [ ] 已启用生物识别时，本地解锁流程自动触发系统 BiometricPrompt。
- [ ] 用户可查看和搜索密码列表。
- [ ] 用户可查看、复制、显示 / 隐藏已有密码。
- [ ] 用户可在同一详情页进入编辑并保存。
- [ ] 用户可新增条目。
- [ ] 用户可从密码字段打开生成器。
- [ ] 生成器可复制并将密码回填到条目。
- [ ] UI 遵守 Newsprint：浅色纸张背景、锐角、明确边框、高对比排版、极少量红色强调。
- [ ] Android Back、键盘、安全区及基本可访问性行为正确。
