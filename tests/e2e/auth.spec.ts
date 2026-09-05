import { expect, test } from "@playwright/test";

const MEMBER = "e2e-member";
const MEMBER_PW = process.env.TP_E2E_MEMBER_PW ?? "e2e-member-password-1";

async function login(
  page: import("@playwright/test").Page,
  username: string,
  password: string,
  // The marker proves the post-login state actually rendered.
  marker = "新建条目",
) {
  await page.goto("/vault");
  await page.getByLabel("用户名").fill(username);
  await page.getByLabel("密码").fill(password);
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.getByText(marker).first()).toBeVisible();
}

test.describe("authentication", () => {
  test("session state stays in memory only", async ({ page }) => {
    await login(page, MEMBER, MEMBER_PW);
    const storage = await page.evaluate(() => ({
      local: window.localStorage.length,
      session: window.sessionStorage.length,
    }));
    expect(storage.local).toBe(0);
    expect(storage.session).toBe(0);
  });

  test("first-login members must rotate before reaching the vault", async ({ page }) => {
    await login(page, "e2e-rotator", "e2e-rotator-initial-1", "首次登录必须设置新密码");
    await expect(page.getByRole("heading", { name: "设置新密码" })).toBeVisible();
    await expect(page.getByText(/首次登录必须设置新密码/)).toBeVisible();

    await page.getByLabel("当前密码", { exact: true }).fill("e2e-rotator-initial-1");
    await page.getByLabel("新密码", { exact: true }).fill("e2e-rotator-new-password-1");
    await page.getByLabel("确认新密码").fill("e2e-rotator-new-password-1");
    await page.getByRole("button", { name: "设置并继续" }).click();

    // After the rotation the workspace loads.
    await expect(page.getByRole("button", { name: "新建条目" })).toBeVisible();

    // Drop the session cookie, then prove the initial password is dead.
    await page.context().clearCookies();
    await page.goto("/vault");
    await page.getByLabel("用户名").fill("e2e-rotator");
    await page.getByLabel("密码").fill("e2e-rotator-initial-1");
    await page.getByRole("button", { name: "登录" }).click();
    await expect(page.getByRole("alert")).toContainText("用户名或密码错误");
  });

  test("member management: create, disable, delete with exact confirmation", async ({ page }) => {
    await login(page, "e2e-admin", "e2e-admin-password-1");
    await page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "管理" }).click();
    await expect(page.getByRole("heading", { name: "成员管理" })).toBeVisible();

    await page.getByRole("button", { name: "创建成员" }).click();
    await page.getByLabel("用户名").fill("e2e-created");
    await page.getByRole("button", { name: "创建成员" }).click();
    await expect(page.getByRole("alert")).toContainText("它只显示这一次");

    const row = page.locator("li", { hasText: "e2e-created" });
    await row.getByRole("button", { name: "停用" }).click();
    await expect(page.getByRole("status")).toContainText("已停用");

    await row.getByRole("button", { name: "删除" }).click();
    await expect(page.getByRole("dialog")).toContainText("删除前请先确认已完成数据导出");
    await page.getByRole("button", { name: "继续删除" }).click();
    await page.getByLabel("目标用户名").fill("e2e-created");
    await page.getByRole("button", { name: "永久删除" }).click();
    await expect(page.getByRole("status")).toContainText("已永久删除");
  });

  test("logout clears the session and the vault requires login again", async ({ page }) => {
    await login(page, MEMBER, MEMBER_PW);
    await page.goto("/account");
    await page.getByRole("button", { name: "退出登录" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "退出" }).click();
    await expect(page.getByRole("heading", { name: "登录" })).toBeVisible();
    await page.goto("/vault");
    await expect(page.getByRole("heading", { name: "登录" })).toBeVisible();
  });
});
