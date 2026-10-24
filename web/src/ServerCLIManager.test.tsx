import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ServerCLIManager } from "./components/ServerCLIManager";
import { I18nProvider } from "./i18n";
import type { Server } from "./types";

const server = { id: "node-1", name: "My server", status: "online" } as Server;
afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test("installs missing Gemini and shows the queued operation", async () => {
  window.localStorage.setItem("wio_language", "en");
  const notify = vi.fn();
  const requests: Array<{ url: string; method: string; body?: string }> = [];
  let queued = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input); const method = init.method ?? "GET";
    requests.push({ url, method, body: init.body as string | undefined });
    let data: unknown;
    if (url.endsWith("/install")) { queued = true; data = { operation_id: "install-1" }; }
    else if (url.endsWith("/versions")) data = { versions: ["0.65.0", "0.64.0"] };
    else data = [{ kind: "codex", version: "codex-cli 0.162.1", ready: true, supported: true }, { kind: "gemini", version: "", ready: false, supported: true, ...(queued ? { operation: { id: "install-1", status: "queued" } } : {}) }];
    return new Response(JSON.stringify(data), { status: 200 });
  }));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={notify} /></I18nProvider>);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Install Gemini 0.65.0" }));
  await waitFor(() => expect(notify).toHaveBeenCalledWith("Gemini 0.65.0 installation queued"));
  expect(requests).toContainEqual({ url: "/api/servers/node-1/ai-clis/gemini/install", method: "POST", body: JSON.stringify({ version: "0.65.0" }) });
  expect(await screen.findByRole("button", { name: "Installation in progress" })).toBeDisabled();
});

test("switches Codex to an explicit older version and exposes failures", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    let data: unknown;
    let status = 200;
    if (url.endsWith("/install")) { requests.push(String(init.body)); data = { error: "finish active turns first" }; status = 409; }
    else if (url.endsWith("/versions")) data = { versions: url.includes("codex") ? ["0.162.1", "0.161.0"] : ["0.65.0"] };
    else data = [{ kind: "codex", version: "codex-cli 0.162.1", ready: true, supported: true }, { kind: "gemini", version: "0.65.0", ready: true, supported: true }];
    return new Response(JSON.stringify(data), { status });
  }));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: /Codex CLI/ }));
  const select = await screen.findByRole("combobox", { name: "Codex version" });
  await screen.findByRole("option", { name: "0.161.0" });
  expect(screen.getByRole("button", { name: "Current version" })).toBeDisabled();
  await user.selectOptions(select, "0.161.0");
  await user.click(screen.getByRole("button", { name: "Switch Codex to 0.161.0" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("finish active turns first");
  expect(requests).toEqual([JSON.stringify({ version: "0.161.0" })]);
});

test("disables installation until Wio Agent supports CLI management", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(String(input).endsWith("/versions") ? { versions: ["0.65.0"] } : [{ kind: "gemini", version: "", ready: false, supported: false }]), { status: 200 })));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  expect(await screen.findByText(/Update Wio Agent to 0.2.61/)).toBeInTheDocument();
  expect(await screen.findByRole("button", { name: "Install Gemini 0.65.0" })).toBeDisabled();
});
