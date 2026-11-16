import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(({ page }) => assertMockApplication(page));

test("shows unavailable isolation and enrolls with sudo mutually exclusive", async ({ page }, testInfo) => {
  const server = { id: "isolated-node", name: "Isolated node", hostname: "node.example.test", status: "online", agent_version: "0.3.31", agent_target_version: "0.3.31", codex_ready: 1, codex_version: "1.0.0", codex_target_version: "1.0.0", address: "node.example.test", configuration: "", notes: "", session_isolation: { mode: "unsupported", reason: "Required namespaces are unavailable" } };
  await installMockApplication(page, {
    configured: true,
    onAPIRequest: request => {
      if (request.path === "/servers" && request.method === "GET") return { body: [server] };
      if (request.path === "/credential-profiles" && request.method === "GET") return { body: [{ id: "codex-profile", kind: "codex", name: "Codex", model: "gpt-6.1-sol" }] };
      if (request.path === "/servers/ssh/probe" && request.method === "POST") return { body: { fingerprint: "SHA256:synthetic-node", key_type: "ssh-ed25519" } };
      if (request.path === "/servers/ssh/bootstrap-stream" && request.method === "POST") return { stream: [{ type: "complete", result: { server_id: "new-node", hostname: "new.example.test", architecture: "amd64", warnings: [] } }] };
      if (["/projects", "/workspaces"].includes(request.path) && request.method === "GET") return { body: [] };
      return undefined;
    }
  });
  await page.goto("/?view=servers");
  await expect(page.getByText("Isolation unavailable", { exact: true })).toBeVisible();
  await expect(page.getByText(server.session_isolation.reason, { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Enroll server", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Enroll Linux server" });
  const isolation = dialog.getByRole("checkbox", { name: /Enable session isolation/ });
  const sudo = dialog.getByRole("checkbox", { name: /Allow passwordless sudo/ });
  await expect(isolation).not.toBeChecked();
  for (const theme of ["dark", "light"]) {
    await page.evaluate(theme => {
      document.documentElement.setAttribute("data-theme", theme);
      document.body.setAttribute("data-theme", theme);
    }, theme);
    await isolation.scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath(`enrollment-options-${theme}.png`) });
  }
  await isolation.check();
  await expect(sudo).toBeDisabled();
  await isolation.uncheck();
  await sudo.check();
  await expect(isolation).toBeDisabled();
  await sudo.uncheck();
  await isolation.check();
  await dialog.getByLabel("Server name", { exact: true }).fill("new isolated node");
  await dialog.getByLabel("SSH host or IP", { exact: true }).fill("new.example.test");
  await dialog.getByRole("combobox", { name: "Authentication", exact: true }).selectOption("none");
  await dialog.getByRole("button", { name: "Check fingerprint", exact: true }).click();
  const install = page.waitForRequest(request => request.method() === "POST" && new URL(request.url()).pathname.endsWith("/servers/ssh/bootstrap-stream"));
  await dialog.getByRole("button", { name: "Confirm and install", exact: true }).click();
  expect(JSON.parse((await install).postData() ?? "{}")).toMatchObject({ session_isolation: true, allow_sudo: false });
});
