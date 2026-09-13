# tiny-password · Android (React Native MVP)

Newsprint 视觉的 Android 原生密码管理器 MVP（React Native 0.87 + TypeScript，Community CLI 工程，非 Expo，无 WebView）。

覆盖的设计文档：`../docs/mobile/android-ui-design.md`。
V1 范围：登录（含同页强制改密）、个人 `login` 条目列表/搜索/分页、查看/编辑/新增/移入回收站、密码生成器 Bottom Sheet。不含生物识别、离线保险库、Settings、注册、共享、回收站页面。

## 环境要求

| 组件 | 版本 | 说明 |
| --- | --- | --- |
| Node.js | ≥ 22.11 | 模板 engines 约束 |
| JDK | 17（或 21） | AGP / Gradle 9.4.1 |
| Android SDK | Platform `android-37.0`、Build-Tools `37.0.0`、Platform-Tools | 由 `sdkmanager` 安装 |
| NDK | 27.1.12297006 | 新架构 C++ 编译；Gradle 可自动装 |
| Android Studio | 可选 | 仅模拟器/设备调试用 |

`android/local.properties`（或 `ANDROID_HOME`）指向 SDK。模拟器需 x86_64 宿主，或带 KVM 的 arm64 宿主。

> 本 MVP 在 linux-aarch64 无 KVM 的环境验证过完整 Gradle 构建：x86_64 的 aapt2/zipalign/NDK clang 通过 `qemu-user-static` binfmt 运行，需 `export QEMU_LD_PREFIX=/usr/x86_64-linux-gnu` 并安装 `libc6-amd64-cross`、`libgcc-s1-amd64-cross`、`libstdc++6-amd64-cross`（zlib1g 的 amd64 版需从 Ubuntu deb 解包放入前缀 `lib/`）。常规 x86_64 或 Apple Silicon 环境无需这些步骤。
linux-aarch64 还需在 `android/app/build.gradle` 的 `react {}` 块指定 `hermesCommand`（插件无 arm64 分支；本工程已指向 `hermes-compiler` 包的 linux64-bin，可经 binfmt 运行）。

## 目录结构

```text
src/
├── api/        # HTTP 客户端、进程内 CookieJar、错误解析、竞态工具
├── auth/       # SessionController：登录/改密/会话状态机（框架无关，可测）
├── vault/      # payload 合并、字段校验、幂等键管理（纯函数）
├── screens/    # SignInScreen / VaultScreen / EntryEditorScreen
├── components/ # Newsprint 按钮/输入/表头/复制行/对话框/生成器 Sheet 等
├── theme/      # colors.ts / typography.ts / spacing.ts（Newsprint token）
└── AppRoot.tsx # 导航状态机、Android Back、后台恢复遮罩、活动节流接线
android/app/src/main/assets/fonts/   # 自托管 TTF（Playfair/Lora/Inter/JetBrains Mono）
```

## 安装与启动

```bash
cd mobile
npm install            # 含原生依赖：clipboard / slider / safe-area-context
npm run start          # Metro 开发服务器
npm run android        # 需连接设备或模拟器；构建并安装 debug 变体
```

## 服务器连接

- 登录页顶部可折叠 `SERVER` 控件，输入 HTTPS 服务源地址（如 `https://vault.example.com`），点 `APPLY SERVER`。地址是唯一被允许记忆的配置；本 MVP 默认不持久化任何内容，冷启动需重新输入/预填。
- 预填默认地址：编辑 `src/config.ts` 的 `DEFAULT_SERVER_URL`（构建期常量，非敏感）。
- 服务端需已完成一次 Web 初始化（setup）；MVP 不提供 setup 页面。测试服务器本地启动方式见仓库根 `compose.yaml` 或 `Makefile`。
- 认证完全依赖 Cookie + `X-CSRF-Token`：登录前 `POST /api/v1/csrf` 获取预认证上下文，登录后使用会话 Cookie 与响应体中的 CSRF token；改密后消费 `X-CSRF-Token` 响应头完成轮换。

## 测试

```bash
npx tsc --noEmit                 # 类型检查
npx jest                         # 单元测试：状态机/字段保留/幂等/竞态/校验
MOBILE_INTEGRATION=1 npx jest __tests__/integration
# ↑ API 级联调：自动 go build 并启动真实服务端（临时目录、随机端口、一次性
#   setup token），用应用同款客户端代码走通 setup→成员创建→强制改密→
#   列表/搜索/详情→幂等重试→409 冲突→回收站→登出 401 全流程
```

注意：`MOBILE_INTEGRATION=1` 会真实创建管理员/成员账号并写入临时数据目录，仅供本地验证，不要对准生产实例。

## 构建 APK

```bash
cd mobile/android
./gradlew assembleRelease   # app/build/outputs/apk/release/app-release.apk
./gradlew assembleE2e       # 本地联调变体：bundle JS + debug 签名 + 允许明文 HTTP
./gradlew assembleDebug     # 开发变体：Metro 热更新，不带 bundle
```

- `release`：打包 Hermes 字节码与字体资源；不允许明文流量；当前以 debug keystore 签名（便于安装体验，上架前必须更换正式签名）。
- `e2e`：在 release 基础上允许对测试服务器使用 HTTP（`usesCleartextTraffic=true`）。不要用于生产。

## 安全行为约定（实现要点）

- Cookie/CSRF/密码/条目内容只存在于进程内存（`CookieJar`、控制器状态）；不使用 AsyncStorage/文件，`allowBackup=false`。冷启动回到登录页。
- 会话生命周期由服务端决定：前台真实操作触发节流的 `POST /auth/session/activity`（≥5 分钟一次）；后台恢复先遮罩内容并 `GET /auth/session` 校验，通过后才恢复展示。
- 退出/切换服务器/401 会中断在途请求并提升会话代号（generation），迟到响应一律丢弃；切换服务器时清空整个 Cookie jar，禁止跨服务器携带 Cookie 或 CSRF。
- 条目更新合并原始 payload（保留 `password_updated_at`/`password_expires_at`），省略 `tags`/`favorite`/`vault_scope`；`revision` 冲突保留草稿，提供“重新加载服务端内容”，重新加载前二次确认。
- 创建使用内容绑定的 `Idempotency-Key`：同内容网络重试复用 key，修改内容即换 key。
- `DELETE /items/{id}` 仅移入回收站；MVP 不调用 `/purge`。
- 强制改密阶段：新密码 ≥12 字符 / ≤1024 UTF-8 字节、不得等于当前密码；确认密码仅本地校验；改密成功后会话确认失败时只重试确认，绝不重复提交改密；Back 不能绕过改密。

## 已知限制

- 仅 `personal` + `login`；其他类型/共享条目在详情加载时拒绝打开并提示用 Web。
- 未实现：生物识别、Settings、回收站页面、历史版本、口令短语/SSH 生成、注册、管理端。
- 模拟器/真机联调需要在有 Android 模拟器运行能力的环境执行（linux-aarch64 无官方模拟器二进制）。
