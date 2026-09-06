import { expect, test } from "@playwright/test";

const MEMBER = "e2e-member";
const MEMBER_PW = process.env.TP_E2E_MEMBER_PW ?? "e2e-member-password-1";

test.describe("personal transfer", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/transfer");
    await page.getByLabel("用户名").fill(MEMBER);
    await page.getByLabel("密码").fill(MEMBER_PW);
    await page.getByRole("button", { name: "登录" }).click();
    await expect(page.getByRole("heading", { name: "导入 / 导出" })).toBeVisible();
  });

  test("export downloads an encrypted archive; import round-trips it", async ({ page }) => {

    // Export: the seeded items are in the archive.
    await page.getByLabel("归档口令", { exact: true }).fill("e2e-export-pass-1");
    await page.getByLabel("确认归档口令").fill("e2e-export-pass-1");
    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "下载加密归档" }).click();
    const download = await downloadPromise;
    await download.saveAs("/tmp/e2e-export.7z");
    await expect(page.getByRole("status")).toContainText("归档已下载");

    // Import the same archive back: preview shows the counts, confirm writes.
    await page.getByLabel("归档文件（.7z）").setInputFiles("/tmp/e2e-export.7z");
    await page.getByLabel("归档口令（导入）").fill("e2e-export-pass-1");
    await page.getByRole("button", { name: "预览导入" }).click();
    await expect(page.getByRole("region", { name: "导入预览" })).toBeVisible();
    await expect(page.getByText(/冲突条目/)).toBeVisible();
    await page.getByRole("button", { name: "确认导入" }).click();
    await expect(page.getByRole("status").last()).toContainText("导入完成");
  });

  test("wrong passphrase fails with the stable error", async ({ page }) => {
    // Seed an archive through the UI first.
    await page.getByLabel("归档口令", { exact: true }).fill("e2e-export-pass-1");
    await page.getByLabel("确认归档口令").fill("e2e-export-pass-1");
    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "下载加密归档" }).click();
    const download = await downloadPromise;
    await download.saveAs("/tmp/e2e-export-wrong.7z");

    await page.getByLabel("归档文件（.7z）").setInputFiles("/tmp/e2e-export-wrong.7z");
    await page.getByLabel("归档口令（导入）").fill("totally-wrong-passphrase");
    await page.getByRole("button", { name: "预览导入" }).click();
    await expect(page.getByText("the archive passphrase is wrong")).toBeVisible();
  });
});
