import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(({ page }) => assertMockApplication(page));
test("unified workspace opens Codex, Gemini and Claude conversations", async ({ page }, testInfo) => {
  const workspace = { id: "workspace", project_id: "project", server_id: "server", server_name: "Server", project_name: "Project", path: "/srv/project", display_name: "Feature", status: "ready", management_mode: "managed" };
  const sessions = ["codex", "gemini", "claude"].map(engine => ({
    engine, id: "shared-id", workspace_id: workspace.id, project_id: "project", server_id: "server",
    project_name: "Project", server_name: "Server", path: workspace.path, title: `${engine} conversation`,
    status: "idle", server_status: "online", native_id: `native-${engine}`, session_id: `native-${engine}`, codex_thread_id: `native-${engine}`,
    model: "test-model", mode: "default", approval_mode: "default", permission_mode: "default", capabilities: {},
    created_at: "2026-11-15T00:00:00Z", updated_at: "2026-11-15T00:00:00Z", archived_at: null,
  }));
  const listRequests: string[] = [];
  await installMockApplication(page, { configured: true, onAPIRequest: request => {
    if (request.path === "/workspaces") return { body: [workspace] };
    if (request.path === "/servers") return { body: [{ id: "server", status: "online" }] };
    if (request.path === "/sessions") { listRequests.push(request.search); return { body: { items: sessions, next_cursor: null, groups: [{ workspace_id: workspace.id, matched_count: 3, project_pinned_at: null }] } }; }
    const thread = sessions.find(item => request.path === `/sessions/${item.engine}/${item.id}`);
    if (thread) return { body: thread };
    if (/\/events$/.test(request.path)) return { body: [] };
    if (/\/goal$/.test(request.path)) return { body: request.path.includes("/codex/") ? { status: "succeeded", supported: true, data: null, error: "" } : null };
    if (request.path === `/workspaces/${workspace.id}/files`) return { body: { files: [], status: "succeeded", error: "", truncated: false } };
    if (/\/workspaces\/workspace\/(gemini|claude)\/models$/.test(request.path)) return { body: { configured: true, models: [{ id: "test-model", name: "Test model" }], default_model: "test-model" } };
    return undefined;
  } });
  await page.goto("/?view=sessions");
  await expect(page.locator(".thread-project")).toHaveCount(1);
  await expect(page.locator(".session-engine")).toHaveText(["Gemini", "Codex", "Claude"]);
  for (const engine of ["codex", "gemini", "claude"]) {
    if (testInfo.project.name === "mobile-chromium") {
      const tabs = page.locator(".codex-mobile-tabs:visible, .gemini-mobile-tabs:visible");
      await tabs.getByRole("button", { name: "Sessions", exact: true }).or(tabs.getByRole("tab", { name: "Sessions", exact: true })).click();
    }
    await page.locator(".thread-select").filter({ hasText: `${engine} conversation` }).click();
    await expect(page.locator(".session-header h2")).toHaveText(`${engine} conversation`);
    await expect(page).toHaveURL(new RegExp(`view=sessions&engine=${engine}&thread=shared-id`));
    await expect(page.locator(".thread.active")).toHaveCount(1);
    await expect(page.locator(".conversation-stream")).toBeVisible();
  }
  await page.screenshot({ path: testInfo.outputPath("unified-sessions.png"), fullPage: true });
  expect(listRequests.every(search => !new URLSearchParams(search).has("engine"))).toBe(true);
});
