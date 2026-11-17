import { defineConfig, devices } from "@playwright/test";

// A port unlikely to clash with anything else running; override with E2E_PORT.
const port = Number(process.env.E2E_PORT ?? 25174);

/** End-to-end tests run against the dev server in mock mode: no backend needed. */
export default defineConfig({
  testDir: "e2e",
  timeout: 60_000,
  expect: { timeout: 15_000 },
  fullyParallel: true,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "github" : "list",
  use: { baseURL: `http://127.0.0.1:${port}`, trace: "retain-on-failure" },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 960 } } },
    { name: "mobile", use: { ...devices["Pixel 7"] }, grep: /@mobile/ },
  ],
  webServer: {
    command: `pnpm dev --host 127.0.0.1 --port ${port} --strictPort`,
    url: `http://127.0.0.1:${port}/signin`,
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
