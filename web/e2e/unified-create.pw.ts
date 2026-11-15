import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication, type MockAPIRequest } from "./support/mock-api";

test.afterEach(({page}) => assertMockApplication(page));
test("unified dialog creates each engine and keeps workspace defaults separate", async ({page}, testInfo) => {
  const workspace = {id: "w", project_id: "p", server_id: "s", project_name: "Project", server_name: "Server", path: "/srv/project", status: "ready", management_mode: "managed"};
  const writes: MockAPIRequest[] = [];
  const sessions: Record<string, unknown>[] = [];
  await installMockApplication(page, {configured: true, onAPIRequest: request => {
    if (request.path === "/workspaces") return {body: [workspace, {...workspace, id: "pending", status: "cloning", path: "/srv/pending"}]};
    if (request.path === "/servers") return {body: [{id: "s", status: "online", claude_profile_model: "sonnet"}]};
    if (request.path.endsWith("/session-engines")) return {body: {workspace_id: "w", server_id: "s", engines: ["codex", "gemini", "claude"].map(engine => ({engine, available: true, reason: "", capabilities: {}, defaults: {model: engine === "claude" ? "sonnet" : "flash", mode: "default", effort: ""}}))}};
    if (request.path === "/sessions") return {body: {items: sessions, next_cursor: null, groups: sessions.length ? [{workspace_id: "w", matched_count: sessions.length, project_pinned_at: null}] : []}};
    if (/^\/sessions\/(codex|gemini|claude)$/.test(request.path) && request.method === "POST") {
      const engine = request.path.split("/")[2];
      writes.push(request);
      const thread = {...workspace, id: engine, workspace_id: "w", title: `${engine} new`, engine, status: "idle", native_id: "native", codex_thread_id: "native", session_id: "native", model: engine === "claude" ? "sonnet" : "flash", permission_mode: "default", approval_mode: "default", server_status: "online", archived_at: null, created_at: "2026-11-15T00:00:00Z", updated_at: "2026-11-15T00:00:00Z", capabilities: {}};
      sessions.push(thread); return {body: thread};
    }
    const thread = sessions.find(item => request.path === `/sessions/${item.engine}/${item.id}`);
    if (thread) return {body: thread};
    if (request.path.endsWith("/events")) return {body: []};
    if (request.path.endsWith("/goal")) return {body: request.path.includes("/codex/") ? {status: "succeeded", supported: true, data: null, error: ""} : null};
    if (request.path === "/workspaces/w/files") return {body: {files: [], status: "succeeded", error: "", truncated: false}};
    if (request.path === "/workspaces/w/codex/skills") return {body: {status: "succeeded", supported: true, data: [], error: ""}};
    if (request.path.endsWith("/claude/models")) return {body: {configured: true, default_model: "sonnet", models: [{id: "sonnet", name: "sonnet"}]}};
    if (request.path.endsWith("/gemini/models")) return {body: {configured: true, default_model: "flash", models: [{id: "flash", name: "flash"}]}};
    return undefined;
  }});
  for (const engine of ["codex", "gemini", "claude"]) {
    await page.goto("/?view=sessions");
    await page.getByRole("button", {name: "New session", exact: true}).first().click();
    const dialog = page.getByRole("dialog", {name: "New session", exact: true});
    await expect(dialog.getByRole("option", {name: /pending/})).toHaveCount(0);
    await dialog.getByLabel("Workspace").selectOption("w");
    await expect(dialog.getByLabel("Engine")).toBeEnabled();
    await dialog.getByLabel("Engine").selectOption(engine);
    const submit = dialog.getByRole("button", {name: engine === "codex" ? "Create session" : `New ${engine === "claude" ? "Claude" : "Gemini"} session`, exact: true});
    await expect(submit).toBeEnabled();
    await page.screenshot({path: testInfo.outputPath(`create-${engine}.png`)});
    await submit.click();
    await expect(dialog).not.toBeVisible();
    await expect(page).toHaveURL(`/?view=sessions&engine=${engine}&thread=${engine}`);
    await expect(page.locator(".session-header h2")).toHaveText(`${engine} new`);
    expect(await page.evaluate(() => localStorage.getItem("wio_workspace_engine:w"))).toBe(engine);
    await page.locator("form.composer textarea").fill("/collab");
    await page.getByRole("option", {name: /\/collab/}).click();
    const collaboration = page.getByRole("dialog", {name: "Create collaboration session"});
    await expect(collaboration).toBeVisible();
    await expect(collaboration.getByLabel("Agent", {exact: true})).toHaveValue(engine);
    await collaboration.getByRole("button", {name: "Cancel", exact: true}).click();
    await expect(collaboration).not.toBeVisible();
  }
  expect(writes.map(request => request.path)).toEqual(["/sessions/codex", "/sessions/gemini", "/sessions/claude"]);
});
