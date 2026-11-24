import { expect, test, type Locator, type WebSocketRoute } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

const occurredAt = "2026-11-24T13:02:03Z";
const timestamp = "11/24/2026, 01:02:03 PM";

test.use({ timezoneId: "UTC" });
test.afterEach(({ page }) => assertMockApplication(page));

async function topInStream(message: Locator) {
  return message.evaluate(element => element.getBoundingClientRect().top - element.closest(".conversation-stream")!.getBoundingClientRect().top);
}

for (const engine of ["codex", "claude", "gemini"] as const) {
  test(`${engine} preserves the reading position as new and older messages arrive`, async ({ page }) => {
    const workspace = { id: "workspace", project_id: "project", server_id: "server", server_name: "Server", project_name: "Project", path: "/srv/project", display_name: "Project", status: "ready", kind: "primary" };
    const thread = {
      id: "history", engine, title: "Session history", native_id: "native", session_id: "native",
      codex_thread_id: "native", workspace_id: workspace.id, project_id: workspace.project_id,
      path: workspace.path, project_name: workspace.project_name, server_id: workspace.server_id,
      server_name: workspace.server_name, server_status: "online", model: "", status: "idle",
      permission_mode: "default", approval_mode: "default", created_at: occurredAt,
      updated_at: occurredAt, archived_at: null, capabilities: {},
    };
    const sessionPath = `/sessions/${engine}/${thread.id}`;
    const messageKind = engine === "codex" ? "user.message" : `${engine}.user.message`;
    const event = (sequence: number, kind: string, payload: unknown) => ({
      event_id: `event-${sequence}`, stream_id: thread.id, sequence, occurred_at: occurredAt, kind, payload,
    });
    const message = (sequence: number) => event(sequence, messageKind, {
      text: `Message ${sequence}\n\n${"Read the conversation history. ".repeat(sequence % 2 ? 3 : 24)}`,
    });
    const command = {
      codex: event(503, "codex.item.completed", { item: { type: "commandExecution", command: "echo timestamp" } }),
      claude: event(503, "claude.tool_use", { name: "Bash", input: { command: "echo timestamp" } }),
      gemini: event(503, "gemini.tool_use", { tool_name: "run_shell_command", parameters: { command: "echo timestamp" } }),
    }[engine];
    const older = Array.from({ length: 5 }, (_, index) => message(index + 1));
    const recent = Array.from({ length: 500 }, (_, index) => {
      const sequence = index + 6;
      if (sequence === command.sequence) return command;
      return sequence === 6 || sequence >= 490 ? message(sequence) : event(sequence, "ignored", {});
    });
    await installMockApplication(page, {
      configured: true,
      onAPIRequest: request => {
        if (request.path === "/workspaces") return { body: [workspace] };
        if (request.path === "/servers") return { body: [{ id: "server", name: "Server", status: "online" }] };
        if (request.path === "/sessions") return { body: { items: [thread], next_cursor: null, groups: [] } };
        if (request.path === sessionPath) return { body: thread };
        if (request.path === `${sessionPath}/events`) {
          const params = new URLSearchParams(request.search);
          if (params.has("before")) return { body: older };
          const after = Number(params.get("after"));
          return { body: recent.filter(item => item.sequence > after) };
        }
        if (request.path === `${sessionPath}/goal`) return { body: engine === "codex" ? { status: "succeeded", supported: true, data: null } : null };
        if (request.path === `/workspaces/${workspace.id}/files`) return { body: { files: [], status: "succeeded" } };
        if (request.path === `/workspaces/${workspace.id}/${engine}/models` && engine !== "codex") return { body: { configured: true, models: [] } };
      },
    });
    let socket: WebSocketRoute | undefined;
    await page.routeWebSocket("**/api/ws", connection => { socket = connection; });

    await page.goto(`/?view=sessions&engine=${engine}&thread=${thread.id}`);
    const stream = page.locator(".conversation-stream");
    const last = stream.locator(".message").filter({ hasText: "Message 505" });
    await expect(last.locator("time")).toHaveText(timestamp);
    await expect(last.getByText("Message 505", { exact: true })).toBeAttached();
    const gap = () => stream.evaluate(element => element.scrollHeight - element.clientHeight - element.scrollTop);
    await stream.evaluate(element => { element.scrollTop = element.scrollHeight - element.clientHeight - 48; });
    await expect.poll(gap).toBeGreaterThan(40);
    const lastTop = await topInStream(last);

    const appended = message(506);
    recent.push(appended);
    socket!.send(JSON.stringify(appended));
    await expect(stream.getByText("Message 506", { exact: true })).toBeAttached();
    await expect.poll(async () => Math.abs(await topInStream(last) - lastTop)).toBeLessThanOrEqual(2);

    if (engine !== "codex") await stream.locator(".command-event-group > summary").click();
    await expect(stream.locator(engine === "codex" ? ".tool-event time" : ".command-event-group time")).toHaveText(timestamp);

    await stream.evaluate(element => { element.scrollTop = 0; });
    const earlierButton = stream.getByRole("button", { name: "Load earlier messages" });
    const first = stream.locator(".message").filter({ hasText: "Message 6" });
    await expect(first).toBeInViewport();
    const firstTop = await topInStream(first);
    await earlierButton.click();
    await expect(earlierButton).toHaveCount(0);
    await expect(stream.getByText("Message 5", { exact: true })).toBeAttached();
    await expect.poll(async () => Math.abs(await topInStream(first) - firstTop)).toBeLessThanOrEqual(2);
  });
}
