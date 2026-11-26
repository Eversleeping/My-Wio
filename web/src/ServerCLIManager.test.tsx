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
  expect(screen.getAllByRole("combobox")).toHaveLength(3);
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
  expect(await screen.findByText(/Update Wio Agent to 0.3.26/)).toBeInTheDocument();
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
  expect(within(list).getAllByRole("row")).toHaveLength(4);
  expect(within(list).getAllByRole("columnheader").map(cell => cell.textContent)).toEqual(["Agent name", "Version", "Status", "Actions"]);
  expect(screen.getByText("Last installation: Succeeded")).toBeInTheDocument();
  expect(screen.getAllByRole("combobox")).toHaveLength(3);
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

test("switches Claude with its official version format and preserves the configured target", async () => {
 window.localStorage.setItem("wio_language", "en");
 const requests: Array<{url:string;body:unknown}> = [];
 vi.stubGlobal("fetch",vi.fn(async(input:RequestInfo|URL,init:RequestInit={})=>{
  const url=String(input);
  if(init.method==="POST") { requests.push({url,body:JSON.parse(String(init.body))});return new Response(JSON.stringify({operation_id:"claude-install"})); }
  return new Response(JSON.stringify(url.endsWith("/versions") ? {versions:["2.1.318","2.1.317","2.1.316","2.1.315"],target_version:"2.1.315"} : [{kind:"claude",version:"2.1.318 (Claude Code)",ready:true,supported:true,active_threads:[],blocking_operations:[]}]));
 }));
 render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()}/></I18nProvider>);
 const user=userEvent.setup();
 const select=screen.getByRole("combobox",{name:"Claude version"});
 await within(select).findByRole("option",{name:"2.1.315"});
 expect(select).toHaveValue("2.1.315");
 await user.selectOptions(select,"2.1.318");
 expect(screen.getByRole("button",{name:"Switch Claude version"})).toBeDisabled();
 await user.selectOptions(select,"2.1.317");
 await user.click(screen.getByRole("button",{name:"Switch Claude version"}));
 expect(screen.getByRole("dialog",{name:"Confirm Claude version change"})).toHaveTextContent("from 2.1.318 to 2.1.317");
 await user.click(screen.getByRole("button",{name:"Switch Claude to 2.1.317"}));
 await waitFor(()=>expect(requests).toContainEqual({url:"/api/servers/node-1/ai-clis/claude/install",body:{version:"2.1.317"}}));
});

function mockBatch(options: { fail?: boolean; readFailure?: boolean; versions?: string[] } = {}) {
  const installs: Array<{ kind: string; version: string }> = [];
  let polls = 0;
  let complete = false;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (url.endsWith("/versions")) return new Response(JSON.stringify({ versions: options.versions ?? ["2.0.0", "1.0.0"], target_version: "1.0.0" }));
    if (init.method === "POST") {
      installs.push({ kind: url.split("/").at(-2)!, version: JSON.parse(String(init.body)).version });
      return new Response(JSON.stringify({ operation_id: `install-${installs.length}` }));
    }
    if (installs.length && options.readFailure) return new Response(JSON.stringify({ error: "Connection lost" }), { status: 502 });
    const latest = installs.at(-1);
    if (latest) polls++;
    return new Response(JSON.stringify(["codex", "gemini", "claude"].map(kind => ({
      kind, version: kind === "codex" ? "codex-cli 1.0.0" : kind === "claude" ? "1.0.0 (Claude Code)" : "1.0.0", ready: true, supported: true,
      ...(kind === latest?.kind ? { operation: polls === 1
        ? { id: "previous-install", status: "succeeded" }
        : { id: `install-${installs.length}`, status: complete ? options.fail ? "failed" : "succeeded" : "running", result: options.fail ? "npm unavailable" : "" } } : {})
    }))));
  }));
  return { installs, finish: () => { complete = true; } };
}

async function startBatch() {
  const user = userEvent.setup();
  await waitFor(() => expect(screen.getByRole("button", { name: "Update all Agents" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Update all Agents" }));
  const dialog = screen.getByRole("dialog", { name: "Update all Agents" });
  expect(dialog).toHaveTextContent("Codex CLI → 2.0.0");
  expect(dialog).toHaveTextContent("Gemini CLI → 2.0.0");
  expect(dialog).toHaveTextContent("Claude CLI → 2.0.0");
  await user.click(within(dialog).getByRole("button", { name: "Update all Agents" }));
}

test("updates all three Agents sequentially to latest releases, ignoring selected targets and old operation results", async () => {
  window.localStorage.setItem("wio_language", "en");
  const batch = mockBatch();
  const notify = vi.fn();
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={notify} /></I18nProvider>);
  await startBatch();
  await screen.findByText("Updating Codex · 0/3 completed");
  expect(screen.getByRole("combobox", { name: "Codex version" })).toHaveValue("1.0.0");
  expect(batch.installs).toEqual([{ kind: "codex", version: "2.0.0" }]);
  expect(screen.getByRole("button", { name: "Switch Gemini version" })).toBeDisabled();
  // The first status response is an older succeeded operation. Only the new ID may advance the batch.
  batch.finish();
  await waitFor(() => expect(notify).toHaveBeenCalledWith("All Agent updates completed (3)"), { timeout: 4500 });
  expect(batch.installs).toEqual(["codex", "gemini", "claude"].map(kind => ({ kind, version: "2.0.0" })));
});

test("stops the remaining updates when an installation fails", async () => {
  window.localStorage.setItem("wio_language", "en");
  const batch = mockBatch({ fail: true });
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  await startBatch();
  batch.finish();
  await waitFor(() => expect(screen.getAllByRole("alert").some(alert => alert.textContent?.includes("Remaining updates have stopped"))).toBe(true), { timeout: 4500 });
  expect(screen.getAllByRole("alert").some(alert => alert.textContent?.includes("npm unavailable"))).toBe(true);
  expect(batch.installs).toHaveLength(1);
});

test("stops remaining updates without cancelling the queued installation", async () => {
  window.localStorage.setItem("wio_language", "en");
  const batch = mockBatch();
  const notify = vi.fn();
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={notify} /></I18nProvider>);
  await startBatch();
  await screen.findByText("Updating Codex · 0/3 completed");
  await userEvent.setup().click(screen.getByRole("button", { name: "Stop remaining updates" }));
  expect(notify).toHaveBeenCalledWith("Remaining updates stopped. Any installation already started will continue.");
  expect(screen.queryByText("Updating Codex · 0/3 completed")).not.toBeInTheDocument();
  expect(batch.installs).toEqual([{ kind: "codex", version: "2.0.0" }]);
  await waitFor(() => expect(screen.getByRole("button", { name: "Switch Gemini version" })).toBeDisabled());
});

test("does not send further install requests when polling fails after queueing an update", async () => {
  window.localStorage.setItem("wio_language", "en");
  const batch = mockBatch({ readFailure: true });
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  await startBatch();
  expect(await screen.findByText(/Codex update failed: Connection lost/)).toBeInTheDocument();
  expect(batch.installs).toEqual([{ kind: "codex", version: "2.0.0" }]);
});

test("skips current, newer, missing and unsupported Agents and prevents downgrades", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(String(input).endsWith("/versions") ? { versions: ["2.0.0", "1.0.0"] } : [
    { kind: "codex", version: "codex-cli 2.0.0", ready: true, supported: true },
    { kind: "gemini", version: "3.0.0", ready: true, supported: true },
    { kind: "claude", version: "", ready: false, supported: false }
  ]))));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  await screen.findByText(/Upgrade Wio Agent/);
  expect(screen.getByRole("button", { name: "Update all Agents" })).toBeDisabled();
});

test("offers only outdated installed Agents in the batch confirmation", async () => {
  window.localStorage.setItem("wio_language", "en");
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(String(input).endsWith("/versions") ? { versions: ["1.10.0", "1.9.0"] } : [
    { kind: "codex", version: "codex-cli 1.9.0", ready: true, supported: true },
    { kind: "gemini", version: "1.10.0", ready: true, supported: true },
    { kind: "claude", version: "2.0.0 (Claude Code)", ready: true, supported: true }
  ]))));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  await waitFor(() => expect(screen.getByRole("button", { name: "Update all Agents" })).toBeEnabled());
  await userEvent.setup().click(screen.getByRole("button", { name: "Update all Agents" }));
  const dialog = screen.getByRole("dialog", { name: "Update all Agents" });
  expect(within(dialog).getAllByRole("listitem").map(item => item.textContent)).toEqual(["Codex CLI → 1.10.0"]);
});

test("rechecks active conversations before starting the batch", async () => {
  window.localStorage.setItem("wio_language", "en");
  let blocked = false;
  const installs: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    if (init.method === "POST") installs.push(String(input));
    return new Response(JSON.stringify(String(input).endsWith("/versions") ? { versions: ["2.0.0"] } : ["codex", "gemini", "claude"].map(kind => ({
      kind, version: "1.0.0", supported: true, ready: true, active_threads: blocked ? [{ id: "active", title: "Active turn", status: "running" }] : []
    }))));
  }));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={vi.fn()} /></I18nProvider>);
  await waitFor(() => expect(screen.getByRole("button", { name: "Update all Agents" })).toBeEnabled());
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Update all Agents" }));
  blocked = true;
  await user.click(within(screen.getByRole("dialog", { name: "Update all Agents" })).getByRole("button", { name: "Update all Agents" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("This server has active conversations or maintenance tasks");
  expect(installs).toEqual([]);
});

test("starts all supported engines in parallel and reports each failure after the others finish", async () => {
  window.localStorage.setItem("wio_language", "en");
  const installs: string[] = [];
  let finished = false;
  const notify = vi.fn();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (url.endsWith("/versions")) return new Response(JSON.stringify({ versions: ["2.0.0", "1.0.0"] }));
    if (init.method === "POST") {
      const kind = url.split("/").at(-2)!;
      installs.push(kind);
      return new Response(JSON.stringify({ operation_id: `parallel-${kind}` }));
    }
    return new Response(JSON.stringify(["codex", "gemini", "claude"].map(kind => ({
      kind, version: finished && kind !== "codex" ? "2.0.0" : "1.0.0", supported: true, ready: true, parallel_supported: true,
      ...(installs.includes(kind) ? { operation: { id: `parallel-${kind}`, status: kind === "codex" ? "failed" : finished ? "succeeded" : "running", result: kind === "codex" ? "network unavailable" : "" } } : {})
    }))));
  }));
  render(<I18nProvider><ServerCLIManager server={server} onClose={vi.fn()} notify={notify} /></I18nProvider>);
  await startBatch();
  await waitFor(() => expect(installs).toEqual(["codex", "gemini", "claude"]));
  expect(screen.getByText("Updating Codex / Gemini / Claude · 0/3 completed")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Stop waiting" })).toBeEnabled();
  finished = true;
  await waitFor(() => expect(screen.getByText(/Agent updates finished: 2\/3 succeeded. Codex: network unavailable/)).toBeInTheDocument(), { timeout: 4500 });
  expect(notify).not.toHaveBeenCalledWith("All Agent updates completed (3)");
  expect(installs).toHaveLength(3);
});
