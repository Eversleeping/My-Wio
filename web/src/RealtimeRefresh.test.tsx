import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { realtimeScopesForEvent } from "./App";
import { DashboardPage } from "./pages/DashboardPage";
import { I18nProvider } from "./i18n";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("realtime refresh scopes", () => {
  test("keeps thread events away from unrelated pages", () => {
    expect(realtimeScopesForEvent({ kind: "codex.item.completed" })).toEqual(["dashboard", "codex"]);
    expect(realtimeScopesForEvent({ kind: "approval.requested" })).toEqual(["codex", "approvals"]);
    expect(realtimeScopesForEvent({ kind: "deployment.succeeded" })).toEqual(["dashboard", "deployments", "operations", "monitoring"]);
    expect(realtimeScopesForEvent({ kind: "inventory.updated" })).not.toContain("settings");
  });

  test("falls back to a safe global refresh for unknown events", () => {
    expect(realtimeScopesForEvent({ kind: "operation.succeeded" })).toEqual(expect.arrayContaining(["dashboard", "servers", "projects", "codex", "deployments", "monitoring", "settings", "approvals"]));
    expect(realtimeScopesForEvent({})).toHaveLength(9);
  });
});

test("throttles dashboard summary reloads during an event burst", async () => {
  vi.useFakeTimers();
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input));
    return new Response(JSON.stringify({
      counts: { online: 1, servers: 1, projects: 1, threads: 1, deployments: 0, alerts: 0 },
      deployments: [],
      alerts: []
    }), { status: 200, headers: { "Content-Type": "application/json" } });
  }));

  const view = (realtime: number) => <I18nProvider><DashboardPage realtime={realtime} onNavigate={vi.fn()} /></I18nProvider>;
  const rendered = render(view(0));
  await act(async () => { await Promise.resolve(); });
  expect(requests.filter(url => url === "/api/summary")).toHaveLength(1);

  rendered.rerender(view(1));
  rendered.rerender(view(2));
  rendered.rerender(view(3));
  await act(async () => { vi.advanceTimersByTime(999); await Promise.resolve(); });
  expect(requests.filter(url => url === "/api/summary")).toHaveLength(1);

  await act(async () => { vi.advanceTimersByTime(1); await Promise.resolve(); });
  expect(requests.filter(url => url === "/api/summary")).toHaveLength(2);
});
