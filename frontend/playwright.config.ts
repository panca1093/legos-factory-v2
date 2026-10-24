import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "tests/e2e",
  timeout: 120_000,
  workers: 1,
  fullyParallel: false,
  use: { baseURL: "http://127.0.0.1:15173", trace: "retain-on-failure" },
  webServer: [
    {
      command: "node scripts/start-e2e-backend.mjs",
      url: "http://127.0.0.1:18081/health",
      reuseExistingServer: false,
      gracefulShutdown: { signal: "SIGTERM", timeout: 5000 },
      timeout: 120_000,
    },
    {
      command: "npm run dev -- --host 127.0.0.1 --port 15173 --strictPort",
      url: "http://127.0.0.1:15173",
      reuseExistingServer: false,
      env: { FACTORY_API_URL: "http://127.0.0.1:18081" },
      timeout: 30_000,
    },
    {
      command: "node scripts/serve-prototype.mjs",
      url: "http://127.0.0.1:15174",
      reuseExistingServer: false,
      timeout: 30_000,
    },
  ],
});
