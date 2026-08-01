import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { SessionView } from "./App";
import { I18nProvider } from "./i18n";
import type { StreamEvent, Thread } from "./types";

function response(value: unknown) {
  return new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } });
}

function thread(id: string): Thread {
  return {
    id,
    workspace_id: `workspace-${id}`,
    project_id: "project-1",
    codex_thread_id: "",
    title: `Session ${id}`,
    status: "idle",
    path: `/srv/${id}`,
    server_id: "server-1",
    server_name: "Server",
    project_name: "Project",
    created_at: "2026-07-23T00:00:00Z",
    updated_at: "2026-07-23T00:00:00Z",
    pinned_at: null,
    archived_at: null,
    project_pinned_at: null,
    project_hidden_at: null
  };
}

function event(streamID: string, sequence: number, text: string, kind = "user.message"): StreamEvent {
  return { event_id: `${streamID}-${sequence}-${text}`, stream_id: streamID, sequence, kind, occurred_at: "2026-07-23T00:00:00Z", payload: kind === "user.message" ? { text } : { item: { type: "agentMessage" } } };
}

function session(value: Thread, realtime = 0, refresh: { globalStreamRevision?: number; streamRevision?: number; invalidationSequence?: number | null } = {}) {
  return <I18nProvider><SessionView key={value.id} thread={value} approvals={[]} realtime={realtime} {...refresh} reloadApprovals={vi.fn()} notify={vi.fn()} onOpenFile={vi.fn()} onNewTask={vi.fn()} /></I18nProvider>;
}

beforeEach(() => {
  window.localStorage.clear();
  window.localStorage.setItem("wio_language", "en");
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

test("loads the first session view from its recent window without a cursor", async () => {
  const value = thread("recent-window");
  const eventRequests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = String(input);
    if (url.includes("/events")) {
      eventRequests.push(url);
      return response([event(value.id, 502, "Recent window message")]);
    }
    return response([]);
  }));

  render(session(value));

  expect(await screen.findByText("Recent window message")).toBeInTheDocument();
  expect(eventRequests).toEqual([`/api/threads/${value.id}/events?view=conversation`]);
});

test("loads earlier events with a before cursor and preserves the reading anchor", async () => {
  const value = thread("earlier-window");
  const eventRequests: string[] = [];
  const recent = Array.from({ length: 500 }, (_, index) => event(value.id, index + 501, `Recent ${index + 501}`));
  let renderedHeight = 1_000;
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = String(input);
    if (!url.includes("/events")) return response([]);
    eventRequests.push(url);
    if (url.includes("before=501")) {
      renderedHeight = 1_300;
      return response([event(value.id, 499, "Earlier 499"), event(value.id, 500, "Earlier 500")]);
    }
    return response(recent);
  }));

  const user = userEvent.setup();
  const { container } = render(session(value));
  expect(await screen.findByText("Recent 501")).toBeInTheDocument();
  const stream = container.querySelector<HTMLElement>(".event-stream")!;
  Object.defineProperty(stream, "scrollHeight", { configurable: true, get: () => renderedHeight });
  stream.scrollTop = 120;

  await user.click(screen.getByRole("button", { name: "Load earlier messages" }));

  expect(await screen.findByText("Earlier 499")).toBeInTheDocument();
  expect(stream.scrollTop).toBe(420);
  expect(screen.queryByRole("button", { name: "Load earlier messages" })).not.toBeInTheDocument();
  expect(eventRequests).toEqual([
    `/api/threads/${value.id}/events?view=conversation`,
    `/api/threads/${value.id}/events?view=conversation&before=501&limit=500`
  ]);
});

test("uses an incremental cursor and deduplicates replayed event IDs and sequences", async () => {
  const value = thread("deduplicated-increment");
  const eventRequests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = String(input);
    if (!url.includes("/events")) return response([]);
    eventRequests.push(url);
    if (!url.includes("after=")) return response([event(value.id, 1, "First event"), event(value.id, 2, "Second event")]);
    return response([
      event(value.id, 2, "Second event"),
      { ...event(value.id, 2, "Conflicting sequence"), event_id: "a-different-id" },
      event(value.id, 3, "Third event")
    ]);
  }));

  const { rerender } = render(session(value));
  expect(await screen.findByText("Second event")).toBeInTheDocument();
  rerender(session(value, 1, { streamRevision: 1, invalidationSequence: 3 }));

  expect(await screen.findByText("Third event")).toBeInTheDocument();
  expect(screen.getAllByText("Second event")).toHaveLength(1);
  expect(screen.queryByText("Conflicting sequence")).not.toBeInTheDocument();
  expect(eventRequests).toEqual([
    `/api/threads/${value.id}/events?view=conversation`,
    `/api/threads/${value.id}/events?view=conversation&after=2&limit=1000`
  ]);
});

test("reloads the recent window and replaces a stale tail when a stream rewinds", async () => {
  const value = thread("rewrite-rewind");
  const eventRequests: string[] = [];
  let rewritten = false;
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = String(input);
    if (!url.includes("/events")) return response([]);
    eventRequests.push(url);
    return response(rewritten
      ? [event(value.id, 1, "Rewritten opening"), event(value.id, 4, "Replacement tail")]
      : [event(value.id, 1, "Original opening"), event(value.id, 10, "Stale tail")]);
  }));

  const { rerender } = render(session(value));
  expect(await screen.findByText("Stale tail")).toBeInTheDocument();
  rewritten = true;
  rerender(session(value, 1, { streamRevision: 1, invalidationSequence: 4 }));

  expect(await screen.findByText("Replacement tail")).toBeInTheDocument();
  expect(screen.queryByText("Stale tail")).not.toBeInTheDocument();
  expect(eventRequests).toEqual([
    `/api/threads/${value.id}/events?view=conversation`,
    `/api/threads/${value.id}/events?view=conversation`
  ]);
});

test("reloads the recent window when a stream revision has no positive sequence", async () => {
  const value = thread("unknown-sequence");
  const eventRequests: string[] = [];
  let refreshed = false;
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = String(input);
    if (!url.includes("/events")) return response([]);
    eventRequests.push(url);
    return response([event(value.id, refreshed ? 1 : 8, refreshed ? "Safe reset window" : "Old cached event")]);
  }));

  const { rerender } = render(session(value));
  expect(await screen.findByText("Old cached event")).toBeInTheDocument();
  refreshed = true;
  rerender(session(value, 1, { streamRevision: 1, invalidationSequence: 0 }));

  expect(await screen.findByText("Safe reset window")).toBeInTheDocument();
  expect(screen.queryByText("Old cached event")).not.toBeInTheDocument();
  expect(eventRequests).toEqual([
    `/api/threads/${value.id}/events?view=conversation`,
    `/api/threads/${value.id}/events?view=conversation`
  ]);
});

test("reloads the recent window after a global stream resynchronization", async () => {
  const value = thread("global-resync");
  const eventRequests: string[] = [];
  let resynchronized = false;
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = String(input);
    if (!url.includes("/events")) return response([]);
    eventRequests.push(url);
    return response([event(value.id, 1, resynchronized ? "Reconnected window" : "Cached window")]);
  }));

  const { rerender } = render(session(value));
  expect(await screen.findByText("Cached window")).toBeInTheDocument();
  resynchronized = true;
  rerender(session(value, 0, { globalStreamRevision: 1 }));

  expect(await screen.findByText("Reconnected window")).toBeInTheDocument();
  expect(screen.queryByText("Cached window")).not.toBeInTheDocument();
  expect(eventRequests).toEqual([
    `/api/threads/${value.id}/events?view=conversation`,
    `/api/threads/${value.id}/events?view=conversation`
  ]);
});

test("retries an initial load as a fresh window when recovery starts without cached events", async () => {
  const value = thread("retry-window");
  const eventRequests: string[] = [];
  let fail = true;
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = String(input);
    if (!url.includes("/events")) return response([]);
    eventRequests.push(url);
    if (fail) return new Response(JSON.stringify({ error: "Temporary event failure" }), { status: 503, headers: { "Content-Type": "application/json" } });
    return response([event(value.id, 1, "Recovered window event")]);
  }));

  const user = userEvent.setup();
  render(session(value));
  expect(await screen.findByText("Temporary event failure")).toBeInTheDocument();
  fail = false;
  await user.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText("Recovered window event")).toBeInTheDocument();
  expect(eventRequests).toEqual([
    `/api/threads/${value.id}/events?view=conversation`,
    `/api/threads/${value.id}/events?view=conversation`
  ]);
});

test("continues incremental requests when a page reaches the maximum size", async () => {
  const value = thread("paged-increment");
  const eventRequests: string[] = [];
  const fullPage = Array.from({ length: 1000 }, (_, index) => event(value.id, index + 2, `Hidden ${index + 2}`, "codex.item.started"));
  vi.stubGlobal("fetch", vi.fn(async input => {
    const url = String(input);
    if (!url.includes("/events")) return response([]);
    eventRequests.push(url);
    if (!url.includes("after=")) return response([event(value.id, 1, "Window event")]);
    if (url.includes("&after=1&")) return response(fullPage);
    if (url.includes("&after=1001&")) return response([event(value.id, 1002, "Caught up event")]);
    return response([]);
  }));

  const { rerender } = render(session(value));
  expect(await screen.findByText("Window event")).toBeInTheDocument();
  rerender(session(value, 1));

  expect(await screen.findByText("Caught up event")).toBeInTheDocument();
  expect(eventRequests).toEqual([
    `/api/threads/${value.id}/events?view=conversation`,
    `/api/threads/${value.id}/events?view=conversation&after=1&limit=1000`,
    `/api/threads/${value.id}/events?view=conversation&after=1001&limit=1000`
  ]);
});

test("keeps thread and view changes isolated from events already shown", async () => {
  const first = thread("isolated-first");
  const second = thread("isolated-second");
  let resolveRaw: ((value: Response) => void) | undefined;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (!url.includes("/events")) return Promise.resolve(response([]));
    if (url.includes(`${first.id}/events?view=raw`)) return new Promise<Response>(resolve => { resolveRaw = resolve; });
    if (url.includes(first.id)) return Promise.resolve(response([event(first.id, 1, "First conversation")])) ;
    return Promise.resolve(response([event(second.id, 1, "Second conversation")]));
  }));

  const user = userEvent.setup();
  const { rerender } = render(session(first));
  expect(await screen.findByText("First conversation")).toBeInTheDocument();

  await user.click(screen.getByTitle("Show raw events"));
  await waitFor(() => expect(resolveRaw).toBeDefined());
  await user.click(screen.getByTitle("Show conversation"));
  resolveRaw!(response([event(first.id, 2, "Late raw event")]));
  await waitFor(() => expect(screen.queryByText("Late raw event")).not.toBeInTheDocument());
  expect(screen.getByText("First conversation")).toBeInTheDocument();

  rerender(session(second));
  expect(await screen.findByText("Second conversation")).toBeInTheDocument();
  expect(screen.queryByText("First conversation")).not.toBeInTheDocument();
});
