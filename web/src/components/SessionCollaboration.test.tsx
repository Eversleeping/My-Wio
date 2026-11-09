import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { SessionCollaboration } from "./SessionCollaboration";

afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

test("saves explicit collaboration scope and displays child provenance", async () => {
  localStorage.setItem("wio_language", "en");
  const saved: unknown[] = [];
  const state = { supported: true, grant: { enabled: false, workspace_ids: [], session_refs: [], worktree: false, auto_continue: false, delegate: false, target_modes: {} }, requests: [{ id: "r", request_id: "delegate-test", tool: "wio_create_session", status: "preparing", phase: "await_source", target_engine: "claude", target_id: "child" }, { id: "s", request_id: "send-test", tool: "wio_send_message", status: "failed", phase: "", target_engine: "claude", target_id: "child", error: { code: "BUSY", message: "session busy" } }], links: [{ source_key: "codex:source", target_key: "claude:child", relation: "created", missing: false, session: { engine: "claude", id: "child", title: "Test task", status: "running", waiting_user: true, workspace_id: "workspace", url: "/?view=claude&thread=child" } }] };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, options: RequestInit = {}) => {
    if (options.method === "PUT") { saved.push(JSON.parse(String(options.body))); return new Response(JSON.stringify(state)); }
    return new Response(JSON.stringify(String(input).includes("/collaboration") ? state : []));
  }));
  const user = userEvent.setup();
  render(<I18nProvider><SessionCollaboration engine="codex" id="source" workspaceID="workspace" projectID="project" revision={0} /></I18nProvider>);
  await user.click(await screen.findByRole("button", { name: /Session collaboration/ }));
  expect(screen.getByRole("link", { name: /Test task/ })).toHaveAttribute("href", "/?view=claude&thread=child");
  expect(screen.getByText("Waiting for the source turn to end")).toBeInTheDocument();
  expect(screen.getByText("delegate-test")).toBeInTheDocument();
  expect(screen.queryByText("send-test")).not.toBeInTheDocument();
  expect(screen.queryByText(/session busy/)).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Collaboration settings" }));
  await user.click(screen.getByRole("checkbox", { name: "Allow creating and managing child sessions" }));
  await user.click(screen.getByRole("checkbox", { name: "Allow creating separate worktrees" }));
  await user.click(screen.getByRole("checkbox", { name: "Allow managing other agents' existing sessions in this project" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "claude collaboration permissions" }), "default");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(saved).toEqual([expect.objectContaining({ enabled: true, all_project_sessions: true, session_refs: [], worktree: true, auto_continue: false, target_modes: { claude: "default" } })]));
});

test("hides deleted and archived children and deletes a child after confirmation", async () => {
  localStorage.setItem("wio_language", "en");
  const deleted: string[] = [];
  const session = (id: string, title: string, extra: object = {}) => ({ engine: "gemini", id, title, status: "completed", workspace_id: "workspace", url: `/?view=gemini&thread=${id}`, operation_id: "", recovery_pending: false, ...extra });
  const state = { supported: true, grant: { enabled: true, workspace_ids: [], session_refs: [], worktree: false, auto_continue: false, delegate: false, target_modes: {} }, requests: [], links: [
    { source_key: "codex:source", target_key: "gemini:done", relation: "created", missing: false, session: session("done", "Finished child") },
    { source_key: "codex:source", target_key: "gemini:busy", relation: "created", missing: false, session: session("busy", "Running child", { status: "running" }) },
    { source_key: "codex:source", target_key: "gemini:old", relation: "created", missing: false, session: session("old", "Archived child", { archived_at: "2026-11-01T00:00:00Z" }) },
    { source_key: "codex:source", target_key: "gemini:gone", relation: "created", missing: true, session: session("gone", "") }
  ] };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, options: RequestInit = {}) => {
    if (options.method === "DELETE") { deleted.push(String(input)); return new Response("{}"); }
    return new Response(JSON.stringify(String(input).includes("/collaboration") ? state : []));
  }));
  const user = userEvent.setup();
  render(<I18nProvider><SessionCollaboration engine="codex" id="source" workspaceID="workspace" projectID="project" revision={0} /></I18nProvider>);
  await user.click(await screen.findByRole("button", { name: /Session collaboration/ }));
  expect(screen.getByRole("link", { name: /Finished child/ })).toBeInTheDocument();
  expect(screen.queryByText(/Archived child/)).not.toBeInTheDocument();
  expect(screen.queryByText(/provenance retained/)).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Delete child session Running child" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "Delete child session Finished child" }));
  expect(deleted).toEqual([]);
  await user.click(screen.getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(deleted).toEqual([expect.stringContaining("/gemini/threads/done")]));
});
