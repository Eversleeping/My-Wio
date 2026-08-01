import { expect, test } from "@playwright/test";
import { installMockApplication } from "./support/mock-api";

test("an unconfigured installation can reach sign-in without a backend", async ({ page }) => {
  await installMockApplication(page, { configured: false });
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Create administrator" })).toBeVisible();
  await page.getByRole("radio", { name: "Username + fixed password Use one password for every sign-in." }).check();
  await page.getByLabel("Password", { exact: true }).fill("safe-test-password");
  await page.getByLabel("Confirm password", { exact: true }).fill("safe-test-password");

  const setupRequest = page.waitForRequest(request =>
    request.method() === "POST" && new URL(request.url()).pathname === "/api/setup"
  );
  await page.getByRole("button", { name: "Create administrator" }).click();
  expect(JSON.parse((await setupRequest).postData() ?? "{}")).toMatchObject({
    username: "admin",
    auth_mode: "password"
  });

  await expect(page.getByRole("heading", { name: "Administrator ready" })).toBeVisible();
  await page.getByRole("button", { name: "Continue to sign in" }).click();
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();

  await page.getByLabel("Password", { exact: true }).fill("safe-test-password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
});

test("an authenticated user can navigate the primary console", async ({ page }) => {
  test.skip(test.info().project.name !== "desktop-chromium", "Primary sidebar navigation is covered at desktop width.");
  await installMockApplication(page, { configured: true });
  await page.goto("/");

  const topbar = page.locator("header.topbar");
  await expect(topbar.getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
  const navigation = page.getByRole("navigation", { name: "Primary navigation" });
  await navigation.getByRole("button", { name: "Servers", exact: true }).click();
  await expect(page).toHaveURL(/\?view=servers$/);
  await expect(topbar.getByRole("heading", { name: "Servers", exact: true })).toBeVisible();

  await navigation.getByRole("button", { name: "Projects", exact: true }).click();
  await expect(page).toHaveURL(/\?view=projects$/);
  await expect(topbar.getByRole("heading", { name: "Projects", exact: true })).toBeVisible();
});

test("the 390px mobile project opens and closes its responsive navigation", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "mobile-chromium", "Responsive menu is covered by the mobile project.");
  await installMockApplication(page, { configured: true });
  await page.goto("/");

  const sidebar = page.locator(".sidebar");
  await expect(sidebar).not.toHaveClass(/\bopen\b/);
  await page.getByRole("button", { name: "Open navigation" }).click();
  await expect(sidebar).toHaveClass(/\bopen\b/);

  await page.getByRole("navigation", { name: "Primary navigation" }).getByRole("button", { name: "Projects", exact: true }).click();
  await expect(page).toHaveURL(/\?view=projects$/);
  await expect(sidebar).not.toHaveClass(/\bopen\b/);
  await expect(page.locator("header.topbar").getByRole("heading", { name: "Projects", exact: true })).toBeVisible();
});
