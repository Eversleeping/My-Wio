import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { CodexPage } from "./App";
import { I18nProvider } from "./i18n";
import type { Thread, Workspace } from "./types";

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

function thread(id: string, title: string, archived = false): Thread {
  return {
    id,
    workspace_id: `workspace-${id}`,
    project_id: "project-1",
    codex_thread_id: `codex-${id}`,
    title,
    status: "idle",
    path: `/srv/${id}`,
    server_id: "server-1",
    server_name: "Server",
    project_name: "Project",
    created_at: "2026-08-02T00:00:00Z",
    updated_at: "2026-08-02T00:00:00Z",
    pinned_at: null,
    archived_at: archived ? "2026-08-02T00:00:00Z" : null,
    project_pinned_at: null,
    project_hidden_at: null
  };
}

function workspace(value: Thread): Workspace {
  return {
    id: value.workspace_id,
    project_id: value.project_id,
    server_id: value.server_id,
    path: value.path,
    display_name: value.title,
    management_mode: "managed",
    status: "ready",
    branch: "main",
    commit_sha: "abcdef123456",
    dirty: 0,
    last_git_refresh_at: null,
    git_error: "",
    kind: "primary",
    parent_workspace_id: null,
    server_name: value.server_name,
    project_name: value.project_name
  };
}

function renderCodex(realtime: number, selectedThreadID = "", onSelectThread = vi.fn()) {
  return render(<I18nProvider><CodexPage realtime={realtime} streamRevisions={{}} approvals={[]} approvalSignal={0} reloadApprovals={vi.fn()} notify={vi.fn()} selectedThreadID={selectedThreadID} onSelectThread={onSelectThread} /></I18nProvider>);
}

function auxiliaryResponse(path: string, threads: Thread[]) {
  if (path === "/api/workspaces") return jsonResponse(threads.map(workspace));
  if (/\/api\/threads\/[^/]+\/events$/.test(path)) return jsonResponse([]);
  if (/\/api\/threads\/[^/]+\/goal$/.test(path)) return jsonResponse({ status: "idle", supported: true, data: {}, error: "", reason: "", updated_at: null });
  if (/\/api\/workspaces\/[^/]+\/files$/.test(path)) return jsonResponse({ workspace_id: path.split("/")[3], files: [], truncated: false, status: "succeeded", error: "", requested_at: null, updated_at: null });
  return null;
}

beforeEach(() => {
  window.localStorage.clear();
  window.localStorage.setItem("wio_language", "en");
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

test("loads more sessions and preserves the loaded tail during a realtime first-page refresh", async () => {
  const first = thread("first", "First page session");
  const second = thread("second", "Second page session");
  let firstTitle = first.title;
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = new URL(String(input), window.location.origin);
    if (url.pathname === "/api/threads" && url.search === "?limit=50") return jsonResponse({ items: [{ ...first, title: firstTitle }], has_more: true, next: 50 });
    if (url.pathname === "/api/threads" && url.search === "?limit=50&offset=50") return jsonResponse({ items: [second], has_more: false, next: null });
    if (url.pathname === "/api/threads" && url.search === "?archived=true&limit=50") return jsonResponse({ items: [], has_more: false, next: null });
    return auxiliaryResponse(url.pathname, [first, second]) ?? jsonResponse({ error: `unmocked ${url.pathname}${url.search}` }, 501);
  }));

  const { rerender } = renderCodex(0);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Load more sessions" }));
  expect(await screen.findAllByText(second.title)).not.toHaveLength(0);

  firstTitle = "Refreshed first page session";
  rerender(<I18nProvider><CodexPage realtime={1} streamRevisions={{}} approvals={[]} approvalSignal={0} reloadApprovals={vi.fn()} notify={vi.fn()} selectedThreadID="" onSelectThread={vi.fn()} /></I18nProvider>);
  expect(await screen.findAllByText(firstTitle)).not.toHaveLength(0);
  expect(screen.getAllByText(second.title)).not.toHaveLength(0);
});

test("keeps a deep-linked session selected when it is outside the first page", async () => {
  const first = thread("first", "First page session");
  const deep = thread("deep", "Deep linked session");
  const onSelectThread = vi.fn();
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = new URL(String(input), window.location.origin);
    if (url.pathname === "/api/threads" && url.search === "?limit=50") return jsonResponse({ items: [first], has_more: true, next: 50 });
    if (url.pathname === "/api/threads" && url.search === "?archived=true&limit=50") return jsonResponse({ items: [], has_more: false, next: null });
    if (url.pathname === `/api/threads/${deep.id}`) return jsonResponse(deep);
    return auxiliaryResponse(url.pathname, [first, deep]) ?? jsonResponse({ error: `unmocked ${url.pathname}${url.search}` }, 501);
  }));

  renderCodex(0, deep.id, onSelectThread);
  expect(await screen.findByRole("heading", { name: deep.title })).toBeVisible();
  await waitFor(() => expect(onSelectThread).not.toHaveBeenCalledWith(first.id, true));
});

test("revalidates loaded tail pages after a realtime refresh removes a session", async () => {
  const first = thread("first", "First page session");
  const second = thread("second", "Stale second page session");
  let secondVisible = true;
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = new URL(String(input), window.location.origin);
    if (url.pathname === "/api/threads" && url.search === "?limit=50") return jsonResponse({ items: [first], has_more: secondVisible, next: secondVisible ? 50 : null });
    if (url.pathname === "/api/threads" && url.search === "?limit=50&offset=50") return jsonResponse({ items: secondVisible ? [second] : [], has_more: false, next: null });
    if (url.pathname === "/api/threads" && url.search === "?archived=true&limit=50") return jsonResponse({ items: [], has_more: false, next: null });
    return auxiliaryResponse(url.pathname, [first, second]) ?? jsonResponse({ error: `unmocked ${url.pathname}${url.search}` }, 501);
  }));

  const { rerender } = renderCodex(0);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Load more sessions" }));
  expect(await screen.findAllByText(second.title)).not.toHaveLength(0);

  secondVisible = false;
  rerender(<I18nProvider><CodexPage realtime={1} streamRevisions={{}} approvals={[]} approvalSignal={0} reloadApprovals={vi.fn()} notify={vi.fn()} selectedThreadID="" onSelectThread={vi.fn()} /></I18nProvider>);
  await waitFor(() => expect(screen.queryByText(second.title)).not.toBeInTheDocument());
});

test("requires the accessible danger dialog before deleting a session", async () => {
  const value = thread("delete", "Delete protected session");
  let deleted = false;
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), window.location.origin);
    const method = init?.method ?? "GET";
    if (url.pathname === "/api/threads" && url.search === "?limit=50") return jsonResponse({ items: deleted ? [] : [value], has_more: false, next: null });
    if (url.pathname === "/api/threads" && url.search === "?archived=true&limit=50") return jsonResponse({ items: [], has_more: false, next: null });
    if (url.pathname === `/api/threads/${value.id}` && method === "DELETE") {
      requests.push(`${method} ${url.pathname}`);
      deleted = true;
      return jsonResponse({});
    }
    return auxiliaryResponse(url.pathname, [value]) ?? jsonResponse({ error: `unmocked ${method} ${url.pathname}${url.search}` }, 501);
  }));

  renderCodex(0);
  const user = userEvent.setup();
  await user.click(await screen.findByTitle("Delete session"));
  const dialog = screen.getByRole("dialog", { name: "Delete session" });
  expect(dialog).toHaveTextContent(`Delete session ${value.title}? This removes its conversation history.`);
  expect(requests).toHaveLength(0);

  await user.click(within(dialog).getByRole("button", { name: "Delete session" }));
  await waitFor(() => expect(requests).toEqual([`DELETE /api/threads/${value.id}`]));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Delete session" })).not.toBeInTheDocument());
});
