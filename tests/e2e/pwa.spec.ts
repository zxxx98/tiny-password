import { expect, test } from "@playwright/test";

test.describe("PWA", () => {
  test("publishes install metadata and an independent offline explanation", async ({ page, request }) => {
    const manifestResponse = await request.get("/manifest.webmanifest");
    expect(manifestResponse.ok()).toBeTruthy();
    const manifest = await manifestResponse.json();

    expect(manifest.name).toBe("Tiny Password");
    expect(manifest.short_name).toBe("Tiny Password");
    expect(manifest.display).toBe("standalone");
    expect(manifest.start_url).toBe("/vault");
    expect(manifest.theme_color).toBe("#f9f9f7");
    expect(manifest.icons).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ src: "/icons/icon-192.png", sizes: "192x192", type: "image/png" }),
        expect.objectContaining({ src: "/icons/icon-512.png", sizes: "512x512", type: "image/png" }),
      ]),
    );

    await page.goto("/offline.html");
    await expect(page.getByRole("heading", { name: "当前处于离线状态" })).toBeVisible();
    await expect(page.getByText(/不会显示或解锁上次打开的保险库内容/)).toBeVisible();
  });

  test("caches only the explicit static allowlist and never persists vault data", async ({ page }) => {
    await page.goto("/vault");
    await page.evaluate(async () => {
      await navigator.serviceWorker.ready;
    });
    await page.reload();
    await page.evaluate(async () => {
      await navigator.serviceWorker.ready;
    });

    const result = await page.evaluate(async () => {
      const cacheNames = await caches.keys();
      const entries = (
        await Promise.all(
          cacheNames.map(async (name) => {
            const cache = await caches.open(name);
            return (await cache.keys()).map((request) => new URL(request.url).pathname);
          }),
        )
      ).flat();

      // A real API request must remain network-only. The response is allowed
      // to fail in this isolated test; it must never create a cached entry.
      await fetch("/api/v1/items").catch(() => undefined);

      return {
        cacheNames,
        entries,
        localStorage: window.localStorage.length,
        sessionStorage: window.sessionStorage.length,
        indexedDB: "indexedDB" in window ? await indexedDB.databases() : [],
      };
    });

    expect(result.cacheNames).toEqual(["tp-static-v1"]);
    expect(result.entries).not.toContain("/api/v1/items");
    expect(result.entries.every((path) =>
      ["/", "/index.html", "/offline.html", "/manifest.webmanifest", "/icons/icon-192.png", "/icons/icon-512.png"].includes(path) ||
      path.startsWith("/assets/"),
    )).toBeTruthy();
    expect(result.localStorage).toBe(0);
    expect(result.sessionStorage).toBe(0);
    expect(result.indexedDB).toEqual([]);
  });
});
