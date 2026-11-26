import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(({ page }) => assertMockApplication(page));

for (const parallel of [true, false]) test(`updates all installed AI Agents ${parallel ? "in parallel" : "on legacy Agents"} with one confirmation`, async ({ page }, testInfo) => {
  const kinds = ["codex", "gemini", "claude"];
  const versions: Record<string, string> = { codex: "1.0.0", gemini: "1.0.0", claude: "1.0.0" };
  const operations: Record<string, { id: string; status: string }> = {};
  const installs: string[] = [];
  let finish = false;
  const server = { id: "cli-node", name: "CLI node", hostname: "node.example.test", status: "online", agent_version: "0.3.59", codex_ready: 1, codex_version: "1.0.0", gemini_ready: 1, gemini_version: "1.0.0", claude_ready: 1, claude_version: "1.0.0" };
  await installMockApplication(page, { configured: true, onAPIRequest: request => {
    if (request.path === "/servers") return { body: [server] };
    if (request.path === "/credential-profiles") return { body: [] };
    if (request.path.endsWith("/versions")) return { body: { versions: ["2.0.0", "1.0.0"], target_version: "1.0.0" } };
    if (request.path.endsWith("/install")) {
      const kind = request.path.split("/").at(-2)!;
      // No later installation may be queued before the preceding operation has succeeded.
      if (!parallel) expect(Object.values(operations).every(operation => operation.status === "succeeded")).toBe(true);
      expect(installs).not.toContain(kind);
      expect(JSON.parse(request.body!)).toEqual({ version: "2.0.0" });
      installs.push(kind);
      operations[kind] = { id: `install-${kind}`, status: "running" };
      return { status: 202, body: { operation_id: operations[kind].id } };
    }
    if (request.path === "/servers/cli-node/ai-clis") {
      if (finish) for (const kind of Object.keys(operations)) { operations[kind].status = "succeeded"; versions[kind] = "2.0.0"; }
      return { body: kinds.map(kind => ({ kind, version: versions[kind], ready: true, supported: true, parallel_supported: parallel, operation: operations[kind] })) };
    }
    return undefined;
  } });
  await page.goto("/?view=servers");
  await page.getByRole("button", { name: "Manage AI Agents", exact: true }).click();
  const manager = page.getByRole("dialog", { name: "AI Agents on CLI node" });
  await expect(manager.getByRole("button", { name: "Update all Agents", exact: true })).toBeEnabled();
  for (const theme of ["dark", "light"]) {
    await page.evaluate(theme => document.documentElement.setAttribute("data-theme", theme), theme);
    await page.screenshot({ path: testInfo.outputPath(`cli-batch-${theme}.png`) });
  }
  await manager.getByRole("button", { name: "Update all Agents", exact: true }).click();
  const confirmation = page.getByRole("dialog", { name: "Update all Agents", exact: true });
  await expect(confirmation).toContainText("Codex CLI → 2.0.0");
  await confirmation.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(installs).toEqual([]);
  await manager.getByRole("button", { name: "Update all Agents", exact: true }).click();
  await confirmation.getByRole("button", { name: "Update all Agents", exact: true }).click();
  await expect(manager.getByRole("status")).toContainText(parallel ? "Updating Codex / Gemini / Claude · 0/3 completed" : "Updating Codex · 0/3 completed");
  await expect(manager.getByRole("button", { name: "Switch Gemini version" })).toBeDisabled();
  await expect(manager.locator(".dialog-actions").getByRole("button", { name: "Close", exact: true })).toBeDisabled();
  await expect.poll(() => installs).toEqual(parallel ? kinds : ["codex"]);
  await page.screenshot({ path: testInfo.outputPath("cli-batch-progress.png") });
  finish = true;
  await expect(page.getByText("All Agent updates completed (3)", { exact: true })).toBeVisible();
  expect(installs).toEqual(kinds);
  await expect(manager.getByRole("button", { name: "Update all Agents", exact: true })).toBeDisabled();
  await expect(manager.locator(".dialog-actions").getByRole("button", { name: "Close", exact: true })).toBeEnabled();
  expect(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)).toBe(false);
});
