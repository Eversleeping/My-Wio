import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ChangedFilesView, WorkspaceFilesPanel } from "./App";
import { I18nProvider } from "./i18n";
import type { WorkspaceChangesSnapshot } from "./types";

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

const changesSnapshot: WorkspaceChangesSnapshot = {
  workspace_id: "workspace-1",
  changes: [
    { path: "src/modified.ts", status: "modified", staged: false, unstaged: true },
    { path: "src/added.ts", status: "added", staged: true, unstaged: false },
    { path: "src/new.ts", status: "untracked", staged: false, unstaged: true },
    { path: "src/deleted.ts", status: "deleted", staged: false, unstaged: true }
  ],
  status: "succeeded",
  error: "",
  requested_at: null,
  updated_at: "2026-07-22T12:00:00Z"
};

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

test("shows modified, added, untracked, and deleted files", () => {
  window.localStorage.setItem("wio_language", "en");
  const { container } = render(<I18nProvider><ChangedFilesView workspaceID="workspace-1" snapshot={changesSnapshot} loading={false} activePath="" onOpenFile={vi.fn()} /></I18nProvider>);

  expect(screen.getByText("modified.ts")).toBeInTheDocument();
  expect(screen.getByText("added.ts")).toBeInTheDocument();
  expect(screen.getByText("new.ts")).toBeInTheDocument();
  expect(screen.getByText("deleted.ts")).toBeInTheDocument();
  expect(screen.getAllByText("Added")).toHaveLength(2);
  expect(screen.getByText("Deleted")).toBeInTheDocument();
  expect(container.querySelector(".change-file-status.untracked")).toHaveTextContent("?");
  expect(container.querySelector(".change-file-status.deleted")).toHaveTextContent("D");
});

test("rescans whenever changes are opened and after the active task finishes", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests: Array<{ method: string; url: string }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    requests.push({ method, url });
    if (url.endsWith("/api/workspaces/workspace-1/files")) {
      return jsonResponse({ workspace_id: "workspace-1", files: [], truncated: false, status: "succeeded", error: "", requested_at: null, updated_at: null });
    }
    if (url.endsWith("/api/workspaces/workspace-1/changes") && method === "GET") return jsonResponse(changesSnapshot);
    if (url.endsWith("/api/workspaces/workspace-1/changes/refresh") && method === "POST") return jsonResponse({ operation_id: "operation-1" }, 202);
    return jsonResponse({ error: `Unexpected request: ${method} ${url}` }, 404);
  }));
  const user = userEvent.setup();
  const renderPanel = (taskStatus: string) => <I18nProvider><WorkspaceFilesPanel workspaceID="workspace-1" taskID="task-1" taskStatus={taskStatus} realtime={0} notify={vi.fn()} activePath="" activeMode="file" onOpenFile={vi.fn()} /></I18nProvider>;
  const { rerender } = render(renderPanel("running"));

  await user.click(screen.getByRole("button", { name: "Show file changes" }));
  await waitFor(() => expect(requests.filter(request => request.method === "POST")).toHaveLength(1));
  expect(await screen.findByText("new.ts")).toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Show project files" }));
  await user.click(screen.getByRole("button", { name: "Show file changes" }));
  await waitFor(() => expect(requests.filter(request => request.method === "POST")).toHaveLength(2));

  rerender(renderPanel("idle"));
  await waitFor(() => expect(requests.filter(request => request.method === "POST")).toHaveLength(3));
});
