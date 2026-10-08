# Android APK 发布指南（GitHub Actions）

本文说明如何发布 tiny-password 的 Android APK。日常发布只需要**改一个版本号然后 push**，
编译、签名、打包、发布全部由 CI 完成，无需额外配置签名 Secrets。普通 PR 和 push 另有独立的移动端类型检查、单测与 Android debug 构建，不依赖版本号变更。

流水线文件：[`.github/workflows/build-apk.yml`](../../.github/workflows/build-apk.yml)

## 日常发布流程

### 1. 修改版本号

编辑 [`mobile/android/app/build.gradle`](../../mobile/android/app/build.gradle) 的 `defaultConfig`：

```groovy
defaultConfig {
    ...
    versionCode 3        // 整数，每次发布 +1
    versionName "1.2.0"  // 用户可见的版本号
}
```

两个都建议一起改：

- `versionName`：用户看到的版本号
- `versionCode`：整数单调递增，Android 用它判断升级

### 2. 提交并推送到 master

```bash
git add -A
git commit -m "release: v1.2.0"
git push
```

推送后自动触发构建（只要改动涉及 `mobile/**` 或工作流文件本身）。

### 3. 查看构建进度

仓库 GitHub 页面 → **Actions** 标签页 → "Build Android APK" 工作流。

- 首次构建约 15~25 分钟（NDK ~2GB 下载 + C++ 编译）；之后有 npm/Gradle 缓存会快很多
- 编译前会先跑 `tsc --noEmit` 和 jest 单测，测试失败则不会产出 APK

### 4. 获取 APK

构建成功后有两个出口：

| 位置 | 说明 |
| --- | --- |
| **Releases**（推荐） | 仓库 → Releases → 对应 `TinyPassword v{versionName}` → 下载 `app-release.apk`，自动生成 release notes |
| **Artifacts** | Actions → 对应 run → Artifacts 区域，仅登录 GitHub 可见，适合测试用 |

APK 沿用仓库内的 `mobile/android/app/debug.keystore` 签名，与历史已发布版本保持同一签名，支持覆盖安装。

## 版本门控原理

流水线每次运行会：

1. 从 `build.gradle` 读取 `versionName` / `versionCode`
2. 计算 tag 名 `app-v{versionName}-{versionCode}`（例如 `app-v1.0-1`）
3. 查询该 tag 对应的 GitHub Release，只有正式发布且包含已上传的非空 `app-release.apk` 才跳过
4. 没有 Release、只有 tag、草稿或缺少 APK → 执行构建，成功后发布 APK；API 查询异常则报错

因此：

- 同一版本的 APK 已发布 → **不会**重复发布
- 上次发布失败、只有 tag 没有 APK → 可以重跑补发
- 改了版本号 → 必然触发一次发布
- 发布产物与 tag 一一对应，可随时从 Releases 回滚下载历史版本

## 手动触发与重新发布

- **手动触发**：Actions → Build Android APK → Run workflow（同样受版本门控约束）
- **重试失败的发布**：Actions → Build Android APK → Run workflow；只有 tag、尚无 APK 时仍会构建。
- 已发布的版本请增加版本号后发布，保留历史产物。若修复构建需要新提交，应确保尚未发布的 tag 指向实际构建提交。

## 本地构建（对照）

不想走 CI 时可以本地编译，产物路径相同：

```bash
cd mobile
npm install
cd android
./gradlew assembleRelease   # app/build/outputs/apk/release/app-release.apk
```

本地开发调试（Metro 热更新）用 debug 变体：`npm run start` + `npm run android`，
见 [mobile/README.md](../../mobile/README.md)。

## 签名说明

`release`、`debug` 和 `e2e` 均使用仓库中的 `mobile/android/app/debug.keystore`，沿用最初发布的签名身份；无需 `ANDROID_KEYSTORE_*` 或 `ANDROID_KEY_*` Secrets。

此密钥已公开，不具备私有正式签名密钥的身份保护能力。目前沿用它是为了兼容旧版本覆盖安装。以后切换独立签名时需另行安排迁移，不能直接覆盖安装旧签名版本。

Release 仍禁止明文 HTTP，`e2e` 仅供本地 HTTP 联调。

## 常见问题

- **构建在测试步骤失败**：单测（jest）或类型检查（tsc）不通过，本地跑
  `cd mobile && npx tsc --noEmit && npx jest` 复现修复后重新 push。
- **版本号改了但没触发**：确认推送的分支是 `master`，且改动涉及 `mobile/**` 路径。
- **想同时出 debug 包**：工作流里把 `assembleRelease` 改为
  `assembleRelease assembleDebug`，并在上传步骤加对应路径。
- **覆盖安装失败**：确认应用包名相同、构建号递增且使用同一 `debug.keystore`。
