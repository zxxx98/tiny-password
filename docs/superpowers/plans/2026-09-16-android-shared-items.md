# Android Shared Items Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Android vault show the current user's personal items together with all readable shared items, including search results, and publish a new APK-triggering Android version.

**Architecture:** Keep the server-side readable-item policy as the source of truth. Change only the mobile API client to omit the restrictive `scope=personal` filter, so the existing server default returns the union of the caller's personal items and shared items. Keep item metadata and the existing detail/editor permissions unchanged; update only stale mobile copy that says the list is personal-only.

**Tech Stack:** React Native 0.87, TypeScript, Jest, Node fetch client, Go integration server, Android Gradle plugin.

---

### Task 1: Lock the API request contract with a failing unit test

**Files:**
- Modify: `mobile/__tests__/client.test.ts:163-182`
- Modify: `mobile/src/api/client.ts:295-315`

- [ ] **Step 1: Change the client contract assertions first**

In `mobile/__tests__/client.test.ts`, rename the test to `list/search cover all readable vault items` and replace the personal-scope assertions with:

```ts
it('list/search cover all readable vault items', async () => {
  const client = new ApiClient('https://vault.example.com');
  const urls: string[] = [];
  const bodies: unknown[] = [];
  setFetch(client, async (url: string, init: FetchInit) => {
    urls.push(url);
    bodies.push(init.body ? JSON.parse(init.body) : undefined);
    return jsonResponse(200, {items: [], next_cursor: null});
  });
  const api = new TinyPasswordApi(client);
  await api.listItems('cursor-1', 37);
  expect(urls[0]).toContain('/items?limit=37');
  expect(urls[0]).not.toContain('scope=personal');
  expect(urls[0]).toContain('cursor=cursor-1');

  await api.searchItems('git hub', 'cursor-2', 'csrf-1', undefined, 37);
  expect(urls[1]).toContain('/items/search');
  expect(bodies[1]).toMatchObject({query: 'git hub', cursor: 'cursor-2', limit: 37});
  expect(bodies[1]).not.toHaveProperty('scope');
});
```

- [ ] **Step 2: Run the focused test and verify the expected failure**

Run:

```bash
cd mobile && npx jest __tests__/client.test.ts --runInBand
```

Expected: the new test fails because the current client still sends `scope=personal` and the list URL starts with `scope=personal`.

### Task 2: Remove the client-side personal-only filter

**Files:**
- Modify: `mobile/src/api/client.ts:295-315`
- Modify: `mobile/src/screens/VaultScreen.tsx:51-55,322-326`
- Modify: `mobile/README.md:6,91`
- Test: `mobile/__tests__/VaultScreen.test.tsx`

- [ ] **Step 1: Make list and search use the server's readable-item default**

Change `listItems` to initialize its path as:

```ts
let path = `/items?limit=${limit}`;
```

and build the search request body as:

```ts
const body: Record<string, unknown> = {
  query,
  limit,
};
```

Keep cursor encoding, CSRF, pagination, and all other request behavior unchanged.

- [ ] **Step 2: Update stale Android list copy and documentation**

Change the `VaultScreen` documentation from “Personal vault” / “Only `scope=personal`...” to describe the readable vault containing personal and shared items. Change the empty-state strings to `没有可见条目。` and `可见保险库中还没有条目，点击下方新增。`; leave the row's `item.vault_scope` label intact so shared items remain visibly identified.

Update `mobile/README.md` to state that personal and readable shared items appear in list/search, while shared detail/edit remains a Web-only limitation.

Add a React Native renderer regression asserting the mixed list header says `READABLE VAULT`.

- [ ] **Step 3: Run the focused unit test and typecheck**

Run:

```bash
cd mobile && npx jest __tests__/client.test.ts --runInBand
cd mobile && npx tsc --noEmit
```

Expected: the focused Jest suite passes and TypeScript exits with code 0.

### Task 3: Add a real-server regression for shared list and search visibility

**Files:**
- Modify: `mobile/__tests__/integration/api.integration.test.ts:190-305`

- [ ] **Step 1: Add a shared item through the real API**

In the existing authenticated `vault: create with idempotent retry, list Meta, search, detail preservation` test, after the personal item fixtures are created, issue the same API request shape the server exposes:

```ts
const sharedPayload = {
  name: 'Shared GitHub',
  username: 'shared@example.com',
  password: 'shared-secret-1',
};
const shared = await api.request<{id: string}>({
  method: 'POST',
  path: '/items',
  body: {item_type: 'login', vault_scope: 'shared', payload: sharedPayload},
  csrfToken: member.csrfToken!,
  extraHeaders: {'Idempotency-Key': 'idem-key-shared-00001'},
});
expect(shared.kind).toBe('success');
```

This deliberately keeps `TinyPasswordApi.createItem` personal-only; the regression setup uses the server contract without adding an unrelated mobile shared-create feature.

- [ ] **Step 2: Assert both scopes appear in list and search**

Update the existing list expectation from 7 to 8 and replace the fixed first-item scope assertion with:

```ts
expect(listData.items).toHaveLength(8);
expect(new Set(listData.items.map(item => item.vault_scope))).toEqual(new Set(['personal', 'shared']));
expect(listData.items.some(item => item.title === 'Shared GitHub' && item.vault_scope === 'shared')).toBe(true);
```

Add a search assertion alongside the existing personal search assertions:

```ts
const sharedHit = await api.searchItems('Shared GitHub', null, member.csrfToken!);
expect((sharedHit as unknown as {data: {items: Array<{title?: string; vault_scope: string}>}}).data.items).toEqual([
  expect.objectContaining({title: 'Shared GitHub', vault_scope: 'shared'}),
]);
```

- [ ] **Step 3: Run the integration regression with the real Go server**

Run:

```bash
cd mobile && MOBILE_INTEGRATION=1 npx jest __tests__/integration/api.integration.test.ts --runInBand
```

Expected: the complete integration suite passes, including the shared list and search assertions.

### Task 4: Bump the Android release version and verify packaging inputs

**Files:**
- Modify: `mobile/android/app/build.gradle:88-89`

- [ ] **Step 1: Increment both Android release values**

Change the current values to:

```gradle
versionCode 5
versionName "1.2.2"
```

The GitHub Actions gate will therefore derive the new tag `app-v1.2.2-5` instead of skipping the already published `app-v1.2.1-4` version.

- [ ] **Step 2: Verify the version gate and diff**

Run:

```bash
cd /home/ubuntu/code/personal/tiny-password/.worktrees/android-shared-items
grep -E 'version(Code|Name)' mobile/android/app/build.gradle
git diff --check
git diff --stat
```

Expected: `versionCode 5`, `versionName "1.2.2"`, no whitespace errors, and only the planned mobile client, screen copy, integration test, and Android version files differ from the design/plan commits.

### Task 5: Full verification, review, commit, and push

**Files:**
- No additional files; verify all changes above.

- [ ] **Step 1: Run the complete mobile verification suite**

Run:

```bash
cd mobile && npx tsc --noEmit
cd mobile && npx jest --runInBand
```

Expected: TypeScript exits 0; Jest reports 0 failed tests with the existing skipped integration suite remaining skipped unless `MOBILE_INTEGRATION=1` is set.

- [ ] **Step 2: Run the Android release build if the local SDK/toolchain is available**

Run:

```bash
cd mobile/android && ./gradlew assembleRelease --no-daemon
```

Expected: Gradle exits 0 and creates `app/build/outputs/apk/release/app-release.apk`; if the host lacks the Android SDK, record the exact environmental failure and still verify the CI workflow's version input locally.

- [ ] **Step 3: Inspect the final diff and request review**

Run:

```bash
git diff --check
git status --short
git diff HEAD -- mobile/src/api/client.ts mobile/src/screens/VaultScreen.tsx mobile/README.md mobile/__tests__/VaultScreen.test.tsx mobile/__tests__/client.test.ts mobile/__tests__/integration/api.integration.test.ts mobile/__tests__/androidBranding.test.ts mobile/android/app/build.gradle
```

Confirm no server or permission code changed, shared list/search coverage is present, and version values are 5/1.2.2.

- [ ] **Step 4: Commit the implementation**

```bash
git add mobile/src/api/client.ts mobile/src/screens/VaultScreen.tsx mobile/README.md mobile/__tests__/VaultScreen.test.tsx mobile/__tests__/client.test.ts mobile/__tests__/integration/api.integration.test.ts mobile/__tests__/androidBranding.test.ts mobile/android/app/build.gradle
git commit -m "fix: show shared items in Android vault"
```

- [ ] **Step 5: Push the fix to the remote**

```bash
git push -u origin fix/android-shared-items
```

Report the pushed commit and branch; the APK workflow will run only after the implementation is merged to `master`, where it will see `app-v1.2.2-5` as a new version.
