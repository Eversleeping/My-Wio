import { render, screen, waitFor, within } from "@testing-library/react";
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
  await user.click(await screen.findByRole("button", { name: "Switch Gemini version" }));
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
  await user.click(await screen.findByRole("button", { name: "Switch Codex version" }));
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
  await userEvent.setup().click(await screen.findByRole("button", { name: "Switch Gemini version" }));
  expect(await screen.findByText(/Update Wio Agent to 0.2.61/)).toBeInTheDocument();
  expect(await screen.findByRole("button", { name: "Install Gemini 0.65.0" })).toBeDisabled();
});

test("preselects the Gemini target saved in Settings", async () => {
 window.localStorage.setItem("wio_language", "en");
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(String(input).endsWith("/versions") ? {versions:["0.65.0","0.64.0"],target_version:"0.64.0"} : [{kind:"gemini",version:"",ready:false,supported:true}]),{status:200})));
 render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
 await userEvent.setup().click(await screen.findByRole("button",{name:"Switch Gemini version"}));
 expect(await screen.findByRole("button",{name:"Install Gemini 0.64.0"})).toBeEnabled();
 expect(screen.getByRole("combobox")).toHaveValue("0.64.0");
});

test("lists each CLI on one row and distinguishes active conversations from a previous successful install", async () => {
  window.localStorage.setItem("wio_language", "en");
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input); requests.push(url);
    return new Response(JSON.stringify(url.endsWith("/versions") ? { versions: ["0.66.0", "0.65.0"] } : [
      { kind: "codex", version: "codex-cli 0.165.0", ready: true, supported: true, active_threads: [], blocking_operations: [] },
      { kind: "gemini", version: "0.65.0", ready: true, supported: true, operation: { id: "old-install", status: "succeeded" }, active_threads: [{ id: "thread-1", title: "Blocked conversation", status: "running" }], blocking_operations: [] }
    ]));
  }));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  const list = screen.getByRole("list", { name: "Manage AI Agents" });
  await screen.findByText("Active tasks (1)");
  expect(within(list).getAllByRole("listitem")).toHaveLength(2);
  expect(screen.getByText("Last installation: Succeeded")).toBeInTheDocument();
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  await userEvent.setup().click(screen.getByRole("button", { name: "Switch Gemini version" }));
  expect(await screen.findByRole("button", { name: "Switch Gemini to 0.66.0" })).toBeDisabled();
  expect(screen.getByRole("link", { name: "Blocked conversation" })).toHaveAttribute("href", "?view=gemini&thread=thread-1");
  expect(screen.getByText(/finish or stop the current task/)).toBeInTheDocument();
  expect(requests.some(url => url.endsWith("/install"))).toBe(false);
});

test("translates a busy response and reloads the actual blocking conversations", async () => {
  window.localStorage.setItem("wio_language", "zh-CN");
  let blocked = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (init.method === "POST") { blocked = true; return new Response(JSON.stringify({ error: "finish active turns and AI CLI installations on this server first", code: "cli_install_busy" }), { status: 409 }); }
    return new Response(JSON.stringify(url.endsWith("/versions") ? { versions: ["0.66.0"] } : [{ kind: "gemini", version: "0.65.0", ready: true, supported: true, active_threads: blocked ? [{ id: "new-task", title: "新任务", status: "running" }] : [] }]));
  }));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "切换 Gemini 版本" }));
  await user.click(await screen.findByRole("button", { name: "切换 Gemini 至 0.66.0" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("此服务器仍有运行中的会话或维护任务");
  expect(await screen.findByRole("link", { name: "新任务" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "切换 Gemini 至 0.66.0" })).toBeDisabled();
});
