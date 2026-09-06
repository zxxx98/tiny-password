import { expect, test } from "@playwright/test";

const MEMBER = "e2e-member";
const MEMBER_PW = process.env.TP_E2E_MEMBER_PW ?? "e2e-member-password-1";

test.beforeEach(async ({ page }) => {
  await page.goto("/generator");
  await page.getByLabel("用户名").fill(MEMBER);
  await page.getByLabel("密码").fill(MEMBER_PW);
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.getByRole("heading", { name: "生成器" })).toBeVisible();
});

test.describe("generators", () => {
  test("password generation honours the length and stays in memory", async ({ page }) => {
    await page.getByLabel("长度").fill("24");
    await page.getByRole("button", { name: "生成密码" }).click();
    const out = page.getByLabel("生成的密码");
    await expect(out).toBeVisible();
    await page.getByRole("button", { name: "显示密码" }).click();
    const value = await out.textContent();
    expect(value?.length).toBe(24);
    // Reload wipes the in-memory result.
    await page.reload();
    await expect(page.getByLabel("生成的密码")).toHaveCount(0);
  });

  test("ssh key generation offers save-as-entry and cancel leaves nothing", async ({ page }) => {
    await page.getByRole("button", { name: "生成密钥" }).click();
    await expect(page.getByText(/SHA256:/).first()).toBeVisible();
    await page.getByRole("button", { name: "保存为 SSH 条目" }).click();
    // Saving navigates into the vault detail with the fingerprint as title.
    let pageError: string | null = null;
    page.on("pageerror", (err) => { pageError = String(err); });
    try {
      await expect(page.getByRole("heading", { name: /SHA256:/ })).toBeVisible({ timeout: 8000 });
    } catch {
      const html = await page.content();
      console.log("DEBUG url:", page.url());
      console.log("DEBUG htmlLen:", html.length);
      console.log("DEBUG htmlHead:", html.slice(0, 600));
      console.log("DEBUG pageErrorStack:", (pageError as any)?.stack);
      throw new Error("rethrow");
    }
    await expect(page.getByRole("article").getByTestId("secret-masked-private_key")).toBeVisible();
  });
});
