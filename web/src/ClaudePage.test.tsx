import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import ClaudePage from "./pages/ClaudePage";
import { I18nProvider } from "./i18n";

const baseThread = { id: "claude-1", workspace_id: "workspace-1", project_id: "project-1", project_name: "Project", server_id: "server-1", server_name: "Server", server_status: "online", path: "/project", title: "Claude", status: "idle", operation_id: "op-1", session_id: "", model: "sonnet", permission_mode: "default", effort: "high", effort_supported: true, archived_at: null, recovery_pending: false, created_at: "2026-11-05T00:00:00Z", updated_at: "2026-11-05T00:00:00Z" };

function setup(options: { selected?: boolean; recovery?: boolean; status?: string; appliedModel?: string; effortSupported?: boolean } = {}) {
  window.localStorage.setItem("wio_language", "en");
  const thread = { ...baseThread, effort_supported: options.effortSupported ?? true, status: options.status ?? (options.recovery ? "interrupted" : "idle"), recovery_pending: Boolean(options.recovery) };
  const requests: Array<{ url: string; body: unknown }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input); let body: unknown = [];
    if (init.method === "POST") { requests.push({ url, body: JSON.parse(String(init.body)) }); body = url === "/api/claude/threads" ? thread : { ok: true }; }
    else if (url === "/api/servers") body = [{ id: "server-1", claude_profile_model: options.appliedModel || "" }];
    else if (url === "/api/workspaces") body = [{ id: "workspace-1", server_id: "server-1", project_name: "Project", server_name: "Server", path: "/project", status: "ready" }];
    else if (url.startsWith("/api/claude/threads?") && options.selected) body = [thread];
    else if (url === "/api/claude/threads/claude-1") body = thread;
    else if (url.startsWith("/api/claude/threads/claude-1/events")) body = [
      { event_id: "u", stream_id: thread.id, sequence: 1, kind: "claude.user.message", payload: { text: "Hello" } },
      { event_id: "a1", stream_id: thread.id, sequence: 2, kind: "claude.message", payload: { text: "Hi " } },
      { event_id: "a2", stream_id: thread.id, sequence: 3, kind: "claude.message", payload: { text: "there" } }
    ];
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  const onSelectThread = vi.fn();
  render(<I18nProvider><ClaudePage realtime={0} notify={vi.fn()} selectedThreadID={options.selected ? thread.id : ""} onSelectThread={onSelectThread} /></I18nProvider>);
  return { user: userEvent.setup(), requests, onSelectThread };
}

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); window.localStorage.clear(); });

test("creates a Claude session for a workspace with the selected permissions", async () => {
  const { user, requests, onSelectThread } = setup();
  await user.click((await screen.findAllByRole("button", { name: "New Claude session" }))[0]);
  await user.selectOptions(screen.getByLabelText("Workspace"), "workspace-1");
  await user.selectOptions(screen.getByLabelText("Tool permissions"), "acceptEdits");
  const createButtons = screen.getAllByRole("button", { name: "New Claude session" });
  await user.click(createButtons[createButtons.length - 1]);
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads", body: { workspace_id: "workspace-1", model: "sonnet", permission_mode: "acceptEdits", effort: "" } }));
  expect(onSelectThread).toHaveBeenCalledWith("claude-1");
});

test("renders streamed text and sends the next turn", async () => {
  const { user, requests } = setup({ selected: true });
  expect(await screen.findByText("Hi there")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Delete Claude session" }));
  expect(await screen.findByRole("dialog", { name: "Delete Claude session" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  const input = await screen.findByRole("textbox", { name: "Ask Claude to work on this project…" });
  expect(screen.getByLabelText("Reasoning effort")).toHaveValue("high");
  await user.selectOptions(screen.getByLabelText("Reasoning effort"), "max");
  await user.type(input, "Continue the task");
  await user.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads/claude-1/turns", body: { prompt: "Continue the task", model: "sonnet", permission_mode: "default", effort: "max" } }));
});

test("requires explicit confirmation before releasing a lost task", async () => {
  const { user, requests } = setup({ selected: true, recovery: true });
  expect(await screen.findByRole("textbox", { name: "Ask Claude to work on this project…" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "Confirm process stopped" }));
  expect(requests).toHaveLength(0);
  await user.click(screen.getAllByRole("button", { name: "Confirm process stopped" }).at(-1)!);
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads/claude-1/confirm-stopped", body: { operation_id: "op-1", confirmed: true } }));
});

test.each([
  ["running", "Claude is working…"],
  ["interrupting", "Interrupting Claude…"],
])("shows the Claude status while %s", async (status, expected) => {
  setup({ selected: true, status });
  expect(await screen.findByText(expected)).toBeInTheDocument();
});

test("fills every missing event page after a large realtime gap", async () => {
  window.localStorage.setItem("wio_language", "en");
  let jumped = false;
  const requests: string[] = [];
  const event = (sequence: number, kind = "claude.init", text = "") => ({ event_id: `event-${sequence}`, stream_id: baseThread.id, sequence, kind, payload: { text } });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input); requests.push(url);
    let body: unknown = [];
    if (url.startsWith("/api/claude/threads?") || url === "/api/claude/threads/claude-1") body = url.includes("?") ? [baseThread] : baseThread;
    else if (url.endsWith("/events?after=1")) body = Array.from({ length: 500 }, (_, index) => index === 248 ? event(250, "claude.message", "middle ") : event(index + 2));
    else if (url.endsWith("/events?after=501")) body = [event(502), event(503)];
    else if (url.endsWith("/events")) body = jumped ? [event(504), event(505, "claude.message", "end")] : [event(1, "claude.user.message", "Start")];
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  const view = (realtime: number) => <I18nProvider><ClaudePage realtime={realtime} notify={vi.fn()} selectedThreadID={baseThread.id} onSelectThread={vi.fn()} /></I18nProvider>;
  const rendered = render(view(0));
  expect(await screen.findByText("Start")).toBeInTheDocument();
  jumped = true;
  rendered.rerender(view(1));
  expect(await screen.findByText("middle end", {}, { timeout: 3000 })).toBeInTheDocument();
  expect(requests).toContain("/api/claude/threads/claude-1/events?after=1");
  expect(requests).toContain("/api/claude/threads/claude-1/events?after=501");
});

test("defaults new sessions to the model last applied to the selected server", async () => {
 const { user, requests } = setup({ selected: true, appliedModel: "claude-opus-4-6" });
 await user.click((await screen.findAllByRole("button", { name: "New Claude session" }))[0]);
 await user.selectOptions(screen.getByLabelText("Workspace"), "workspace-1");
 expect(within(screen.getByRole("dialog", { name: "New Claude session" })).getByLabelText("Claude model or alias")).toHaveValue("claude-opus-4-6");
 await user.click(screen.getAllByRole("button", { name: "New Claude session" }).at(-1)!);
 await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads", body: { workspace_id: "workspace-1", model: "claude-opus-4-6", permission_mode: "default", effort: "" } }));
});

test("groups sessions by workspace and supports collapsing, searching and the session menu", async () => {
  const { user } = setup({ selected: true });
  const group = await screen.findByRole("button", { name: /Project · project Server 1/ });
  expect(group).toHaveAttribute("aria-expanded", "true");
  await user.click(group);
  expect(screen.queryByRole("button", { name: "Claude Server" })).not.toBeInTheDocument();
  await user.click(group);
  expect(screen.getByRole("button", { name: "Claude Server" })).toBeInTheDocument();
  await user.type(screen.getByRole("searchbox"), "no-match");
  expect(screen.queryByRole("button", { name: "Claude Server" })).not.toBeInTheDocument();
  await user.clear(screen.getByRole("searchbox"));
  await user.click(screen.getByRole("button", { name: "Task menu for Claude" }));
  await user.click(screen.getByRole("menuitem", { name: "Rename session" }));
  expect(screen.getByRole("dialog", { name: "Rename session" })).toBeInTheDocument();
});

test("allows resetting a saved effort when an older Agent cannot apply it", async () => {
  const { user, requests } = setup({ selected: true, effortSupported: false });
  const effort = await screen.findByLabelText("Reasoning effort");
  expect(screen.getByRole("option", { name: "Max" })).toBeDisabled();
  await user.selectOptions(effort, "");
  await user.type(screen.getByRole("textbox", { name: "Ask Claude to work on this project…" }), "Use the default");
  await user.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads/claude-1/turns", body: { prompt: "Use the default", model: "sonnet", permission_mode: "default", effort: "" } }));
});
