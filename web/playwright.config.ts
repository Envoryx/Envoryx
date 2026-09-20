import { defineConfig, devices } from "@playwright/test";

// Browser end-to-end tests against the real Envoryx binary (DEVELOPMENT.md → Tests).
// e2e/global-setup.ts starts bin/envoryx on this port with throw-away directories.
export const port = Number(process.env.ENVORYX_E2E_PORT ?? 18790);

export default defineConfig({
  testDir: "e2e",
  globalSetup: "./e2e/global-setup.ts",
  // One Envoryx instance, one browser: the specs build on each other (setup → project → delete).
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    locale: "en-US",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
