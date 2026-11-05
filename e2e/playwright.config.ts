import { defineConfig, devices } from "@playwright/test";

import { consoleURL } from "./support/env";

// One wasim instance serves every test, and each test resets it, so tests run
// one at a time.
export default defineConfig({
  testDir: "./tests",
  globalSetup: "./global-setup.ts",
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: consoleURL,
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "e2e",
      testIgnore: /screenshots\.spec\.ts/,
      use: { ...devices["Desktop Chrome"] },
    },
    {
      // Regenerates the README images. Run on demand, not in CI.
      name: "screenshots",
      testMatch: /screenshots\.spec\.ts/,
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 },
    },
  ],
});
