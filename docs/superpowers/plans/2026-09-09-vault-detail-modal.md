# Vault Detail Modal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将保险库桌面右侧详情和移动端详情页统一为一个响应式、可访问、由路由驱动的详情弹窗，同时保留现有条目业务能力。

**Architecture:** `VaultPage` 保持列表挂载并用单一 modal state 管理 `closed/loading/detail/create/edit/history/error`，`ItemDialog` 负责弹窗外壳和模式内容，现有 `ItemDetail`、`ItemEditor`、`HistoryPage` 负责各自正文。`Dialog` 使用原生 `<dialog>` 管理 top layer、焦点恢复、Esc、遮罩关闭和滚动锁；路由增加 replace、来源标记和 dirty 导航拦截所需的最小能力。

**Tech Stack:** React 19, TypeScript, Tailwind CSS 4, Vitest, Testing Library, Playwright, 原生 History API 和 `<dialog>`。

---

### Task 1: Establish modal and router behavior tests

**Files:**
- Modify: `web/src/design-system/design-system.test.tsx`
- Modify: `web/src/features/vault/vault.test.tsx`
- Create: `web/src/app/router.test.tsx`
- Modify: `tests/e2e/vault.spec.ts`

- [ ] **Step 1: Add failing Dialog behavior tests**

覆盖 `role="dialog"` 的 accessible name、打开时焦点进入、关闭时恢复触发按钮、Esc 只关闭最上层、遮罩点击关闭、面板点击不关闭、Tab/Shift+Tab 循环，以及打开时 body 不滚动、关闭后恢复滚动。

```tsx
it("uses native dialog semantics and closes only from the backdrop", async () => {
  const user = userEvent.setup();
  const onClose = vi.fn();
  render(<Dialog open title="条目详情" onClose={onClose}><button>正文按钮</button></Dialog>);
  expect(screen.getByRole("dialog", { name: "条目详情" })).toHaveAttribute("aria-modal", "true");
  await user.click(screen.getByRole("dialog", { name: "条目详情" }).querySelector("button")!);
  expect(onClose).not.toHaveBeenCalled();
  await user.click(screen.getByTestId("dialog-backdrop"));
  expect(onClose).toHaveBeenCalledTimes(1);
});
```

- [ ] **Step 2: Add failing router tests**

断言 `navigate` 默认 push、`replace` 不增加 history length、导航 state 只保留本次页面生命周期的来源标记；断言 `usePath` 在 popstate 后更新。测试路径只保存 `source`/`kind` 等非敏感元数据，不保存条目标题、搜索词或 payload。

- [ ] **Step 3: Add failing VaultPage modal tests**

断言点击条目立即出现 loading dialog，成功后只有一个 dialog；桌面和 390px viewport 不再渲染 `mobile-detail` 或桌面详情副本；关闭后回到 `/vault`、列表仍保留搜索筛选和触发按钮焦点；直达 `/vault/:itemId` 使用 replace 关闭。

- [ ] **Step 4: Add failing dirty-navigation tests**

覆盖编辑内容变更后点击关闭、Esc、遮罩、取消编辑、站内导航和浏览器后退都会先出现“放弃编辑”确认；取消保持 `/vault/:itemId` 和编辑内容，确认才完成原导航意图且不通过重复 push 增长历史栈。

- [ ] **Step 5: Run focused tests and verify the new assertions fail**

Run: `npm --prefix web test -- --run src/design-system/design-system.test.tsx src/app/router.test.tsx src/features/vault/vault.test.tsx`

Expected: FAIL because the reusable `Dialog`, replace-aware router and unified modal behavior do not exist yet.

---

### Task 2: Replace the hand-rolled confirmation overlay with a reusable Dialog

**Files:**
- Modify: `web/src/design-system/Dialog.tsx`
- Modify: `web/src/design-system/design-system.test.tsx`
- Modify: `web/src/features/vault/HistoryPage.tsx`
- Modify: `web/src/features/vault/TrashPage.tsx`
- Modify: `web/src/app/AppShell.tsx`
- Modify: `web/src/features/admin/UsersPage.tsx`
- Modify: `web/src/features/auth/AccountPage.tsx`

- [ ] **Step 1: Implement the Dialog API**

Export `DialogProps` and `Dialog` with `open`, `title`, optional `description`, `onClose`, `children`, `initialFocusRef`, `dismissible`, `danger`, and `footer`. Render one native `<dialog>` with `useId()` generated title/description IDs. On open call `showModal()` once; on close call the controlled `onClose`; listen to `cancel` for Esc; close from the backdrop only when `event.target === event.currentTarget`; capture the active element before opening and restore it after unmount. Add a module-level open count so body overflow is restored only when the last layer closes.

```tsx
export type DialogProps = {
  open: boolean;
  title: string;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  onClose: () => void;
  initialFocusRef?: RefObject<HTMLElement | null>;
  dismissible?: boolean;
  danger?: boolean;
};
```

The dialog panel uses `max-w-3xl`, `max-h-[calc(100dvh-48px)]` at `md`, `max-h-[calc(100dvh-16px)]` below `md`, `border-2 border-ink`, `bg-paper`, and a flex column. Only the content region scrolls. The backdrop uses `bg-ink/40`; there is no radius, blur, gradient, or soft shadow. Keep the close control at least 44×44px.

- [ ] **Step 2: Make ConfirmDialog a Dialog wrapper**

Keep the existing `ConfirmDialogProps` and call sites. Render the existing title, description, cancel and confirm buttons through `Dialog`; ensure confirm/cancel callbacks are not invoked twice when native close events fire. Give the confirm layer a higher z-index and keep the underlying layer inert through native top-layer behavior.

- [ ] **Step 3: Run design-system and affected component tests**

Run: `npm --prefix web test -- --run src/design-system/design-system.test.tsx src/features/vault/vault.test.tsx`

Expected: PASS for existing confirmation behaviors plus the new native-dialog tests.

---

### Task 3: Extend routing with replace, navigation metadata, and a dirty leave gate

**Files:**
- Modify: `web/src/app/router.tsx`
- Create: `web/src/app/router.test.tsx`
- Modify: `web/src/features/vault/VaultPage.tsx`

- [ ] **Step 1: Add replace and route metadata primitives**

Implement `replace(path, state?)` beside `navigate`, and make `navigateWithState` accept an internal metadata object stored in `history.state` only when it contains route bookkeeping (`source`, `kind`, `modal`). Keep generator handoff in the existing module-level transient state so passwords never enter browser history. Dispatch one `PopStateEvent` after push or replace.

```ts
export type NavigationMeta = { source?: "list" | "direct" | "generator"; modal?: "item" };
export function replace(path: string, meta?: NavigationMeta): void {
  window.history.replaceState(meta ?? {}, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
}
```

- [ ] **Step 2: Add a single navigation guard subscription**

Expose `setNavigationGuard((intent) => boolean | void)` or an equivalent minimal callback API that can pause a popstate intent. The guard must not push a compensating URL. `VaultPage` uses it only while an editor is dirty: cancel restores the current route, confirm executes the original back/forward intent once. Clear the guard on unmount and session expiry.

- [ ] **Step 3: Add tests for history length and repeated back**

Use `window.history.pushState` with a known base entry and assert closing a list-origin modal uses `history.back()`, direct modal access uses `replace("/vault")`, and canceling a dirty back leaves the history length unchanged. Assert a second back after confirmation does not reopen a stale modal.

- [ ] **Step 4: Run router tests**

Run: `npm --prefix web test -- --run src/app/router.test.tsx`

Expected: PASS with no URL storing sensitive entry content.

---

### Task 4: Make detail, editor, history, and sensitive fields fit one modal body

**Files:**
- Create: `web/src/features/vault/ItemDialog.tsx`
- Modify: `web/src/features/vault/ItemDetail.tsx`
- Modify: `web/src/features/vault/ItemEditor.tsx`
- Modify: `web/src/features/vault/HistoryPage.tsx`
- Modify: `web/src/features/vault/SensitiveField.tsx`
- Modify: `web/src/features/vault/vault.test.tsx`

- [ ] **Step 1: Add ItemDialog mode composition tests**

Test that `detail`, `create`, `edit`, `history`, `loading`, and `error` each render one title, one scrollable body, and one bottom action area; test the close button has a stable accessible label and the mode-specific back action does not create a URL entry.

- [ ] **Step 2: Implement ItemDialog**

Compose the shared `Dialog` header from type label, item title, scope/creator, revision and updated time. Use a single `aria-labelledby` title. Render `ItemDetail`, `ItemEditor`, `HistoryPage`, `Loading`, or `ErrorSummary` in the content slot. Keep footer actions in the dialog footer; detail controls are supplied through callbacks. Error mode clears old detail data and offers retry plus close.

- [ ] **Step 3: Refactor ItemDetail to content-only rendering**

Remove its outer title/header and footer duplication. Keep all five payload branches, safe website URL handling, shared permission notice, tags, and sensitive field rendering. Expose the existing action callbacks to `ItemDialog` so the modal has one title and one operation area. Preserve read-only shared behavior.

- [ ] **Step 4: Make ItemEditor report dirty and busy state**

Add optional `onDirtyChange` and `onBusyChange` callbacks. Capture the initial serialized editor snapshot from type, scope, payload, tags and favorite; report dirty when it changes, reset it after a successful save, and keep current edits after revision conflicts. Change the cancel button to invoke the parent callback only; the parent decides whether confirmation is required.

- [ ] **Step 5: Adapt HistoryPage to modal content**

Keep pagination, restore request, and restore confirmation. Replace its nested title/header and “返回” layout with a body heading and `onClose` callback that means “return to detail”. The restore confirmation must remain a top-level Dialog layer and must block the parent modal’s Esc/Tab handling until closed.

- [ ] **Step 6: Move clipboard cleanup to session-level management**

Create a small module in `SensitiveField.tsx` or `web/src/features/vault/clipboardCleanup.ts` that stores only the latest copied value and timer in memory. Unmounting `SensitiveField` cancels reveal timers but does not cancel the 30-second clipboard cleanup. A new copy replaces the previous cleanup. At cleanup time compare `navigator.clipboard.readText()` to the copied value before clearing; release the retained value after completion/failure. Clear the manager on `SESSION_EXPIRED_EVENT`.

- [ ] **Step 7: Run component tests**

Run: `npm --prefix web test -- --run src/features/vault/vault.test.tsx src/design-system/design-system.test.tsx`

Expected: PASS for all item types, shared read-only access, revision conflicts, reveal/copy masking, nested restore confirmation, and the new modal content tests.

---

### Task 5: Rewrite VaultPage around one modal state and request lifecycle

**Files:**
- Modify: `web/src/features/vault/VaultPage.tsx`
- Modify: `web/src/features/vault/vault.test.tsx`
- Modify: `web/src/features/generator/GeneratorPage.tsx`

- [ ] **Step 1: Replace independent booleans with modal state**

Use a discriminated state such as:

```ts
type ModalState =
  | { mode: "closed" }
  | { mode: "loading"; itemId: string }
  | { mode: "detail"; item: ItemDetailData }
  | { mode: "create"; draft?: { password: string } }
  | { mode: "edit"; item: ItemDetailData }
  | { mode: "history"; itemId: string }
  | { mode: "error"; itemId?: string; message: string; requestId?: string };
```

Keep only one `ItemDialog` mounted. On close clear item, draft, mode and pending confirmation after aborting the active detail controller. Reopen always starts masked because detail content and `SensitiveField` instances unmount.

- [ ] **Step 2: Add detail request cancellation and stale-response protection**

Create one `AbortController` per detail fetch and increment a request sequence. Ignore `AbortError`, stale sequence values and mismatched item IDs in success/error/finally handlers. A close, route change, session expiry, or item switch aborts the prior controller.

- [ ] **Step 3: Drive opening and closing from `/vault/:itemId`**

Clicking a list row pushes `/vault/:itemId` with `{ source: "list", modal: "item" }` and immediately sets loading. The route effect opens direct URLs with loading and loads the item. Closing a list-origin modal calls `history.back()`; closing a direct URL calls `replace("/vault")`. Route removal always closes the modal rather than reopening from stale selected state. New/create remains on `/vault`; a successful create navigates to the new detail route.

- [ ] **Step 4: Preserve list state and refresh behavior**

Keep query, type filter, favorite filter, cursor, loaded items and scroll position in `VaultPage`. After save, favorite, restore or delete, refresh the list independently. If refresh fails after a successful write, keep the newest detail and show a separate retryable list error without resubmitting the write.

- [ ] **Step 5: Connect all modal actions**

Wire edit, history, favorite, trash, retry, close, create draft and generated password handoff. Confirm trash must disable duplicate submits, close the detail only after a successful delete, replace the current item URL with `/vault`, and leave the error context visible on failure. Use `canManage` from the existing owner/creator policy.

- [ ] **Step 6: Remove duplicate desktop/mobile detail rendering**

The list uses the full `max-w-screen-xl` content width and responsive newspaper rows. Delete the old `lg` detail pane and `mobile-detail` branch. Keep the list row as one focusable button with no nested controls; add `min-width: 0` to the search input and allow the toolbar to wrap.

- [ ] **Step 7: Run the VaultPage tests**

Run: `npm --prefix web test -- --run src/features/vault/vault.test.tsx`

Expected: PASS with exactly one dialog for each open state, correct focus restoration, no duplicate detail markup, correct list refresh/error behavior, and generator draft creation.

---

### Task 6: Migrate end-to-end expectations and visual-responsive behavior

**Files:**
- Modify: `tests/e2e/vault.spec.ts`
- Modify: `web/src/app/app-shell.test.tsx` only if shared Dialog changes affect shell behavior
- Modify: `web/src/design-system/tokens.css` only if an existing token is required for the modal dimensions

- [ ] **Step 1: Update desktop E2E assertions**

Open a seeded entry and assert a visible dialog named by the entry title, masked password, close button, action footer, and no side detail pane. Verify delete confirmation is nested above the item dialog and after success the dialog closes and the list updates.

- [ ] **Step 2: Update mobile E2E assertions**

At 390×844 open an entry and assert the same dialog, no `mobile-detail` test id, no horizontal overflow (`document.documentElement.scrollWidth <= window.innerWidth`), usable close button, and preserved list navigation after closing.

- [ ] **Step 3: Add long-content responsive coverage**

Use a long title, multiline secure note and long SSH private key fixture; assert the body scrolls while the close button and footer remain visible. Check `prefers-reduced-motion` disables opacity transitions through the existing CSS media rule.

- [ ] **Step 4: Run the browser suite through the repository script**

Run: `scripts/test-browser-e2e.sh`

Expected: PASS for desktop, mobile, auth, generator and existing vault flows using the repository’s temporary server setup.

---

### Task 7: Full verification and handoff

**Files:**
- Modify: `docs/design/2026-09-09-vault-detail-modal.md` only to record completed validation commands and any concrete limitation found during implementation.

- [ ] **Step 1: Run TypeScript validation**

Run: `npm --prefix web run typecheck`

Expected: exit code 0 with no TypeScript diagnostics.

- [ ] **Step 2: Run all frontend component tests**

Run: `npm --prefix web test -- --run`

Expected: all Vitest files pass.

- [ ] **Step 3: Run backend tests because the feature exercises existing item contracts**

Run: `go test ./...`

Expected: all Go packages pass; no backend source or schema changes are expected.

- [ ] **Step 4: Inspect the final diff and working tree**

Run: `git diff --check`, `git status --short`, and `git diff --stat`.

Expected: no whitespace errors; only the modal implementation, focused tests, plan/design validation notes and required generated assets are present.

- [ ] **Step 5: Report evidence and material limitations**

Include changed files, the commands that passed, viewport checks at 390/768/1440, and any browser limitation around native dialog support or best-effort clipboard cleanup.
