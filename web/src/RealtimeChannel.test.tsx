import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import App from "./App";
import { I18nProvider } from "./i18n";

vi.mock("./pages/DashboardPage", async () => { const {useResourceRevision} = await import("./data/resources"); return { default: () => <div data-testid="dashboard">{useResourceRevision(["dashboard"]).split("|")[0]}</div> }; });
vi.mock("./pages/ServersPage", async () => { const {useResourceRevision} = await import("./data/resources"); return { default: () => <div data-testid="servers">{useResourceRevision(["servers"]).split("|")[0]}</div> }; });
vi.mock("./sessions/engines/codex", () => ({ default: ({ selectedThreadID }: { selectedThreadID: string }) => <div data-testid="codex">{selectedThreadID}</div> }));

class Socket {
  static instances: Socket[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor() { Socket.instances.push(this); }
  close() { this.onclose?.(); }
}

beforeEach(() => {
  window.localStorage.setItem("wio_language", "en");
  window.history.replaceState(null, "", "/");
  Socket.instances = [];
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const body = url.endsWith("/setup/status") ? { configured: true, auth_mode: "password" }
      : url.endsWith("/auth/session") ? { username: "operator", csrf_token: "csrf", expires_at: "2099-01-01T00:00:00Z" } : [];
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
  window.history.replaceState(null, "", "/");
});

test("batches socket events into their refresh scopes without changing unrelated revisions", async () => {
  const user = userEvent.setup();
  render(<I18nProvider><App /></I18nProvider>);
  await screen.findByTestId("dashboard");
  const socket = Socket.instances[0];
  act(() => socket.onopen?.());
  await waitFor(() => expect(screen.getByTestId("dashboard")).toHaveTextContent("1"));
  act(() => {
    for (const sequence of [1, 2]) socket.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ stream_id: "thread-1", sequence, kind: "codex.item.completed" }) }));
  });
  await waitFor(() => expect(screen.getByTestId("dashboard")).toHaveTextContent("2"));
  await user.click(screen.getByRole("button", { name: "Servers" }));
  expect(await screen.findByTestId("servers")).toHaveTextContent("1");
  act(() => socket.onmessage?.(new MessageEvent("message", { data: "invalid json" })));
  await waitFor(() => expect(screen.getByTestId("servers")).toHaveTextContent("2"));
});

test("keeps views and thread selection synchronized across browser back and forward", async () => {
  const user = userEvent.setup();
  render(<I18nProvider><App /></I18nProvider>);
  await screen.findByTestId("dashboard");
  await user.click(screen.getByRole("button", { name: "Servers" }));
  await screen.findByTestId("servers");
  window.history.pushState(null, "", "/?view=codex&thread=saved-thread");
  act(() => window.dispatchEvent(new PopStateEvent("popstate")));
  expect(await screen.findByTestId("codex")).toHaveTextContent("saved-thread");
  act(() => window.history.back());
  await screen.findByTestId("servers");
  expect(window.location.search).toBe("?view=servers");
  act(() => window.history.forward());
  expect(await screen.findByTestId("codex")).toHaveTextContent("saved-thread");
  expect(window.location.search).toBe("?view=codex&thread=saved-thread");
});
