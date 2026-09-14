# Android Branding and Safe Area Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Update the Android app label to `77Password`, replace its launcher artwork with the approved geometric keyhole icon, and keep every back-header below the Android status bar.

**Architecture:** Keep the existing `com.tinypassword` package, Manifest icon references, raster mipmap layout, and Web/PWA branding unchanged. Move top safe-area ownership into the shared `NewsprintHeader`, so Generator and Entry Editor screens cannot double-apply or omit the status-bar inset; preserve each screen’s existing bottom inset handling.

**Tech Stack:** React Native 0.87, TypeScript, `react-native-safe-area-context`, Jest/react-test-renderer, Android Gradle Plugin, PNG launcher resources rendered from the existing SVG brand asset.

---

### Task 1: Add failing regression tests

**Files:**
- Create: `mobile/__tests__/androidBranding.test.ts`
- Create: `mobile/__tests__/NewsprintHeader.test.tsx`

- [ ] **Step 1: Write the failing Android metadata/resource test**

Create `mobile/__tests__/androidBranding.test.ts` with a label assertion and exact PNG signature/dimension checks:

```ts
import * as fs from 'node:fs';
import * as path from 'node:path';

const androidMain = path.resolve(__dirname, '..', 'android', 'app', 'src', 'main');

test('uses 77Password as the Android app label', () => {
  const strings = fs.readFileSync(path.join(androidMain, 'res', 'values', 'strings.xml'), 'utf8');

  expect(strings).toContain('<string name="app_name">77Password</string>');
});

test('increments the Android release version so CI packages this branding update', () => {
  const buildGradle = fs.readFileSync(path.join(androidMain, '..', '..', '..', 'app', 'build.gradle'), 'utf8');

  expect(buildGradle).toMatch(/versionCode\s+4/);
  expect(buildGradle).toMatch(/versionName\s+"1\.2\.1"/);
});

test.each([
  ['mdpi', 48],
  ['hdpi', 72],
  ['xhdpi', 96],
  ['xxhdpi', 144],
  ['xxxhdpi', 192],
] as const)('has valid %s launcher PNG resources at %dx%d', (density, size) => {
  for (const name of ['ic_launcher.png', 'ic_launcher_round.png']) {
    const file = path.join(androidMain, 'res', `mipmap-${density}`, name);
    const data = fs.readFileSync(file);

    expect(data.length).toBeGreaterThan(24);
    expect(data.subarray(0, 8)).toEqual(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]));
    expect(data.readUInt32BE(16)).toBe(size);
    expect(data.readUInt32BE(20)).toBe(size);
  }
});
```

- [ ] **Step 2: Write the failing safe-area test**

Create `mobile/__tests__/NewsprintHeader.test.tsx`:

```tsx
import React from 'react';
import {StyleSheet} from 'react-native';
import renderer, {act, type ReactTestRenderer} from 'react-test-renderer';
import {SafeAreaProvider} from 'react-native-safe-area-context';
import {NewsprintHeader} from '../src/components/NewsprintHeader';

test('places the header after the status-bar inset plus the standard top spacing', () => {
  let tree!: ReactTestRenderer;
  act(() => {
    tree = renderer.create(
      <SafeAreaProvider
        initialMetrics={{
          frame: {x: 0, y: 0, width: 360, height: 800},
          insets: {top: 28, right: 0, bottom: 16, left: 0},
        }}>
        <NewsprintHeader
          title="ENTRY"
          backLabel="Back"
          onBack={() => undefined}
          testID="header"
        />
      </SafeAreaProvider>,
    );
  });

  const header = tree.root.findByProps({testID: 'header'});
  expect(StyleSheet.flatten(header.props.style).paddingTop).toBe(44);
});
```

- [ ] **Step 3: Run the focused tests and verify they fail for the intended reasons**

Run:

```bash
npm --prefix mobile test -- --runInBand __tests__/androidBranding.test.ts __tests__/NewsprintHeader.test.tsx
```

Expected: the app-label assertion fails because the current value is `TinyPassword`, the release-version assertion fails because the current values are `3`/`1.2.0`, and the Header assertion fails because the current fixed top padding is `16dp` rather than `28 + 16dp`. The PNG shape/dimension test may pass because those resources already exist; that is acceptable because it guards the resource contract, while the generated artwork is verified by the build and visual inspection in Task 3.

- [ ] **Step 4: Commit the regression tests**

```bash
git add mobile/__tests__/androidBranding.test.ts mobile/__tests__/NewsprintHeader.test.tsx
git commit -m "test: cover Android branding and header safe area"
```

### Task 2: Update the Android label and centralize Header safe-area handling

**Files:**
- Modify: `mobile/android/app/src/main/res/values/strings.xml:2`
- Modify: `mobile/android/app/build.gradle:86-87`
- Modify: `mobile/src/components/NewsprintHeader.tsx:1-72`
- Modify: `mobile/src/screens/GeneratorScreen.tsx:27,41`
- Modify: `mobile/src/screens/EntryEditorScreen.tsx:396-408,1126`

- [ ] **Step 1: Change the Android resource label**

Change only the existing resource value:

```xml
<string name="app_name">77Password</string>
```

Keep the Manifest references and `applicationId "com.tinypassword"` unchanged.

- [ ] **Step 2: Bump the Android release metadata so CI packages the change**

Change the Android `defaultConfig` values from `versionCode 3`/`versionName "1.2.0"` to:

```gradle
versionCode 4
versionName "1.2.1"
```

This is required because `.github/workflows/build-apk.yml` skips a version whose `app-v<versionName>-<versionCode>` tag already exists; `app-v1.2.0-3` is already present on `origin`.

- [ ] **Step 3: Make `NewsprintHeader` consume the top inset exactly once**

In `mobile/src/components/NewsprintHeader.tsx`, import `useSafeAreaInsets`:

```tsx
import {useSafeAreaInsets} from 'react-native-safe-area-context';
```

At the start of `NewsprintHeader`, read the inset:

```tsx
const insets = useSafeAreaInsets();
```

Change the returned container from `style={styles.container}` to:

```tsx
<View
  style={[styles.container, {paddingTop: insets.top + spacing.md}]}
  testID={testID}>
```

Remove the static `paddingTop` from `styles.container` so the dynamic safe-area calculation is the only source of top padding. Keep all horizontal, row, typography, touch-target, and rule styles unchanged.

- [ ] **Step 4: Remove duplicate screen-level top padding for shared-header screens**

In `GeneratorScreen.tsx`, retain `const insets = useSafeAreaInsets()` for the bottom inset, but change the root to:

```tsx
<View style={[styles.root, {paddingBottom: insets.bottom}]}>
```

In `EntryEditorScreen.tsx`, change both loading/error roots from:

```tsx
<View style={[styles.root, {paddingTop: insets.top}]}>
```

to:

```tsx
<View style={styles.root}>
```

Leave the normal editor root as its existing bottom-inset-only style:

```tsx
<View style={[styles.root, {paddingBottom: insets.bottom}]}>
```

This makes loading, error, detail, edit, and generator Header positions use the same inset path, while preserving bottom safe areas.

- [ ] **Step 5: Run the focused tests and verify they pass**

Run:

```bash
npm --prefix mobile test -- --runInBand __tests__/androidBranding.test.ts __tests__/NewsprintHeader.test.tsx
```

Expected: all tests pass, including `paddingTop === 44` for a simulated 28dp status-bar inset.

- [ ] **Step 6: Commit the label and safe-area implementation**

```bash
git add mobile/android/app/src/main/res/values/strings.xml mobile/android/app/build.gradle mobile/src/components/NewsprintHeader.tsx mobile/src/screens/GeneratorScreen.tsx mobile/src/screens/EntryEditorScreen.tsx
git commit -m "feat: brand Android app and respect header safe area"
```

### Task 3: Generate the approved Android launcher artwork

**Files:**
- Modify: `mobile/android/app/src/main/res/mipmap-mdpi/ic_launcher.png`
- Modify: `mobile/android/app/src/main/res/mipmap-mdpi/ic_launcher_round.png`
- Modify: `mobile/android/app/src/main/res/mipmap-hdpi/ic_launcher.png`
- Modify: `mobile/android/app/src/main/res/mipmap-hdpi/ic_launcher_round.png`
- Modify: `mobile/android/app/src/main/res/mipmap-xhdpi/ic_launcher.png`
- Modify: `mobile/android/app/src/main/res/mipmap-xhdpi/ic_launcher_round.png`
- Modify: `mobile/android/app/src/main/res/mipmap-xxhdpi/ic_launcher.png`
- Modify: `mobile/android/app/src/main/res/mipmap-xxhdpi/ic_launcher_round.png`
- Modify: `mobile/android/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png`
- Modify: `mobile/android/app/src/main/res/mipmap-xxxhdpi/ic_launcher_round.png`

- [ ] **Step 1: Render the existing approved SVG at every Android density**

Use the existing `web/public/icons/icon.svg` as the source and render exact square PNGs with the installed FFmpeg SVG renderer:

```bash
for spec in "mdpi 48" "hdpi 72" "xhdpi 96" "xxhdpi 144" "xxxhdpi 192"; do
  set -- $spec
  density="$1"
  size="$2"
  output="mobile/android/app/src/main/res/mipmap-${density}/ic_launcher.png"
  ffmpeg -v error -i web/public/icons/icon.svg \
    -vf "scale=${size}:${size}:flags=lanczos,format=rgba" \
    -frames:v 1 -y "$output"
  cp "$output" "mobile/android/app/src/main/res/mipmap-${density}/ic_launcher_round.png"
done
```

Do not edit `web/public/icons/icon.svg` or the Web/PWA PNGs. The two Android resource names intentionally use the same artwork; Android launchers apply their own round mask to `ic_launcher_round` where supported, and the raster fallback remains available for minSdk 24.

- [ ] **Step 2: Inspect the generated resources**

Run:

```bash
npm --prefix mobile test -- --runInBand __tests__/androidBranding.test.ts
```

Expected: the Android metadata/resource test passes and reports all 10 files with the expected dimensions. Visually inspect the `xxxhdpi` output with the image viewer and confirm it is the selected black/red/cream geometric keyhole artwork rather than the old teal React Native robot.

- [ ] **Step 3: Commit the launcher resources**

```bash
git add mobile/android/app/src/main/res/mipmap-mdpi mobile/android/app/src/main/res/mipmap-hdpi mobile/android/app/src/main/res/mipmap-xhdpi mobile/android/app/src/main/res/mipmap-xxhdpi mobile/android/app/src/main/res/mipmap-xxxhdpi
git commit -m "feat: add 77Password Android launcher icon"
```

### Task 4: Run local verification and publish to the main remote branch

**Files:**
- No source changes expected; inspect `.github/workflows/build-apk.yml` and the pushed GitHub Actions run for APK packaging.

- [ ] **Step 1: Run the complete mobile test suite**

Run:

```bash
npm --prefix mobile test -- --runInBand
```

Expected: all mobile Jest tests pass with no test failures.

- [ ] **Step 2: Run mobile lint and TypeScript validation**

Run:

```bash
npm --prefix mobile run lint
(cd mobile && npx tsc --noEmit)
```

Expected: both commands exit 0 with no lint or type errors.

- [ ] **Step 3: Verify the GitHub Actions APK workflow configuration**

Run:

```bash
rg -n "assemble(Release|Debug)|app-release\\.apk|Build Android APK|artifact" .github/workflows/build-apk.yml
```

Expected: the workflow contains its Android build command and release APK artifact upload. APK compilation is intentionally delegated to GitHub Actions rather than run locally.

- [ ] **Step 4: Verify source resources and package configuration locally**

Run:

```bash
npm --prefix mobile test -- --runInBand __tests__/androidBranding.test.ts
git diff --check
```

Expected: all source label/icon checks pass and there is no whitespace error. The pushed GitHub Actions run is the authoritative APK packaging check.

- [ ] **Step 5: Review the final diff, merge to main, and push the main branch**

Run:

```bash
git diff --check master..HEAD
git status --short
git log -3 --oneline
cd /home/ubuntu/code/personal/tiny-password
git merge --ff-only feature/android-branding-safe-area
git push origin master
```

Expected: only the approved design/plan docs, Android label/icon resources, safe-area implementation, and regression tests are present; the push succeeds to `origin/master`. Do not stage `.superpowers/`, Android build outputs, or unrelated worktree files.
