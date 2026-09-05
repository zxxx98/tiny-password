import { expect, test } from "@playwright/test";

/**
 * Browser E2E for the personal vault workspace (plan T16). Runs against the
 * isolated test instance; credentials come from the e2e harness.
 */

test.describe("vault workspace", () => {
  test("desktop grid: list column and detail pane side by side", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto("/vault");
    await expect(page.getByRole("heading", { name: /tiny password/i })).toBeVisible();
    // With seeded data the list shows the decrypted title and the scope text.
    await expect(page.getByText("e2e login")).toBeVisible();
    await expect(page.getByText(/个人/).first()).toBeVisible();

    await page.getByText("e2e login").click();
    // The detail pane reveals the payload fields; the password stays masked.
    await expect(page.getByTestId("secret-masked-password")).toBeVisible();
    await expect(page.getByText("SYNSECRET-e2e")).not.toBeVisible();
  });

  test("sensitive reveal is explicit, audited and time-bound", async ({ page }) => {
    await page.goto("/vault");
    await page.getByText("e2e login").click();
    await page.getByRole("button", { name: "显示" }).click();
    await expect(page.getByTestId("secret-value-password")).toBeVisible();
    // The value re-masks without user action.
    await expect(page.getByTestId("secret-masked-password")).toBeVisible({ timeout: 40_000 });
  });

  test("create, search and trash a secure note", async ({ page }) => {
    await page.goto("/vault");
    await page.getByRole("button", { name: "新建条目" }).click();
    await page.getByLabel("类型").selectOption("secure_note");
    await page.getByLabel("标题").fill("e2e note");
    await page.getByLabel("正文").fill("recovery codes e2e");
    await page.getByRole("button", { name: "创建条目" }).click();
    await expect(page.getByRole("heading", { name: "e2e note" })).toBeVisible();

    await page.getByLabel("搜索条目").fill("e2e note");
    await page.getByRole("button", { name: "搜索" }).click();
    await expect(page.getByText("e2e note")).toBeVisible();

    // Trash from the detail, then restore from the trash page.
    await page.getByRole("button", { name: "移入回收站" }).click();
    await page.getByRole("button", { name: "移入回收站" }).last().click();
    await page.getByRole("link", { name: "回收站" }).click();
    await expect(page.getByText("e2e note")).toBeVisible();
    await page.getByRole("button", { name: "恢复", exact: false }).first().click();
    await expect(page.getByText(/内容与版本号不变/)).toBeVisible();
  });

  test("mobile: single column with bottom navigation and full-page detail", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/vault");
    const bottomNav = page.getByRole("navigation", { name: "移动端主导航" });
    await expect(bottomNav).toBeVisible();
    await page.getByText("e2e login").click();
    // Detail opens as its own page below the desktop breakpoint.
    await expect(page.getByTestId("secret-masked-password")).toBeVisible();
    await expect(page.getByRole("button", { name: "← 返回列表" })).toBeVisible();
  });
});
