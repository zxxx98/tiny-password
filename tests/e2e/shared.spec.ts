import { expect, test } from "@playwright/test";

const MEMBER = "e2e-member";
const MEMBER_PW = process.env.TP_E2E_MEMBER_PW ?? "e2e-member-password-1";
const SECOND = "e2e-second";
const SECOND_PW = process.env.TP_E2E_SECOND_PW ?? "e2e-second-password-1";

async function login(page: import("@playwright/test").Page, username: string, password: string) {
  await page.goto("/vault");
  await page.getByLabel("用户名").fill(username);
  await page.getByLabel("密码").fill(password);
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.getByRole("button", { name: "新建条目" })).toBeVisible();
}

test.describe("shared workspace", () => {
  test("creator sees manage controls; reader sees the read-only notice", async ({ page }) => {
    await login(page, MEMBER, MEMBER_PW);
    await page.getByText("e2e shared login").click();
    await expect(page.getByRole("button", { name: "编辑" })).toBeVisible();

    await page.context().clearCookies();
    await page.goto("/vault");
    await login(page, SECOND, SECOND_PW);
    await page.getByText("e2e shared login").click();
    // The reader sees the creator signature and the read-only reason, but no
    // edit/trash controls (design §5.2, D06).
    await expect(page.getByText(/只有创建者能修改/)).toBeVisible();
    await expect(page.getByRole("button", { name: "编辑" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "移入回收站" })).toHaveCount(0);
  });
});
