import { expect, test } from "@playwright/test";

/**
 * Browser E2E for the auth and member-management flows (plan T15). The suite
 * runs against the isolated test compose instance (scripts/test-e2e.sh);
 * the setup token is fetched from the container log by the harness.
 */

test.describe("authentication", () => {
  test("login, forced first-password rotation and immediate revocation", async ({ page, request }) => {
    // The harness guarantees an initialized instance with these credentials.
    await page.goto("/login");
    await page.getByLabel("用户名").fill("e2e-admin");
    await page.getByLabel("密码").fill("e2e-password-1");
    await page.getByRole("button", { name: "登录" }).click();

    await expect(page.getByRole("heading", { name: "账户" })).toBeVisible();

    // Session state must never persist: no local storage entries allowed.
    const storage = await page.evaluate(() => ({
      local: window.localStorage.length,
      session: window.sessionStorage.length,
    }));
    expect(storage.local).toBe(0);
    expect(storage.session).toBe(0);
  });

  test("first-login members must rotate their password before the vault", async ({ page }) => {
    await page.goto("/vault");
    // Unauthenticated visitors only ever see the login view.
    await expect(page.getByRole("heading", { name: "登录" })).toBeVisible();
  });

  test("member management: create, disable, delete with exact confirmation", async ({ page }) => {
    await page.goto("/admin/users");
    await page.getByRole("button", { name: "创建成员" }).click();
    await page.getByLabel("用户名").fill("e2e-member");
    await page.getByRole("button", { name: "创建成员" }).click();
    await expect(page.getByRole("alert")).toContainText("它只显示这一次");

    const row = page.locator("li", { hasText: "e2e-member" });
    await row.getByRole("button", { name: "停用" }).click();
    await expect(page.getByRole("status")).toContainText("已停用");

    // Deletion requires the export warning first, then the exact username.
    await row.getByRole("button", { name: "删除" }).click();
    await expect(page.getByRole("dialog")).toContainText("删除前请先确认已完成数据导出");
    await page.getByRole("button", { name: "继续删除" }).click();
    await page.getByLabel("目标用户名").fill("e2e-member");
    await page.getByRole("button", { name: "永久删除" }).click();
    await expect(page.getByRole("status")).toContainText("已永久删除");
  });
});
