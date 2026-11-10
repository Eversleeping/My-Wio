import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication, type MockAPIRequest } from "./support/mock-api";

const screenshot = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII=", "base64");

test.afterEach(({ page }) => assertMockApplication(page));

for (const engine of ["codex", "claude", "gemini"] as const) {
  test(`${engine} keeps mobile scrolling inside panes and attaches screenshots without changing desktop paste`, async ({ page }, testInfo) => {
    const mobile = testInfo.project.name === "mobile-chromium";
    const base = engine === "codex" ? "/threads" : `/${engine}/threads`;
    const workspace = { id: "workspace", project_id: "project", server_id: "server", server_name: "Server", project_name: "Project", path: "/srv/project", display_name: "Project", status: "ready", kind: "primary" };
    const thread = {
      id: "screenshot-thread", workspace_id: workspace.id, project_id: workspace.project_id,
      title: "Screenshot review", status: "idle", path: workspace.path, server_id: workspace.server_id,
      server_name: workspace.server_name, server_status: "online", project_name: workspace.project_name,
      model: engine === "claude" ? "sonnet" : engine === "gemini" ? "auto" : "gpt-5.6-sol",
      session_id: "native-session", codex_thread_id: "native-session", permission_mode: "default", approval_mode: "default",
      input_supported: true, created_at: "2026-11-10T00:00:00Z", updated_at: "2026-11-10T00:00:00Z", archived_at: null,
    };
    const events = Array.from({ length: 30 }, (_, index) => ({
      event_id: `message-${index}`, stream_id: thread.id, sequence: index + 1, occurred_at: thread.updated_at,
      kind: engine === "codex" ? "user.message" : `${engine}.user.message`,
      payload: { text: `Screenshot review ${index + 1}\n${"Review the mobile layout and image attachment. ".repeat(8)}` },
    }));
    const turns: MockAPIRequest[] = [];
    await installMockApplication(page, {
      configured: true,
      onAPIRequest: request => {
        if (request.path === "/workspaces") return { body: [workspace] };
        if (request.path === "/servers") return { body: [{ id: "server", name: "Server", status: "online" }] };
        if (request.path === base) return { body: engine === "codex" ? { items: request.search.includes("archived=true") ? [] : [thread], has_more: false, next: null } : [thread] };
        if (request.path === `${base}/${thread.id}`) return { body: thread };
        if (request.path === `${base}/${thread.id}/events`) return { body: events };
        if (request.path === `${base}/${thread.id}/goal`) return { body: engine === "codex" ? { status: "succeeded", supported: true, data: null, error: "" } : null };
        if (request.path === `/workspaces/${workspace.id}/files`) return { body: { files: [], status: "succeeded", error: "", truncated: false } };
        if (request.path === `/workspaces/${workspace.id}/${engine}/models`) return { body: engine === "codex"
          ? { status: "succeeded", supported: true, data: [{ id: thread.model, model: thread.model, displayName: "Test model", supportedReasoningEfforts: [] }], error: "" }
          : { configured: true, models: [{ id: thread.model, name: "Test model" }], default_model: thread.model } };
        if (request.path === `${base}/${thread.id}/turns` && request.method === "POST") { turns.push(request); return { body: { operation_id: "turn" } }; }
        return undefined;
      },
    });
    await page.goto(`/?view=${engine}&thread=${thread.id}`);
    await expect(page.locator(".session-header h2")).toHaveText(thread.title);
    const composer = page.locator("form.composer");
    const attach = composer.getByRole("button", { name: "Attach image", exact: true });
    const stream = page.locator(".conversation-stream");
    await expect.poll(() => stream.evaluate(element => element.scrollHeight > element.clientHeight)).toBe(true);

    if (mobile) {
      for (const viewport of [{ width: 320, height: 568 }, { width: 390, height: 844 }, { width: 430, height: 932 }, { width: 760, height: 600 }]) {
        await page.setViewportSize(viewport);
        await expect.poll(() => page.evaluate(() => ({
          vertical: document.documentElement.scrollHeight - window.innerHeight,
          horizontal: document.documentElement.scrollWidth - window.innerWidth,
        }))).toEqual({ vertical: 0, horizontal: 0 });
        await expect(composer.getByRole("button", { name: "Send", exact: true })).toBeInViewport();
        await expect(attach).toBeInViewport();
      }
      await page.setViewportSize({ width: 390, height: 844 });
      // A changing browser toolbar must not leave the shell at the larger vh height.
      await page.addStyleTag({ content: ".app-shell, .workspace { min-height: 960px; }" });
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollHeight - window.innerHeight)).toBe(0);
      const chooseScreenshot = async () => {
        const chooser = page.waitForEvent("filechooser");
        await attach.click();
        await (await chooser).setFiles({ name: "screenshot.png", mimeType: "image/png", buffer: screenshot });
        await expect(composer.locator(".composer-images img")).toHaveCount(1);
      };
      await chooseScreenshot();
      await composer.locator(".composer-images button").click();
      await chooseScreenshot();
    } else {
      await expect(attach).toBeHidden();
      await expect(page.locator(".codex-sidebar")).toBeVisible();
      await expect.poll(() => page.locator(".codex-layout").evaluate(element => getComputedStyle(element).gridTemplateColumns.split(" ").length)).toBe(2);
      // Desktop still accepts an image from the clipboard without opening a picker.
      await composer.locator("textarea").evaluate((element, bytes) => {
        const clipboardData = new DataTransfer();
        clipboardData.items.add(new File([new Uint8Array(bytes)], "screenshot.png", { type: "image/png" }));
        element.dispatchEvent(new ClipboardEvent("paste", { clipboardData, bubbles: true, cancelable: true }));
      }, Array.from(screenshot));
      await expect(composer.locator(".composer-images img")).toHaveCount(1);
    }
    await composer.getByRole("button", { name: "Send", exact: true }).click();
    await expect.poll(() => turns.length).toBe(1);
    const body = JSON.parse(turns[0].body ?? "{}");
    expect(body.prompt).toBe("");
    expect(body.images).toHaveLength(1);
    expect(body.images[0].data_url).toMatch(/^data:image\/(webp|png|jpeg);base64,/);
    await expect(composer.locator(".composer-images img")).toHaveCount(0);
  });
}
