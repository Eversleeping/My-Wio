import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "../i18n";
import { SessionCollaboration } from "./SessionCollaboration";

afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

test("displays child provenance without per-session settings", async () => {
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
  expect(screen.queryByRole("button", { name: "Collaboration settings" })).not.toBeInTheDocument();
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
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
  await waitFor(() => expect(deleted).toEqual([expect.stringContaining("/sessions/gemini/done")]));
});

test("uses global display preferences supplied by the collaboration state", async () => {
 localStorage.setItem("wio_language", "en");
 const state = { supported: true, grant: { enabled: true, show_unread_results: true, show_creation_results: true }, notifications: [{target_key:"claude:child", event_id:"result"}], links: [], requests: [{id:"success", request_id:"create-success", tool:"wio_create_session", status:"succeeded", target_engine:"claude", target_id:"child"}] };
 vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(state))));
 const user = userEvent.setup();
 render(<I18nProvider><SessionCollaboration engine="codex" id="source" workspaceID="workspace" projectID="project" revision={0}/></I18nProvider>);
 await user.click(await screen.findByRole("button", {name:/Session collaboration/}));
 expect(screen.getByText("1 unread results")).toBeInTheDocument();
 expect(screen.getByText("create-success")).toBeInTheDocument();
 expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
});
