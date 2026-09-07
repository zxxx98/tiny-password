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

for (const viewport of [
  { name: "phone", width: 360, height: 800 },
  { name: "tablet", width: 768, height: 1024 },
  { name: "desktop", width: 1280, height: 900 },
]) {
  test.describe(`accessible shell at ${viewport.name}`, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test("keeps primary controls labelled, keyboard reachable, and touch-sized", async ({ page }) => {
      await login(page);
      await expect(page.getByRole("button", { name: "新建条目" })).toBeVisible();

      const controls = await page.evaluate(() =>
        Array.from(document.querySelectorAll<HTMLElement>("a, button, input, select, textarea"))
          .filter((element) => {
            const style = window.getComputedStyle(element);
            return style.display !== "none" && style.visibility !== "hidden" && element.getClientRects().length > 0;
          })
          .map((element) => {
            const rect = element.getBoundingClientRect();
            return {
              role: element.tagName.toLowerCase(),
              label: element.getAttribute("aria-label") || element.textContent?.trim() || element.getAttribute("name"),
              width: rect.width,
              height: rect.height,
            };
          }),
      );

      expect(controls.length).toBeGreaterThan(0);
      expect(controls.every((control) => control.label)).toBeTruthy();
      expect(controls.filter((control) => ["a", "button", "select", "textarea"].includes(control.role)).every((control) =>
        control.width >= 44 && control.height >= 44,
      )).toBeTruthy();

      await page.keyboard.press("Tab");
      await expect(page.locator(":focus")).toHaveCount(1);
      await expect(page.locator(":focus")).toBeVisible();
    });
  });
}
