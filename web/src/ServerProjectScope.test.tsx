import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import CodexPage from "./pages/CodexPage";
import GeminiPage from "./pages/GeminiPage";
import { I18nProvider } from "./i18n";

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test.each(["codex", "gemini"])("%s pins same-repository projects independently by server", async kind => {
  window.localStorage.setItem("wio_language", "en");
  const now = "2026-10-28T00:00:00Z";
  let threads = ["a", "b"].map(id => ({ id: `thread-${id}`, workspace_id: `workspace-${id}`, project_id: "shared-project", server_id: `server-${id}`, server_name: `Server ${id.toUpperCase()}`, server_status: "online", project_name: "same", path: "/srv/same", title: `Task ${id}`, status: "idle", codex_thread_id: `native-${id}`, session_id: `native-${id}`, model: "flash", approval_mode: "default", operation_id: "", archived_at: null, pinned_at: null, project_pinned_at: null as string | null, project_hidden_at: null, created_at: now, updated_at: now, fork_supported: true, rewrite_supported: true }));
  const requests: { url: string; body: Record<string, unknown> }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), window.location.origin);
    let body: unknown = [];
    if (init.method === "PATCH") {
      const patch = JSON.parse(String(init.body));
      requests.push({ url: url.pathname, body: patch });
      threads = threads.map(thread => thread.server_id === patch.server_id ? { ...thread, project_pinned_at: patch.pinned ? now : null } : thread);
      body = {};
    } else if (url.pathname === "/api/threads") body = { items: url.searchParams.get("archived") === "true" ? [] : threads, has_more: false, next: null };
    else if (url.pathname === "/api/gemini/threads") body = url.searchParams.get("archived") === "true" ? [] : threads;
    else if (url.pathname === "/api/workspaces") body = threads.map(thread => ({ id: thread.workspace_id, project_id: thread.project_id, server_id: thread.server_id, server_name: thread.server_name, project_name: "same", path: "/srv/same", branch: "main", status: "ready", management_mode: "managed" }));
    else if (url.pathname.endsWith("/gemini/models")) body = { configured: true, models: [{ id: "flash", name: "flash" }] };
    else if (/\/threads\/thread-[ab]$/.test(url.pathname)) body = threads.find(thread => url.pathname.endsWith(thread.id));
    else if (url.pathname.includes("/workspaces/") || url.pathname.endsWith("/goal")) body = { status: "succeeded", data: {}, files: [], changes: [] };
    return new Response(JSON.stringify(body), { status: 200 });
  }));
  const shared = { realtime: 0, notify: vi.fn(), selectedThreadID: "", onSelectThread: vi.fn() };
  const { container } = render(<I18nProvider>{kind === "codex" ? <CodexPage {...shared} streamRevisions={{}} approvals={[]} approvalSignal={0} reloadApprovals={vi.fn()} /> : <GeminiPage {...shared} />}</I18nProvider>);
  await waitFor(() => expect(container.querySelectorAll(".thread-project")).toHaveLength(2));
  const group = (server: string) => Array.from(container.querySelectorAll<HTMLElement>(".thread-project")).find(item => item.querySelector(".thread-project-toggle small")?.textContent === server)!;
  const user = userEvent.setup();
  await user.click(within(group("Server A")).getByRole("button", { name: /Workspace menu/ }));
  await user.click(screen.getByRole("menuitem", { name: "Pin project" }));
  await waitFor(() => expect(group("Server A").querySelector(".pinned-icon")).toBeTruthy());
  expect(group("Server B").querySelector(".pinned-icon")).toBeNull();
  expect(requests).toContainEqual({ url: "/api/projects/shared-project", body: { pinned: true, server_id: "server-a" } });
  await user.click(within(group("Server A")).getByRole("button", { name: /Workspace menu/ }));
  await user.click(screen.getByRole("menuitem", { name: "Unpin project" }));
  await waitFor(() => expect(group("Server A").querySelector(".pinned-icon")).toBeNull());
  await user.click(within(group("Server B")).getByRole("button", { name: /Workspace menu/ }));
  await user.click(screen.getByRole("menuitem", { name: "Pin project" }));
  await waitFor(() => expect(group("Server B").querySelector(".pinned-icon")).toBeTruthy());
  expect(group("Server A").querySelector(".pinned-icon")).toBeNull();
});
