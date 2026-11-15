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

  const current = await (await page.request.get(`${baseURL}/api/auth/session`)).json();
  const headers = { "X-CSRF-Token": current.csrf_token };
  const servers = await (await page.request.get(`${baseURL}/api/servers`)).json();
  const serverID = servers.find((server: {is_control_plane: boolean}) => server.is_control_plane).id;
  // Configure synthetic profiles through the same public APIs used by Settings,
  // and wait for the real built-in Agent to apply each encrypted operation.
  for (const engine of ["codex", "claude", "gemini"]) {
    const profileResponse = await page.request.post(`${baseURL}/api/credential-profiles`, { headers, data: { kind: engine, name: `Browser smoke ${engine}`, endpoint: `${process.env.WIO_E2E_MODEL_URL}/${engine}`, model: engine === "claude" ? "sonnet" : engine === "gemini" ? "flash" : "gpt-5.6-sol", secret: "browser-smoke-synthetic-secret" } });
    expect(profileResponse.ok()).toBe(true);
    const profile = await profileResponse.json();
    const applyResponse = await page.request.post(`${baseURL}/api/servers/${serverID}/${engine === "codex" ? "credential-profiles" : `${engine}-credential-profile`}`, { headers, data: engine === "codex" ? {scope: "codex", codex_profile_id: profile.id} : {profile_id: profile.id} });
    expect(applyResponse.ok()).toBe(true);
    const operation = await applyResponse.json();
    await expect.poll(async () => (await (await page.request.get(`${baseURL}/api/operations/${operation.operation_id}`)).json()).status).toBe("succeeded");
  }

  for (const engine of ["Codex", "Claude", "Gemini"]) {
    await test.step(`${engine} session through the unified dialog and real Agent`, async () => {
      await page.goto(`${baseURL}/?view=sessions`);
      await page.getByRole("button", { name: "New session", exact: true }).first().click();
      const dialog = page.getByRole("dialog", { name: "New session", exact: true });
      await expect(dialog).toBeVisible();
      const workspace = dialog.getByLabel("Workspace");
      await expect(workspace.locator("option").filter({ hasText: process.env.WIO_E2E_WORKSPACE_PATH! })).toHaveCount(1);
      const value = await workspace.locator("option").filter({ hasText: process.env.WIO_E2E_WORKSPACE_PATH! }).getAttribute("value");
      await workspace.selectOption(value!);
      await expect(dialog.getByLabel("Engine")).toBeEnabled();
      await dialog.getByLabel("Engine").selectOption(engine.toLowerCase());
      const submit = dialog.getByRole("button", { name: engine === "Codex" ? "Create session" : `New ${engine} session`, exact: true });
      await expect(submit).toBeEnabled();
      await submit.click();
      await expect(dialog).not.toBeVisible();
      await expect(page).toHaveURL(new RegExp(`view=sessions&engine=${engine.toLowerCase()}&thread=`));
      await page.locator("textarea").fill("P0-07 normal reply");
      await page.getByRole("button", { name: "Send", exact: true }).click();
      await expect(page.locator("article.message.assistant").getByText(`P0-07 fake ${engine} reply`, { exact: true })).toBeVisible();
    });
  }
});
