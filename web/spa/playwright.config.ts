import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.SQLWISE_E2E_URL ?? "http://localhost:5174";

export default defineConfig({
  testDir: "./tests/e2e",
  outputDir: "./test-results",
  workers: 2,
  use: { baseURL },
  projects: [
    {
      name: "desktop",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } },
    },
    {
      name: "mobile",
      use: { ...devices["Desktop Chrome"], viewport: { width: 390, height: 844 } },
    },
    {
      name: "narrow",
      use: { ...devices["Desktop Chrome"], viewport: { width: 320, height: 720 } },
    },
  ],
  webServer: process.env.SQLWISE_E2E_URL
    ? undefined
    : {
        command: "vp dev",
        url: "http://localhost:5174",
        reuseExistingServer: !process.env.CI,
      },
});
