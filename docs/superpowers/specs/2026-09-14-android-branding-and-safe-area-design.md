# Android 品牌与顶部安全区设计

## 目标

本次改动只针对 React Native Android 包：应用在启动器中显示新的几何钥匙孔图标，应用名显示为 `77Password`，所有带返回按钮的页面与 Android 状态栏保持足够间距。

Web/PWA、iOS、服务端接口、包名和现有登录/保险库行为不变。

## 已确认的视觉方向

采用方案 A「几何钥匙孔」：沿用 `web/public/icons/icon.svg` 的黑、红、米白配色和八边形钥匙孔构图。Android 使用同一视觉稿生成普通与圆形 launcher 的各密度 PNG；不修改 Web/PWA 图标文件，因此 Android 品牌升级不会改变 Web/PWA 展示。

## 资源与应用名

`mobile/android/app/src/main/res/values/strings.xml` 中的 `app_name` 改为 `77Password`。现有 `AndroidManifest.xml` 已通过 `android:label="@string/app_name"` 同时配置 application 和 launcher activity，无需改变 Manifest 结构。

保留 `applicationId "com.tinypassword"`，保证新 APK 能作为原应用升级安装。保留 `mobile/app.json`、iOS 配置及 Web/PWA 名称 `TinyPassword`，避免扩大本次 Android-only 需求范围。

由于 GitHub Actions 会按 `versionName`/`versionCode` 跳过已发布版本，本次 release metadata 从 `1.2.0`/`3` 提升为 `1.2.1`/`4`，仅用于触发这次 Android APK 打包，不改变应用行为。

launcher 资源继续放在现有目录：

- `mipmap-mdpi`：48 × 48
- `mipmap-hdpi`：72 × 72
- `mipmap-xhdpi`：96 × 96
- `mipmap-xxhdpi`：144 × 144
- `mipmap-xxxhdpi`：192 × 192

每个密度同时更新 `ic_launcher.png` 和 `ic_launcher_round.png`。沿用现有 Manifest 引用，兼容当前 minSdk 24，不引入 Adaptive Icon 的额外前景/背景层和裁切差异。

## 顶部安全区与返回按钮

### 根因

`NewsprintHeader` 当前只有固定的 `16dp` 顶部内边距，状态栏安全区由调用页面自行添加。密码生成器及条目加载/错误状态添加了 `insets.top`，但条目正常详情/编辑路径没有，因此返回按钮可能落入状态栏附近。这个问题源于安全区职责分散，而不是返回按钮的点击逻辑。

### 设计

由共享的 `NewsprintHeader` 统一调用 `useSafeAreaInsets()`，将 `insets.top + spacing.md` 作为 Header 顶部内边距。这样所有使用该组件的返回按钮都得到相同的状态栏避让和原有 16dp 设计留白。

同步移除密码生成器和条目页面对 Header 的重复 `paddingTop: insets.top`，避免安全区叠加；条目详情/编辑的正常渲染路径不再遗漏安全区。页面底部安全区保持各页面现有处理方式。

受影响的 Header 页面为密码生成器、条目详情和条目编辑；登录页和 Vault 顶部自有布局，不改变其现有安全区处理。

## 测试与验收

先增加一个 Header 单元测试，使用带有非零顶部 inset 的 `SafeAreaProvider` 渲染组件，并断言 Header 顶部内边距等于安全区加设计间距。该测试在修复前应失败，修复后通过。

增加 Android 元数据回归测试，读取 `strings.xml` 并断言 `app_name` 为 `77Password`；同时断言 5 个密度目录中的普通与圆形 launcher 文件均存在且为非空 PNG。

完成修改后运行：

1. `npm --prefix mobile test -- --runInBand`
2. `npm --prefix mobile run lint`
3. 检查 `.github/workflows/build-apk.yml` 仍会在 GitHub Actions 中执行 Android release 构建。

APK 不在本地构建，由推送后的 GitHub Actions 工作流负责打包。工作流完成后检查其产物构建成功；任何 Android 资源尺寸或格式错误都应由 GitHub Actions 的资源合并/构建阶段直接暴露，不添加运行时兜底逻辑。

## 非目标

- 不修改 Web/PWA 的名称和图标。
- 不修改 Android 包名、登录逻辑或导航行为；版本号仅按发布门控从 `1.2.0`/`3` 提升为 `1.2.1`/`4`。
- 不把返回按钮改成系统导航按钮，也不改变 Android BackHandler 行为。
- 不引入新的图标设计系统或 Adaptive Icon 架构。
