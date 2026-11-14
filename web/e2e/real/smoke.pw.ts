import { expect, test } from "@playwright/test";

// Every request reaches the compiled control plane; no route interception or
// direct database setup is used. Only the external model CLIs are substitutes.
test("setup, login, built-in Agent and three engine replies", async ({ page }) => {
  const baseURL = process.env.WIO_E2E_URL!;
  await page.addInitScript(() => localStorage.setItem("wio_language", "en"));
  await page.goto(baseURL);
  await page.getByLabel("Setup token", { exact: true }).fill("p0-08-browser-setup-token");
  await page.locator('input[name="auth-mode"][value="password"]').check();
  await page.getByLabel("Password", { exact: true }).fill("p0-08-browser-password");
  await page.getByLabel("Confirm password", { exact: true }).fill("p0-08-browser-password");
  await page.getByRole("button", { name: "Create administrator", exact: true }).click();
  await page.getByRole("button", { name: "Continue to sign in", exact: true }).click();
  await page.getByLabel("Password", { exact: true }).fill("p0-08-browser-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Sign in", exact: true })).not.toBeVisible();

  await page.goto(`${baseURL}/?view=servers`);
  const server = page.locator("tbody tr").filter({ hasText: "Control plane" });
  await expect(server.getByText("Online", { exact: true })).toBeVisible();

  for (const engine of ["Codex", "Claude", "Gemini"]) {
    await test.step(`${engine} session through the real Agent`, async () => {
      await page.goto(`${baseURL}/?view=${engine.toLowerCase()}`);
      await page.getByRole("button", { name: `New ${engine} session`, exact: true }).first().click();
      const dialog = page.getByRole("dialog", { name: `New ${engine} session`, exact: true });
      await expect(dialog).toBeVisible();
      const workspace = dialog.getByLabel("Workspace");
      await expect(workspace.locator("option").filter({ hasText: process.env.WIO_E2E_WORKSPACE_PATH! })).toHaveCount(1);
      const value = await workspace.locator("option").filter({ hasText: process.env.WIO_E2E_WORKSPACE_PATH! }).getAttribute("value");
      await workspace.selectOption(value!);
      if (engine === "Claude") await dialog.getByLabel("Claude model or alias", { exact: true }).selectOption("sonnet");
      if (engine === "Gemini") {
        await dialog.getByLabel("Gemini model", { exact: true }).selectOption("__custom__");
        await dialog.getByLabel("Model name", { exact: true }).fill("flash");
      }
      await dialog.getByRole("button", { name: engine === "Codex" ? "Create session" : `New ${engine} session`, exact: true }).click();
      await expect(dialog).not.toBeVisible();
      await page.locator("textarea").fill("P0-07 normal reply");
      await page.getByRole("button", { name: "Send", exact: true }).click();
      await expect(page.locator("article.message.assistant").getByText(`P0-07 fake ${engine} reply`, { exact: true })).toBeVisible();
    });
  }
});
