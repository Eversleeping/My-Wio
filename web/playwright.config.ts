import { defineConfig } from "@playwright/test";
import { existsSync, readdirSync } from "node:fs";
import { join } from "node:path";

const port = 4174;
const baseURL = `http://127.0.0.1:${port}`;

function cachedChromiumExecutable() {
  const root = process.platform === "win32"
    ? join(process.env.LOCALAPPDATA ?? "", "ms-playwright")
    : join(process.env.HOME ?? "", ".cache", "ms-playwright");
  if (!existsSync(root)) return undefined;

  const executable = process.platform === "win32"
    ? ["chrome-win64", "chrome.exe"]
    : ["chrome-linux", "chrome"];
  const directories = readdirSync(root, { withFileTypes: true })
    .filter(entry => entry.isDirectory() && /^chromium-\d+$/.test(entry.name))
    .sort((left, right) => Number(right.name.slice(9)) - Number(left.name.slice(9)));
  return directories.map(entry => join(root, entry.name, ...executable)).find(existsSync);
}

const executablePath = cachedChromiumExecutable();
const chromium = {
  browserName: "chromium" as const,
  ...(executablePath ? { launchOptions: { executablePath } } : {})
};

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.pw.ts",
  outputDir: "../outputs/playwright/test-results",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  timeout: 30_000,
  expect: { timeout: 7_000 },
  reporter: [["list"], ["html", { outputFolder: "../outputs/playwright/report", open: "never" }]],
  use: {
    baseURL,
    serviceWorkers: "block",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    video: "retain-on-failure"
  },
  webServer: {
    command: `npm run dev -- --port ${port} --strictPort`,
    url: baseURL,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000
  },
  projects: [
    {
      name: "desktop-chromium",
      use: { ...chromium, viewport: { width: 1440, height: 900 } }
    },
    {
      name: "mobile-chromium",
      use: {
        ...chromium,
        viewport: { width: 390, height: 844 },
        isMobile: true,
        hasTouch: true
      }
    }
  ]
});
