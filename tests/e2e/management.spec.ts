import { expect, test, type Page } from "@playwright/test";

async function login(page: Page) {
  await page.goto("/vault");
  await page.getByLabel("用户名").fill("e2e-member");
  await page.getByLabel("密码").fill(process.env.TP_E2E_MEMBER_PW ?? "e2e-member-password-1");
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("button", { name: "新建条目" })).toBeVisible();
}

test.beforeEach(async ({ page }) => login(page));

test("duplicates an existing item and generates a password in the personal draft", async ({ page }) => {
  await page.getByRole("button", { name: /e2e login/ }).click();
  await page.getByRole("button", { name: "复制为新条目" }).click();
  await expect(page.getByLabel("名称")).toHaveValue("e2e login（副本）");
  await expect(page.getByLabel("个人保险库")).toBeChecked();
  await page.getByRole("button", { name: "生成密码", exact: true }).click();
  await page.getByLabel("密码长度").fill("28");
  await page.getByRole("button", { name: "生成并填入" }).click();
  await expect(page.getByLabel("密码", { exact: true })).toHaveValue(/.{28}/);
  await expect(page.getByText(/密码强度：/)).toBeVisible();
  await page.getByLabel("名称").fill("generated test account");
  await page.getByRole("button", { name: "创建条目" }).click();
  await expect(page.getByRole("dialog", { name: "generated test account" })).toBeVisible();
  await page.getByRole("button", { name: "关闭弹窗" }).click();
  await expect(page.getByText("e2e login", { exact: true })).toBeVisible();
  await expect(page.getByText("generated test account", { exact: true })).toBeVisible();
});

test("sorts, groups and filters real items, then adds tags, moves and trashes a batch", async ({ page }) => {
  for (const name of ["manage Alpha", "manage Beta"]) {
    await page.getByRole("button", { name: "新建条目" }).click();
    await page.getByLabel("类型", { exact: true }).selectOption("secure_note");
    await page.getByLabel("标题").fill(name);
    await page.getByLabel("正文").fill("test note");
    await page.getByRole("button", { name: "创建条目" }).click();
    await expect(page.getByRole("dialog", { name })).toBeVisible();
    await page.getByRole("button", { name: "关闭弹窗" }).click();
  }
  await page.getByLabel("搜索条目").fill("manage");
  await page.getByRole("button", { name: "搜索", exact: true }).click();
  await page.getByLabel("排序", { exact: true }).selectOption("title_asc");
  await expect(page.locator("button[data-vault-item]")).toHaveText([/manage Alpha/, /manage Beta/]);
  await page.getByLabel("选择已加载的全部条目").check();
  await page.getByRole("button", { name: "批量添加标签", exact: true }).click();
  await page.getByLabel("添加标签（逗号分隔）").fill("manage-group");
  await page.getByRole("button", { name: "确认执行" }).click();
  await expect(page.getByText("完成 2 项。", { exact: true })).toBeVisible();
  await page.getByLabel("按标签筛选").selectOption("manage-group");
  await page.getByLabel("分组", { exact: true }).selectOption("tag");
  await expect(page.getByRole("heading", { name: "manage-group · 2" })).toBeVisible();
  await page.getByLabel("选择已加载的全部条目").check();
  await page.getByRole("button", { name: "批量移至共享", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "批量移至共享" })).toContainText("所有成员都能读取");
  await page.getByRole("button", { name: "确认执行" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.locator("button[data-vault-item]")).toHaveText([/manage Alpha.*共享/s, /manage Beta.*共享/s]);
  await page.getByLabel("选择已加载的全部条目").check();
  await page.getByRole("button", { name: "批量移入回收站", exact: true }).click();
  await page.getByRole("button", { name: "确认执行" }).click();
  await expect(page.locator("button[data-vault-item]")).toHaveCount(0);
  await page.getByRole("link", { name: "回收站", exact: true }).click();
  await expect(page.getByText("manage Alpha", { exact: true })).toBeVisible();
  await expect(page.getByText("manage Beta", { exact: true })).toBeVisible();
});

test("opens health findings and copies a password from the list without revealing it", async ({ page }) => {
  await page.getByRole("button", { name: "新建条目" }).click();
  await page.getByLabel("名称").fill("manage weak login");
  await page.getByLabel("用户名", { exact: true }).fill("test");
  await page.getByLabel("密码", { exact: true }).fill("short");
  await page.getByRole("button", { name: "创建条目" }).click();
  await expect(page.getByRole("dialog", { name: "manage weak login" })).toBeVisible();
  await page.getByRole("button", { name: "关闭弹窗" }).click();
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  const quickCopy = page.getByRole("group", { name: "e2e login 的快捷复制" });
  await quickCopy.getByRole("button", { name: "复制密码", exact: true }).click();
  await expect(quickCopy.getByRole("status")).toContainText("已复制");
  const clipboard = await page.evaluate(() => navigator.clipboard.readText());
  expect(clipboard).toBeTruthy();
  await expect(page.getByText(clipboard, { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "查看问题条目" }).click();
  await expect(page.getByRole("dialog", { name: "密码健康", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "查看条目", exact: true }).first().click();
  await expect(page.getByRole("button", { name: "历史记录", exact: true })).toBeVisible();
});

test("blocks stale rows during delayed filtering and batches only the new scope", async ({ page }) => {
  const oldRow = page.getByLabel("选择 e2e login", { exact: true });
  await oldRow.check();
  let release!: () => void;
  const delayed = new Promise<void>((resolve) => { release = resolve; });
  let started = false;
  let delayOnce = true;
  await page.route("**/api/v1/items/browse", async (route) => {
    if (delayOnce && route.request().postDataJSON().scope === "shared") {
      delayOnce = false;
      const response = await route.fetch();
      started = true;
      await delayed;
      await route.fulfill({ response });
    } else await route.continue();
  });
  const createdNames: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && new URL(request.url()).pathname === "/api/v1/items") createdNames.push(request.postDataJSON().payload.name);
  });
  await page.getByLabel("按保险库筛选").selectOption("shared");
  await expect.poll(() => started).toBe(true);
  await expect(oldRow).toBeDisabled();
  await expect(page.getByLabel("选择已加载的全部条目")).toBeDisabled();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  release();
  await expect(oldRow).toHaveCount(0);
  await expect(page.getByLabel("选择 e2e shared login", { exact: true })).not.toBeChecked();
  await page.getByLabel("选择 e2e shared login", { exact: true }).check();
  await page.getByRole("button", { name: "复制为个人条目", exact: true }).click();
  await page.getByRole("button", { name: "确认执行" }).click();
  await expect(page.getByText("完成 1 项。", { exact: true })).toBeVisible();
  expect(createdNames).toEqual(["e2e shared login（副本）"]);
});

test("audits and cleans a successful clipboard write that finishes after the row unmounts", async ({ page }) => {
  await page.clock.install();
  await page.evaluate(() => {
    const state = { value: "", started: false, finish: () => {} };
    (window as unknown as { delayedClipboard: typeof state }).delayedClipboard = state;
    let first = true;
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: {
      readText: async () => state.value,
      writeText: async (value: string) => {
        if (first) {
          first = false;
          state.started = true;
          await new Promise<void>((resolve) => { state.finish = resolve; });
        }
        state.value = value;
      },
    } });
  });
  let audits = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && new URL(request.url()).pathname.endsWith("/copy")) audits++;
  });
  const quickCopy = page.getByRole("group", { name: "e2e login 的快捷复制", exact: true });
  await quickCopy.getByRole("button", { name: "复制密码", exact: true }).click();
  await expect.poll(() => page.evaluate(() => (window as unknown as { delayedClipboard: { started: boolean } }).delayedClipboard.started)).toBe(true);
  await page.getByLabel("搜索条目").fill("no-results-delayed-copy");
  await page.getByRole("button", { name: "搜索", exact: true }).click();
  await expect(quickCopy).toHaveCount(0);
  await page.evaluate(() => (window as unknown as { delayedClipboard: { finish: () => void } }).delayedClipboard.finish());
  await expect.poll(() => audits).toBe(1);
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).not.toBe("");
  await page.clock.fastForward(30_000);
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("");
});
