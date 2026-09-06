import { expect, test } from "@playwright/test";

const ADMIN = "e2e-admin";
const ADMIN_PW = process.env.TP_E2E_ADMIN_PW ?? "e2e-admin-password-1";

async function loginAsAdmin(page: import("@playwright/test").Page) {
  await page.goto("/admin/backups");
  await page.getByLabel("用户名").fill(ADMIN);
  await page.getByLabel("密码").fill(ADMIN_PW);
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.getByRole("heading", { name: "实例备份" })).toBeVisible();
}

test.describe("backup administration", () => {
  test("per-target configuration, manual run, and independent results", async ({ page }) => {
    await loginAsAdmin(page);

    // Both targets are listed; R2 has no credentials in the test instance
    // and is flagged as not delivery-ready.
    await expect(page.getByText("本地备份")).toBeVisible();
    await expect(page.getByText("R2 备份")).toBeVisible();
    await expect(page.getByText("未配置交付")).toBeVisible();

    // Adjust the local schedule and retention, then save.
    await page.getByLabel("每日执行时间（HH:MM）").first().fill("03:30");
    await page.getByLabel("日备份保留数").first().fill("5");
    await page.getByRole("button", { name: "保存配置" }).first().click();
    await expect(page.getByText("本地备份配置已保存。")).toBeVisible();

    // Trigger a manual run for the local target only: it must succeed even
    // though the R2 target is not configured (independent results).
    await page.getByRole("button", { name: "立即执行" }).first().click();
    await expect(page.getByText("备份已开始，完成后可在下方历史中查看结果。")).toBeVisible();

    // The history eventually shows a succeeded local run.
    await expect(page.getByText(/本地备份 · 成功/)).toBeVisible({ timeout: 120_000 });
  });

  test("system settings show ready state, credentials policy, and offline restore", async ({ page }) => {
    await loginAsAdmin(page);
    await page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "系统" }).click();
    await expect(page.getByRole("heading", { name: "系统设置" })).toBeVisible();

    await expect(page.getByText("就绪", { exact: true })).toBeVisible();
    // The test instance mounts no R2 credentials, so the system page must
    // report their absence honestly (the flag is real, not hard-coded).
    await expect(page.getByText("未挂载")).toBeVisible();
    // The restore section explains the offline command and never offers a
    // web-side restore button.
    await expect(page.getByText(/tiny-password restore/)).toBeVisible();
    await expect(page.getByRole("button", { name: /恢复/ })).toHaveCount(0);
  });

  test("audit page filters by result", async ({ page }) => {
    await loginAsAdmin(page);
    await page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "审计" }).click();
    await expect(page.getByRole("heading", { name: "系统审计" })).toBeVisible();

    // The admin login produced at least one success event.
    await page.getByLabel("结果筛选").selectOption("success");
    await page.getByRole("button", { name: "应用筛选" }).click();
    await expect(page.getByText(/auth.login.success/).first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("失败").first()).not.toBeVisible();
  });
});
