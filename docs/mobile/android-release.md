# Android APK 发布指南（GitHub Actions）

本文说明如何发布 tiny-password 的 Android APK。日常发布只需要**改一个版本号然后 push**，
编译、签名、打包、发布全部由 CI 完成。

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

APK 直接传到手机安装即可（当前以仓库内 `debug.keystore` 签名，安装时如提示未知来源属正常现象）。

## 版本门控原理

流水线每次运行会：

1. 从 `build.gradle` 读取 `versionName` / `versionCode`
2. 计算 tag 名 `app-v{versionName}-{versionCode}`（例如 `app-v1.0-1`）
3. 该 tag **不存在** → 视为新版本，执行构建，并在成功后创建该 tag 和 Release
4. 该 tag **已存在** → 说明这个版本编译过，任务直接跳过（Actions 里会显示 notice）

因此：

- 只改代码不改版本号 → **不会**发布新 APK
- 改了版本号 → 必然触发一次发布
- 发布产物与 tag 一一对应，可随时从 Releases 回滚下载历史版本

## 手动触发与重新发布

- **手动触发**：Actions → Build Android APK → Run workflow（同样受版本门控约束）
- **重新发布同一版本**（例如构建机故障后重试）：
  ```bash
  git push origin :refs/tags/app-v1.2.0-3   # 删除远端 tag
  git tag -d app-v1.2.0-3                    # 删除本地 tag
  ```
  然后到 Actions 手动 Run workflow，或在 GitHub 上删除对应 Release 后重推版本号改动。

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

## 签名注意事项

**当前状态**：release 变体使用仓库内 `mobile/android/app/debug.keystore`（调试签名）。
这个 key 是 RN 模板的公开调试密钥，仅适合开发与体验安装，**不能用于正式上架**。

换成正式签名的步骤概要：

1. 生成正式 keystore（**不要提交进仓库**）：
   ```bash
   keytool -genkeypair -v -keystore tiny-password-release.keystore \
     -alias tiny-password -keyalg RSA -keysize 2048 -validity 10000
   ```
2. 把 keystore base64 后存入 GitHub Secrets（如 `ANDROID_KEYSTORE_B64`），
   密码与 alias 存入 `ANDROID_KEYSTORE_PASSWORD` / `ANDROID_KEY_ALIAS`
3. 修改 `mobile/android/app/build.gradle` 的 release `signingConfig`，从环境变量
   / CI 解码的 keystore 文件读取
4. 在工作流里加解码 keystore 的步骤，并删除上面"debug 签名"的注释说明

## 常见问题

- **构建在测试步骤失败**：单测（jest）或类型检查（tsc）不通过，本地跑
  `cd mobile && npx tsc --noEmit && npx jest` 复现修复后重新 push。
- **版本号改了但没触发**：确认推送的分支是 `master`，且改动涉及 `mobile/**` 路径。
- **想同时出 debug 包**：工作流里把 `assembleRelease` 改为
  `assembleRelease assembleDebug`，并在上传步骤加对应路径。
- **keystore 丢失**：debug 签名无所谓；正式签名 keystore 一旦丢失将无法以同一签名
  更新应用，务必备份在密码管理器之外的安全位置。
