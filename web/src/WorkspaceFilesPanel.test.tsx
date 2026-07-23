import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ChangedFilesView, FileDiffPane, WorkspaceFilesPanel } from "./App";
import { I18nProvider } from "./i18n";
import type { WorkspaceChangesSnapshot, WorkspaceGitSnapshot } from "./types";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
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

const gitSnapshot: WorkspaceGitSnapshot = {
  workspace_id: "workspace-1",
  data: {
    workspace_id: "workspace-1",
    status: { branch: "main", detached: false, unborn: false, head: "abc123", upstream: "origin/main", ahead: 0, behind: 0, staged: 1, unstaged: 3, untracked: 1, dirty: true },
    changes: changesSnapshot.changes,
    branches: [],
    remotes: [{ name: "origin", fetch_urls: ["https://example.com/repo.git"], push_urls: ["https://example.com/repo.git"] }],
    commits: [],
    has_more: false
  },
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
    if (url.endsWith("/api/workspaces/workspace-1/git") && method === "GET") return jsonResponse(gitSnapshot);
    if (url.endsWith("/api/workspaces/workspace-1/git/refresh") && method === "POST") return jsonResponse({ operation_id: "operation-git" }, 202);
    return jsonResponse({ error: `Unexpected request: ${method} ${url}` }, 404);
  }));
  const user = userEvent.setup();
  const renderPanel = (taskStatus: string) => <I18nProvider><WorkspaceFilesPanel workspaceID="workspace-1" taskID="task-1" taskStatus={taskStatus} realtime={0} notify={vi.fn()} activePath="" activeMode="file" onOpenFile={vi.fn()} /></I18nProvider>;
  const { rerender } = render(renderPanel("running"));

  await user.click(screen.getByRole("button", { name: "Show file changes" }));
  await waitFor(() => expect(requests.filter(request => request.method === "POST" && request.url.endsWith("/changes/refresh"))).toHaveLength(1));
  expect(await screen.findByText("new.ts")).toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Show project files" }));
  await user.click(screen.getByRole("button", { name: "Show file changes" }));
  await waitFor(() => expect(requests.filter(request => request.method === "POST" && request.url.endsWith("/changes/refresh"))).toHaveLength(2));

  rerender(renderPanel("idle"));
  await waitFor(() => expect(requests.filter(request => request.method === "POST" && request.url.endsWith("/changes/refresh"))).toHaveLength(3));
});

test("keeps Git actions below the scrolling list and queues commit, pull, and push", async () => {
  window.localStorage.setItem("wio_language", "en");
  let currentChanges = changesSnapshot;
  const requests: Array<{ method: string; url: string; body: string }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = String(init?.body ?? "");
    requests.push({ method, url, body });
    if (url.endsWith("/api/workspaces/workspace-1/files")) return jsonResponse({ workspace_id: "workspace-1", files: [], truncated: false, status: "succeeded", error: "", requested_at: null, updated_at: null });
    if (url.endsWith("/api/workspaces/workspace-1/changes") && method === "GET") return jsonResponse(currentChanges);
    if (url.endsWith("/api/workspaces/workspace-1/changes/refresh") && method === "POST") return jsonResponse({ operation_id: "changes-refresh" }, 202);
    if (url.endsWith("/api/workspaces/workspace-1/git") && method === "GET") return jsonResponse({ ...gitSnapshot, data: { ...gitSnapshot.data, status: { ...gitSnapshot.data.status, dirty: currentChanges.changes.length > 0 } } });
    if (url.endsWith("/api/workspaces/workspace-1/git/refresh") && method === "POST") return jsonResponse({ operation_id: "git-refresh" }, 202);
    if (url.endsWith("/api/workspaces/workspace-1/git/commit") && method === "POST") {
      currentChanges = { ...changesSnapshot, changes: [] };
      return jsonResponse({ operation_id: "commit-operation" }, 202);
    }
    if (url.endsWith("/api/workspaces/workspace-1/git/pull") && method === "POST") return jsonResponse({ operation_id: "pull-operation" }, 202);
    if (url.endsWith("/api/workspaces/workspace-1/git/push") && method === "POST") return jsonResponse({ operation_id: "push-operation" }, 202);
    return jsonResponse({ error: `Unexpected request: ${method} ${url}` }, 404);
  }));
  const user = userEvent.setup();
  const { container } = render(<I18nProvider><WorkspaceFilesPanel workspaceID="workspace-1" taskID="task-1" taskStatus="idle" realtime={0} notify={vi.fn()} writable activePath="" activeMode="file" onOpenFile={vi.fn()} /></I18nProvider>);

  await user.click(screen.getByRole("button", { name: "Show file changes" }));
  expect(await screen.findByText("new.ts")).toBeInTheDocument();
  const list = container.querySelector(".workspace-file-body");
  const actions = container.querySelector(".workspace-change-actions");
  expect(list?.nextElementSibling).toBe(actions);

  await user.type(screen.getByRole("textbox", { name: "Commit message" }), "sidebar commit");
  await user.click(screen.getByRole("button", { name: "Commit" }));
  await waitFor(() => expect(requests.some(request => request.url.endsWith("/git/commit") && request.body === JSON.stringify({ message: "sidebar commit", all: true }))).toBe(true));
  expect(await screen.findByText("No file changes")).toBeInTheDocument();

  await waitFor(() => expect(screen.getByRole("button", { name: "Pull" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Pull" }));
  await waitFor(() => expect(requests.some(request => request.url.endsWith("/git/pull") && request.body === JSON.stringify({ remote: "origin", branch: "main" }))).toBe(true));
  await user.click(screen.getByRole("button", { name: "Push" }));
  await waitFor(() => expect(requests.some(request => request.url.endsWith("/git/push") && request.body === JSON.stringify({ remote: "origin", ref: "main", set_upstream: false }))).toBe(true));
});

test("discards the reviewed file including staged changes", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.spyOn(window, "confirm").mockReturnValue(true);
  const requests: Array<{ method: string; url: string; body: string }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const body = String(init?.body ?? "");
    requests.push({ method, url, body });
    if (url.includes("/api/workspaces/workspace-1/diff-preview") && method === "GET") return jsonResponse({ workspace_id: "workspace-1", path: "src/modified.ts", content: "@@ -1 +1 @@\n-before\n+after\n", additions: 1, deletions: 1, binary: false, truncated: false, status: "succeeded", error: "", requested_at: null, updated_at: "2026-07-22T12:00:00Z" });
    if (url.endsWith("/api/workspaces/workspace-1/diff-preview") && method === "POST") return jsonResponse({ operation_id: "preview-operation" }, 202);
    if (url.endsWith("/api/workspaces/workspace-1/git/discard") && method === "POST") return jsonResponse({ operation_id: "discard-operation" }, 202);
    return jsonResponse({ error: `Unexpected request: ${method} ${url}` }, 404);
  }));
  const onClose = vi.fn();
  render(<I18nProvider><FileDiffPane workspaceID="workspace-1" selection={{ path: "src/modified.ts", mode: "diff" }} realtime={0} writable notify={vi.fn()} onClose={onClose} /></I18nProvider>);

  await userEvent.click(await screen.findByRole("button", { name: "Discard file changes" }));
  await waitFor(() => expect(requests.some(request => request.url.endsWith("/git/discard") && request.body === JSON.stringify({ paths: ["src/modified.ts"], all: false, include_staged: true }))).toBe(true));
  expect(onClose).toHaveBeenCalledTimes(1);
});
