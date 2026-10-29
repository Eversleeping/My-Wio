import { compressImage } from "./imageCompression";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import GeminiPage from "./pages/GeminiPage";
import { I18nProvider } from "./i18n";
import { formatTime } from "./format";

vi.mock("./imageCompression", () => ({ compressImage: vi.fn() }));

const thread = {id:"thread-1",workspace_id:"workspace-1",project_name:"Project",server_name:"Production",server_status:"online",path:"/project",title:"Gemini",status:"idle",model:"auto",approval_mode:"default",thinking_level:"",archived_at:null,updated_at:"2026-10-25T00:00:00Z",created_at:"2026-10-25T00:00:00Z"};
function setup(selected = false, model = "auto", upstreamState = "configured", events: unknown[] = [], options: { rewriteSupported?: boolean; rewriteFailure?: boolean; status?: string; inputSupported?: boolean; answerFailure?: boolean; steerSupported?: boolean; steerReady?: boolean; steerFailure?: boolean } = {}) {
  window.localStorage.setItem("wio_language","en");
  let eventLog = events;
  const currentThread = {...thread, steer_supported:options.steerSupported ?? false,steer_ready:options.steerReady ?? false, input_supported:options.inputSupported ?? true,operation_id:"op1", project_id:"project-1",session_id:"native-1",fork_supported:true, rewrite_supported: options.rewriteSupported ?? true, status: options.status ?? "idle"};
  const requests: Array<{url:string; body:unknown}> = [];
  const modelRequests: string[] = [];
  vi.stubGlobal("fetch",vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    let body: unknown = [];
    if (init.method === "POST") {
      const input = JSON.parse(String(init.body)); requests.push({url,body:input}); body = {...currentThread,model,id:"thread-created"};
      if (url.endsWith("/steer")) {
        if (options.steerFailure) return new Response(JSON.stringify({error:"Turn is no longer running"}), {status:409,headers:{"Content-Type":"application/json"}});
        body={operation_id:"steer-op"};
        eventLog=[...eventLog,{event_id:"steer",stream_id:thread.id,sequence:100,kind:"gemini.steer.queued",occurred_at:thread.updated_at,payload:{text:input.prompt,operation_id:input.operation_id,request_id:input.request_id}}];
      }
      if (url.endsWith("/answer")) body={operation_id:"answer-op"};
      if (url.endsWith("/fork")) body={operation_id:"fork-op",target_thread_id:"forked-thread"};
      if (url.endsWith("/rewrite")) {
        if (options.rewriteFailure) return new Response(JSON.stringify({error:"Native history unavailable; original conversation was kept"}),{status:409,headers:{"Content-Type":"application/json"}});
        const target = url.split("/").at(-2);
        const index = eventLog.findIndex(item => (item as {event_id:string}).event_id === target);
        eventLog = [...eventLog.slice(0,index),{event_id:"replacement",stream_id:thread.id,sequence:100,kind:"gemini.user.message",occurred_at:thread.updated_at,payload:{text:input.prompt,operation_id:"rewrite",history_rewritten:true}},{event_id:"answer",stream_id:thread.id,sequence:101,kind:"gemini.message",occurred_at:thread.updated_at,payload:{role:"assistant",content:"Revised answer",operation_id:"rewrite"}}];
      }
    }
    else if (url === "/api/operations/answer-op") body={status:options.answerFailure ? "failed" : "succeeded",result:"Question expired"};
    else if (url === "/api/operations/fork-op") body={status:"succeeded"};
    else if (url.endsWith("/gemini/models")) { modelRequests.push(url); if (upstreamState === "failed") return new Response(JSON.stringify({error:"Upstream unavailable"}),{status:502,headers:{"Content-Type":"application/json"}}); if (upstreamState === "unconfigured") body = {models:[],configured:false}; else body = {configured:true,models:[{id:modelRequests.length === 1 ? "gemini-upstream-current" : "gemini-upstream-new",name:"Upstream"}]}; }
    else if (url.includes("/gemini/threads/thread-1/events")) { const after = new URL(url,"http://localhost").searchParams.get("after"); body = after === null ? eventLog.slice(-500) : eventLog.filter(item => Number((item as {sequence:number}).sequence) > Number(after)).slice(0,500); }
    else if (url === "/api/workspaces") body = [{id:"workspace-1",project_name:"Project",server_name:"Production",path:"/project",status:"ready"},{id:"workspace-2",project_name:"Empty project",server_name:"Production",path:"/empty",status:"ready"}];
    else if (url.includes("/gemini/threads?") && selected) body = [{...currentThread,model}];
    else if (url === "/api/gemini/threads/thread-1") body = {...currentThread,model};
    return new Response(JSON.stringify(body),{status:200,headers:{"Content-Type":"application/json"}});
  }));
  const onSelectThread = vi.fn();
  render(<I18nProvider><GeminiPage realtime={0} notify={vi.fn()} selectedThreadID={selected ? thread.id : ""} onSelectThread={onSelectThread} /></I18nProvider>);
  return {user:userEvent.setup(),requests,modelRequests,onSelectThread};
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); window.localStorage.clear(); });

test("sends a correction to the current Gemini turn while retaining the interrupt button", async () => {
  const ready = [{event_id:"ready",sequence:1,kind:"gemini.steer.ready",stream_id:thread.id,occurred_at:thread.updated_at,payload:{operation_id:"op1"}}];
  const {user,requests}=setup(true,"auto","configured",ready,{status:"running",steerSupported:true});
  const input=await screen.findByRole("textbox",{name:"Ask Gemini to work on this project…"});
  await waitFor(()=>expect(input).toBeEnabled());
  await user.type(input,"Focus on the storage bug");
  await user.click(screen.getByRole("button",{name:"Send correction"}));
  await screen.findByText("Waiting for the next model request");
  expect(input).toHaveValue("");
  expect(screen.getByRole("button",{name:"Interrupt"})).toBeEnabled();
  expect(requests).toHaveLength(1);
  expect(requests[0]).toMatchObject({url:"/api/gemini/threads/thread-1/steer",body:{prompt:"Focus on the storage bug",operation_id:"op1",request_id:expect.any(String)}});
  expect(screen.getByRole("combobox",{name:"Gemini model"})).toBeDisabled();
});

test("retains the draft and reuses its request ID if a correction fails", async()=>{
  const {user,requests}=setup(true,"auto","configured",[],{status:"running",steerSupported:true,steerReady:true,steerFailure:true});
  const input=await screen.findByRole("textbox",{name:"Ask Gemini to work on this project…"});
  await waitFor(()=>expect(input).toBeEnabled());
  await user.type(input,"Keep my changes");
  await user.click(screen.getByRole("button",{name:"Send correction"}));
  await screen.findByText("Turn is no longer running");
  expect(input).toHaveValue("Keep my changes");
  await user.click(screen.getByRole("button",{name:"Send correction"}));
  await waitFor(()=>expect(requests).toHaveLength(2));
  expect(requests[0].body).toEqual(requests[1].body);
});

test("does not steer a new Gemini turn using a previous turn's readiness",async()=>{
  const ready=[{event_id:"old-ready",sequence:1,kind:"gemini.steer.ready",stream_id:thread.id,occurred_at:thread.updated_at,payload:{operation_id:"old"}}];
  setup(true,"auto","configured",ready,{status:"running",steerSupported:true});
  expect(await screen.findByRole("textbox",{name:"Ask Gemini to work on this project…"})).toBeDisabled();
  expect(screen.getByRole("button",{name:"Send correction"})).toBeDisabled();
});

test("resuming hides 25 replayed commands and shows the 29 actual completed commands", async () => {
  const events: Array<Record<string, unknown>> = [];
  const append = (kind: string, operation_id: string, payload: Record<string, unknown>) => events.push({
    event_id: String(events.length + 1), sequence: events.length + 1, stream_id: thread.id,
    occurred_at: thread.updated_at, kind, payload: { ...payload, operation_id },
  });
  append("gemini.init", "old", { session_id: "native-1" });
  for (let i = 0; i < 25; i++) {
    append("gemini.tool_use", "old", { tool_id: `read_file__call_${i}`, tool_name: `old-${i}.ts`, parameters: [] });
    append("gemini.tool_result", "old", { tool_id: `read_file__call_${i}`, status: "completed", output: [] });
  }
  append("gemini.user.message", "op1", { text: "Continue" });
  append("gemini.init", "op1", { session_id: "native-1" });
  for (let i = 0; i < 25; i++) append("gemini.tool_use", "op1", { tool_id: `read_file__call_${i}`, tool_name: "ReadFile", parameters: [] });
  for (let i = 25; i < 54; i++) {
    append("gemini.tool_use", "op1", { tool_id: `read_file__call_${i}`, tool_name: `new-${i}.ts`, parameters: [] });
    append("gemini.tool_result", "op1", { tool_id: `read_file__call_${i}`, status: "completed", output: [] });
  }
  const { user } = setup(true, "auto", "configured", events, { status: "running" });
  const title = await screen.findByText("Ran 29 commands");
  expect(screen.queryByText("Ran 54 commands")).not.toBeInTheDocument();
  expect(screen.queryByText("ReadFile")).not.toBeInTheDocument();
  await user.click(title);
  const group = title.closest("details")!;
  expect(within(group).getAllByText("Completed")).toHaveLength(29);
  expect(group.querySelectorAll(".gemini-tool-status.running")).toHaveLength(0);
  expect(group.querySelectorAll("time")).toHaveLength(29);
  // Tool completion does not mean the model's turn has completed.
  expect(screen.getByRole("button", { name: "Interrupt" })).toBeEnabled();
});

test("renders one expandable tool record per call with separate parameters and output", async () => {
  const events = [
    { kind: "gemini.tool_use", payload: { tool_id: "read_file__call_1", tool_name: "README.md", parameters: [{type:"content",content:{type:"text",text:"Read instructions"}}] } },
    { kind: "gemini.tool_result", payload: { tool_id: "read_file__call_1", tool_name: "README.md", status: "completed", output: [{type:"content",content:{type:"text",text:"File contents"}}] } },
    { kind: "gemini.tool_use", payload: { tool_id: "run_shell_command__call_2", tool_name: "printf hello", parameters: [] } },
    { kind: "gemini.tool_result", payload: { tool_id: "run_shell_command__call_2", status: "failed", output: "Command failed" } },
  ].map((e, index) => ({...e, event_id:String(index),stream_id:thread.id,sequence:index + 1,occurred_at:thread.updated_at,payload:{...e.payload,operation_id:"op1"}}));
  const { user } = setup(true, "auto", "configured", events);
  const groupTitle = await screen.findByText("Ran 2 commands");
  const group = groupTitle.closest("details")!;
  expect(group).not.toHaveAttribute("open");
  await user.click(groupTitle);
  expect(group).toHaveAttribute("open");
  const title = await screen.findByText("README.md");
  expect(screen.getAllByText("README.md")).toHaveLength(1);
  const tool = title.closest("details")!;
  expect(within(tool).getByText("Completed")).toBeInTheDocument();
  expect(tool.querySelector(".lucide-file-text")).toBeTruthy();
  expect(tool.querySelector("time")).toHaveAttribute("datetime", thread.updated_at);
  expect(tool.querySelector("time")).toHaveTextContent(formatTime(thread.updated_at));
  await user.click(title);
  expect(tool).toHaveAttribute("open");
  expect(within(tool).getByText("Parameters")).toBeVisible();
  expect(within(tool).getByText("Read instructions")).toBeVisible();
  expect(within(tool).getByText("File contents")).toBeVisible();
  const failed = screen.getByText("printf hello").closest("details")!;
  expect(within(failed).getByText("Failed")).toBeInTheDocument();
  expect(failed.querySelector(".lucide-square-terminal")).toBeTruthy();
  expect(failed.querySelector("time")).toHaveTextContent(formatTime(thread.updated_at));
  await user.click(groupTitle);
  expect(group).not.toHaveAttribute("open");
});

test("renders separate collapsed command groups around Gemini replies", async () => {
  const events = [
    { kind: "gemini.message", payload: { role: "assistant", content: "Starting" } },
    { kind: "gemini.tool_use", payload: { tool_id: "run_shell_command__1", tool_name: "pwd" } },
    { kind: "gemini.tool_use", payload: { tool_id: "run_shell_command__2", tool_name: "ls" } },
    { kind: "gemini.message", payload: { role: "assistant", content: "Next step" } },
    { kind: "gemini.tool_use", payload: { tool_id: "run_shell_command__3", tool_name: "git status" } },
  ].map((e, index) => ({ ...e, event_id: String(index), stream_id: thread.id, sequence: index + 1, occurred_at: `2026-10-25T00:00:0${index}Z`, payload: { ...e.payload, operation_id: "op1" } }));
  const { user } = setup(true, "auto", "configured", events);
  const first = (await screen.findByText("Ran 2 commands")).closest("details")!;
  const second = screen.getByText("Ran 1 commands").closest("details")!;
  expect(first).not.toHaveAttribute("open");
  expect(second).not.toHaveAttribute("open");
  await user.click(within(first).getByText("Ran 2 commands"));
  expect(within(first).getByText("pwd")).toBeVisible();
  expect(within(first).getByText("ls")).toBeVisible();
  expect(second).not.toHaveAttribute("open");
  expect(first.querySelectorAll("time")).toHaveLength(2);
  expect(first.querySelectorAll("time")[1]).toHaveTextContent(formatTime(events[2].occurred_at));
});

test("shows only fetched models and creates a session with a selected model",async () => {
  const {user,requests} = setup();
  await user.click(screen.getAllByRole("button",{name:"New Gemini session"})[0]);
  const dialog = screen.getByRole("dialog");
  let models = within(dialog).getByRole("combobox",{name:"Gemini model"});
  expect(within(models).getAllByRole("option").map(option => option.textContent)).toEqual(["Select a model","Custom model"]);
  await user.selectOptions(within(dialog).getByRole("combobox",{name:"Workspace"}),"workspace-1");
  models = within(dialog).getByRole("combobox",{name:"Gemini model"});
  await within(models).findByRole("option",{name:"Upstream · gemini-upstream-current"});
  expect(within(models).getAllByRole("option").map(option => option.textContent)).toEqual(["Select a model","Upstream · gemini-upstream-current","Custom model"]);
  await user.selectOptions(models,"gemini-upstream-current");
  await user.click(within(dialog).getByRole("button",{name:"New Gemini session"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads",body:{workspace_id:"workspace-1",model:"gemini-upstream-current",approval_mode:"default",thinking_level:""}}));
});

test("creates a session with never approve selected",async () => {
  const {user,requests} = setup();
  await user.click(screen.getAllByRole("button",{name:"New Gemini session"})[0]);
  const dialog = screen.getByRole("dialog");
  await user.selectOptions(within(dialog).getByRole("combobox",{name:"Workspace"}),"workspace-1");
  await within(dialog).findByRole("option",{name:"Upstream · gemini-upstream-current"});
  await user.selectOptions(within(dialog).getByRole("combobox",{name:"Tool permissions"}),"yolo");
  expect(within(dialog).getByRole("option",{name:"Never approve"})).toBeInTheDocument();
  await user.click(within(dialog).getByRole("button",{name:"New Gemini session"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads",body:{workspace_id:"workspace-1",model:"gemini-upstream-current",approval_mode:"yolo",thinking_level:""}}));
});

test("switches an existing session model for the next turn",async () => {
  const {user,requests} = setup(true);
  const models = await screen.findByRole("combobox",{name:"Gemini model"});
  await within(models).findByRole("option",{name:"Upstream · gemini-upstream-current"});
  await user.selectOptions(models,"gemini-upstream-current");
  await user.selectOptions(screen.getByRole("combobox",{name:"Tool permissions"}),"yolo");
  await user.type(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"}),"Hello");
  await user.click(screen.getByRole("button",{name:"Send"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads/thread-1/turns",body:{prompt:"Hello",model:"gemini-upstream-current",approval_mode:"yolo",thinking_level:""}}));
});

test("preserves and edits a custom model name",async () => {
  const {user,requests} = setup(true,"gemini-custom-model");
  expect(await screen.findByRole("combobox",{name:"Gemini model"})).toHaveValue("__custom__");
  const name = screen.getByRole("textbox",{name:"Model name"});
  expect(name).toHaveValue("gemini-custom-model");
  await user.clear(name); await user.type(name,"gemini-custom-next");
  await user.type(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"}),"Hello");
  await user.click(screen.getByRole("button",{name:"Send"}));
  await waitFor(() => expect(requests[0].body).toMatchObject({model:"gemini-custom-next"}));
});

test("automatically fetches upstream models once on opening a session and supports refresh",async () => {
  const {user,modelRequests,requests} = setup(true);
  await screen.findByRole("option",{name:"Upstream · gemini-upstream-current"});
  expect(modelRequests).toEqual(["/api/workspaces/workspace-1/gemini/models"]);
  await user.selectOptions(screen.getByRole("combobox",{name:"Gemini model"}),"gemini-upstream-current");
  await user.type(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"}),"Hello");
  expect(modelRequests).toHaveLength(1);
  await user.click(screen.getByRole("button",{name:"Send"}));
  await waitFor(() => expect(requests[0].body).toMatchObject({model:"gemini-upstream-current"}));
  await user.click(screen.getByRole("button",{name:"Refresh model list"}));
  await screen.findByRole("option",{name:"Upstream · gemini-upstream-new"});
  expect(modelRequests).toHaveLength(2);
  expect(screen.getByRole("textbox",{name:"Model name"})).toHaveValue("gemini-upstream-current");
});

test("loads upstream models after selecting a workspace for a new session",async () => {
  const {user,modelRequests} = setup();
  await user.click(screen.getAllByRole("button",{name:"New Gemini session"})[0]);
  const dialog = screen.getByRole("dialog");
  expect(modelRequests).toEqual([]);
  await user.selectOptions(within(dialog).getByRole("combobox",{name:"Workspace"}),"workspace-1");
  await within(dialog).findByRole("option",{name:"Upstream · gemini-upstream-current"});
  expect(modelRequests).toEqual(["/api/workspaces/workspace-1/gemini/models"]);
});

test.each(["failed","unconfigured"])("keeps manual model selection available when upstream is %s",async state => {
  const {user,requests} = setup(true,"auto",state);
  if (state === "failed") await screen.findByText("Upstream unavailable");
  else await screen.findByText("Configure Gemini API in server credentials to load upstream models.");
  const models = screen.getByRole("combobox",{name:"Gemini model"});
  for (const alias of ["auto","pro","flash","flash-lite"]) expect(within(models).queryByRole("option",{name:alias})).not.toBeInTheDocument();
  await user.selectOptions(models,"__custom__");
  await user.type(screen.getByRole("textbox",{name:"Model name"}),"gemini-manual");
  await user.type(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"}),"Hello");
  await user.click(screen.getByRole("button",{name:"Send"}));
  await waitFor(() => expect(requests[0].body).toMatchObject({model:"gemini-manual"}));
});


test("starts a new independent session in the current project with its model preselected",async () => {
  const {user,requests} = setup(true,"gemini-upstream-current");
  await user.click(await screen.findByRole("button",{name:"New session in this project"}));
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByRole("combobox",{name:"Workspace"})).toHaveValue("workspace-1");
  expect(await within(dialog).findByRole("textbox",{name:"Model name"})).toHaveValue("gemini-upstream-current");
  await user.click(within(dialog).getByRole("button",{name:"New Gemini session"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads",body:{workspace_id:"workspace-1",model:"gemini-upstream-current",approval_mode:"default",thinking_level:""}}));
});

test("hides projects without sessions but allows their first session through the global new button",async () => {
  const {user,requests} = setup();
  expect(screen.queryByText("Connect Gemini CLI")).not.toBeInTheDocument();
  await screen.findByText("No Gemini sessions yet");
  expect(screen.queryByRole("button",{name:/Workspace menu/})).not.toBeInTheDocument();
  expect(screen.queryByRole("button",{name:/Empty project/})).not.toBeInTheDocument();
  await user.click(screen.getAllByRole("button",{name:"New Gemini session"})[0]);
  const dialog = screen.getByRole("dialog");
  await user.selectOptions(within(dialog).getByRole("combobox",{name:"Workspace"}),"workspace-2");
  await waitFor(() => expect(within(dialog).getByRole("button",{name:"New Gemini session"})).toBeEnabled());
  await user.click(within(dialog).getByRole("button",{name:"New Gemini session"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads",body:{workspace_id:"workspace-2",model:"gemini-upstream-current",approval_mode:"default",thinking_level:""}}));
});

test.each(["button","right-click"])("creates a separate session directly from the Codex-style project menu via %s",async trigger => {
  const {user,requests,onSelectThread} = setup(true,"gemini-upstream-current");
  const project = await screen.findByRole("button",{name:/Project · project.*1/});
  expect(screen.queryByRole("button",{name:/Empty project/})).not.toBeInTheDocument();
  expect(screen.queryByRole("button",{name:"New session in Project · Production"})).not.toBeInTheDocument();
  if (trigger === "right-click") await user.pointer({target:project,keys:"[MouseRight]"});
  else {
    const menu = screen.getByRole("button",{name:"Workspace menu for Project · project"});
    menu.focus(); await user.keyboard("{Enter}");
  }
  await user.click(await screen.findByRole("menuitem",{name:"New session"}));
  await waitFor(() => expect(requests).toEqual([{url:"/api/gemini/threads",body:{workspace_id:"workspace-1",model:"gemini-upstream-current",approval_mode:"default",thinking_level:""}}]));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(onSelectThread).toHaveBeenCalledWith("thread-created");
});

test("filters and collapses project sessions without losing the open conversation",async () => {
  const {user} = setup(true);
  const search = screen.getByRole("searchbox",{name:"Search sessions or projects"});
  await screen.findByRole("button",{name:"Workspace menu for Project · project"});
  await user.type(search,"missing-project");
  expect(screen.queryByRole("button",{name:"Workspace menu for Project · project"})).not.toBeInTheDocument();
  expect(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"})).toBeInTheDocument();
  await user.clear(search);
  await user.type(search,"Production");
  const group = await screen.findByRole("button",{name:/Project · project.*1/});
  expect(group).toHaveAttribute("aria-expanded","true");
  await user.click(group);
  expect(group).toHaveAttribute("aria-expanded","false");
  expect(screen.queryByRole("button",{name:"Delete Gemini session"})).not.toBeInTheDocument();
});

test("keeps session management in the Codex-style task menu",async () => {
  const {user} = setup(true);
  await user.click(await screen.findByRole("button",{name:"Task menu for Gemini"}));
  await user.click(screen.getByRole("menuitem",{name:"Rename session"}));
  expect(screen.getByRole("dialog",{name:"Rename session"})).toBeInTheDocument();
});

test("renders legacy usage as known counts and unavailable values",async () => {
  setup(true,"gemini-upstream-current","configured",[{event_id:"usage",stream_id:"thread-1",sequence:1,kind:"gemini.result",occurred_at:thread.updated_at,payload:{stats:{input_tokens:"[REDACTED]",input:321,output_tokens:"[REDACTED]",tool_calls:0}}}]);
  expect(await screen.findByText("Input 321 tokens · Output — tokens · 0 tool calls")).toBeInTheDocument();
  expect(screen.queryByText(/\[REDACTED\]/)).not.toBeInTheDocument();
});


test("exports all pages of a conversation rather than only the visible recent history",async () => {
  const events = Array.from({length:501},(_,index) => ({event_id:String(index+1),stream_id:"thread-1",sequence:index+1,kind:"gemini.user.message",occurred_at:thread.updated_at,payload:{text:`Prompt ${index+1}`,operation_id:String(index+1)}}));
  const {user} = setup(true,"gemini-upstream-current","configured",events);
  const createURL = vi.fn((_blob: Blob | MediaSource) => "blob:test-export");
  const revokeURL = vi.fn();
  vi.stubGlobal("URL",class extends URL { static createObjectURL = createURL; static revokeObjectURL = revokeURL; });
  const clicked = vi.spyOn(HTMLAnchorElement.prototype,"click").mockImplementation(() => {});
  await user.click(await screen.findByRole("button",{name:"Export conversation"}));
  await waitFor(() => expect(createURL).toHaveBeenCalledOnce());
  const text = await new Promise<string>((resolve,reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.onerror = reject; reader.readAsText(createURL.mock.calls[0][0] as Blob); });
  expect(text).toContain("Prompt 1\n");
  expect(text).toContain("Prompt 501");
  expect(clicked).toHaveBeenCalledOnce();
  expect(revokeURL).toHaveBeenCalledWith("blob:test-export");
  expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).includes("after=500"))).toBe(true);
});

const rewriteEvents = [
  {event_id:"keep",stream_id:thread.id,sequence:1,kind:"gemini.user.message",occurred_at:thread.updated_at,payload:{text:"Keep this earlier prompt",operation_id:"one"}},
  {event_id:"edit",stream_id:thread.id,sequence:2,kind:"gemini.user.message",occurred_at:thread.updated_at,payload:{text:"Original prompt",operation_id:"two"}},
  {event_id:"old-answer",stream_id:thread.id,sequence:3,kind:"gemini.message",occurred_at:thread.updated_at,payload:{role:"assistant",content:"Discard this later answer",operation_id:"two"}}
];

test("edits and resends at the selected message and replaces the stale tail",async () => {
  Element.prototype.scrollIntoView = vi.fn();
  const {user,requests} = setup(true,"gemini-upstream-current","configured",rewriteEvents);
  await screen.findByText("Discard this later answer");
  await user.click(screen.getAllByRole("button",{name:"Edit and resend"})[1]);
  const prompt = screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"});
  expect(prompt).toHaveValue("Original prompt");
  expect(screen.getByText("Editing an earlier message; later replies will be discarded")).toBeInTheDocument();
  await user.clear(prompt); await user.type(prompt,"Updated prompt");
  await user.selectOptions(screen.getByRole("combobox",{name:"Tool permissions"}),"yolo");
  await user.click(screen.getByRole("button",{name:"Send"}));
  await screen.findByText("Revised answer");
  expect(requests).toContainEqual({url:"/api/gemini/threads/thread-1/events/edit/rewrite",body:{prompt:"Updated prompt",model:"gemini-upstream-current",approval_mode:"yolo",thinking_level:""}});
  expect(screen.getByText("Keep this earlier prompt")).toBeInTheDocument();
  expect(screen.getByText("Updated prompt")).toBeInTheDocument();
  expect(screen.queryByText("Original prompt")).not.toBeInTheDocument();
  expect(screen.queryByText("Discard this later answer")).not.toBeInTheDocument();
});

test("cancels editing without changing conversation history",async () => {
  Element.prototype.scrollIntoView = vi.fn();
  const {user,requests} = setup(true,"gemini-upstream-current","configured",rewriteEvents);
  await screen.findByText("Discard this later answer");
  await user.click(screen.getAllByRole("button",{name:"Edit and resend"})[1]);
  await user.click(screen.getByRole("button",{name:"Cancel editing"}));
  expect(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"})).toHaveValue("");
  expect(screen.getByText("Discard this later answer")).toBeInTheDocument();
  expect(requests).toEqual([]);
});

test("keeps the draft and original history when a rewrite is rejected",async () => {
  Element.prototype.scrollIntoView = vi.fn();
  const {user} = setup(true,"gemini-upstream-current","configured",rewriteEvents,{rewriteFailure:true});
  await screen.findByText("Discard this later answer");
  await user.click(screen.getAllByRole("button",{name:"Edit and resend"})[1]);
  await user.click(screen.getByRole("button",{name:"Send"}));
  await screen.findByText("Native history unavailable; original conversation was kept");
  expect(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"})).toHaveValue("Original prompt");
  expect(screen.getByText("Discard this later answer")).toBeInTheDocument();
  expect(screen.getByRole("button",{name:"Cancel editing"})).toBeInTheDocument();
});

test.each([{rewriteSupported:false},{status:"running"}])("blocks edits for unsupported or active Agents: %j",async options => {
  setup(true,"gemini-upstream-current","configured",rewriteEvents,options);
  await screen.findByText("Original prompt");
  for (const button of screen.getAllByRole("button",{name:"Edit and resend"})) expect(button).toBeDisabled();
});


test("offers Gemini project management and task utilities", async()=>{
 const {user}=setup(true);
 await user.click(await screen.findByRole("button",{name:"Workspace menu for Project · project"}));
 const projectMenu=screen.getByRole("menu");
 for(const name of ["Pin project","Rename project","Create permanent worktree","Archive all project tasks","Hide project"]) expect(within(projectMenu).getByRole("menuitem",{name})).toBeEnabled();
 await user.keyboard("{Escape}");
 await user.click(screen.getByRole("button",{name:"Task menu for Gemini"}));
 const taskMenu=screen.getByRole("menu");
 for(const name of ["Pin task","Create scheduled task","Continue in new task","Continue in new worktree","Copy working directory","Copy Wio session ID","Copy Gemini session ID","Copy deep link","Open in new window"]) expect(within(taskMenu).getByRole("menuitem",{name})).toBeEnabled();
});

test("branches a completed Gemini reply into an independent task",async()=>{
 const events=[{event_id:"reply-1",sequence:1,stream_id:"thread-1",kind:"gemini.message",occurred_at:thread.updated_at,payload:{role:"assistant",content:"Completed answer",operation_id:"op1"}},{event_id:"result-1",sequence:2,stream_id:"thread-1",kind:"gemini.result",occurred_at:thread.updated_at,payload:{status:"success",operation_id:"op1"}}];
 const {user,requests,onSelectThread}=setup(true,"gemini-upstream-current","configured",events);
 await screen.findByText("Completed answer");
 const article=screen.getByText("Completed answer").closest("article")!;
 await user.click(within(article).getByRole("button",{name:"Message actions"}));
 await user.click(screen.getByRole("menuitem",{name:"Branch to new chat"}));
 await waitFor(()=>expect(requests).toContainEqual({url:"/api/gemini/threads/thread-1/fork",body:{event_id:"reply-1"}}));
 await waitFor(()=>expect(onSelectThread).toHaveBeenCalledWith("forked-thread"));
});

test("creates a Gemini scheduled task with Gemini approval options",async()=>{
 const {user,requests}=setup(true);
 await user.click(await screen.findByRole("button",{name:"Task menu for Gemini"}));
 await user.click(screen.getByRole("menuitem",{name:"Create scheduled task"}));
 const dialog=screen.getByRole("dialog");
 expect(within(dialog).getByRole("combobox",{name:"Agent"})).toHaveValue("gemini");
 expect(within(dialog).queryByRole("combobox",{name:"Reasoning effort"})).not.toBeInTheDocument();
 await user.selectOptions(within(dialog).getByRole("combobox",{name:"Tool permissions"}),"yolo");
 await user.type(within(dialog).getByRole("textbox",{name:"Name"}),"Gemini review");
 await user.type(within(dialog).getByRole("textbox",{name:"Prompt"}),"Review this project");
 await user.click(within(dialog).getByRole("button",{name:"Save"}));
 await waitFor(()=>expect(requests.some(r=>r.url==="/api/scheduled-tasks"&&(r.body as {agent_kind:string;approval_mode:string}).agent_kind==="gemini"&&(r.body as {approval_mode:string}).approval_mode==="yolo")).toBe(true));
});


test("displays the upstream model thinking preset without offering conflicting levels",async()=>{
 setup(true,"gemini-3.8-flash-high");
 const picker=await screen.findByRole("combobox",{name:"Thinking level"});
 expect(picker).toBeDisabled();
 expect(within(picker).getByRole("option",{name:"High (model preset)"})).toBeInTheDocument();
 expect(within(picker).queryByRole("option",{name:"Low"})).not.toBeInTheDocument();
});

test("sends an explicitly selected native thinking level with the next turn",async()=>{
 const {user,requests}=setup(true,"gemini-3-flash-preview");
 const picker=await screen.findByRole("combobox",{name:"Thinking level"});
 await user.selectOptions(picker,"medium");
 await user.type(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"}),"Think carefully");
 await user.click(screen.getByRole("button",{name:"Send"}));
 await waitFor(()=>expect(requests).toContainEqual({url:"/api/gemini/threads/thread-1/turns",body:{prompt:"Think carefully",model:"gemini-3-flash-preview",approval_mode:"default",thinking_level:"medium"}}));
});

const pastedImage = "data:image/png;base64,c21hbGwtcG5n";
test("pastes an image, sends it without text and keeps its history preview", async () => {
  vi.mocked(compressImage).mockResolvedValue(pastedImage);
  const { user, requests } = setup(true);
  const prompt = await screen.findByRole("textbox", { name: "Ask Gemini to work on this project…" });
  const file = new File(["image"], "clipboard.png", { type: "image/png" });
  fireEvent.paste(prompt, { clipboardData: { items: [{ type: "image/png", getAsFile: () => file }] } });
  await waitFor(() => expect(document.querySelector(".composer-images img")).toHaveAttribute("src", pastedImage));
  expect(compressImage).toHaveBeenCalledWith(file, expect.objectContaining({ signal: expect.any(AbortSignal) }));
  await user.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(requests[0]).toMatchObject({url:"/api/gemini/threads/thread-1/turns",body:{prompt:"",images:[{data_url:pastedImage}]}}));
  expect(document.querySelector(".composer-images")).not.toBeInTheDocument();
});

test("preserves image attachments when editing an earlier message", async () => {
  const events=[{event_id:"with-image",sequence:1,kind:"gemini.user.message",occurred_at:thread.updated_at,payload:{text:"Inspect it",images:[{data_url:pastedImage}],operation_id:"old"}}];
  const { user, requests }=setup(true,"auto","configured",events);
  await user.click(await screen.findByRole("button",{name:"Edit and resend"}));
  expect(document.querySelector(".composer-images img")).toHaveAttribute("src",pastedImage);
  await user.click(screen.getByRole("button",{name:"Send"}));
  await waitFor(()=>expect(requests[0]).toMatchObject({url:"/api/gemini/threads/thread-1/events/with-image/rewrite",body:{images:[{data_url:pastedImage}]}}));
});

const interactiveQuestion = {event_id:"question-1",sequence:1,kind:"gemini.question",occurred_at:thread.updated_at,payload:{operation_id:"op1",questions:[{id:"color",header:"Color",question:"Which color?",isOther:true,options:[{label:"Blue",description:"Ocean"},{label:"Green",description:"Forest"}]}]}};
test("waits for an explicit answer and confirms Agent delivery", async () => {
  const {user,requests}=setup(true,"auto","configured",[interactiveQuestion],{status:"running"});
  const card=await screen.findByRole("region",{name:"Gemini needs your input"});
  const submit=within(card).getByRole("button",{name:"Submit answers"});expect(submit).toBeDisabled();expect(requests).toHaveLength(0);
  await user.click(within(card).getByRole("radio",{name:"Blue Ocean"}));await user.click(submit);
  await waitFor(()=>expect(requests[0]).toEqual({url:"/api/gemini/threads/thread-1/questions/question-1/answer",body:{answers:{color:{answers:["Blue"]}}}}));
  expect(await within(card).findByRole("button",{name:"Answers sent"})).toBeDisabled();
});

test.each(["idle","interrupting"])("expires question cards when the turn is %s", async status => {
  setup(true,"auto","configured",[interactiveQuestion],{status});
  expect(await screen.findByRole("radio",{name:"Blue Ocean"})).toBeDisabled();
});

test("shows answer delivery failure without marking the answer sent", async () => {
  const {user}=setup(true,"auto","configured",[interactiveQuestion],{status:"running",answerFailure:true});
  await user.click(await screen.findByRole("radio",{name:"Blue Ocean"}));await user.click(screen.getByRole("button",{name:"Submit answers"}));
  expect(await screen.findByRole("alert")).toHaveTextContent("Question expired");expect(screen.queryByRole("button",{name:"Answers sent"})).not.toBeInTheDocument();
});
