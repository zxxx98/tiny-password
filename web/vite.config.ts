import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The build lands inside the Go module so `go:embed` picks it up.
// emptyOutDir stays false so the committed dist/.keep survives; stale hashed
// bundles are removed by the npm build script instead.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/webassets/dist",
    emptyOutDir: false,
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/healthz": "http://127.0.0.1:8080",
      "/readyz": "http://127.0.0.1:8080",
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: "./vitest.setup.ts",
  },
});
