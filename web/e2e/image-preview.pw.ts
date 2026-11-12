import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(({ page }) => assertMockApplication(page));

for (const engine of ["codex", "claude", "gemini"] as const) {
  test(`${engine} previews project images and opens model screenshots in a dialog`, async ({ page }, testInfo) => {
    const png = await page.evaluate(() => {
      const canvas = document.createElement("canvas");
      canvas.width = 1600; canvas.height = 1200;
      const context = canvas.getContext("2d")!;
      context.fillStyle = "#0c223c"; context.fillRect(0, 0, 1600, 1200);
      context.fillStyle = "#22d3ee"; context.font = "64px sans-serif";
      context.fillText("Wio screenshot preview", 100, 150);
      return canvas.toDataURL("image/png").split(",")[1];
    });
    const imageSize = Buffer.from(png, "base64").length;
    const base = engine === "codex" ? "/threads" : `/${engine}/threads`;
    const workspace = { id: "workspace", project_id: "project", server_id: "server", server_name: "Server", project_name: "Project", path: "/srv/project", display_name: "Project", status: "ready", kind: "primary", management_mode: "managed" };
    const thread = {
      id: "images", workspace_id: workspace.id, project_id: workspace.project_id, title: "Image review", status: "idle",
      path: workspace.path, server_id: workspace.server_id, server_name: "Server", server_status: "online", project_name: "Project",
      model: engine === "claude" ? "sonnet" : engine === "gemini" ? "auto" : "gpt-5.6-sol", operation_id: "turn",
      session_id: "native", codex_thread_id: "native", permission_mode: "default", approval_mode: "default", input_supported: true,
      created_at: "2026-11-10T00:00:00Z", updated_at: "2026-11-10T00:00:00Z", archived_at: null,
    };
    const markdown = "Here is the screenshot: ![Generated screenshot](/srv/project/screenshot.png)";
    const native = engine === "claude" ? { type: "image", source: { type: "base64", media_type: "image/png", data: png } } : { type: "image", mimeType: "image/png", data: png };
    const event = (sequence: number, kind: string, payload: unknown) => ({ event_id: `event-${sequence}`, stream_id: thread.id, sequence, occurred_at: thread.updated_at, kind, payload });
    const events = engine === "codex" ? [
      event(1, "codex.item.completed", { item: { id: "screenshot", type: "mcpToolCall", result: { content: [native] } } }),
      event(2, "codex.item.completed", { item: { id: "reply", type: "agentMessage", text: markdown } }),
    ] : engine === "claude" ? [
      event(1, "claude.tool_use", { id: "shot", name: "Screenshot", input: {}, operation_id: "turn" }),
      event(2, "claude.tool_result", { id: "shot", content: [native], operation_id: "turn" }),
      event(3, "claude.message", { text: markdown, operation_id: "turn" }),
    ] : [
      event(1, "gemini.tool_use", { tool_id: "shot", tool_name: "Screenshot", parameters: {}, operation_id: "turn" }),
      event(2, "gemini.tool_result", { tool_id: "shot", output: [{ type: "content", content: native }], status: "completed", operation_id: "turn" }),
      event(3, "gemini.message", { role: "assistant", content: markdown, operation_id: "turn" }),
    ];
    let imageReads = 0;
    await installMockApplication(page, { configured: true, onAPIRequest: request => {
      if (request.path === "/workspaces") return { body: [workspace] };
      if (request.path === "/servers") return { body: [{ id: "server", name: "Server", status: "online" }] };
      if (request.path === base) return { body: engine === "codex" ? { items: request.search.includes("archived=true") ? [] : [thread], has_more: false, next: null } : [thread] };
      if (request.path === `${base}/${thread.id}`) return { body: thread };
      if (request.path === `${base}/${thread.id}/events`) return { body: events };
      if (request.path === `${base}/${thread.id}/goal`) return { body: { status: "succeeded", supported: true, data: null, error: "" } };
      if (request.path === "/workspaces/workspace/files") return { body: { workspace_id: workspace.id, files: [{ path: "screenshot.png", kind: "file", size: imageSize }], status: "succeeded", error: "", truncated: false } };
      if (request.path === "/workspaces/workspace/image-preview") { imageReads++; return { status: 202, body: { operation_id: "image" } }; }
      if (request.path === "/operations/image") return { body: { status: "succeeded", result_data: JSON.stringify({ path: "screenshot.png", content: png, encoding: "base64", mime_type: "image/png" }) } };
      if (request.path === "/workspaces/workspace/file-preview") return request.method === "POST" ? { status: 202, body: { operation_id: "file" } } : { body: { path: "screenshot.png", content: png, encoding: "base64", mime_type: "image/png", size: imageSize, status: "succeeded", truncated: false, error: "" } };
      if (request.path === `/workspaces/workspace/${engine}/models`) return { body: engine === "codex" ? { status: "succeeded", supported: true, data: [], error: "" } : { configured: true, models: [], default_model: thread.model } };
      return undefined;
    } });
    await page.goto(`/?view=${engine}&thread=${thread.id}`);
    await expect(async () => { await page.locator(".markdown-image").scrollIntoViewIfNeeded(); }).toPass();
    const thumbnail = page.getByRole("img", { name: "Generated screenshot" });
    await expect(thumbnail).toBeVisible();
    await expect.poll(() => thumbnail.evaluate((image: HTMLImageElement) => image.naturalWidth)).toBe(1600);
    expect(imageReads).toBe(1);
    await thumbnail.click();
    const dialog = page.getByRole("dialog", { name: "Generated screenshot" });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("img", { name: "Generated screenshot" })).toBeVisible();
    const viewport = dialog.locator(".image-viewer-viewport");
    await expect.poll(() => viewport.evaluate(element => element.scrollWidth <= element.clientWidth && element.scrollHeight <= element.clientHeight)).toBe(true);
    await dialog.getByRole("button", { name: "Original size" }).click();
    await expect.poll(() => viewport.evaluate(element => element.scrollWidth > element.clientWidth)).toBe(true);
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();

    // Native screenshot content uses the same click-to-open behavior.
    if (engine !== "codex") {
      await page.locator(".command-event-group > summary").click();
      await page.locator(".gemini-tool > summary").click();
    }
    const nativeImage = page.locator(".message-images img").first();
    await expect(nativeImage).toBeVisible();
    await expect.poll(() => nativeImage.evaluate((image: HTMLImageElement) => image.naturalWidth)).toBe(1600);
    await nativeImage.click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();

    if (testInfo.project.name === "mobile-chromium") await page.locator(".codex-mobile-tabs, .gemini-mobile-tabs").locator("button").nth(1).click();
    await page.locator(".file-tree").getByRole("button", { name: "screenshot.png" }).click();
    const panel = page.locator(".image-preview-panel");
    await expect(panel.getByRole("img", { name: "screenshot.png" })).toBeVisible();
    await expect(panel.getByRole("button", { name: "Edit file" })).toBeDisabled();
    await panel.getByRole("button", { name: "Original size" }).click();
    await expect.poll(() => panel.locator(".image-viewer-viewport").evaluate(element => element.scrollWidth > element.clientWidth)).toBe(true);
    await panel.getByRole("button", { name: "Fit to view" }).click();
    await expect.poll(() => panel.locator(".image-viewer-viewport").evaluate(element => element.scrollWidth <= element.clientWidth && element.scrollHeight <= element.clientHeight)).toBe(true);
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0);
  });
}
