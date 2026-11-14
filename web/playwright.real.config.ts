import { defineConfig } from "@playwright/test";
import mocked from "./playwright.config";

export default defineConfig({
  testDir: "./e2e/real",
  testMatch: "**/*.pw.ts",
  globalSetup: "./e2e/real/start-controlplane.mjs",
  outputDir: "../outputs/playwright/real-test-results",
  workers: 1,
  retries: 0,
  forbidOnly: Boolean(process.env.CI),
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: [["list"], ["html", { outputFolder: "../outputs/playwright/real-report", open: "never" }]],
  use: { ...mocked.use, baseURL: undefined, actionTimeout: 15_000 },
  projects: [mocked.projects![0]]
});
