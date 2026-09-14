# Android 登录信息记忆设计

## 目标

让 Android App 在冷启动时自动回填上次使用的服务器地址，并通过用户显式选择提供“记住密码”能力；退出登录后必须清除已记住的用户名和密码。

## 范围

- 服务器地址：记忆规范化后的有效 `http(s)` 地址。
- 登录凭据：记忆当前一次登录的用户名和密码，不自动登录。
- 强制改密：只有改密成功且会话确认解除后，才保存新密码。
- 退出登录：清除记忆的用户名和密码，保留服务器地址。
- 切换服务器：清除记忆的用户名和密码，避免跨服务器回填凭据。
- Android 密码存储使用 Android Keystore 保护；服务器地址不属于敏感数据。
- 不实现生物识别、本地会话恢复或离线保险库。

## 用户体验

1. App 冷启动先加载本地登录偏好，再显示登录页，避免服务器地址和凭据出现闪烁。
2. 有已保存地址时，`SERVER` 控件默认折叠并显示地址；没有地址时保持展开。
3. 登录页增加 `REMEMBER PASSWORD` 复选框。没有已保存凭据时默认不勾选；有已保存凭据时自动勾选并回填用户名和密码。
4. 用户取消复选框时立即删除已保存的用户名和密码，但不清空当前输入。
5. 用户点击 `APPLY SERVER` 或登录时，保存有效的服务器地址。
6. 普通登录成功后，若复选框已勾选，保存本次登录使用的用户名和密码；未勾选时不保存。
7. 强制改密阶段不保存旧密码。新密码提交成功并通过 `GET /auth/session` 确认后，若复选框仍勾选，则用新密码替换记忆值。
8. Vault 退出登录及强制改密阶段的退出登录都会清除已记住凭据；服务器地址保持不变。
9. 切换到不同服务器时清除已记住凭据；当前输入的用户名可继续保留，密码输入保持由用户重新填写。
10. 不自动登录，用户仍需点击 `SIGN IN`。

## 架构与数据流

新增 `RememberedLoginStore` 抽象，供 `AppRoot` 和 `SignInScreen` 使用：

- 服务器地址通过 `@react-native-async-storage/async-storage` 存储。
- 用户名和密码通过 `react-native-keychain` 的 Generic Password 接口存储，Android 实现使用 Android Keystore。
- Store 负责统一读写 key、空值处理和异常传播；UI 层负责将存储失败转成非阻塞提示。
- `AppRoot` 在首次渲染登录流程前异步读取 `{serverUrl, credentials}`。读取失败时使用空值继续显示登录页，不阻塞认证。
- `SignInScreen` 接收启动时的初始值以及保存/清除回调，不直接管理存储依赖。
- `SessionController` 继续只管理内存中的 Cookie、CSRF 和认证状态，不保存明文密码。

成功登录的数据流：

```text
冷启动 -> Store.load -> SignInScreen 回填
输入/Apply Server -> normalizeServerUrl -> Store.saveServerUrl
SIGN IN -> SessionController.login
  -> authenticated -> Store.saveCredentials (勾选时)
  -> must-change -> 改密 + confirmSession -> Store.saveCredentials(新密码)
SIGN OUT -> SessionController.signOut + Store.clearCredentials
```

存储操作均为 best effort，不影响服务器认证主流程；保存失败时登录仍可成功，但登录页显示“记住密码未能保存”的提示。清除失败时同样显示明确提示，并继续清理进程内会话和敏感状态。

## 测试策略

- Store 单元测试覆盖：空存储读取、地址读写、凭据读写、取消/退出清除、Keychain 无凭据结果、底层异常传播。
- 登录页行为测试覆盖：启动回填、默认勾选状态、普通登录成功后保存、取消勾选删除、切换服务器清除、退出登录清除、强制改密保存新密码。
- 既有 SessionController 测试保持不变，确认会话、强制改密和退出流程没有回归。
- 运行 `npx tsc --noEmit`、`npx jest --runInBand` 和 Android `./gradlew assembleDebug`。

## 版本

本次 Android App 版本从 `versionCode 1 / versionName 1.0` 提升为 `versionCode 2 / versionName 1.1.0`；`mobile/package.json` 及 lockfile 的移动端包版本同步为 `1.1.0`。
