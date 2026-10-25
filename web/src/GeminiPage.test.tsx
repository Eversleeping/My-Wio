import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import GeminiPage from "./pages/GeminiPage";
import { I18nProvider } from "./i18n";

const thread = {id:"thread-1",workspace_id:"workspace-1",project_name:"Project",server_name:"Production",server_status:"online",path:"/project",title:"Gemini",status:"idle",model:"auto",approval_mode:"default",archived_at:null,updated_at:"2026-10-25T00:00:00Z",created_at:"2026-10-25T00:00:00Z"};
function setup(selected = false, model = "auto", upstreamState = "configured", events: unknown[] = []) {
  window.localStorage.setItem("wio_language","en");
  const requests: Array<{url:string; body:unknown}> = [];
  const modelRequests: string[] = [];
  vi.stubGlobal("fetch",vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    let body: unknown = [];
    if (init.method === "POST") { requests.push({url,body:JSON.parse(String(init.body))}); body = {...thread,model,id:"thread-created"}; }
    else if (url.endsWith("/gemini/models")) { modelRequests.push(url); if (upstreamState === "failed") return new Response(JSON.stringify({error:"Upstream unavailable"}),{status:502,headers:{"Content-Type":"application/json"}}); if (upstreamState === "unconfigured") body = {models:[],configured:false}; else body = {configured:true,models:[{id:modelRequests.length === 1 ? "gemini-upstream-current" : "gemini-upstream-new",name:"Upstream"}]}; }
    else if (url.includes("/gemini/threads/thread-1/events")) { const after = new URL(url,"http://localhost").searchParams.get("after"); body = after === null ? events.slice(-500) : events.filter(item => Number((item as {sequence:number}).sequence) > Number(after)).slice(0,500); }
    else if (url === "/api/workspaces") body = [{id:"workspace-1",project_name:"Project",server_name:"Production",path:"/project",status:"ready"},{id:"workspace-2",project_name:"Empty project",server_name:"Production",path:"/empty",status:"ready"}];
    else if (url.includes("/gemini/threads?") && selected) body = [{...thread,model}];
    else if (url === "/api/gemini/threads/thread-1") body = {...thread,model};
    return new Response(JSON.stringify(body),{status:200,headers:{"Content-Type":"application/json"}});
  }));
  const onSelectThread = vi.fn();
  render(<I18nProvider><GeminiPage realtime={0} notify={vi.fn()} selectedThreadID={selected ? thread.id : ""} onSelectThread={onSelectThread} /></I18nProvider>);
  return {user:userEvent.setup(),requests,modelRequests,onSelectThread};
}
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); window.localStorage.clear(); });

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
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads",body:{workspace_id:"workspace-1",model:"gemini-upstream-current",approval_mode:"default"}}));
});

test("switches an existing session model for the next turn",async () => {
  const {user,requests} = setup(true);
  const models = await screen.findByRole("combobox",{name:"Gemini model"});
  await within(models).findByRole("option",{name:"Upstream · gemini-upstream-current"});
  await user.selectOptions(models,"gemini-upstream-current");
  await user.type(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"}),"Hello");
  await user.click(screen.getByRole("button",{name:"Send"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads/thread-1/turns",body:{prompt:"Hello",model:"gemini-upstream-current",approval_mode:"default"}}));
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
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads",body:{workspace_id:"workspace-1",model:"gemini-upstream-current",approval_mode:"default"}}));
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
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads",body:{workspace_id:"workspace-2",model:"gemini-upstream-current",approval_mode:"default"}}));
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
  await waitFor(() => expect(requests).toEqual([{url:"/api/gemini/threads",body:{workspace_id:"workspace-1",model:"gemini-upstream-current",approval_mode:"default"}}]));
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
