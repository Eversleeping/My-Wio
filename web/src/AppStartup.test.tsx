import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import App from "./App";
import { I18nProvider } from "./i18n";

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test("starts setup and session requests together and retains the configured login mode", async () => {
  window.localStorage.setItem("wio_language", "en");
  let resolveSetup!: (response: Response) => void;
  const fetchMock = vi.fn((input: string) => input === "/api/setup/status"
    ? new Promise<Response>(resolve => { resolveSetup = resolve; })
    : Promise.resolve(new Response(JSON.stringify({ error: "unauthorized" }), { status: 401 })));
  vi.stubGlobal("fetch", fetchMock);
  render(<I18nProvider><App /></I18nProvider>);
  expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(expect.arrayContaining(["/api/auth/session", "/api/setup/status"]));
  await act(async () => resolveSetup(new Response(JSON.stringify({ configured: true, auth_mode: "password" }))));
  expect(await screen.findByLabelText("Password")).toBeInTheDocument();
});

test("opens setup without waiting for the speculative session request", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.stubGlobal("fetch", vi.fn((input: string) => input === "/api/setup/status"
    ? Promise.resolve(new Response(JSON.stringify({ configured: false })))
    : new Promise<Response>(() => {})));
  render(<I18nProvider><App /></I18nProvider>);
  expect(await screen.findByRole("button", { name: "Create administrator" })).toBeInTheDocument();
});
