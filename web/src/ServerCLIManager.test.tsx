import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ServerCLIManager } from "./components/ServerCLIManager";
import { I18nProvider } from "./i18n";
import type { Server } from "./types";

const server = { id: "node-1", name: "My server", status: "online" } as Server;
afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test("shows only the latest three releases per agent and cancels a version switch without a request", async () => {
  window.localStorage.setItem("wio_language", "en");
  const installs: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (init.method === "POST") installs.push(url);
    const data = url.endsWith("/versions") ? { versions: url.includes("codex") ? ["0.165.0", "0.164.0", "0.163.0", "0.162.0", "0.161.0"] : ["0.66.0", "0.65.0", "0.64.0", "0.63.0"] } : [
      { kind: "codex", version: "codex-cli 0.165.0", ready: true, supported: true },
      { kind: "gemini", version: "0.66.0", ready: true, supported: true },
    ];
    return new Response(JSON.stringify(data));
  }));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  const user = userEvent.setup();
  await screen.findByRole("option", { name: "0.164.0" });
  const codex = screen.getByRole("combobox", { name: "Codex version" });
  const gemini = screen.getByRole("combobox", { name: "Gemini version" });
  expect(within(codex).getAllByRole("option").map(option => (option as HTMLOptionElement).value)).toEqual(["0.165.0", "0.164.0", "0.163.0"]);
  expect(within(gemini).getAllByRole("option").map(option => (option as HTMLOptionElement).value)).toEqual(["0.66.0", "0.65.0", "0.64.0"]);
  await user.selectOptions(codex, "0.164.0");
  expect(gemini).toHaveValue("0.66.0");
  await user.click(screen.getByRole("button", { name: "Refresh versions" }));
  await waitFor(() => expect(codex).toBeEnabled());
  expect(codex).toHaveValue("0.164.0");
  await user.click(screen.getByRole("button", { name: "Switch Codex version" }));
  const confirmation = screen.getByRole("dialog", { name: "Confirm Codex version change" });
  expect(confirmation).toHaveTextContent("Change Codex on My server from 0.165.0 to 0.164.0?");
  expect(installs).toEqual([]);
  await user.click(within(confirmation).getByRole("button", { name: "Cancel" }));
  expect(screen.queryByRole("dialog", { name: "Confirm Codex version change" })).not.toBeInTheDocument();
  expect(installs).toEqual([]);
  expect(screen.getAllByRole("combobox")).toHaveLength(2);
});

test("keeps Gemini available when the Codex version request fails and supports refreshing", async () => {
  window.localStorage.setItem("wio_language", "en");
  let fail = true;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/codex/versions") && fail) return new Response(JSON.stringify({ error: "Registry unavailable" }), { status: 502 });
    return new Response(JSON.stringify(url.endsWith("/versions") ? { versions: url.includes("codex") ? ["0.165.0"] : ["0.66.0"] } : [
      { kind: "codex", version: "0.164.0", ready: true, supported: true },
      { kind: "gemini", version: "0.65.0", ready: true, supported: true },
    ]));
  }));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  expect(await screen.findByRole("alert")).toHaveTextContent("Codex: Registry unavailable");
  expect(screen.getByRole("combobox", { name: "Codex version" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Switch Gemini version" })).toBeEnabled();
  fail = false;
  await userEvent.setup().click(screen.getByRole("button", { name: "Refresh versions" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Switch Codex version" })).toBeEnabled());
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

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
  await within(screen.getByRole("combobox", { name: "Gemini version" })).findByRole("option", { name: "0.65.0" });
  await user.click(screen.getByRole("button", { name: "Switch Gemini version" }));
  expect(requests.some(request => request.method === "POST")).toBe(false);
  const confirmation = screen.getByRole("dialog", { name: "Confirm Gemini version change" });
  expect(confirmation).toHaveTextContent("My server");
  await user.click(within(confirmation).getByRole("button", { name: "Install Gemini 0.65.0" }));
  await waitFor(() => expect(notify).toHaveBeenCalledWith("Gemini 0.65.0 installation queued"));
  expect(requests).toContainEqual({ url: "/api/servers/node-1/ai-clis/gemini/install", method: "POST", body: JSON.stringify({ version: "0.65.0" }) });
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "Confirm Gemini version change" })).not.toBeInTheDocument());
  expect(screen.getByRole("button", { name: "Switch Gemini version" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Switch Gemini version" })).toHaveTextContent("Installation in progress");
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
  const select = await screen.findByRole("combobox", { name: "Codex version" });
  await screen.findByRole("option", { name: "0.161.0" });
  expect(screen.getByRole("button", { name: "Switch Codex version" })).toBeDisabled();
  await user.selectOptions(select, "0.161.0");
  await user.click(screen.getByRole("button", { name: "Switch Codex version" }));
  expect(screen.getByRole("dialog", { name: "Confirm Codex version change" })).toHaveTextContent("from 0.162.1 to 0.161.0");
  expect(requests).toEqual([]);
  await user.click(screen.getByRole("button", { name: "Switch Codex to 0.161.0" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("finish active turns first");
  expect(requests).toEqual([JSON.stringify({ version: "0.161.0" })]);
});

test("disables installation until Wio Agent supports CLI management", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(String(input).endsWith("/versions") ? { versions: ["0.65.0"] } : [{ kind: "gemini", version: "", ready: false, supported: false }]), { status: 200 })));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  expect(await screen.findByText(/Update Wio Agent to 0.2.61/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Switch Gemini version" })).toBeDisabled();
});

test("preselects the Gemini target saved in Settings", async () => {
 window.localStorage.setItem("wio_language", "en");
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(String(input).endsWith("/versions") ? {versions:["0.65.0","0.64.0"],target_version:"0.64.0"} : [{kind:"gemini",version:"",ready:false,supported:true}]),{status:200})));
 render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
 await within(screen.getByRole("combobox", { name: "Gemini version" })).findByRole("option", { name: "0.64.0" });
 expect(screen.getByRole("button",{name:"Switch Gemini version"})).toBeEnabled();
 expect(screen.getByRole("combobox", { name: "Gemini version" })).toHaveValue("0.64.0");
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
  const list = screen.getByRole("table", { name: "Manage AI Agents" });
  await screen.findByText("Active tasks (1)");
  expect(within(list).getAllByRole("row")).toHaveLength(3);
  expect(within(list).getAllByRole("columnheader").map(cell => cell.textContent)).toEqual(["Agent name", "Version", "Status", "Actions"]);
  expect(screen.getByText("Last installation: Succeeded")).toBeInTheDocument();
  expect(screen.getAllByRole("combobox")).toHaveLength(2);
  expect(screen.getByRole("button", { name: "Switch Gemini version" })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "Switch Gemini to 0.66.0" })).not.toBeInTheDocument();
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
  expect(screen.getByRole("button", { name: "切换 Gemini 版本" })).toBeDisabled();
});
