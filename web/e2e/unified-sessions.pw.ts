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
    if (request.path === "/servers") return { body: [{ id: "server", status: "online", session_isolation: { mode: "disabled" } }] };
    if (request.path === "/sessions") { listRequests.push(request.search); return { body: { items: sessions, next_cursor: null, groups: [{ workspace_id: workspace.id, matched_count: 3, project_pinned_at: null }] } }; }
    const thread = sessions.find(item => request.path === `/sessions/${item.engine}/${item.id}`);
    if (thread) return { body: thread };
    if (request.path === "/sessions/codex/shared-id/collaboration") return {body:{supported:true,grant:{enabled:true,show_unread_results:true},notifications:[{target_key:"claude:shared-id",event_id:"old-result"}],requests:[],links:[{source_key:"codex:shared-id",target_key:"claude:shared-id",relation:"created",missing:false,session:{engine:"claude",id:"shared-id",title:"Historical result",status:"idle",workspace_id:workspace.id,url:"/?view=claude&thread=shared-id"}}]}};
    if (/\/events$/.test(request.path)) return { body: [] };
    if (/\/goal$/.test(request.path)) return { body: request.path.includes("/codex/") ? { status: "succeeded", supported: true, data: null, error: "" } : null };
    if (request.path === `/workspaces/${workspace.id}/files`) return { body: { files: [], status: "succeeded", error: "", truncated: false } };
    if (/\/workspaces\/workspace\/(gemini|claude)\/models$/.test(request.path)) return { body: { configured: true, models: [{ id: "test-model", name: "Test model" }], default_model: "test-model" } };
    return undefined;
  } });
  let refresh = () => {};
  await page.routeWebSocket("**/api/ws", socket => {
    refresh = () => socket.send(JSON.stringify({ kind: "codex.turn.completed" }));
  });
  await page.goto("/?view=sessions");
  await expect(page.locator(".thread-project")).toHaveCount(1);
  await expect(page.locator(".session-engine")).toHaveText(["Codex", "Gemini", "Claude"]);
  // Simulate one HTTP read failing while the realtime connection stays healthy.
  await expect(page.locator(".connection")).toContainText("Live");
  let failReads = true;
  await page.route("**/api/sessions?**", async route => {
    if (failReads) await route.abort("failed");
    else await route.fallback();
  });
  refresh();
  await expect(page.locator(".refresh-warning")).toBeVisible();
  await expect(page.locator(".thread-select")).toHaveCount(3);
  await expect(page.locator(".thread-items .error-state")).toHaveCount(0);
  failReads = false;
  await page.screenshot({ path: testInfo.outputPath("session-refresh-warning.png") });
  await expect(page.locator(".refresh-warning")).toHaveCount(0);
  await expect(page.locator(".thread-select")).toHaveCount(3);
  const codexGroup = page.getByRole("button", { name: "Codex sessions", exact: true });
  await codexGroup.click();
  await expect(codexGroup).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator('.thread-select').filter({hasText: 'codex conversation'})).toHaveCount(0);
  await expect(page.locator('.thread-select').filter({hasText: 'gemini conversation'})).toBeVisible();
  await codexGroup.click();
  const visited = new Set<string>();
  for (const engine of ["codex", "gemini", "claude", "codex", "gemini", "claude"]) {
    if (testInfo.project.name === "mobile-chromium") {
      const tabs = page.locator(".codex-mobile-tabs:visible, .gemini-mobile-tabs:visible");
      await tabs.getByRole("button", { name: "Sessions", exact: true }).or(tabs.getByRole("tab", { name: "Sessions", exact: true })).click();
    }
    await page.locator(".thread-select").filter({ hasText: `${engine} conversation` }).click();
    await expect(page.locator(".session-header h2")).toHaveText(`${engine} conversation`);
    await expect(page).toHaveURL(new RegExp(`view=sessions&engine=${engine}&thread=shared-id`));
    await expect(page.locator(".thread.active")).toHaveCount(1);
    await expect(page.locator(".conversation-stream")).toBeVisible();
    await expect(page.locator(".session-collaboration")).toBeVisible();
    await expect.poll(async () => {
      const collaboration = await page.locator(".session-collaboration").boundingBox();
      const body = await page.locator(".session-panes").boundingBox();
      return Boolean(collaboration && body && collaboration.y + collaboration.height <= body.y + 1 && body.height > 100);
    }).toBe(true);
    const input = page.locator("form.composer textarea");
    const draft = `Unsent ${engine} draft`;
    if (visited.has(engine)) await expect(input).toHaveValue(draft);
    else { await expect(input).toHaveValue(""); await input.fill(draft); visited.add(engine); }
  }
  // A persisted collaboration result and its unread notification retain old URLs.
  await page.goto("/?view=codex&thread=shared-id");
  await expect(page).toHaveURL(/view=sessions&engine=codex&thread=shared-id/);
  await page.getByRole("button",{name:/Session collaboration/}).click();
  await expect(page.getByText("1 unread results")).toBeVisible();
  await page.getByRole("link",{name:"Historical result · claude"}).click();
  await expect(page).toHaveURL(/view=sessions&engine=claude&thread=shared-id/);
  await expect(page.locator(".session-header h2")).toHaveText("claude conversation");
  await page.goto("/?view=gemini");
  await expect(page).toHaveURL("/?view=sessions");
  await expect(page.getByLabel("Engine",{exact:true})).toHaveValue("gemini");
  await page.screenshot({ path: testInfo.outputPath("unified-sessions.png"), fullPage: true });
  expect(listRequests.some(search => new URLSearchParams(search).get("engine") === "gemini")).toBe(true);
});
