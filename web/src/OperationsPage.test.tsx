import { render, screen, waitFor, within } from "@testing-library/react";
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

test("interrupts active scans, waits for confirmation, and permits retry after failure", async () => {
  window.localStorage.setItem("wio_language", "en");
  const active = { id: "stuck", server_id: "node", server_name: "node", kind: "git.workspace.inspect", status: "running", interrupt_supported: true, interrupt_status: "", interrupt_error: "", result: "", created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z" };
  const old = { ...active, id: "old", server_name: "old Agent", interrupt_supported: false };
  const complete = { ...active, id: "done", server_name: "done", status: "succeeded" };
  let postCount = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === "POST") {
      expect(String(input)).toBe("/api/operations/stuck/interrupt");
      postCount++;
      active.interrupt_status = postCount === 1 ? "running" : "failed";
      active.interrupt_error = postCount === 1 ? "" : "termination not confirmed";
      return new Response(JSON.stringify({ operation_id: "stop" }), { status: 202 });
    }
    return new Response(JSON.stringify([active, old, complete]), { status: 200 });
  }));
  const user = userEvent.setup();
  const { rerender } = render(<I18nProvider><OperationsPage realtime={0} /></I18nProvider>);
  const resource = await screen.findByRole("link", { name: "node" });
  const row = resource.closest("tr")!;
  expect(within(screen.getByRole("link", { name: "done" }).closest("tr")!).queryByRole("button")).not.toBeInTheDocument();
  expect(within(screen.getByRole("link", { name: "old Agent" }).closest("tr")!).getByRole("button", { name: "Interrupt" })).toBeDisabled();
  await user.click(within(row).getByRole("button", { name: "Interrupt" }));
  expect(await within(row).findByRole("button", { name: "Interrupting…" })).toBeDisabled();
  expect(within(row).getByText("Running")).toBeInTheDocument();
  expect(postCount).toBe(1);
  active.interrupt_status = "failed";
  active.interrupt_error = "termination not confirmed";
  rerender(<I18nProvider><OperationsPage realtime={1} /></I18nProvider>);
  await waitFor(() => expect(within(row).getByRole("button", { name: "Interrupt" })).toBeEnabled());
  expect(within(row).getByText(/termination not confirmed/)).toBeInTheDocument();
  await user.click(within(row).getByRole("button", { name: "Interrupt" }));
  expect(postCount).toBe(2);
});

test("shows a failed interruption request and leaves retry available", async () => {
  window.localStorage.setItem("wio_language", "en");
  const active = { id: "scan", server_name: "node", kind: "workspace.files", status: "running", interrupt_supported: true, created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z" };
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => init?.method === "POST"
    ? new Response(JSON.stringify({ error: "Connection unavailable" }), { status: 503 })
    : new Response(JSON.stringify([active]), { status: 200 })));
  render(<I18nProvider><OperationsPage realtime={0} /></I18nProvider>);
  await userEvent.click(await screen.findByRole("button", { name: "Interrupt" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Connection unavailable");
  await waitFor(() => expect(screen.getByRole("button", { name: "Interrupt" })).toBeEnabled());
});
