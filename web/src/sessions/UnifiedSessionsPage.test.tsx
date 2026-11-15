import { useState } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { DataProvider } from "../data/resources";
import { I18nProvider } from "../i18n";
import { locationFor, readLocationState } from "../navigation";
import type { SessionRef } from "./types";
import { readUnifiedSidebarHeight } from "./SessionPage";
import UnifiedSessionsPage from "./UnifiedSessionsPage";

vi.mock("./engines/codexUnified", () => ({ Conversation: () => <h2>Codex conversation</h2>, Create: () => null, Schedule: () => null }));
vi.mock("./engines/geminiUnified", () => ({ Conversation: () => <h2>Gemini conversation</h2>, Create: () => null, Schedule: () => null }));
vi.mock("./engines/claudeUnified", () => ({ Conversation: () => <h2>Claude conversation</h2>, Create: () => null, Schedule: () => null }));
vi.mock("../components/SessionCollaboration", () => ({ SessionCollaboration: () => null }));
vi.mock("../workspace/WorkspaceFilesPanel", () => ({ WorkspaceFilesPanel: () => null }));
const workspace = { id: "w", project_id: "p", project_name: "Project", server_id: "s", server_name: "Server", path: "/srv/project", display_name: "Feature", status: "ready", management_mode: "managed" };
const rows = ["codex", "gemini", "claude"].map((engine, index) => ({ id: "same", engine, native_id: `native-${engine}`, workspace_id: "w", project_id: "p", project_name: "Project", server_id: "s", server_name: "Server", server_status: "online", path: workspace.path, title: `${engine} title`, status: "idle", updated_at: `2026-11-15T00:00:0${index}Z`, archived_at: null, capabilities: {} }));
function setup(initial?: SessionRef) {
  localStorage.setItem("wio_language", "en");
  const requests: { path: string; method: string; body: unknown }[] = [];
  const data = rows.map(row => ({ ...row }));
  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit = {}) => {
    const parsed = new URL(url, "http://localhost"), path = parsed.pathname.replace(/^\/api/, "");
    requests.push({ path: path + parsed.search, method: init.method ?? "GET", body: init.body ? JSON.parse(String(init.body)) : null });
    if (init.method === "PATCH") { const item = data.find(row => path === `/sessions/${row.engine}/${row.id}`); if (item) Object.assign(item, JSON.parse(String(init.body))); }
    if (init.method === "DELETE") { const index = data.findIndex(row => path === `/sessions/${row.engine}/${row.id}`); if (index >= 0) data.splice(index, 1); }
    let body: unknown = {};
    if (path === "/workspaces") body = [workspace, { ...workspace, id: "empty", path: "/srv/empty", display_name: "Empty" }];
    else if (path === "/servers") body = [{ id: "s", status: "online" }];
    else if (path === "/sessions") { const query = parsed.searchParams; const items = data.filter(row => (!query.get("engine") || row.engine === query.get("engine")) && Boolean(row.archived_at) === (query.get("archived") === "true") && (!query.get("q") || row.title.includes(query.get("q")!))); body = { items, next_cursor: null, groups: items.length ? [{ workspace_id: "w", matched_count: items.length, project_pinned_at: null }] : [] }; }
    else if (/^\/sessions\/[^/]+\/same$/.test(path)) body = data.find(row => path === `/sessions/${row.engine}/${row.id}`);
    return new Response(JSON.stringify(body ?? {}), { status: 200 });
  }));
  function Harness({ revision = 0 }) {
    const [selected, select] = useState(initial);
    return <I18nProvider><DataProvider revisions={{ codex: revision }}><UnifiedSessionsPage realtime={0} streamRevisions={{}} approvals={[]} approvalSignal={0} reloadApprovals={() => undefined} notify={() => undefined} selected={selected} onSelect={ref => select(ref ?? undefined)} /></DataProvider></I18nProvider>;
  }
  const rendered = render(<Harness />);
  return { user: userEvent.setup(), requests, data, rendered, refresh: () => rendered.rerender(<Harness revision={1} />) };
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); localStorage.clear(); window.history.replaceState(null, "", "/"); });

test("opens three engines in one workspace, preserving identity for equal IDs", async () => {
  const { user } = setup();
  await screen.findByText("claude title");
  expect(document.querySelectorAll(".thread-project")).toHaveLength(1);
  for (const engine of ["codex", "gemini", "claude"]) {
    await user.click(screen.getByText(`${engine} title`).closest("button")!);
    await screen.findByText(`${engine[0].toUpperCase()}${engine.slice(1)} conversation`);
    expect(document.querySelectorAll(".thread.active")).toHaveLength(1);
    expect(document.querySelector(".thread.active")).toHaveTextContent(`${engine} title`);
  }
});

test("filters and searches on the server, stores engine locally and shows empty groups on request", async () => {
  const { user, requests } = setup();
  await screen.findByText("codex title");
  expect(screen.queryByText("Project · Empty")).not.toBeInTheDocument();
  await user.selectOptions(screen.getByLabelText("Engine"), "gemini");
  await waitFor(() => expect(screen.queryByText("codex title")).not.toBeInTheDocument());
  expect(localStorage.getItem("wio_sessions_engine")).toBe("gemini");
  expect(window.location.search).toBe("");
  await user.type(screen.getByRole("searchbox"), "gemini");
  await waitFor(() => expect(requests.some(r => r.path.includes("engine=gemini") && r.path.includes("q=gemini"))).toBe(true));
  await user.clear(screen.getByRole("searchbox"));
  await user.click(screen.getByLabelText("Show all workspaces"));
  await screen.findByText("Project · Empty");
});

test("pins the selected engine and keeps rename/restore/delete and project management in archives", async () => {
  const { user, requests, data, refresh } = setup();
  const title = await screen.findByText("gemini title");
  fireEvent.contextMenu(title);
  await user.click(await screen.findByRole("menuitem", { name: "Pin task" }));
  await waitFor(() => expect(requests).toContainEqual({ path: "/sessions/gemini/same", method: "PATCH", body: { pinned: true } }));
  data.forEach(row => { row.archived_at = "now" as never; });
  await user.click(screen.getByRole("button", { name: "Show archived sessions" }));
  const archived = await screen.findByText("claude title");
  fireEvent.contextMenu(archived);
  expect(await screen.findByRole("menuitem", { name: "Rename session" })).toBeEnabled();
  expect(screen.getByRole("menuitem", { name: "Restore session" })).toBeEnabled();
  await user.keyboard("{Escape}");
  fireEvent.contextMenu(screen.getByText("Project · Feature"));
  expect(await screen.findByRole("menuitem", { name: "Pin project" })).toBeEnabled();
  expect(screen.getByRole("menuitem", { name: "Rename project" })).toBeEnabled();
  expect(screen.getByRole("menuitem", { name: "Hide project" })).toBeEnabled();
  await user.keyboard("{Escape}");
  const row = screen.getByText("claude title").closest(".thread")!;
  await user.click(within(row as HTMLElement).getByRole("button", { name: "Delete session" }));
  expect(requests.some(r => r.method === "DELETE")).toBe(false);
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete session" }));
  await waitFor(() => expect(requests.some(r => r.method === "DELETE" && r.path === "/sessions/claude/same")).toBe(true));
});

test("refreshes the mixed list from a shared realtime revision", async () => {
  const { requests, refresh } = setup();
  await screen.findByText("codex title");
  const before = requests.filter(r => r.path.startsWith("/sessions?")).length;
  refresh();
  await waitFor(() => expect(requests.filter(r => r.path.startsWith("/sessions?")).length).toBeGreaterThan(before));
  expect(requests.filter(r => r.path.startsWith("/sessions?")).every(r => !r.path.includes("engine="))).toBe(true);
});

test("understands canonical and legacy URLs converts legacy pages", () => {
  window.history.replaceState(null, "", "/?view=sessions&engine=gemini&thread=a%2Fb");
  expect(readLocationState()).toEqual({ view: "sessions", engine: "gemini", threadID: "a/b" });
  expect(locationFor("sessions", "a/b", "gemini")).toBe("/?view=sessions&engine=gemini&thread=a%2Fb");
  window.history.replaceState(null, "", "/?view=claude&thread=old");
  expect(readLocationState()).toEqual({ view: "sessions", engine: "claude", threadID: "old" });
  window.history.replaceState(null, "", "/?view=sessions&engine=invalid&thread=bad");
  expect(readLocationState().threadID).toBe("");
});

test("uses only the shared height preference and ignores obsolete engine keys", () => {
  localStorage.setItem("wio_codex_sidebar_height", "29");
  localStorage.setItem("wio_gemini_sidebar_height", "64");
  expect(readUnifiedSidebarHeight()).toBe(38);
  localStorage.setItem("wio_sessions_sidebar_height", "99");
  expect(readUnifiedSidebarHeight()).toBe(80);
  localStorage.setItem("wio_sessions_sidebar_height", "invalid");
  expect(readUnifiedSidebarHeight()).toBe(38);
});

test("collapses a workspace without losing its selected conversation", async () => {
  const {user} = setup({engine:"gemini",id:"same"});
  await screen.findByText("Gemini conversation");
  const group = screen.getByRole("button",{name:/Project · Feature.*3/});
  await user.click(group);
  expect(group).toHaveAttribute("aria-expanded","false");
  expect(screen.queryByText("gemini title")).not.toBeInTheDocument();
  expect(screen.getByText("Gemini conversation")).toBeInTheDocument();
  await user.click(group);
  expect(screen.getByText("gemini title")).toBeInTheDocument();
});
