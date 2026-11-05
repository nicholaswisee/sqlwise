import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  outputDir: "./test-results",
  use: { baseURL: "http://localhost:5174" },
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
  webServer: {
    command: "vp dev",
    url: "http://localhost:5174",
    reuseExistingServer: !process.env.CI,
  },
});
