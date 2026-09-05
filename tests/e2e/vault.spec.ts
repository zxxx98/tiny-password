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

    await page.getByText("e2e login").click();
    // The detail pane reveals the fields; the password stays masked.
    await expect(page.getByTestId("secret-masked-password")).toBeVisible();
    await expect(page.getByText("SYNSECRET-e2e")).toHaveCount(0);
  });

  test("sensitive reveal is explicit and re-masks itself", async ({ page }) => {
    test.setTimeout(60_000);
    await page.getByText("e2e login").click();
    await page.getByRole("button", { name: "显示" }).click();
    await expect(page.getByTestId("secret-value-password")).toBeVisible();
    // The value re-masks without user action (30s window).
    await expect(page.getByTestId("secret-masked-password")).toBeVisible({ timeout: 40_000 });
  });

  test("create, search and trash a secure note", async ({ page }) => {
    await page.getByRole("button", { name: "新建条目" }).click();
    await page.getByLabel("类型", { exact: true }).selectOption("secure_note");
    await page.getByLabel("标题").fill("e2e note");
    await page.getByLabel("正文").fill("recovery codes e2e");
    await page.getByRole("button", { name: "创建条目" }).click();
    await expect(page.getByRole("heading", { name: "e2e note" })).toBeVisible({ timeout: 10_000 });

    await page.getByLabel("搜索条目").fill("e2e note");
    await page.getByRole("button", { name: "搜索" }).click();
    await expect(page.getByText("e2e note").first()).toBeVisible();

    // Trash from the detail, then restore from the trash page.
    await page.getByRole("button", { name: "移入回收站" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "移入回收站" }).click();
    await page.getByRole("link", { name: "回收站" }).click();
    await expect(page.getByText("e2e note")).toBeVisible();
    await page.getByRole("button", { name: "恢复" }).first().click();
    await expect(page.getByText(/内容与版本号不变/)).toBeVisible();
  });

  test("mobile: single column with bottom navigation and full-page detail", async ({ page }) => {
    // beforeEach 已登录；整页刷新后会话应从 cookie 静默恢复。
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/vault");
    await expect(page.getByRole("button", { name: "新建条目" })).toBeVisible({ timeout: 10_000 });
    const bottomNav = page.getByRole("navigation", { name: "移动端主导航" });
    await expect(bottomNav).toBeVisible();
    await page.getByText("e2e login").click();
    // Detail opens as its own page below the desktop breakpoint.
    const mobileDetail = page.getByTestId("mobile-detail");
    await expect(mobileDetail.getByTestId("secret-masked-password")).toBeVisible({ timeout: 10_000 });
    await expect(mobileDetail.getByRole("button", { name: "← 返回列表" })).toBeVisible();
  });
});
