# Android 共享条目显示设计

## 目标

修复 Android React Native 客户端不显示用户有权读取的共享条目。登录后的主列表和搜索结果都应包含当前用户自己的个人条目以及所有可读取的共享条目；打开共享条目沿用服务端权限，非创建者不能写入。

## 根因

服务端 `GET /api/v1/items` 与 `POST /api/v1/items/search` 在省略 `scope` 时已经返回“当前用户个人条目 + 全部共享条目”。Android 的 `TinyPasswordApi.listItems` 和 `searchItems` 却显式发送 `scope=personal`，因此服务端合法地过滤掉共享条目。`VaultScreen` 只消费这两个接口，所以列表、分页和搜索均受影响。

## 方案

让 Android 列表与搜索请求省略 scope 参数，以使用服务端默认的可见条目集合。保留 `ItemMeta.vault_scope`，列表行继续展示 `PERSONAL` 或 `SHARED`，确保用户能区分来源；共享详情和现有创建/编辑流程不扩大写权限。空列表文案改为描述“可见条目”，避免共享条目存在时文案仍声称只有个人库。

不修改服务端、数据库、权限模型或分页协议。版本号从当前 Android `versionCode 4` / `versionName "1.2.1"` 提升到 `versionCode 5` / `versionName "1.2.2"`，触发 GitHub Actions 的 APK 构建门控。

## 测试与验收

- API 客户端单元测试断言列表 URL 不含 `scope=personal`，搜索 body 不含 `scope: personal`，并保留 cursor/limit。
- Android 集成测试创建个人和共享条目，断言 `listItems` 同时返回两者，搜索也能命中共享条目。
- 运行 `npx tsc --noEmit` 与 `npx jest --runInBand`。
- 检查 Android 版本号递增，运行可用的 Gradle 校验或 release 构建；推送后确认远端工作流以 `app-v1.2.2-5` 作为新 APK 版本。

