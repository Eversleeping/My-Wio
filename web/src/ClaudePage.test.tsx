import { compressImage } from "./imageCompression";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import ClaudePage from "./pages/ClaudePage";
import { I18nProvider } from "./i18n";

vi.mock("./imageCompression", () => ({ compressImage: vi.fn() }));

const baseThread = { questions_supported: true, input_supported: true, id: "claude-1", workspace_id: "workspace-1", project_id: "project-1", project_name: "Project", server_id: "server-1", server_name: "Server", server_status: "online", path: "/project", title: "Claude", status: "idle", operation_id: "op-1", session_id: "", model: "sonnet", permission_mode: "default", effort: "high", effort_supported: true, archived_at: null, recovery_pending: false, created_at: "2026-11-05T00:00:00Z", updated_at: "2026-11-05T00:00:00Z" };

function setup(options: { selected?: boolean; recovery?: boolean; status?: string; appliedModel?: string; effortSupported?: boolean; inputSupported?: boolean; nativeID?: string; skills?: {name:string;description:string;argument_hint?:string}[]; skillsError?: boolean; skillSupported?:boolean; steerSupported?:boolean; steerReady?:boolean; steerFailures?:number; rewriteSupported?:boolean; rewriteFailures?:number; events?: unknown[] } = {}) {
  window.localStorage.setItem("wio_language", "en");
  const thread = { ...baseThread, rewrite_supported: options.rewriteSupported ?? true, steer_supported: options.steerSupported ?? false, steer_ready:options.steerReady ?? false, skills_supported: options.skillSupported ?? options.skills !== undefined, input_supported:options.inputSupported ?? true, session_id:options.nativeID ?? "", effort_supported: options.effortSupported ?? true, status: options.status ?? (options.recovery ? "interrupted" : "idle"), recovery_pending: Boolean(options.recovery) };
  const requests: Array<{ url: string; body: unknown }> = [];
  let steerFailures=options.steerFailures??0;
  let rewriteFailures=options.rewriteFailures??0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input); let body: unknown = [];
    if(url.endsWith("/claude/skills")) { requests.push({url,body:{}}); return new Response(JSON.stringify({operation_id:"skill-op"}), {status:202}); }
    if(url.endsWith("/claude/skills/skill-op")) return new Response(JSON.stringify(options.skillsError ? {status:"failed",error:"Could not initialize CLI"} : {status:"succeeded",skills:options.skills ?? []}),{status:200});
    if (init.method === "POST" || init.method === "PATCH") { requests.push({ url, body: JSON.parse(String(init.body)) }); if(url.endsWith("/rewrite") && rewriteFailures-->0)return new Response(JSON.stringify({error:"Rewrite temporarily failed"}),{status:503}); if(url.endsWith("/steer") && steerFailures-->0)return new Response(JSON.stringify({error:"Temporary network failure"}),{status:503}); body = url.endsWith("/answer") ? {operation_id:"answer-op"} : url === "/api/claude/threads" ? thread : url.endsWith("/fork") || url.endsWith("/fork-worktree") ? {operation_id:"fork-op",target_thread_id:"fork-target"} : { ok: true }; }
    else if (url.startsWith("/api/operations/")) body={status:"succeeded"};
    else if (url === "/api/workspaces/workspace-1/claude/models") body = { configured: true, models: [{ id: "custom-upstream-model", name: "Custom upstream model" }] };
    else if (url === "/api/servers") body = [{ id: "server-1", claude_profile_model: options.appliedModel || "" }];
    else if (url === "/api/workspaces") body = [{ id: "workspace-1", server_id: "server-1", project_name: "Project", server_name: "Server", path: "/project", status: "ready" }];
    else if (url.startsWith("/api/claude/threads?") && options.selected) body = [thread];
    else if (url === "/api/claude/threads/claude-1") body = thread;
    else if (url === "/api/claude/threads/claude-1/usage") body = {input_tokens:18626887,output_tokens:89133,output_per_second:26.24,partial:false};
    else if (url.startsWith("/api/claude/threads/claude-1/events")) body = options.events ?? [
      { event_id: "u", stream_id: thread.id, sequence: 1, kind: "claude.user.message", payload: { text: "Hello" } },
      { event_id: "a1", stream_id: thread.id, sequence: 2, kind: "claude.message", payload: { text: "Hi " } },
      { event_id: "a2", stream_id: thread.id, sequence: 3, kind: "claude.message", payload: { text: "there" } }
    ];
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  const onSelectThread = vi.fn();
  const view = (realtime: number) => <I18nProvider><ClaudePage realtime={realtime} notify={vi.fn()} selectedThreadID={options.selected ? thread.id : ""} onSelectThread={onSelectThread} /></I18nProvider>;
  const rendered = render(view(0)); let revision = 0;
  return { user: userEvent.setup(), requests, onSelectThread, thread, refresh: () => rendered.rerender(view(++revision)) };
}

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); window.localStorage.clear(); });

test("shows token totals and average output below the Claude session title", async () => {
  setup({selected:true});
  await screen.findByRole("heading",{name:"Claude",level:2});
  const header = document.querySelector(".gemini-session .session-header")! as HTMLElement;
  expect(await within(header).findByText("Input 18,626,887 tokens")).toBeInTheDocument();
  expect(within(header).getByText("Output 89,133 tokens")).toBeInTheDocument();
  expect(within(header).getByText("Average output 26.24 tokens/sec")).toBeInTheDocument();
  expect(within(header).getByText("In 18.63M")).toHaveAttribute("title","Input 18,626,887 tokens");
});

test("steers a running Claude task and switches between stopping and sending", async () => {
  const { user, requests } = setup({selected:true,status:"running",steerSupported:true,steerReady:true});
  const input=await screen.findByRole("textbox",{name:"Ask Claude to work on this project…"});
  expect(input).toBeEnabled();
  expect(screen.getByLabelText("Claude model or alias")).toBeDisabled();
  expect(screen.getByRole("button",{name:"Interrupt"})).toBeEnabled();
  await user.type(input,"Focus on the failing tests");
  await user.click(screen.getByRole("button",{name:"Send correction"}));
  await waitFor(()=>expect(requests).toContainEqual({url:"/api/claude/threads/claude-1/steer",body:{prompt:"Focus on the failing tests",operation_id:"op-1",request_id:expect.any(String)}}));
  expect(input).toHaveValue("");
  expect(requests.filter(request=>request.url.endsWith("/turns"))).toHaveLength(0);
  await user.click(screen.getByRole("button",{name:"Interrupt"}));
  await waitFor(()=>expect(requests).toContainEqual({url:"/api/claude/threads/claude-1/interrupt",body:{}}));
});

test("preserves a failed correction and retries with the same request ID", async () => {
  const {user,requests}=setup({selected:true,status:"running",steerSupported:true,steerReady:true,steerFailures:1});
  const input=await screen.findByRole("textbox",{name:"Ask Claude to work on this project…"});
  await user.type(input,"Keep the public API");
  await user.click(screen.getByRole("button",{name:"Send correction"}));
  expect(await screen.findByText("Temporary network failure")).toBeInTheDocument();
  expect(input).toHaveValue("Keep the public API");
  await user.click(screen.getByRole("button",{name:"Send correction"}));
  await waitFor(()=>expect(input).toHaveValue(""));
  const steers=requests.filter(request=>request.url.endsWith("/steer"));
  expect(steers).toHaveLength(2);expect(steers[1].body).toEqual(steers[0].body);
});

test.each(["", "Use the attached screenshot"])("pastes images into a running correction, retains failed input and retries once (prompt=%s)", async prompt => {
  const dataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII=";
  vi.mocked(compressImage).mockResolvedValue(dataURL);
  const {user,requests} = setup({selected:true,status:"running",steerSupported:true,steerReady:true,steerFailures:1});
  const input = await screen.findByRole("textbox",{name:"Ask Claude to work on this project…"});
  if (prompt) await user.type(input,prompt);
  fireEvent.paste(input,{clipboardData:{items:[{type:"image/png",getAsFile:()=>new File(["image"],"correction.png",{type:"image/png"})}]}});
  await waitFor(()=>expect(document.querySelector(".composer-images img")).toHaveAttribute("src",dataURL));
  await user.click(screen.getByRole("button",{name:"Send correction"}));
  expect(await screen.findByText("Temporary network failure")).toBeInTheDocument();
  expect(input).toHaveValue(prompt);
  expect(document.querySelector(".composer-images img")).toHaveAttribute("src",dataURL);
  await user.click(screen.getByRole("button",{name:"Send correction"}));
  await waitFor(()=>expect(document.querySelector(".composer-images img")).toBeNull());
  const steers = requests.filter(request=>request.url.endsWith("/steer"));
  expect(steers).toHaveLength(2);
  expect(steers[0].body).toEqual({operation_id:"op-1",request_id:expect.any(String),prompt,images:[{data_url:dataURL}]});
  expect(steers[1].body).toEqual(steers[0].body);
  expect(requests.filter(request=>request.url.endsWith("/turns"))).toHaveLength(0);
  expect(screen.getByRole("button",{name:"Interrupt"})).toBeEnabled();
});

test("shows one skill menu entry and accepts the plural alias",async()=>{
  Element.prototype.scrollIntoView=vi.fn();
  const {user}=setup({selected:true,skills:[{name:"review",description:"Review changes"}]});
  const input=await screen.findByRole("textbox",{name:"Ask Claude to work on this project…"});
  await user.type(input,"/");
  const menu=screen.getByRole("listbox");
  expect(within(menu).getByRole("option",{name:/^\/skill Browse/})).toBeInTheDocument();
  expect(within(menu).queryByRole("option",{name:/^\/skills /})).not.toBeInTheDocument();
  await user.clear(input);await user.type(input,"/skills");await user.keyboard("{Enter}");
  expect(await within(screen.getByRole("listbox")).findByRole("option",{name:/\/review/})).toBeInTheDocument();
});

test("discovers skills before the first turn and sends namespaced native commands", async () => {
  Element.prototype.scrollIntoView = vi.fn();
  const { user, requests } = setup({ selected: true, events: [], skills: [{ name: "plugin:code.review", description: "Review project changes", argument_hint: "[scope]" }] });
  const input = await screen.findByRole("textbox", { name: "Ask Claude to work on this project…" });
  await user.type(input, "/skill");
  await user.keyboard("{Enter}");
  const skill = await within(screen.getByRole("listbox")).findByRole("option", { name: /plugin:code.review.*Review project changes.*\[scope\]/ });
  await user.click(skill);
  expect(input).toHaveValue("/plugin:code.review ");
  expect(requests.filter(request => request.url.endsWith("/turns"))).toHaveLength(0);
  await user.type(input, "src");
  await user.keyboard("{Enter}");
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads/claude-1/turns", body: expect.objectContaining({ prompt: "/plugin:code.review src" }) }));
  expect(requests.filter(request => request.url.endsWith("/claude/skills"))).toHaveLength(1);
});

test.each([false, true])("shows skill empty or failure state and allows refresh (failure=%s)", async failure => {
  Element.prototype.scrollIntoView = vi.fn();
  const { user, requests } = setup({ selected: true, events: [], skills: [], skillsError: failure });
  const input = await screen.findByRole("textbox", { name: "Ask Claude to work on this project…" });
  await user.type(input, "/skills");
  await user.keyboard("{Enter}");
  await waitFor(() => expect(within(screen.getByRole("listbox")).getByRole("status")).toHaveTextContent(failure ? "Could not load skills" : "No skills found"));
  await user.click(within(screen.getByRole("listbox")).getByRole("option", { name: "Refresh skills" }));
  await waitFor(() => expect(requests.filter(request => request.url.endsWith("/claude/skills"))).toHaveLength(2));
  expect(requests.filter(request => request.url.endsWith("/turns"))).toHaveLength(0);
});

test("selects an upstream model and accepts a custom model with reasoning effort", async () => {
  const { user, requests } = setup({ selected: true });
  await screen.findByRole("option", { name: "Custom upstream model · custom-upstream-model" });
  const select = screen.getByLabelText("Claude model or alias");
  expect(select.tagName).toBe("SELECT");
  await user.selectOptions(select, "custom-upstream-model");
  expect(select).toHaveValue("custom-upstream-model");
  await user.selectOptions(select, "__custom__");
  const custom = screen.getByLabelText("Custom model name");
  await user.type(custom, "my-custom-model");
  await user.selectOptions(screen.getByLabelText("Reasoning effort"), "max");
  await user.type(screen.getByRole("textbox", { name: "Ask Claude to work on this project…" }), "Review");
  await user.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads/claude-1/turns", body: expect.objectContaining({ model: "my-custom-model", effort: "max" }) }));
});

test("creates a Claude session for a workspace with the selected permissions", async () => {
  const { user, requests, onSelectThread } = setup();
  await user.click((await screen.findAllByRole("button", { name: "New Claude session" }))[0]);
  await user.selectOptions(screen.getByLabelText("Workspace"), "workspace-1");
  await user.selectOptions(screen.getByLabelText("Tool permissions"), "acceptEdits");
  const createButtons = screen.getAllByRole("button", { name: "New Claude session" });
  await user.click(createButtons[createButtons.length - 1]);
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads", body: { workspace_id: "workspace-1", model: "sonnet", permission_mode: "acceptEdits", effort: "" } }));
  expect(onSelectThread).toHaveBeenCalledWith("claude-1");
});

test("renders streamed text and sends the next turn", async () => {
  const { user, requests } = setup({ selected: true });
  expect(await screen.findByText("Hi there")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Delete Claude session" }));
  expect(await screen.findByRole("dialog", { name: "Delete Claude session" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Cancel" }));
  const input = await screen.findByRole("textbox", { name: "Ask Claude to work on this project…" });
  expect(screen.getByLabelText("Reasoning effort")).toHaveValue("high");
  await user.selectOptions(screen.getByLabelText("Reasoning effort"), "max");
  await user.type(input, "Continue the task");
  await user.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads/claude-1/turns", body: { prompt: "Continue the task", model: "sonnet", permission_mode: "default", effort: "max" } }));
});

test("requires explicit confirmation before releasing a lost task", async () => {
  const { user, requests } = setup({ selected: true, recovery: true });
  expect(await screen.findByRole("textbox", { name: "Ask Claude to work on this project…" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "Confirm process stopped" }));
  expect(requests).toHaveLength(0);
  await user.click(screen.getAllByRole("button", { name: "Confirm process stopped" }).at(-1)!);
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads/claude-1/confirm-stopped", body: { operation_id: "op-1", confirmed: true } }));
});

test.each([
  ["running", "Claude is working…"],
  ["interrupting", "Interrupting Claude…"],
])("shows the Claude status while %s", async (status, expected) => {
  setup({ selected: true, status });
  expect(await screen.findByText(expected)).toBeInTheDocument();
});

test("fills every missing event page after a large realtime gap", async () => {
  window.localStorage.setItem("wio_language", "en");
  let jumped = false;
  const requests: string[] = [];
  const event = (sequence: number, kind = "claude.init", text = "") => ({ event_id: `event-${sequence}`, stream_id: baseThread.id, sequence, kind, payload: { text } });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input); requests.push(url);
    let body: unknown = [];
    if (url.startsWith("/api/claude/threads?") || url === "/api/claude/threads/claude-1") body = url.includes("?") ? [baseThread] : baseThread;
    else if (url.endsWith("/events?after=1")) body = Array.from({ length: 500 }, (_, index) => index === 248 ? event(250, "claude.message", "middle ") : event(index + 2));
    else if (url.endsWith("/events?after=501")) body = [event(502), event(503)];
    else if (url.includes("/events?session=")) body = jumped ? [event(504), event(505, "claude.message", "end")] : [event(1, "claude.user.message", "Start")];
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
  const view = (realtime: number) => <I18nProvider><ClaudePage realtime={realtime} notify={vi.fn()} selectedThreadID={baseThread.id} onSelectThread={vi.fn()} /></I18nProvider>;
  const rendered = render(view(0));
  expect(await screen.findByText("Start")).toBeInTheDocument();
  jumped = true;
  rendered.rerender(view(1));
  expect(await screen.findByText("middle end", {}, { timeout: 3000 })).toBeInTheDocument();
  expect(requests).toContain("/api/claude/threads/claude-1/events?after=1");
  expect(requests).toContain("/api/claude/threads/claude-1/events?after=501");
});

test("defaults new sessions to the model last applied to the selected server", async () => {
 const { user, requests } = setup({ selected: true, appliedModel: "claude-opus-4-6" });
 await user.click((await screen.findAllByRole("button", { name: "New Claude session" }))[0]);
 await user.selectOptions(screen.getByLabelText("Workspace"), "workspace-1");
 expect(within(screen.getByRole("dialog", { name: "New Claude session" })).getByLabelText("Claude model or alias")).toHaveValue("claude-opus-4-6");
 await user.click(screen.getAllByRole("button", { name: "New Claude session" }).at(-1)!);
 await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads", body: { workspace_id: "workspace-1", model: "claude-opus-4-6", permission_mode: "default", effort: "" } }));
});

test("groups sessions by workspace and supports collapsing, searching and the session menu", async () => {
  const { user } = setup({ selected: true });
  const group = await screen.findByRole("button", { name: /Project · project Server 1/ });
  expect(group).toHaveAttribute("aria-expanded", "true");
  await user.click(group);
  expect(screen.queryByRole("button", { name: "Claude Server" })).not.toBeInTheDocument();
  await user.click(group);
  expect(screen.getByRole("button", { name: "Claude Server" })).toBeInTheDocument();
  await user.type(screen.getByRole("searchbox"), "no-match");
  expect(screen.queryByRole("button", { name: "Claude Server" })).not.toBeInTheDocument();
  await user.clear(screen.getByRole("searchbox"));
  await user.click(screen.getByRole("button", { name: "Task menu for Claude" }));
  await user.click(screen.getByRole("menuitem", { name: "Rename session" }));
  expect(screen.getByRole("dialog", { name: "Rename session" })).toBeInTheDocument();
});

test("allows resetting a saved effort when an older Agent cannot apply it", async () => {
  const { user, requests } = setup({ selected: true, effortSupported: false });
  const effort = await screen.findByLabelText("Reasoning effort");
  expect(screen.getByRole("option", { name: "Max" })).toBeDisabled();
  await user.selectOptions(effort, "");
  await user.type(screen.getByRole("textbox", { name: "Ask Claude to work on this project…" }), "Use the default");
  await user.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(requests).toContainEqual({ url: "/api/claude/threads/claude-1/turns", body: { prompt: "Use the default", model: "sonnet", permission_mode: "default", effort: "" } }));
});


test("offers the complete project and task menus and queues an independent continuation",async()=>{
 const {user,requests,onSelectThread}=setup({selected:true,nativeID:"native-claude"});
 await user.click(await screen.findByRole("button",{name:"Task menu for Claude"}));
 for(const label of ["Pin task","Rename session","Create scheduled task","Continue in new task","Continue in new worktree","Archive session","Copy working directory","Copy Wio session ID","Copy Claude session ID","Copy deep link","Open in new window"]){expect(screen.getByRole("menuitem",{name:label})).toBeInTheDocument();}
 await user.click(screen.getByRole("menuitem",{name:"Continue in new task"}));
 await waitFor(()=>expect(requests).toContainEqual({url:"/api/claude/threads/claude-1/fork",body:{}}));
 await waitFor(()=>expect(onSelectThread).toHaveBeenCalledWith("fork-target"));
 await user.click(screen.getByRole("button",{name:/Workspace menu/}));
 for(const label of ["New session","Pin project","Rename project","Create permanent worktree","Archive all project tasks","Hide project"]){expect(screen.getByRole("menuitem",{name:label})).toBeInTheDocument();}
 await user.click(screen.getByRole("menuitem",{name:"Rename project"}));
 const dialog=screen.getByRole("dialog",{name:"Rename project"});await user.clear(within(dialog).getByRole("textbox"));await user.type(within(dialog).getByRole("textbox"),"Renamed");await user.click(within(dialog).getByRole("button",{name:"Save"}));
 await waitFor(()=>expect(requests).toContainEqual({url:"/api/projects/project-1",body:{name:"Renamed",server_id:"server-1"}}));
});

test("pastes and removes images, sends image-only input, and previews image history",async()=>{
 const dataURL="data:image/png;base64,aGVsbG8=";vi.mocked(compressImage).mockResolvedValue(dataURL);
 const {user,requests}=setup({selected:true,events:[{event_id:"image-history",sequence:1,kind:"claude.user.message",payload:{text:"previous",images:[{data_url:dataURL}]}}]});
 const input=await screen.findByRole("textbox",{name:"Ask Claude to work on this project…"});
 expect(document.querySelector(".message.user img")).toHaveAttribute("src",dataURL);
 const paste=()=>fireEvent.paste(input,{clipboardData:{items:[{type:"image/png",getAsFile:()=>new File(["image"],"example.png",{type:"image/png"})}]}});
 paste();await waitFor(()=>expect(document.querySelector(".composer-images img")).toHaveAttribute("src",dataURL));
 await user.click(document.querySelector<HTMLButtonElement>(".composer-images button")!);expect(document.querySelector(".composer-images img")).toBeNull();
 paste();await waitFor(()=>expect(document.querySelector(".composer-images img")).toBeInTheDocument());await user.click(screen.getByRole("button",{name:"Send"}));
 await waitFor(()=>expect(requests).toContainEqual({url:"/api/claude/threads/claude-1/turns",body:{prompt:"",model:"sonnet",permission_mode:"default",effort:"high",images:[{data_url:dataURL}]}}));
 expect(document.querySelector(".composer-images img")).toBeNull();
});

test("uses keyboard slash options locally and sends discovered native skills",async()=>{
 Element.prototype.scrollIntoView=vi.fn();
 const {user,requests}=setup({selected:true,events:[{event_id:"init",sequence:1,kind:"claude.init",payload:{skills:["review-code"]}}]});
 const input=await screen.findByRole("textbox",{name:"Ask Claude to work on this project…"});
 await user.type(input,"/effort");await user.keyboard("{Enter}");expect(screen.getByRole("listbox")).toBeInTheDocument();
 await user.click(within(screen.getByRole("listbox")).getByRole("option",{name:"Max"}));expect(screen.getByLabelText("Reasoning effort")).toHaveValue("max");expect(requests).toHaveLength(0);
 await user.type(input,"/plan");await user.keyboard("{Enter}");expect(screen.getByLabelText("Tool permissions")).toHaveValue("plan");expect(requests).toHaveLength(0);
 await user.type(input,"/review-code");await user.keyboard("{Enter}");expect(input).toHaveValue("/review-code ");await user.type(input,"inspect changes");await user.keyboard("{Enter}");
 await waitFor(()=>expect(requests).toContainEqual({url:"/api/claude/threads/claude-1/turns",body:{prompt:"/review-code inspect changes",model:"sonnet",permission_mode:"plan",effort:"max"}}));
});

test("creates a scheduled Claude task with custom model, effort and permissions",async()=>{
 const {user,requests}=setup({selected:true});await user.click(await screen.findByRole("button",{name:"Task menu for Claude"}));await user.click(screen.getByRole("menuitem",{name:"Create scheduled task"}));
 const dialog=screen.getByRole("dialog");await user.type(within(dialog).getByLabelText("Name"),"Daily review");await user.type(within(dialog).getByLabelText("Prompt"),"Review the repository");await user.type(within(dialog).getByLabelText("Claude model or alias"),"custom-model");await user.selectOptions(within(dialog).getByLabelText("Reasoning effort"),"max");await user.selectOptions(within(dialog).getByLabelText("Tool permissions"),"plan");await user.click(within(dialog).getByRole("button",{name:"Save"}));
 await waitFor(()=>expect(requests).toContainEqual({url:"/api/scheduled-tasks",body:expect.objectContaining({agent_kind:"claude",thread_id:"claude-1",model:"custom-model",reasoning_effort:"max",approval_mode:"plan",prompt:"Review the repository"})}));
});


test("applies explicit slash option arguments without creating a model turn",async()=>{
 const {user,requests}=setup({selected:true});const input=await screen.findByRole("textbox",{name:"Ask Claude to work on this project…"});Element.prototype.scrollIntoView=vi.fn();
 for(const command of ["/model custom-model","/effort max","/permissions plan"]){await user.type(input,command);await user.keyboard("{Enter}");expect(input).toHaveValue("");}
 expect(screen.getByLabelText("Claude model or alias")).toHaveValue("custom-model");expect(screen.getByLabelText("Reasoning effort")).toHaveValue("max");expect(screen.getByLabelText("Tool permissions")).toHaveValue("plan");expect(requests).toHaveLength(0);
 await user.type(input,"/effort invalid");await user.keyboard("{Enter}");expect(requests).toHaveLength(0);expect(await screen.findByText(/Invalid command option/)).toBeInTheDocument();
});


test("offers refreshed upstream models in slash menu while preserving the current choice", async () => {
  const { user, requests } = setup({ selected: true });
  const input = await screen.findByRole("textbox", { name: "Ask Claude to work on this project…" });
  await screen.findByRole("option", { name: "Custom upstream model · custom-upstream-model" });
  expect(screen.getByLabelText("Claude model or alias")).toHaveValue("sonnet");
  Element.prototype.scrollIntoView = vi.fn();
  await user.type(input, "/model");
  await user.keyboard("{Enter}");
  await user.click(within(screen.getByRole("listbox")).getByRole("option", { name: /custom-upstream-model/ }));
  expect(screen.getByLabelText("Claude model or alias")).toHaveValue("custom-upstream-model");
  expect(requests).toHaveLength(0);
});

test("collapses commands between replies and expands paired parameters and output", async () => {
  const entries = [
    ["message", { text: "I will inspect the project" }],
    ["tool_use", { id: "one", name: "Bash", input: { command: "git status" } }],
    ["tool_result", { id: "one", content: "working tree clean" }],
    ["tool_use", { id: "two", name: "Read", input: { file_path: "README.md" } }],
    ["tool_result", { id: "two", content: "project documentation" }],
    ["message", { text: "The project is ready" }],
    ["tool_use", { id: "three", name: "Bash", input: { command: "git log -1" } }],
  ];
  const { user } = setup({ selected: true, events: entries.map(([kind, payload], index) => ({ event_id: `group-${index}`, sequence: index + 1, kind: `claude.${kind}`, payload })) });
  const summary = await screen.findByText("Ran 2 commands");
  const group = summary.closest("details")!;
  expect(group).not.toHaveAttribute("open");
  expect(screen.getByText("Ran 1 commands").closest("details")).not.toHaveAttribute("open");
  expect(screen.getByText("I will inspect the project")).toBeVisible();
  expect(screen.getByText("The project is ready")).toBeVisible();
  await user.click(summary);
  expect(group).toHaveAttribute("open");
  await user.click(within(group).getByText("git status", { selector: ".gemini-tool-title" }));
  expect(within(group).getByText("working tree clean")).toBeVisible();
  expect(within(group).getByText("git status", { selector: "pre" })).toBeVisible();
  await user.click(summary);
  expect(group).not.toHaveAttribute("open");
});

test("branches the last streamed reply using its final event and selects the new chat", async () => {
  const {user, requests, onSelectThread} = setup({selected:true, nativeID:"native"});
  expect(await screen.findByText("Hi there")).toBeInTheDocument();
  await user.click(screen.getByRole("button", {name:"Message actions"}));
  await user.click(screen.getByRole("menuitem", {name:"Branch to new chat"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/claude/threads/claude-1/fork",body:{event_id:"a2"}}));
  await waitFor(() => expect(onSelectThread).toHaveBeenCalledWith("fork-target"));
});

test("does not offer a reply branch after a newer user message", async () => {
  setup({selected:true, nativeID:"native", events:[
    {event_id:"a",sequence:1,kind:"claude.message",payload:{text:"Earlier reply"}},
    {event_id:"u",sequence:2,kind:"claude.user.message",payload:{text:"Next input"}},
  ]});
  expect(await screen.findByText("Earlier reply")).toBeInTheDocument();
  expect(screen.queryByRole("button",{name:"Message actions"})).not.toBeInTheDocument();
});

test("edits a user message with images, keeps a failed draft and retries the rewrite endpoint", async () => {
  Element.prototype.scrollIntoView=vi.fn();
  const image="data:image/png;base64,aGVsbG8=";
  const {user,requests}=setup({selected:true,nativeID:"native",rewriteFailures:1,events:[{event_id:"user-image",sequence:1,kind:"claude.user.message",payload:{text:"Original input",images:[{data_url:image}]}}]});
  await user.click(await screen.findByRole("button",{name:"Edit and resend"}));
  const input=screen.getByRole("textbox",{name:"Ask Claude to work on this project…"});
  expect(input).toHaveValue("Original input");
  expect(document.querySelector(".composer-images img")).toHaveAttribute("src",image);
  expect(screen.getByText("Editing an earlier message; later replies will be discarded")).toBeInTheDocument();
  await user.clear(input); await user.type(input,"/model revised");
  await user.click(screen.getByRole("button",{name:"Send"}));
  expect(await screen.findByText("Rewrite temporarily failed")).toBeInTheDocument();
  expect(input).toHaveValue("/model revised");
  expect(document.querySelector(".composer-images img")).toHaveAttribute("src",image);
  await user.click(screen.getByRole("button",{name:"Send"}));
  await waitFor(()=>expect(input).toHaveValue(""));
  expect(requests.filter(r=>r.url.endsWith("/rewrite"))).toEqual(Array(2).fill({url:"/api/claude/threads/claude-1/events/user-image/rewrite",body:{prompt:"/model revised",model:"sonnet",permission_mode:"default",effort:"high",images:[{data_url:image}]}}));
  expect(requests.some(r=>r.url.endsWith("/turns"))).toBe(false);
});

test("cancels a message edit", async () => {
  Element.prototype.scrollIntoView=vi.fn();
  const {user}=setup({selected:true,nativeID:"native"});
  await user.click(await screen.findByRole("button",{name:"Edit and resend"}));
  await user.click(screen.getByRole("button",{name:"Cancel editing"}));
  expect(screen.getByRole("textbox",{name:"Ask Claude to work on this project…"})).toHaveValue("");
  expect(screen.queryByText("Editing an earlier message; later replies will be discarded")).not.toBeInTheDocument();
});

test.each([{status:"running",steerSupported:true,steerReady:true},{rewriteSupported:false},{nativeID:""}])("disables message editing when unavailable (%j)",async options=>{
  setup({selected:true,nativeID:"native",...options});
  expect(await screen.findByRole("button",{name:"Edit and resend"})).toBeDisabled();
});

test("removes the discarded tail when a rewrite changes the native session", async () => {
  const options={selected:true,nativeID:"original",events:[
    {event_id:"u",sequence:1,kind:"claude.user.message",payload:{text:"Keep prefix"}},
    {event_id:"a",sequence:2,kind:"claude.message",payload:{text:"Retained reply"}},
    {event_id:"u2",sequence:3,kind:"claude.user.message",payload:{text:"Discard input"}},
    {event_id:"a2",sequence:4,kind:"claude.message",payload:{text:"Discard reply"}},
  ]};
  const {thread,refresh}=setup(options);
  expect(await screen.findByText("Discard reply")).toBeInTheDocument();
  options.events=[...options.events.slice(0,2),{event_id:"replacement",sequence:7,kind:"claude.user.message",payload:{text:"Revised input",history_rewritten:true,replaced_sequence:3} as {text:string}},{event_id:"new-answer",sequence:8,kind:"claude.message",payload:{text:"New reply"}}];
  thread.session_id="replacement"; refresh();
  expect(await screen.findByText("New reply",{}, {timeout:3000})).toBeInTheDocument();
  expect(screen.getByText("Keep prefix")).toBeInTheDocument();
  expect(screen.queryByText("Discard input")).not.toBeInTheDocument();
  expect(screen.queryByText("Discard reply")).not.toBeInTheDocument();
});

const questionEvent = { event_id: "op-1:req", stream_id: "claude-1", sequence: 1, kind: "claude.question", payload: { operation_id: "op-1", request_id: "op-1:req", questions: [
  {id:"0",header:"Database",question:"Choose a database",isOther:true,options:[{label:"PostgreSQL",description:"SQL"},{label:"SQLite",description:"Embedded"}]},
  {id:"1",header:"Deliverables",question:"Choose deliverables",multiSelect:true,isOther:true,options:[{label:"Tests",description:"Verify"},{label:"Docs",description:"Explain"}]}
] } };

test("answers Claude native questions with single, multiple and custom choices", async()=>{
  const {user,requests}=setup({selected:true,status:"running",events:[questionEvent]});
  expect(await screen.findByText("Claude needs your input")).toBeInTheDocument();
  const submit=screen.getByRole("button",{name:"Submit answers"});
  expect(submit).toBeDisabled();
  await user.click(screen.getByRole("radio",{name:"PostgreSQL SQL"}));
  await user.click(screen.getByRole("checkbox",{name:"Tests Verify"}));
  await user.click(screen.getByRole("checkbox",{name:"Docs Explain"}));
  await user.type(screen.getAllByLabelText("Your answer")[1],"Include examples");
  await user.click(submit);
  expect(await screen.findByRole("button",{name:"Answers sent"})).toBeDisabled();
  expect(requests).toContainEqual({url:"/api/claude/threads/claude-1/questions/op-1:req/answer",body:{answers:{"0":{answers:["PostgreSQL"]},"1":{answers:["Tests","Docs","Include examples"]}}}});
  expect(requests.filter(request=>request.url.endsWith("/turns") || request.url.endsWith("/steer"))).toHaveLength(0);
});

test("restores answered Claude questions from persisted events without duplicate tool cards", async()=>{
  setup({selected:true,status:"idle",events:[
    {...questionEvent,sequence:2},
    {event_id:"tool",stream_id:"claude-1",sequence:1,kind:"claude.tool_use",payload:{id:"q-tool",name:"AskUserQuestion",input:{}}},
    {event_id:"receipt",stream_id:"claude-1",sequence:3,kind:"claude.question.answered",payload:{request_id:"op-1:req",answers:{"0":{answers:["PostgreSQL"]},"1":{answers:["Tests","custom-marker"]}}}},
    {event_id:"result",stream_id:"claude-1",sequence:4,kind:"claude.tool_result",payload:{id:"q-tool",content:"Answers"}}
  ]});
  expect(await screen.findByRole("button",{name:"Answers sent"})).toBeDisabled();
  expect(screen.getByRole("radio",{name:"PostgreSQL SQL"})).toBeChecked();
  expect(screen.getByRole("checkbox",{name:"Tests Verify"})).toBeChecked();
  expect(screen.getAllByLabelText("Your answer")[1]).toHaveValue("custom-marker");
  expect(document.querySelector(".claude-tool-group")).not.toBeInTheDocument();
});

test.each(["idle","interrupting","interrupted"])("does not answer expired Claude questions (status=%s)",async status=>{
  setup({selected:true,status,events:[questionEvent]});
  expect(await screen.findByRole("radio",{name:"PostgreSQL SQL"})).toBeDisabled();
  expect(screen.getByRole("button",{name:"Submit answers"})).toBeDisabled();
});

test("disables a cancelled Claude question while its task is still running",async()=>{
 setup({selected:true,status:"running",events:[questionEvent,{event_id:"cancelled",stream_id:"claude-1",sequence:2,kind:"claude.question.cancelled",payload:{request_id:"op-1:req"}}]});
 expect(await screen.findByText("This question cannot be answered now. Check the task status.")).toBeInTheDocument();
 expect(screen.getByRole("radio",{name:"PostgreSQL SQL"})).toBeDisabled();
});
