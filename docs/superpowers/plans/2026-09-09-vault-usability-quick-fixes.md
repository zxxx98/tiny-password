# Vault usability quick fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (\`- [ ]\`) syntax for tracking.

**Goal:** Deliver the four approved low-coupling vault usability improvements while deferring personal/shared vault movement.

**Architecture:** Keep destructive styling in the shared Button and ConfirmDialog primitives, keep identity clipboard text generation in a pure vault utility, and add a local plain display mode to SensitiveField used only by card numbers. No server, schema, ownership, encryption, or API behavior changes are needed in this phase.

**Tech Stack:** React 19, TypeScript, Tailwind CSS, Vitest, Vite, Playwright, existing clipboard cleanup helper.

---

### Task 1: Add the danger button variant and update vault deletion copy

**Files:**
- Modify: web/src/design-system/Button.tsx
- Modify: web/src/design-system/Dialog.tsx
- Modify: web/src/features/vault/ItemDialog.tsx
- Modify: web/src/features/vault/ItemDetail.tsx
- Modify: web/src/features/vault/TrashPage.tsx
- Modify: web/src/features/vault/vault.test.tsx

- [ ] **Step 1: Add the shared danger variant.**

Extend ButtonVariant with "danger" and add this entry to variantClasses:

~~~ts
danger: "bg-paper text-accent hover:bg-paper hover:text-accent",
~~~

This keeps the background light, keeps the text red in the default and hover states, and inherits the existing focus-visible outline and disabled opacity classes. Do not add a text-accent override at individual call sites.

- [ ] **Step 2: Make dangerous confirmation actions use the variant.**

In ConfirmDialog, change only the confirm button to:

~~~tsx
<Button variant={danger ? "danger" : "primary"} disabled={confirmDisabled} onClick={onConfirm}>
  {confirmLabel}
</Button>
~~~

The cancel button remains secondary; danger continues to control the dialog accent border and is not added to unrelated dialogs.

- [ ] **Step 3: Route vault delete entry points through danger.**

Change these call sites:

~~~tsx
// ItemDialog.tsx
<Button variant="danger" className="ml-auto" onClick={onTrash}>移入回收站</Button>

// ItemDetail.tsx
<Button variant="danger" onClick={onTrash}>移入回收站</Button>

// TrashPage.tsx
<Button variant="danger" disabled={busy !== null} onClick={() => setPurgeTarget(item)}>
  彻底删除
</Button>
~~~

Update the trash confirmation in TrashPage.tsx to use title="彻底删除？" and confirmLabel={busy !== null ? "删除中…" : "彻底删除"}. Keep the description's “永久清除” wording. The vault confirmation in VaultPage.tsx already has danger and keeps its “移入回收站” label, so the new ConfirmDialog behavior covers its execution button.

- [ ] **Step 4: Update only stale existing UI assertions.**

In web/src/features/vault/vault.test.tsx, replace the two trash purge role-name assertions from 永久删除 to 彻底删除. Do not add new UI test cases or alter admin member deletion copy.

- [ ] **Step 5: Run the focused existing checks.**

Run from web/:

~~~bash
npm run typecheck
npm test -- --run src/design-system/design-system.test.tsx src/features/vault/vault.test.tsx
~~~

Expected: typecheck exits 0; all selected design-system and vault tests pass.

- [ ] **Step 6: Commit the completed task.**

~~~bash
git add web/src/design-system/Button.tsx web/src/design-system/Dialog.tsx \
  web/src/features/vault/ItemDialog.tsx web/src/features/vault/ItemDetail.tsx \
  web/src/features/vault/TrashPage.tsx web/src/features/vault/vault.test.tsx
git commit -m "fix: style vault destructive actions in red"
~~~

### Task 2: Add identity address formatting and copy feedback

**Files:**
- Create: web/src/features/vault/identityClipboard.ts
- Modify: web/src/features/vault/ItemDetail.tsx
- Test: web/src/features/vault/identityClipboard.test.ts

- [ ] **Step 1: Write the pure formatter tests first.**

Create identityClipboard.test.ts with cases covering the exact three-line output, address order and country exclusion, empty values, trimming, newline normalization, and preservation of normal internal spaces:

~~~ts
import { describe, expect, it } from "vitest";
import { formatIdentityClipboard } from "./identityClipboard";

describe("formatIdentityClipboard", () => {
  it("formats the required three lines and excludes country", () => {
    expect(formatIdentityClipboard({
      name: "条目名称",
      full_name: " 张三 ",
      country: "中国",
      state: "广东省",
      city: "深圳市",
      district: "南山区",
      address_line: " 科技园科苑路 1 号 ",
      postal_code: "518000",
      phone: " +86 13800138000 ",
    })).toBe("姓名：张三\n地址：广东省 深圳市 南山区 科技园科苑路 1 号 518000\n联系电话：+86 13800138000");
  });

  it("trims values and turns line breaks into spaces without collapsing normal spaces", () => {
    expect(formatIdentityClipboard({
      name: "x",
      full_name: "  Alice\nSmith  ",
      address_line: "12  Main\r\nStreet",
      phone: " 00123 ext. 4\n ",
    })).toBe("姓名：Alice Smith\n地址：12  Main Street\n联系电话：00123 ext. 4");
  });

  it("keeps all labels when identity values are missing", () => {
    expect(formatIdentityClipboard({ name: "x" })).toBe("姓名：\n地址：\n联系电话：");
  });
});
~~~

Run npm test -- --run src/features/vault/identityClipboard.test.ts. Expected: the test fails because identityClipboard.ts does not exist yet.

- [ ] **Step 2: Implement the minimal formatter.**

Create the utility with this API and behavior:

~~~ts
import type { IdentityPayload } from "./types";

function clean(value: string | null | undefined): string {
  return (value ?? "").replace(/[\r\n]+/g, " ").trim();
}

export function formatIdentityClipboard(payload: IdentityPayload): string {
  const address = [payload.state, payload.city, payload.district, payload.address_line, payload.postal_code]
    .map(clean)
    .filter(Boolean)
    .join(" ");
  return [
    "姓名：" + clean(payload.full_name),
    "地址：" + address,
    "联系电话：" + clean(payload.phone),
  ].join("\n");
}
~~~

- [ ] **Step 3: Run the formatter tests and verify green.**

Run:

~~~bash
npm test -- --run src/features/vault/identityClipboard.test.ts
~~~

Expected: 3 tests pass.

- [ ] **Step 4: Add the detail copy interaction.**

In ItemDetail.tsx, import useState, scheduleClipboardCleanup, and formatIdentityClipboard. Add local state:

~~~ts
const [identityCopyState, setIdentityCopyState] = useState<string | null>(null);
~~~

For the identity branch, keep the existing field rows and append this control after them:

~~~tsx
<div className="flex flex-wrap items-center gap-2 border-t border-divider pt-4">
  <Button
    variant="secondary"
    onClick={async () => {
      try {
        const copied = formatIdentityClipboard(p);
        await navigator.clipboard.writeText(copied);
        setIdentityCopyState("已复制身份地址");
        void scheduleClipboardCleanup(copied);
      } catch {
        setIdentityCopyState("复制失败，请重试或手动复制");
      }
    }}
  >
    复制身份地址
  </Button>
  {identityCopyState && <p role="status">{identityCopyState}</p>}
</div>
~~~

The handler has no request to the server and creates no sensitive-field audit event. It is rendered regardless of canManage, so every readable identity detail can use it. Keep the feedback in the detail component only; never log or persist the formatted value.

- [ ] **Step 5: Run typecheck and the existing vault tests.**

Run:

~~~bash
npm run typecheck
npm test -- --run src/features/vault/identityClipboard.test.ts src/features/vault/vault.test.tsx
~~~

Expected: typecheck exits 0 and all selected tests pass.

- [ ] **Step 6: Commit the completed task.**

~~~bash
git add web/src/features/vault/identityClipboard.ts web/src/features/vault/identityClipboard.test.ts web/src/features/vault/ItemDetail.tsx
git commit -m "feat: add identity address copy"
~~~

### Task 3: Show card numbers plainly while preserving other sensitive fields

**Files:**
- Modify: web/src/features/vault/SensitiveField.tsx
- Modify: web/src/features/vault/ItemDetail.tsx
- Modify: web/src/features/vault/forms/CreditCardFields.tsx

- [ ] **Step 1: Add a local display mode to SensitiveField.**

Add an optional prop with a masked default:

~~~ts
displayMode?: "masked" | "plain";
~~~

Inside SensitiveField, derive const plain = displayMode === "plain". Render the value output whenever plain || revealed, render the 显示/遮蔽 button only when !plain, and keep the existing copy button and audit("copy") path unchanged. The visible output should remain the existing output with break-all, so long card numbers wrap without overflow. Plain mode must not call audit("reveal").

- [ ] **Step 2: Use plain mode only for the card number.**

Change the credit-card detail field to:

~~~tsx
<SensitiveField
  label="卡号"
  field="number"
  value={p.number}
  itemId={detail.id}
  csrfToken={csrfToken}
  displayMode="plain"
/>
~~~

Leave CVV and PIN calls unchanged, along with password, private key, and passphrase calls in their existing masked mode.

- [ ] **Step 3: Change the card number editor input without numeric conversion.**

In CreditCardFields.tsx, replace the card number field with:

~~~tsx
<Field
  id="f-number"
  label="卡号"
  type="text"
  inputMode="numeric"
  autoComplete="off"
  required
  value={payload.number}
  error={errors.number}
  disabled={disabled}
  onChange={(e) => onChange({ number: e.target.value })}
/>
~~~

Do not change the payload type, validation, copy audit field name, or storage representation. The existing Field input props accept inputMode through its native input attributes.

- [ ] **Step 4: Run typecheck and existing frontend tests.**

Run:

~~~bash
npm run typecheck
npm test -- --run
~~~

Expected: typecheck exits 0 and the existing suite passes without adding UI test cases.

- [ ] **Step 5: Commit the completed task.**

~~~bash
git add web/src/features/vault/SensitiveField.tsx web/src/features/vault/ItemDetail.tsx web/src/features/vault/forms/CreditCardFields.tsx
git commit -m "fix: display card numbers in vault details"
~~~

### Task 4: Update the shared copy and perform release checks

**Files:**
- Modify: web/src/features/vault/ItemEditor.tsx

- [ ] **Step 1: Change the create-form label.**

In the create-only save-location fieldset, replace the visible label text 家庭共享 with 共享. Keep the radio value shared, the API field vault_scope, and the personal label 个人保险库 unchanged. Do not add an edit-form scope selector in this phase.

- [ ] **Step 2: Verify user-facing vault copy.**

Run:

~~~bash
rg -n '家庭共享|永久删除' web/src/features/vault web/src/features/vault/vault.test.tsx
~~~

Expected: no 家庭共享 remains in the vault product code; no vault trash button, title, or confirmation action uses 永久删除. Any remaining 永久删除 must be outside the vault scope or be the description wording 永久清除, which is intentionally retained.

- [ ] **Step 3: Run final checks from the worktree.**

Run these commands in order from the worktree root:

~~~bash
cd web
npm run typecheck
npm test -- --run
npm run build
cd ..
go test ./...
~~~

Expected: each command exits 0; the frontend build writes the normal embedded asset output and no Go package regresses.

- [ ] **Step 4: Run browser regression.**

From the repository worktree root, run:

~~~bash
bash scripts/test-browser-e2e.sh
~~~

Expected: the existing browser regression completes successfully. If the environment lacks its browser/runtime dependency, preserve the exact failure output for the handoff instead of treating it as a product failure.

- [ ] **Step 5: Review the complete diff and commit the copy change.**

Run:

~~~bash
git diff --check
git status --short
git diff master...HEAD --stat
~~~

Then commit the remaining editor label change:

~~~bash
git add web/src/features/vault/ItemEditor.tsx
git commit -m "fix: call the shared vault option shared"
~~~

The final handoff must state which commands passed, any browser-environment limitation, and that personal/shared item movement remains deferred as requested.

