import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FileDiffPane, FilePreviewPane } from "./App";
import { I18nProvider } from "./i18n";

const content = "# Getting started\n\nA **formatted** paragraph.\n\n- [x] Ready\n\n| Name | Value |\n| --- | --- |\n| Demo | 1 |\n\n```sh\nnpm start\n```\n\n[Next](./guide.md) [Source](../src/main.ts#L12)\n";

beforeEach(() => {
  window.localStorage.setItem("wio_language", "en");
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const path = new URL(url, "http://localhost").searchParams.get("path");
    const body = init?.method === "POST" ? { operation_id: "preview" } : {
      workspace_id: "workspace", path, content: url.includes("diff-preview") ? "@@ -1 +1 @@\n-old\n+new\n" : content,
      size: content.length, hash: "a".repeat(64), status: "succeeded", error: "", truncated: false,
      additions: 1, deletions: 1, binary: false
    };
    return new Response(JSON.stringify(body), { status: init?.method === "POST" ? 202 : 200 });
  }));
});

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test("previews Markdown formatting and opens links relative to the document", async () => {
  const open = vi.fn();
  render(<I18nProvider><FilePreviewPane workspaceID="workspace" workspaceRoot="/repo" selection={{ path: "docs/README.MD" }} realtime={0} onOpenFile={open} onClose={vi.fn()} /></I18nProvider>);
  expect(await screen.findByRole("heading", { name: "Getting started" }, { timeout: 5000 })).toBeInTheDocument();
  expect(screen.getByText("formatted").tagName).toBe("STRONG");
  expect(screen.getByRole("table")).toHaveTextContent("Demo");
  expect(screen.getByRole("checkbox")).toBeChecked();
  expect(screen.getByText("npm start").closest("pre")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("link", { name: "Next" }));
  expect(open).toHaveBeenLastCalledWith({ path: "docs/guide.md", line: undefined });
  await userEvent.click(screen.getByRole("link", { name: "Source" }));
  expect(open).toHaveBeenLastCalledWith({ path: "src/main.ts", line: 12 });
});

test("switches between formatted Markdown and source without editing", async () => {
  render(<I18nProvider><FilePreviewPane workspaceID="workspace" selection={{ path: "README.md" }} realtime={0} onClose={vi.fn()} /></I18nProvider>);
  await screen.findByRole("heading", { name: "Getting started" }, { timeout: 5000 });
  await userEvent.click(screen.getByRole("button", { name: "View source" }));
  await waitFor(() => expect(document.querySelector(".file-code-line[data-line='1']")).toHaveTextContent("# Getting started"));
  expect(screen.queryByRole("heading", { name: "Getting started" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Preview Markdown" }));
  expect(await screen.findByRole("heading", { name: "Getting started" }, { timeout: 5000 })).toBeInTheDocument();
});

test("opens source for a line target, including a new target in the same document", async () => {
  const preview = (line?: number) => <I18nProvider><FilePreviewPane workspaceID="workspace" selection={{ path: "README.md", line }} realtime={0} onClose={vi.fn()} /></I18nProvider>;
  const view = render(preview(3));
  await waitFor(() => expect(document.querySelector(".file-code-line.target")).toHaveAttribute("data-line", "3"));
  await userEvent.click(screen.getByRole("button", { name: "Preview Markdown" }));
  await screen.findByRole("heading", { name: "Getting started" }, { timeout: 5000 });
  view.rerender(preview(5));
  await waitFor(() => expect(document.querySelector(".file-code-line.target")).toHaveAttribute("data-line", "5"));
});

test("offers formatted Markdown from its diff view", async () => {
  const open = vi.fn();
  render(<I18nProvider><FileDiffPane workspaceID="workspace" selection={{ path: "README.md", mode: "diff" }} realtime={0} writable={false} notify={vi.fn()} onOpenFile={open} onClose={vi.fn()} /></I18nProvider>);
  await userEvent.click(await screen.findByRole("button", { name: "Preview Markdown" }));
  expect(open).toHaveBeenCalledWith({ path: "README.md", mode: "file" });
});
