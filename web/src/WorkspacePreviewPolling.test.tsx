import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { FileDiffPane, FilePreviewPane } from "./App";
import { I18nProvider } from "./i18n";

function response(value: unknown) {
  return new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } });
}

function fileSnapshot(path: string, status: "idle" | "loading" | "succeeded" | "failed" = "loading", content = "") {
  return { workspace_id: "workspace-1", path, content, size: content.length, truncated: false, status, error: "", requested_at: null, updated_at: null };
}

function diffSnapshot(path: string, status: "idle" | "loading" | "succeeded" | "failed" = "loading", overrides: Partial<{ content: string; additions: number; deletions: number; binary: boolean; truncated: boolean; error: string }> = {}) {
  return { workspace_id: "workspace-1", path, content: "", additions: 0, deletions: 0, binary: false, truncated: false, status, error: "", requested_at: null, updated_at: null, ...overrides };
}

function filePreview(path = "notes.txt", realtime = 0) {
  return <I18nProvider><FilePreviewPane workspaceID="workspace-1" selection={{ path, mode: "file" }} realtime={realtime} onClose={vi.fn()} /></I18nProvider>;
}

function fileDiff(path = "src/file.ts", realtime = 0) {
  return <I18nProvider><FileDiffPane workspaceID="workspace-1" selection={{ path, mode: "diff" }} realtime={realtime} writable notify={vi.fn()} onClose={vi.fn()} /></I18nProvider>;
}

function requests(method: string, resource: string) {
  return vi.mocked(fetch).mock.calls.filter(([input, init]) => String(input).includes(resource) && (init?.method ?? "GET") === method);
}

async function settle() {
  await act(async () => {
    for (let index = 0; index < 8; index += 1) await Promise.resolve();
  });
}

let restoreDocumentHidden = () => {};

afterEach(() => {
  restoreDocumentHidden();
  restoreDocumentHidden = () => {};
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.localStorage.clear();
});

test("backs off preview confirmations instead of polling at a fixed interval", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.useFakeTimers();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    return response(method === "POST" ? { operation_id: "preview-1" } : fileSnapshot("notes.txt"));
  }));

  const { unmount } = render(filePreview());
  await settle();
  expect(requests("POST", "/file-preview")).toHaveLength(1);
  expect(requests("GET", "/file-preview")).toHaveLength(1);

  await act(async () => { await vi.advanceTimersByTimeAsync(749); });
  expect(requests("GET", "/file-preview")).toHaveLength(1);
  await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(requests("GET", "/file-preview")).toHaveLength(2);

  await act(async () => { await vi.advanceTimersByTimeAsync(1_499); });
  expect(requests("GET", "/file-preview")).toHaveLength(2);
  await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(requests("GET", "/file-preview")).toHaveLength(3);

  await act(async () => { await vi.advanceTimersByTimeAsync(2_999); });
  expect(requests("GET", "/file-preview")).toHaveLength(3);
  await act(async () => { await vi.advanceTimersByTimeAsync(1); });
  expect(requests("GET", "/file-preview")).toHaveLength(4);

  unmount();
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(requests("GET", "/file-preview")).toHaveLength(4);
});

test("pauses confirmation while hidden and confirms immediately when visible", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.useFakeTimers();
  let hidden = false;
  const previousDescriptor = Object.getOwnPropertyDescriptor(document, "hidden");
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  restoreDocumentHidden = () => {
    if (previousDescriptor) Object.defineProperty(document, "hidden", previousDescriptor);
    else delete (document as { hidden?: boolean }).hidden;
  };
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => response((init?.method ?? "GET") === "POST" ? { operation_id: "preview-1" } : fileSnapshot("notes.txt"))));

  render(filePreview());
  await settle();
  expect(requests("GET", "/file-preview")).toHaveLength(1);

  hidden = true;
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(requests("GET", "/file-preview")).toHaveLength(1);

  hidden = false;
  await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
  await settle();
  expect(requests("GET", "/file-preview")).toHaveLength(2);
});

test("stops diff confirmation after a terminal result", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.useFakeTimers();
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => response((init?.method ?? "GET") === "POST" ? { operation_id: "preview-1" } : diffSnapshot("src/file.ts", "succeeded", { binary: true }))));

  render(fileDiff());
  await settle();
  expect(screen.getByText("Binary file changes cannot be displayed")).toBeInTheDocument();
  expect(requests("GET", "/diff-preview")).toHaveLength(1);

  await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
  expect(requests("GET", "/diff-preview")).toHaveLength(1);
});

test("ignores a cancelled file request after the selection changes", async () => {
  window.localStorage.setItem("wio_language", "en");
  let resolveFirstSnapshot: ((value: Response) => void) | undefined;
  const firstSnapshot = new Promise<Response>(resolve => { resolveFirstSnapshot = resolve; });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (method === "POST") return response({ operation_id: "preview-1" });
    if (url.includes("path=first.ts")) return firstSnapshot;
    return response(diffSnapshot("second.ts", "succeeded", { binary: true }));
  }));

  const { rerender } = render(fileDiff("first.ts"));
  await settle();
  rerender(fileDiff("second.ts"));
  await settle();
  expect(await screen.findByText("Binary file changes cannot be displayed")).toBeInTheDocument();

  resolveFirstSnapshot?.(response(diffSnapshot("first.ts", "succeeded", { content: "@@ -1 +1 @@\n-old\n+stale", additions: 1, deletions: 1 })));
  await settle();
  expect(screen.getByText("Binary file changes cannot be displayed")).toBeInTheDocument();
  expect(screen.queryByText("stale")).not.toBeInTheDocument();
});
