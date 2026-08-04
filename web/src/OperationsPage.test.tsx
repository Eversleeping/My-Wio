import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import OperationsPage from "./pages/OperationsPage";
import { I18nProvider } from "./i18n";

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

test("shows recent operations, prioritizes active work, and filters by status", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests: string[] = [];
  const active = { id: "operation-active", server_id: "server-1", server_name: "server-1", project_id: "project-1", project_name: "project-management", workspace_id: "workspace-1", workspace_path: "/srv/project", workspace_name: "project", resource_type: "workspace", resource_id: "workspace-1", kind: "deploy.start", status: "running", result: "Starting Compose", created_at: "2026-07-21T10:00:00Z", updated_at: "2026-07-21T10:00:08Z", delivered_at: "2026-07-21T10:00:01Z", started_at: "2026-07-21T10:00:02Z", completed_at: null };
  const completed = { ...active, id: "operation-complete", kind: "git.status", status: "succeeded", result: "status refreshed", updated_at: "2026-07-21T09:59:08Z", completed_at: "2026-07-21T09:59:08Z" };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    requests.push(url);
    const payload = url.includes("status=running") ? [active] : [active, completed];
    return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  const user = userEvent.setup();
  render(<I18nProvider><OperationsPage realtime={0} /></I18nProvider>);
  expect(await screen.findByText("Agent operations")).toBeInTheDocument();
  expect(await screen.findByText("Starting Compose")).toBeInTheDocument();
  expect(screen.getAllByRole("option", { name: "Canceled" })).toHaveLength(1);
  expect(screen.getByRole("option", { name: "Superseded" })).toBeInTheDocument();
  expect(screen.getAllByRole("link", { name: /project/ }).filter(link => link.getAttribute("href") === "?view=deployments")).toHaveLength(1);
  await user.selectOptions(screen.getByRole("combobox", { name: "Filter by status" }), "running");
  await waitFor(() => expect(requests).toContain("/api/operations?status=running&limit=200"));
});

test("links operations to their actual resource type", async () => {
  window.localStorage.setItem("wio_language", "en");
  const base = { id: "operation", server_id: "server-1", server_name: "server-1", project_id: "", project_name: "", workspace_id: "", workspace_path: "", workspace_name: "", thread_id: "", thread_title: "", resource_type: "server", resource_id: "server-1", kind: "inventory.scan", status: "succeeded", result: "done", created_at: "2026-07-21T10:00:00Z", updated_at: "2026-07-21T10:00:08Z", delivered_at: null, started_at: null, completed_at: "2026-07-21T10:00:08Z" };
  const operations = [
    base,
    { ...base, id: "project-operation", project_id: "project-1", project_name: "project-management", resource_type: "project", resource_id: "project-1", kind: "git.import" },
    { ...base, id: "workspace-operation", project_id: "project-1", project_name: "project-management", workspace_id: "workspace-1", workspace_path: "/srv/project", workspace_name: "project-workspace", resource_type: "workspace", resource_id: "workspace-1", kind: "workspace.files" },
    { ...base, id: "thread-operation", project_id: "project-1", project_name: "project-management", workspace_id: "workspace-1", workspace_path: "/srv/project", thread_id: "thread-1", thread_title: "Investigate deployment", resource_type: "thread", resource_id: "thread-1", kind: "codex.turn.start" }
  ];
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(operations), { status: 200, headers: { "Content-Type": "application/json" } })));
  render(<I18nProvider><OperationsPage realtime={0} /></I18nProvider>);
  expect(await screen.findByRole("link", { name: /server-1/ })).toHaveAttribute("href", "?view=servers");
  expect(screen.getByRole("link", { name: /project-management/ })).toHaveAttribute("href", "?view=projects&project_name=project-management&project_server_id=server-1");
  expect(screen.getByRole("link", { name: /project-workspace/ })).toHaveAttribute("href", "?view=projects&workspace_path=%2Fsrv%2Fproject&workspace_server_id=server-1");
  expect(screen.getByRole("link", { name: /Investigate deployment/ })).toHaveAttribute("href", "?view=codex&thread=thread-1");
});
