# Remembered Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Android 登录页持久化服务器地址，并以 Android Keystore 保护可选的记住密码凭据；退出登录时清除凭据，最终将版本更新后的功能合回 `master`。

**Architecture:** 用 `RememberedLoginStore` 抽象隔离存储细节：AsyncStorage 保存规范化服务器地址，react-native-keychain 保存用户名/密码。`AppRoot` 在首次渲染前加载配置，`SignInScreen` 只接收初始值和动作回调；`SessionController` 继续只保存进程内会话数据。

**Tech Stack:** React Native 0.87、TypeScript、Jest、`@react-native-async-storage/async-storage`、`react-native-keychain`、Android Keystore、Gradle。

---

### Task 1: Define and test the remembered-login store

**Files:**
- Create: `mobile/src/auth/rememberedLogin.ts`
- Create: `mobile/__tests__/rememberedLogin.test.ts`

- [ ] **Step 1: Write the failing store tests**

  Create in-memory AsyncStorage and Keychain doubles, then cover the public behavior without importing React Native modules:

  ```ts
  import {createRememberedLoginStore, type KeychainLike, type StorageLike} from '../src/auth/rememberedLogin';

  function setup() {
    const values = new Map<string, string>();
    const storage: StorageLike = {
      getItem: jest.fn(async key => values.get(key) ?? null),
      setItem: jest.fn(async (key, value) => values.set(key, value)),
      removeItem: jest.fn(async key => values.delete(key)),
    };
    let secure: {username: string; password: string} | null = null;
    const keychain: KeychainLike = {
      get: jest.fn(async () => secure),
      set: jest.fn(async (username, password) => {
        secure = {username, password};
      }),
      clear: jest.fn(async () => {
        secure = null;
      }),
    };
    return {store: createRememberedLoginStore({storage, keychain}), storage, keychain};
  }

  test('loads empty values when nothing is persisted', async () => {
    await expect(setup().store.load()).resolves.toEqual({serverUrl: null, credentials: null});
  });

  test('persists the server URL independently from credentials', async () => {
    const {store} = setup();
    await store.saveServerUrl('https://vault.example.com');
    await expect(store.loadServerUrl()).resolves.toBe('https://vault.example.com');
  });

  test('persists and loads username and password through the secure adapter', async () => {
    const {store} = setup();
    await store.saveCredentials({username: 'alice', password: 'correct horse'});
    await expect(store.loadCredentials()).resolves.toEqual({username: 'alice', password: 'correct horse'});
  });

  test('clearCredentials removes both username and password but not the server URL', async () => {
    const {store} = setup();
    await store.saveServerUrl('https://vault.example.com');
    await store.saveCredentials({username: 'alice', password: 'secret'});
    await store.clearCredentials();
    await expect(store.load()).resolves.toEqual({serverUrl: 'https://vault.example.com', credentials: null});
  });

  test('treats a keychain miss as no remembered credentials', async () => {
    const {store, keychain} = setup();
    (keychain.get as jest.Mock).mockResolvedValueOnce(null);
    await expect(store.loadCredentials()).resolves.toBeNull();
  });

  test('propagates storage errors so the UI can show a warning', async () => {
    const {store, storage} = setup();
    (storage.setItem as jest.Mock).mockRejectedValueOnce(new Error('storage unavailable'));
    await expect(store.saveServerUrl('https://vault.example.com')).rejects.toThrow('storage unavailable');
  });
  ```

- [ ] **Step 2: Run the focused tests and confirm the expected RED failure**

  Run:

  ```bash
  cd mobile
  npx jest __tests__/rememberedLogin.test.ts --runInBand
  ```

  Expected: the suite fails because `rememberedLogin.ts` and its exported store contract do not exist yet.

- [ ] **Step 3: Implement the minimal storage contract and in-memory-independent factory**

  Define these exact interfaces and behaviors in `mobile/src/auth/rememberedLogin.ts`:

  ```ts
  export const SERVER_URL_STORAGE_KEY = '@tiny-password/server-url';

  export interface RememberedCredentials {
    username: string;
    password: string;
  }

  export interface RememberedLoginSnapshot {
    serverUrl: string | null;
    credentials: RememberedCredentials | null;
  }

  export interface StorageLike {
    getItem(key: string): Promise<string | null>;
    setItem(key: string, value: string): Promise<void>;
    removeItem(key: string): Promise<void>;
  }

  export interface KeychainLike {
    get(): Promise<RememberedCredentials | null>;
    set(username: string, password: string): Promise<void>;
    clear(): Promise<void>;
  }

  export interface RememberedLoginStore {
    load(): Promise<RememberedLoginSnapshot>;
    loadServerUrl(): Promise<string | null>;
    saveServerUrl(serverUrl: string): Promise<void>;
    loadCredentials(): Promise<RememberedCredentials | null>;
    saveCredentials(credentials: RememberedCredentials): Promise<void>;
    clearCredentials(): Promise<void>;
  }

  export function createRememberedLoginStore(deps: {
    storage: StorageLike;
    keychain: KeychainLike;
  }): RememberedLoginStore {
    return {
      async load() {
        const [serverUrl, credentials] = await Promise.all([
          this.loadServerUrl(),
          this.loadCredentials(),
        ]);
        return {serverUrl, credentials};
      },
      async loadServerUrl() {
        const value = await deps.storage.getItem(SERVER_URL_STORAGE_KEY);
        const trimmed = value?.trim() ?? '';
        return trimmed.length > 0 ? trimmed : null;
      },
      saveServerUrl(serverUrl) {
        return deps.storage.setItem(SERVER_URL_STORAGE_KEY, serverUrl);
      },
      loadCredentials() {
        return deps.keychain.get();
      },
      saveCredentials(credentials) {
        return deps.keychain.set(credentials.username, credentials.password);
      },
      clearCredentials() {
        return deps.keychain.clear();
      },
    };
  }
  ```

- [ ] **Step 4: Run the focused tests and confirm GREEN**

  Run the same Jest command. Expected: all six store tests pass.

- [ ] **Step 5: Commit the store contract and tests**

  ```bash
  git add mobile/src/auth/rememberedLogin.ts mobile/__tests__/rememberedLogin.test.ts
  git commit -m "feat: add remembered login storage contract"
  ```

### Task 2: Add native-backed storage adapters

**Files:**
- Modify: `mobile/package.json`
- Modify: `mobile/package-lock.json`
- Create: `mobile/src/auth/nativeRememberedLoginStore.ts`

- [ ] **Step 1: Add the native storage dependencies**

  From `mobile/`, install the packages so both manifest and lockfile are updated:

  ```bash
  npm install @react-native-async-storage/async-storage react-native-keychain
  ```

  The resulting `dependencies` entries must be present in `mobile/package.json`; do not add a second storage library or persist passwords in AsyncStorage.

- [ ] **Step 2: Implement the Android Keystore-backed adapter**

  Create `nativeRememberedLoginStore.ts` with this boundary:

  ```ts
  import AsyncStorage from '@react-native-async-storage/async-storage';
  import * as Keychain from 'react-native-keychain';
  import {createRememberedLoginStore, type KeychainLike} from './rememberedLogin';

  const KEYCHAIN_SERVICE = 'com.tinypassword.remembered-login';

  const keychain: KeychainLike = {
    async get() {
      const result = await Keychain.getGenericPassword({service: KEYCHAIN_SERVICE});
      if (!result) {
        return null;
      }
      return {username: result.username, password: result.password};
    },
    async set(username, password) {
      await Keychain.setGenericPassword(username, password, {
        service: KEYCHAIN_SERVICE,
        accessible: Keychain.ACCESSIBLE.WHEN_UNLOCKED,
      });
    },
    async clear() {
      await Keychain.resetGenericPassword({service: KEYCHAIN_SERVICE});
    },
  };

  export const nativeRememberedLoginStore = createRememberedLoginStore({
    storage: AsyncStorage,
    keychain,
  });
  ```

  Use the package’s Android Keystore-backed Generic Password implementation; the JS layer must never serialize the password into AsyncStorage, logs, or ordinary files. Keep the adapter module free of UI and session logic.

- [ ] **Step 3: Run type checking and the full unit suite**

  ```bash
  npx tsc --noEmit
  npx jest --runInBand
  ```

  Expected: type checking exits 0, six existing suites plus `rememberedLogin.test.ts` pass, and no test failure is introduced by the native imports.

- [ ] **Step 4: Commit the native adapters and dependencies**

  ```bash
  git add mobile/package.json mobile/package-lock.json mobile/src/auth/nativeRememberedLoginStore.ts
  git commit -m "feat: back remembered login with Android secure storage"
  ```

### Task 3: Integrate startup hydration and login-page behavior

**Files:**
- Modify: `mobile/src/AppRoot.tsx`
- Modify: `mobile/src/screens/SignInScreen.tsx`
- Create: `mobile/__tests__/signinRemembered.test.tsx`

- [ ] **Step 1: Write failing UI behavior tests**

  Add a small fake `SessionController`-shaped object and render `SignInScreen` with `react-test-renderer`. Assert these behaviors through the existing `testID`s plus the new `remember-password` test ID:

  ```ts
  test('uses remembered server and credentials as the initial sign-in values', () => {
    const tree = renderSignIn({
      rememberedCredentials: {username: 'alice', password: 'secret'},
      initialServerUrl: 'https://vault.example.com',
    });
    expect(tree.root.findByProps({testID: 'server-input'}).props.value).toBe('https://vault.example.com');
    expect(tree.root.findByProps({testID: 'username-input'}).props.value).toBe('alice');
    expect(tree.root.findByProps({testID: 'password-input'}).props.value).toBe('secret');
    expect(tree.root.findByProps({testID: 'remember-password'}).props.accessibilityState).toEqual({checked: true});
  });

  test('unchecking remember password immediately clears persisted credentials', async () => {
    const onClear = jest.fn();
    const tree = renderSignIn({
      rememberedCredentials: {username: 'alice', password: 'secret'},
      onRememberedCredentialsCleared: onClear,
    });
    await act(async () => {
      tree.root.findByProps({testID: 'remember-password'}).props.onPress();
    });
    expect(onClear).toHaveBeenCalledTimes(1);
  });

  test('saves credentials only after an authenticated login', async () => {
    const onSave = jest.fn();
    const session = makeAuthenticatedFakeSession();
    const tree = renderSignIn({session, onRememberedCredentialsSaved: onSave});
    await act(async () => {
      tree.root.findByProps({testID: 'username-input'}).props.onChangeText('alice');
      tree.root.findByProps({testID: 'password-input'}).props.onChangeText('secret');
      tree.root.findByProps({testID: 'remember-password'}).props.onPress();
      await tree.root.findByProps({testID: 'sign-in-submit'}).props.onPress();
    });
    expect(onSave).toHaveBeenCalledWith({username: 'alice', password: 'secret'});
  });

  test('saves the new password after confirmed forced password change', async () => {
    const onSave = jest.fn();
    const session = makeMustChangeThenConfirmedFakeSession();
    const tree = renderSignIn({session, onRememberedCredentialsSaved: onSave});
    await submitForcedChange(tree, 'old-secret', 'new-secret-123');
    expect(onSave).toHaveBeenCalledWith({username: 'alice', password: 'new-secret-123'});
  });
  ```

  The helper must expose `onChangeText`, `onPress`, and `accessibilityState` from rendered host components; it must not inspect implementation state directly.

- [ ] **Step 2: Run the focused UI tests and confirm the expected RED failure**

  ```bash
  npx jest __tests__/signinRemembered.test.tsx --runInBand
  ```

  Expected: the suite fails because the new props, checkbox, and persistence callbacks are not implemented.

- [ ] **Step 3: Add AppRoot startup hydration and persistence callbacks**

  In `AppRoot.tsx`:

  - Accept an optional `rememberedLoginStore` prop for tests, defaulting to `nativeRememberedLoginStore`.
  - Add `bootstrapped` state and load `store.load()` in an effect before rendering `SignInScreen`; on load failure continue with `{serverUrl: null, credentials: null}` and retain a warning for the sign-in form.
  - Pass `initialServerUrl={remembered.serverUrl ?? DEFAULT_SERVER_URL}` and `rememberedCredentials={remembered.credentials}` to `SignInScreen`.
  - Save normalized server addresses through a callback that updates in-memory defaults and calls `store.saveServerUrl`; display a non-blocking warning on rejection.
  - Save credentials through a callback that updates the in-memory snapshot and calls `store.saveCredentials`; clear through a callback that removes the secure record and preserves `serverUrl`.
  - Call the clear callback from `handleSignOutFromVault` before routing back to sign-in. The callback must not clear the saved server address.
  - Render a small existing-style loading view while `bootstrapped` is false; do not render a login form with empty values before hydration completes.

  The default `SessionController` creation and all existing session masking/navigation behavior remain unchanged.

- [ ] **Step 4: Implement SignInScreen state and lifecycle rules**

  In `SignInScreen.tsx`:

  - Replace the current `defaultServerUrl`-only initialization with `initialServerUrl` plus optional `rememberedCredentials` props; initialize username/password from the remembered record and initialize `rememberPassword` to whether a complete record exists.
  - Add a 48dp `Pressable` checkbox row labeled `REMEMBER PASSWORD`, with `accessibilityRole="checkbox"`, `accessibilityState={{checked: rememberPassword}}`, and `testID="remember-password"`.
  - When unchecked, call `onRememberedCredentialsCleared` immediately and retain current text fields.
  - Keep `applyServer` responsible for normalization/session switching. On a real server change, clear remembered credentials and clear the password field; on a valid apply, call the save-server callback. Do not clear credentials during the initial hydration apply of the same server.
  - When the session subscription observes `authenticated`, save `{username: username.trim(), password}` only if the checkbox is checked and both values are non-empty, then call `onAuthenticated`.
  - When forced password change confirmation returns `confirmed`, save `{username: usernameRef.current.trim(), password: next}` only if the checkbox remains checked; never save the old password while the phase is `must-change`.
  - On either sign-out path, clear remembered credentials before/alongside session sign-out and clear local password state.
  - Catch persistence callback failures and show them in the existing inline `Banner`; do not turn a successful server login into a failed login.

- [ ] **Step 5: Run focused UI tests and confirm GREEN**

  ```bash
  npx jest __tests__/signinRemembered.test.tsx --runInBand
  ```

  Expected: all remembered-login UI tests pass. Then run `npx jest --runInBand` and confirm the original suites remain green.

- [ ] **Step 6: Commit the application integration**

  ```bash
  git add mobile/src/AppRoot.tsx mobile/src/screens/SignInScreen.tsx mobile/__tests__/signinRemembered.test.tsx
  git commit -m "feat: remember Android login details"
  ```

### Task 4: Update documentation and app versions

**Files:**
- Modify: `mobile/README.md`
- Modify: `docs/mobile/android-ui-design.md`
- Modify: `mobile/package.json`
- Modify: `mobile/package-lock.json`
- Modify: `mobile/android/app/build.gradle`

- [ ] **Step 1: Update the documented persistence and security contract**

  Replace the old statements that say only the server address is remembered or that all data is in memory with the exact behavior: normalized server address is stored locally; username/password are stored only when opted in through Keychain/Android Keystore; no auto-login; password and username are cleared on logout and server switch; Cookie/CSRF/session/item content remains in memory only.

- [ ] **Step 2: Update the mobile package version**

  Set the root `version` in `mobile/package.json` and the matching root package-lock metadata to `1.1.0`. Keep dependency lock entries unchanged except those generated by `npm install`.

- [ ] **Step 3: Update the Android version code and name**

  Change the Android block in `mobile/android/app/build.gradle` to:

  ```gradle
  versionCode 2
  versionName "1.1.0"
  ```

- [ ] **Step 4: Run documentation/version consistency checks**

  ```bash
  rg -n "仅保存地址|默认不持久化|冷启动需重新输入|versionCode|versionName|\"version\"" mobile/README.md docs/mobile/android-ui-design.md mobile/package.json mobile/package-lock.json mobile/android/app/build.gradle
  ```

  Expected: no stale statement claims that the server address is the only remembered value or that credentials can never persist; version values are `1.1.0`, `2`, and `1.1.0` in their respective files.

- [ ] **Step 5: Commit docs and version bump**

  ```bash
  git add mobile/README.md docs/mobile/android-ui-design.md mobile/package.json mobile/package-lock.json mobile/android/app/build.gradle
  git commit -m "chore: bump Android app to 1.1.0"
  ```

### Task 5: Verify, finish the branch, and merge to master

**Files:**
- No additional source files; inspect the full branch diff and Git state.

- [ ] **Step 1: Run the full mobile verification commands**

  ```bash
  cd mobile
  npx tsc --noEmit
  npx jest --runInBand
  cd android
  ./gradlew assembleDebug
  ```

  Expected: TypeScript exits 0; Jest has 0 failed tests (the existing integration suite remains skipped unless explicitly enabled); Gradle `assembleDebug` exits 0 and produces the debug APK with versionName `1.1.0`.

- [ ] **Step 2: Inspect the branch diff and verify the requirement checklist**

  ```bash
  git status --short
  git diff master...HEAD --stat
  git diff master...HEAD -- mobile/src/auth mobile/src/AppRoot.tsx mobile/src/screens/SignInScreen.tsx mobile/android/app/build.gradle mobile/README.md docs/mobile/android-ui-design.md
  ```

  Confirm from the diff that: server address hydrates on cold start, password storage uses Keychain/Keystore only, unchecked/logout/server-switch paths clear credentials, forced change stores only the new password, docs are aligned, and Android version is 2/1.1.0.

- [ ] **Step 3: Merge the completed feature branch into the repository’s main branch**

  The repository’s main branch is `master` (verified before worktree creation). From the clean main worktree:

  ```bash
  git checkout master
  git merge --no-ff feature/remember-login -m "Merge remembered Android login"
  ```

  Resolve no conflicts by overwriting user files; if a conflict occurs, stop and inspect the exact overlapping change before proceeding.

- [ ] **Step 4: Verify the merged main branch**

  ```bash
  git status --short --branch
  git log --oneline -5
  git show --stat --oneline HEAD
  ```

  Expected: `master` is clean, the merge commit contains the remembered-login implementation and version bump, and the final report cites the fresh TypeScript, Jest, Gradle, and Git outputs.
