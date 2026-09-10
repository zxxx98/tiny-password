import { expect, test } from "@playwright/test";

const MEMBER = "e2e-member";
const MEMBER_PW = process.env.TP_E2E_MEMBER_PW ?? "e2e-member-password-1";

async function login(page: import("@playwright/test").Page) {
  await page.goto("/vault");
  await page.getByLabel("用户名").fill(MEMBER);
  await page.getByLabel("密码").fill(MEMBER_PW);
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.getByRole("button", { name: "新建条目" })).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await login(page);
});

test.describe("vault workspace", () => {
  test("desktop grid: seeded item lists with scope text and masked payload", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await expect(page.getByText("e2e login")).toBeVisible();
    await expect(page.getByText(/个人/).first()).toBeVisible();

    await page.getByRole("button", { name: /e2e login/ }).click();
    const dialog = page.getByRole("dialog", { name: "e2e login" });
    await expect(dialog).toBeVisible();
    // The modal reveals the fields; the password stays masked.
    await expect(dialog.getByTestId("secret-masked-password")).toBeVisible();
    await expect(page.getByText("SYNSECRET-e2e")).toHaveCount(0);
    await expect(page.getByTestId("mobile-detail")).toHaveCount(0);
  });

  test("sensitive reveal is explicit and re-masks itself", async ({ page }) => {
    test.setTimeout(60_000);
    await page.getByRole("button", { name: /e2e login/ }).click();
    const dialog = page.getByRole("dialog", { name: "e2e login" });
    await dialog.getByRole("button", { name: "显示" }).click();
    await expect(dialog.getByTestId("secret-value-password")).toBeVisible();
    // The value re-masks without user action (30s window).
    await expect(dialog.getByTestId("secret-masked-password")).toBeVisible({ timeout: 40_000 });
  });

  test("create, search and trash a secure note", async ({ page }) => {
    await page.getByRole("button", { name: "新建条目" }).click();
    await page.getByLabel("类型", { exact: true }).selectOption("secure_note");
    await page.getByLabel("标题").fill("e2e note");
    await page.getByLabel("正文").fill("recovery codes e2e");
    await page.getByRole("button", { name: "创建条目" }).click();
    await expect(page.getByRole("heading", { name: "e2e note" })).toBeVisible({ timeout: 10_000 });
    await page.getByRole("dialog", { name: "e2e note" }).getByRole("button", { name: "关闭弹窗" }).click();

    await page.getByLabel("搜索条目").fill("e2e note");
    await page.getByRole("button", { name: "搜索" }).click();
    await expect(page.getByText("e2e note").first()).toBeVisible();
    await page.getByRole("button", { name: /e2e note/ }).click();
    await expect(page.getByRole("dialog", { name: "e2e note" })).toBeVisible();

    // Trash from the detail, then restore from the trash page.
    await page.getByRole("button", { name: "移入回收站" }).click();
    await page.getByRole("dialog", { name: "移入回收站？" }).getByRole("button", { name: "移入回收站" }).click();
    await page.getByRole("link", { name: "回收站" }).click();
    await expect(page.getByText("e2e note")).toBeVisible();
    await page.getByRole("button", { name: "恢复" }).first().click();
    await expect(page.getByText(/内容与版本号不变/)).toBeVisible();
  });

  test("creates, copies, edits and reopens an ordered secret", async ({ page }) => {
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"], { origin: new URL(page.url()).origin });
    const auditedUrls: string[] = [];
    page.on("request", (request) => {
      if (/\/(reveal|copy)$/.test(new URL(request.url()).pathname)) auditedUrls.push(request.url());
    });

    await page.getByRole("button", { name: "新建条目" }).click();
    await page.getByLabel("类型", { exact: true }).selectOption("secret");
    await page.getByLabel("标题").fill("e2e secret");
    await page.getByLabel("键 1").fill("FIRST");
    await page.getByLabel("值 1").fill("one");
    await page.getByRole("button", { name: "添加一组" }).click();
    await page.getByLabel("键 2").fill("EMPTY");
    await page.getByRole("button", { name: "添加一组" }).click();
    await page.getByLabel("键 3").fill("MULTILINE");
    await page.getByLabel("值 3").fill("line1\nline2");
    await page.getByRole("button", { name: "创建条目" }).click();

    const dialog = page.getByRole("dialog", { name: "e2e secret" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText("密钥 · 3 个键值")).toBeVisible();
    await expect(dialog.getByTestId("secret-masked-0")).toBeVisible();
    await expect(dialog.getByTestId("secret-masked-1")).toBeVisible();
    await expect(dialog.getByTestId("secret-masked-2")).toBeVisible();
    await dialog.getByRole("button", { name: "复制全部" }).click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("FIRST=one\nEMPTY=\nMULTILINE=line1\nline2");

    await dialog.getByRole("button", { name: "编辑条目" }).click();
    await page.getByLabel("值 3").fill("changed");
    await page.getByRole("button", { name: "删除第 1 组" }).click();
    await page.getByRole("button", { name: "保存修改" }).click();
    await expect(page.getByRole("heading", { name: "e2e secret" })).toBeVisible();
    await expect(page.getByText("密钥 · 2 个键值")).toBeVisible();
    await page.getByRole("button", { name: "关闭弹窗" }).click();
    await page.getByRole("button", { name: /e2e secret/ }).click();
    const reopened = page.getByRole("dialog", { name: "e2e secret" });
    await expect(reopened.getByTestId("secret-masked-0")).toBeVisible();
    await expect(reopened.getByTestId("secret-masked-1")).toBeVisible();
    await expect(reopened.getByText("EMPTY")).toBeVisible();
    await expect(reopened.getByText("MULTILINE")).toBeVisible();
    await expect(reopened.getByText("changed")).toHaveCount(0);
    expect(auditedUrls).toEqual([]);
  });

  test("mobile: single column with bottom navigation and modal detail", async ({ page }) => {
    // beforeEach 已登录；整页刷新后会话应从 cookie 静默恢复。
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/vault");
    await expect(page.getByRole("button", { name: "新建条目" })).toBeVisible({ timeout: 10_000 });
    const bottomNav = page.getByRole("navigation", { name: "移动端主导航" });
    await expect(bottomNav).toBeVisible();
    await page.getByRole("button", { name: /e2e login/ }).click();
    const dialog = page.getByRole("dialog", { name: "e2e login" });
    await expect(dialog.getByTestId("secret-masked-password")).toBeVisible({ timeout: 10_000 });
    await expect(dialog.getByRole("button", { name: "关闭弹窗" })).toBeVisible();
    await expect(page.getByTestId("mobile-detail")).toHaveCount(0);
    await expect(page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBeTruthy();
    await dialog.getByRole("button", { name: "关闭弹窗" }).click();
    await expect(dialog).toBeHidden();
  });
});
