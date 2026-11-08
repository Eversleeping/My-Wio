import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import App, { websocketReconnectDelay } from "./App";
import { I18nProvider } from "./i18n";

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: ((event: Event) => void) | null = null;
  closed = false;
  constructor(readonly url: string) { FakeWebSocket.instances.push(this); }
  open() { act(() => { this.onopen?.(new Event("open")); }); }
  close() {
    if (this.closed) return;
    this.closed = true;
    this.onclose?.(new Event("close"));
  }
  drop() { act(() => this.close()); }
}

const realSetTimeout = setTimeout;
type Route = { status?: number; body?: unknown; fail?: boolean };
let routes: Record<string, Route>;
let requests: string[];

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

const session = { username: "operator", csrf_token: "csrf-token", expires_at: "2099-01-01T00:00:00Z" };

beforeEach(() => {
  window.localStorage.setItem("wio_language", "en");
  FakeWebSocket.instances = [];
  requests = [];
  routes = {
    "GET /setup/status": { body: { configured: true, auth_mode: "password" } },
    "GET /auth/session": { body: session },
    "GET /approvals": { body: [] },
    "GET /summary": { body: { counts: { online: 1, servers: 1, projects: 1, threads: 1, deployments: 0, alerts: 0 }, deployments: [], alerts: [] } },
    "POST /auth/logout": { body: { ok: true } }
  };
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${String(input).replace(/^\/api/, "")}`;
    requests.push(key);
    const route = routes[key] ?? { body: [] };
    if (route.fail) throw new TypeError("Failed to fetch");
    return jsonResponse(route.body ?? {}, route.status ?? 200);
  }));
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

function renderApp() {
  return render(<I18nProvider><App /></I18nProvider>);
}

// Lets pending fetches finish without advancing the faked reconnect clock.
async function settle() {
  await act(() => new Promise<void>(resolve => { realSetTimeout(resolve, 5); }));
}

async function advance(milliseconds: number) {
  await act(async () => { await vi.advanceTimersByTimeAsync(milliseconds); });
}

test("returns to sign-in when the session expires while the console is open", async () => {
  routes["GET /summary"] = { status: 401, body: { error: "authentication required" } };
  renderApp();

  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  expect(requests).toContain("GET /summary");
  expect(FakeWebSocket.instances).toHaveLength(1);
  expect(FakeWebSocket.instances[0].closed).toBe(true);
});

test("signs out locally even when the logout request fails", async () => {
  routes["POST /auth/logout"] = { fail: true };
  const user = userEvent.setup();
  renderApp();

  await user.click(await screen.findByTitle("Sign out"));

  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  expect(requests).toContain("POST /auth/logout");
  expect(FakeWebSocket.instances[0].closed).toBe(true);
});

test("signs out locally when the server reports an error for logout", async () => {
  routes["POST /auth/logout"] = { status: 500, body: { error: "database unavailable" } };
  const user = userEvent.setup();
  renderApp();

  await user.click(await screen.findByTitle("Sign out"));

  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
});

test("uses exponential reconnect delays with jitter up to thirty seconds", () => {
  expect(websocketReconnectDelay(0, () => 0)).toBe(500);
  expect(websocketReconnectDelay(0, () => 1)).toBe(1_000);
  expect(websocketReconnectDelay(1, () => 0.5)).toBe(1_500);
  expect(websocketReconnectDelay(3, () => 1)).toBe(8_000);
  expect(websocketReconnectDelay(5, () => 0)).toBe(15_000);
  expect(websocketReconnectDelay(40, () => 1)).toBe(30_000);
});

test("backs off websocket reconnects, resets after a stable connection and stops once the session expires", async () => {
  vi.spyOn(Math, "random").mockReturnValue(1);
  renderApp();
  await screen.findByTitle("Sign out");
  expect(FakeWebSocket.instances).toHaveLength(1);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const sockets = FakeWebSocket.instances;

  // A dropped connection waits before reconnecting instead of retrying immediately.
  sockets[0].open();
  sockets[0].drop();
  await advance(999);
  expect(sockets).toHaveLength(1);
  await advance(1);
  expect(sockets).toHaveLength(2);

  // Failed upgrades confirm the session, then wait twice as long each time.
  sockets[1].drop();
  await settle();
  expect(requests.filter(request => request === "GET /auth/session")).toHaveLength(2);
  await advance(1_999);
  expect(sockets).toHaveLength(2);
  await advance(1);
  expect(sockets).toHaveLength(3);
  sockets[2].drop();
  await settle();
  await advance(3_999);
  expect(sockets).toHaveLength(3);
  await advance(1);
  expect(sockets).toHaveLength(4);

  // A connection that stays up for thirty seconds restores the short delay.
  sockets[3].open();
  await advance(30_000);
  sockets[3].drop();
  await advance(1_000);
  expect(sockets).toHaveLength(5);

  // A rejected upgrade with an expired session signs out and stops retrying.
  routes["GET /auth/session"] = { status: 401, body: { error: "authentication required" } };
  sockets[4].drop();
  await settle();
  await advance(120_000);
  expect(sockets).toHaveLength(5);
  expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
});

test("reloads an edited Codex session after switching away while realtime batches arrive", async () => {
  const thread = (id: string, title: string) => ({ id, workspace_id: `workspace-${id}`, project_id: "project-1", codex_thread_id: "", title, status: "idle", path: `/srv/${id}`, server_id: "server-1", server_name: "Server", project_name: "Project", created_at: "2026-07-23T00:00:00Z", updated_at: "2026-07-23T00:00:00Z", pinned_at: null, archived_at: null, project_pinned_at: null, project_hidden_at: null });
  const message = (streamID: string, sequence: number, text: string) => ({ event_id: `${streamID}-${sequence}-${text}`, stream_id: streamID, sequence, kind: "user.message", occurred_at: "2026-07-23T00:00:00Z", payload: { text } });
  const edited = thread("edited-thread", "Edited session");
  const other = thread("other-thread", "Other session");
  let editedEvents = [message(edited.id, 1, "Opening"), message(edited.id, 10, "Pre-edit tail")];
  const eventRequests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), location.origin);
    const path = url.pathname.replace(/^\/api/, "");
    if (path === "/setup/status") return jsonResponse({ configured: true, auth_mode: "password" });
    if (path === "/auth/session") return jsonResponse(session);
    if (path === "/threads") return jsonResponse({ items: url.searchParams.has("archived") ? [] : [edited, other], has_more: false, next: null });
    if (path.endsWith("/goal")) return jsonResponse({ status: "idle", supported: true, data: {} });
    if (path === `/threads/${other.id}/events`) return jsonResponse([message(other.id, 1, "Other conversation")]);
    if (path === `/threads/${edited.id}/events`) {
      eventRequests.push(`${path}${url.search}`);
      const after = Number(url.searchParams.get("after") ?? 0);
      return jsonResponse(editedEvents.filter(event => event.sequence > after));
    }
    return jsonResponse([]);
  }));
  window.history.replaceState(null, "", `/?view=codex&thread=${edited.id}`);
  const user = userEvent.setup();
  try {
    renderApp();
    await screen.findByText("Pre-edit tail");
    await user.click(screen.getByRole("button", { name: "Other session Server" }));
    await screen.findByText("Other conversation");

    // The edit rewinds the stream, then a later batch reports only newer events.
    editedEvents = [message(edited.id, 1, "Opening"), message(edited.id, 4, "Edited message"), message(edited.id, 12, "Answer to the edit")];
    const socket = FakeWebSocket.instances[0];
    for (const sequence of [4, 12]) {
      act(() => socket.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ stream_id: edited.id, sequence, kind: "user.message" }) })));
      await act(() => new Promise(resolve => setTimeout(resolve, 150)));
    }
    await user.click(screen.getByRole("button", { name: "Edited session Server" }));

    await screen.findByText("Answer to the edit");
    expect(screen.getByText("Edited message")).toBeInTheDocument();
    expect(screen.queryByText("Pre-edit tail")).not.toBeInTheDocument();
    expect(eventRequests).toEqual([`/threads/${edited.id}/events?view=conversation`, `/threads/${edited.id}/events?view=conversation`]);
  } finally {
    window.history.replaceState(null, "", "/");
  }
});
