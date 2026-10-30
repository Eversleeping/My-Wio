import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FilePreviewPane, WorkspaceFilesPanel } from "./App";
import { I18nProvider } from "./i18n";
import { clearFileDrafts } from "./workspaceFileEditing";
import { SessionPreviewPanels, SessionPreviewTabs, useSessionPreviews } from "./components/SessionPreviews";

const hash = "a".repeat(64);
let writes: { method: string; path: string; body: Record<string, unknown> }[];
let operationStatus: string;
let failedCheck: boolean;
let truncated: boolean;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
beforeEach(() => {
  clearFileDrafts(); window.localStorage.setItem("wio_language", "en");
  writes = []; operationStatus = "succeeded"; failedCheck = false; truncated = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input), method = init?.method ?? "GET";
    if (method === "PUT" || (method === "POST" && path.endsWith("/files"))) {
      writes.push({ method, path, body: JSON.parse(String(init?.body)) });
      return json({ operation_id: "write-op" }, 202);
    }
    if (path === "/api/operations/write-op") {
      if (failedCheck) { failedCheck = false; return json({ error: "Connection lost" }, 503); }
      return json({ status: operationStatus, result: operationStatus === "failed" ? "File changed since preview" : "" });
    }
    if (method === "POST" && path.endsWith("/file-preview")) return json({ operation_id: "preview" }, 202);
    if (path.includes("/file-preview?") && method === "GET") {
      const file = new URL(path, "http://localhost").searchParams.get("path");
      return json({ workspace_id: "workspace", path: file, content: "original\r\n", hash, size: 10, truncated, status: "succeeded", error: "" });
    }
    if (path === "/api/workspaces/workspace/files" && method === "GET") return json({ workspace_id: "workspace", files: [], status: "succeeded", truncated: false, error: "" });
    return json({ error: `Unexpected ${method} ${path}` }, 404);
  }));
});
afterEach(() => { clearFileDrafts(); vi.unstubAllGlobals(); window.localStorage.clear(); });
const preview = (path = "notes.txt", writable = true) => <I18nProvider><FilePreviewPane workspaceID="workspace" selection={{ path }} realtime={0} writable={writable} onClose={vi.fn()} /></I18nProvider>;
async function begin(user: ReturnType<typeof userEvent.setup>) {
  await waitFor(() => expect(screen.getByRole("button", { name: "Edit file" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Edit file" }));
}

test("edits and saves with the original hash, preserving CRLF", async () => {
  const user = userEvent.setup(); render(preview()); await begin(user);
  const input = screen.getByRole("textbox", { name: "File content" });
  await user.clear(input); await user.type(input, "changed\nnext");
  await user.click(screen.getByRole("button", { name: "Finish editing" }));
  expect(screen.getByRole("button", { name: "Discard changes" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(screen.queryByRole("textbox", { name: "File content" })).not.toBeInTheDocument());
  expect(writes).toEqual([{ method: "PUT", path: "/api/workspaces/workspace/file-content", body: { path: "notes.txt", content: "changed\r\nnext", expected_hash: hash } }]);
});

test("discards without writing and retains drafts across file switches", async () => {
  const user = userEvent.setup(); const view = render(preview()); await begin(user);
  await user.clear(screen.getByRole("textbox", { name: "File content" }));
  await user.type(screen.getByRole("textbox", { name: "File content" }), "draft");
  view.rerender(preview("other.txt"));
  view.rerender(preview());
  expect(screen.getByRole("textbox", { name: "File content" })).toHaveValue("draft");
  await user.click(screen.getByRole("button", { name: "Finish editing" }));
  await user.click(screen.getByRole("button", { name: "Discard changes" }));
  expect(writes).toHaveLength(0);
  expect(screen.queryByRole("textbox", { name: "File content" })).not.toBeInTheDocument();
});

test("preserves failed edits and checks a queued operation again without another write", async () => {
  const user = userEvent.setup(); render(preview()); await begin(user);
  operationStatus = "queued"; failedCheck = true;
  await user.click(screen.getByRole("button", { name: "Finish editing" }));
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Connection lost");
  expect(screen.getByRole("textbox", { name: "File content" })).toHaveValue("original\n");
  operationStatus = "succeeded";
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(writes).toHaveLength(1);
});

test("does not offer editing for read-only or truncated previews", async () => {
  const view = render(preview("readonly.txt", false));
  expect(await screen.findByRole("button", { name: "Edit file" })).toBeDisabled();
  truncated = true; view.rerender(preview("large.txt"));
  await waitFor(() => expect(screen.getByRole("button", { name: "Edit file" })).toBeDisabled());
});

test("adds a text file from the project file toolbar and opens it after completion", async () => {
  const user = userEvent.setup(), open = vi.fn();
  render(<I18nProvider><WorkspaceFilesPanel workspaceID="workspace" taskID="task" taskStatus="idle" realtime={0} notify={vi.fn()} writable activePath="" activeMode="file" onOpenFile={open} /></I18nProvider>);
  await user.click(screen.getByRole("button", { name: "Add file" }));
  const dialog = within(screen.getByRole("dialog"));
  await user.type(dialog.getByRole("textbox", { name: "File path in project" }), "docs/new.md");
  await user.type(dialog.getByRole("textbox", { name: "File content" }), "# New");
  await user.click(dialog.getByRole("button", { name: "Add file" }));
  await waitFor(() => expect(open).toHaveBeenCalledWith({ path: "docs/new.md", mode: "file" }));
  expect(writes[0]).toEqual({ method: "POST", path: "/api/workspaces/workspace/files", body: { path: "docs/new.md", content: "# New" } });
});

test("uploads local binary file bytes without modifying them", async () => {
  const user = userEvent.setup();
  render(<I18nProvider><WorkspaceFilesPanel workspaceID="workspace" taskID="task" taskStatus="idle" realtime={0} notify={vi.fn()} writable activePath="" activeMode="file" onOpenFile={vi.fn()} /></I18nProvider>);
  await user.click(screen.getByRole("button", { name: "Add file" }));
  const dialog = within(screen.getByRole("dialog"));
  const file = new File([new Uint8Array([0, 255, 1])], "asset.bin");
  Object.defineProperty(file, "arrayBuffer", { value: async () => new Uint8Array([0, 255, 1]).buffer });
  await user.upload(dialog.getByLabelText("Upload a local file (optional)"), file);
  await user.click(dialog.getByRole("button", { name: "Add file" }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0].body).toEqual({ path: "asset.bin", content_base64: "AP8B" });
});

function PreviewTabsHarness({ scope = "session-one", workspace = "workspace" }: { scope?: string; workspace?: string }) {
  const previews = useSessionPreviews(scope, workspace);
  return <I18nProvider>
    <button onClick={() => previews.open({ path: "a/same.txt", mode: "file" })}>Open first</button>
    <button onClick={() => previews.open({ path: "b/same.txt", mode: "file" })}>Open second</button>
    <button onClick={() => previews.open({ path: "a/same.txt", mode: "diff" })}>Open diff</button>
    <SessionPreviewTabs previews={previews} />
    <SessionPreviewPanels previews={previews} workspaceID={workspace} workspaceRoot="/project" writable realtime={0} notify={vi.fn()} onOpenFile={previews.open}><div>Conversation content</div></SessionPreviewPanels>
  </I18nProvider>;
}

test("keeps multiple distinct tabs and protects an unsaved draft when closing an inactive tab", async () => {
  const user = userEvent.setup();
  const view = render(<PreviewTabsHarness />);
  await user.click(screen.getByRole("button", { name: "Open first" }));
  await begin(user);
  await user.clear(screen.getByRole("textbox", { name: "File content" }));
  await user.type(screen.getByRole("textbox", { name: "File content" }), "unsaved draft");
  await user.click(screen.getByRole("button", { name: "Open second" }));
  await user.click(screen.getByRole("button", { name: "Open diff" }));
  await user.click(screen.getByRole("button", { name: "Open second" }));
  expect(screen.getAllByRole("tab")).toHaveLength(4);
  expect(screen.getAllByRole("tab").filter(tab => tab.title === "a/same.txt")).toHaveLength(2);
  await user.click(screen.getByRole("tab", { name: "Conversation" }));
  expect(screen.getByText("Conversation content")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Close a/same.txt" }));
  expect(screen.getByRole("dialog")).toHaveTextContent("a/same.txt");
  expect(screen.getByRole("textbox", { name: "File content" })).toHaveValue("unsaved draft");
  await user.click(screen.getByRole("button", { name: "Continue editing" }));
  expect(screen.getAllByRole("tab")).toHaveLength(4);
  await user.click(screen.getByRole("button", { name: "Close a/same.txt" }));
  await user.click(screen.getByRole("button", { name: "Discard changes" }));
  expect(screen.getAllByRole("tab")).toHaveLength(3);
  expect(screen.getByRole("button", { name: "Close b/same.txt" })).toBeInTheDocument();
  view.rerender(<PreviewTabsHarness scope="session-two" workspace="other-workspace" />);
  expect(screen.queryByRole("tab")).not.toBeInTheDocument();
});
