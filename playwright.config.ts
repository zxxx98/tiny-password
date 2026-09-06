import { defineConfig } from "@playwright/test";

// Browser E2E runs against the throwaway server started by
// scripts/test-browser-e2e.sh; baseURL comes from that harness.
export default defineConfig({
  testDir: "tests/e2e",
  timeout: 30_000,
  expect: { timeout: 5_000 },
  use: {
    baseURL: process.env.TP_E2E_BASE_URL ?? "http://127.0.0.1:8091",
    trace: "on",
  },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
  // Failure artifacts land in test-results (gitignored).
  outputDir: "test-results",
});
