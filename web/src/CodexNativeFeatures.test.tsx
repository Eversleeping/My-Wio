import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { SessionView } from "./App";
import { I18nProvider } from "./i18n";
import type { StreamEvent, Thread } from "./types";

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function thread(id: string): Thread {
  return {
    id,
    workspace_id: `workspace-${id}`,
    project_id: "project-1",
    codex_thread_id: `codex-${id}`,
    title: `Session ${id}`,
    status: "idle",
    path: `/srv/${id}`,
    server_id: "server-1",
    server_name: "Server",
    project_name: "Project",
    created_at: "2026-07-23T00:00:00Z",
    updated_at: "2026-07-23T00:00:00Z",
    pinned_at: null,
    archived_at: null,
    project_pinned_at: null,
    project_hidden_at: null
  };
}

function renderSession(value: Thread) {
  return render(<I18nProvider><SessionView thread={value} approvals={[]} realtime={0} reloadApprovals={vi.fn()} notify={vi.fn()} onOpenFile={vi.fn()} onNewTask={vi.fn()} /></I18nProvider>);
}

beforeEach(() => {
  window.localStorage.clear();
  window.localStorage.setItem("wio_language", "en");
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
  delete (HTMLElement.prototype as { scrollIntoView?: unknown }).scrollIntoView;
});

test("starts the first turn after a new goal is persisted", async () => {
  const user = userEvent.setup();
  const value = thread("goal-native");
  const requests: Array<{ url: string; method: string; body: Record<string, unknown> | null }> = [];
  let goalSaved = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = typeof init?.body === "string" ? JSON.parse(init.body) as Record<string, unknown> : null;
    requests.push({ url, method, body });
    if (url.includes(`/threads/${value.id}/events`)) return jsonResponse([]);
    if (url.includes(`/threads/${value.id}/goal`)) {
      if (method === "PUT") { goalSaved = true; return jsonResponse({ operation_id: "goal-op" }, 202); }
      if (method === "POST") return jsonResponse({ operation_id: "refresh-op" }, 202);
      return jsonResponse({ status: goalSaved ? "succeeded" : "idle", supported: true, reason: "", error: "", data: goalSaved ? { thread_id: value.codex_thread_id, objective: "Ship the release", status: "active", token_budget: null, tokens_used: 0, time_used_seconds: 0 } : {}, updated_at: null });
    }
    if (url.includes(`/threads/${value.id}/turns`) && method === "POST") return jsonResponse({ operation_id: "turn-op" }, 202);
    if (url.includes("/codex/skills")) return jsonResponse({ status: "succeeded", supported: true, data: [], error: "", reason: "", updated_at: null });
    return jsonResponse([]);
  }));

  renderSession(value);
  const composer = await screen.findByPlaceholderText("Message Codex");
  await user.type(composer, "/goal");
  await user.click(await screen.findByRole("option", { name: /\/goal/ }));
  const objective = await screen.findByLabelText("Objective");
  await user.type(objective, "Ship the release");
  const save = screen.getByRole("button", { name: "Save" });
  await waitFor(() => expect(save).toBeEnabled());
  await user.click(save);

  await waitFor(() => expect(requests.some(request => request.url.includes(`/threads/${value.id}/turns`) && request.method === "POST")).toBe(true));
  const goalIndex = requests.findIndex(request => request.url.endsWith(`/threads/${value.id}/goal`) && request.method === "PUT");
  const turnIndex = requests.findIndex(request => request.url.endsWith(`/threads/${value.id}/turns`) && request.method === "POST");
  expect(goalIndex).toBeGreaterThanOrEqual(0);
  expect(turnIndex).toBeGreaterThan(goalIndex);
  expect(requests[turnIndex].body?.prompt).toBe("Ship the release");
});

test("shows subagent activity above the composer and in the subagent dialog", async () => {
  const user = userEvent.setup();
  const value = thread("subagents-native");
  const events: StreamEvent[] = [{
    event_id: "event-1",
    stream_id: value.id,
    sequence: 1,
    kind: "codex.item.completed",
    occurred_at: "2026-07-23T10:00:00Z",
    payload: { item: { id: "collab-1", type: "collabToolCall", tool: "spawnAgent", status: "completed", senderThreadId: value.codex_thread_id, receiverThreadId: "sub-thread-1", agentStatus: { status: "running", message: "Inspecting tests" }, prompt: "Inspect the test suite", model: "gpt-5.6-terra", reasoningEffort: "high" } }
  }];
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = String(input);
    if (url.includes(`/threads/${value.id}/events`)) return jsonResponse(events);
    if (url.includes(`/threads/${value.id}/goal`)) return jsonResponse({ status: "idle", supported: true, data: {}, error: "", reason: "", updated_at: null });
    return jsonResponse([]);
  }));

  const { container } = renderSession(value);
  await waitFor(() => expect(container.querySelector(".subagent-progress-row")).not.toBeNull());
  const activity = container.querySelector<HTMLButtonElement>(".subagent-progress-row");
  expect(activity).toHaveTextContent("1 running");
  await user.click(activity!);
  const dialog = await screen.findByRole("dialog", { name: "Subagents" });
  expect(dialog).toHaveTextContent("sub-thread-1");
  expect(within(dialog).getByText("Inspect the test suite")).toBeInTheDocument();
});

test("queues context compaction from the slash command", async () => {
  const user = userEvent.setup();
  const value = thread("compact-native");
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if ((init?.method ?? "GET") === "POST") requests.push(url);
    if (url.includes(`/threads/${value.id}/events`)) return jsonResponse([]);
    if (url.includes(`/threads/${value.id}/goal`)) return jsonResponse({ status: "idle", supported: true, data: {}, error: "", reason: "", updated_at: null });
    if (url.includes(`/threads/${value.id}/compact`)) return jsonResponse({ operation_id: "compact-op" }, 202);
    if (url.includes("/codex/skills")) return jsonResponse({ status: "succeeded", supported: true, data: [], error: "", reason: "", updated_at: null });
    return jsonResponse([]);
  }));

  renderSession(value);
  const composer = await screen.findByPlaceholderText("Message Codex");
  await user.type(composer, "/compact");
  await user.click(await screen.findByRole("option", { name: /\/compact/ }));
  await waitFor(() => expect(requests.some(url => url.endsWith(`/threads/${value.id}/compact`))).toBe(true));
});
