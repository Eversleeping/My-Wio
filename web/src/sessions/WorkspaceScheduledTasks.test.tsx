import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { clearResourceMemory, DataProvider } from "../data/resources";
import { I18nProvider } from "../i18n";
import type { ScheduledTask, Workspace } from "../types";
import type { AISession, Engine } from "./types";
import WorkspaceScheduledTasks from "./WorkspaceScheduledTasks";

const workspace = {id:"w",project_id:"p",project_name:"Project",server_id:"s",server_name:"Server",path:"/srv/project",status:"ready"} as Workspace;
const engines: Engine[] = ["codex","gemini","claude"];
const sessions = engines.map(engine => ({...workspace,id:"same",engine,workspace_id:"w",title:`${engine} session`,model:"",status:"idle",archived_at:null})) as unknown as AISession[];
function task(engine: Engine): ScheduledTask {
  return {id:`task-${engine}`,agent_kind:engine,workspace_id:"w",thread_id:"same",thread_title:`${engine} session`,name:`${engine} job`,prompt:"Review changes",schedule:"0 9 * * *",timezone:"UTC",enabled:true,model:"",reasoning_effort:"",approval_mode:engine === "codex" ? "on-request":"default"} as ScheduledTask;
}
function setup({empty=false,initial=false,failRead=false,failWrite=false,failSessions=false,noSessions=false}: {empty?:boolean;initial?:boolean;failRead?:boolean;failWrite?:boolean;failSessions?:boolean;noSessions?:boolean} = {}) {
  clearResourceMemory(); localStorage.setItem("wio_language","en");
  let tasks = empty ? [] : [...engines.map(task),{...task("codex"),id:"other",workspace_id:"other",name:"Other workspace job"}];
  const requests: {path:string;method:string;body:Record<string,unknown> | null}[] = [];
  vi.stubGlobal("fetch",vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input),"http://localhost"), path=url.pathname.replace(/^\/api/,""), method=init.method ?? "GET";
    const body=init.body ? JSON.parse(String(init.body)):null;
    requests.push({path:path+url.search,method,body});
    let data:unknown = {};
    if (path === "/sessions") {
      if (failSessions) { failSessions=false; return new Response(JSON.stringify({error:"Session list unavailable"}),{status:503}); }
      data = noSessions ? {items:[],next_cursor:null} : url.searchParams.has("cursor") ? {items:sessions.slice(1),next_cursor:null} : {items:[sessions[0],{...sessions[0],id:"foreign",workspace_id:"other",title:"Wrong workspace"}],next_cursor:"next"};
    } else if (path === "/scheduled-tasks" && method === "GET") {
      if (failRead) { failRead=false; return new Response(JSON.stringify({error:"Task list unavailable"}),{status:503}); }
      data=tasks;
    } else if (path.startsWith("/scheduled-tasks") && method !== "GET") {
      if (failWrite) { failWrite=false; return new Response(JSON.stringify({error:"Save unavailable"}),{status:503}); }
      if (method === "POST") { const created={...task(body.agent_kind),...body,id:`new-${body.agent_kind}`}; tasks.push(created); data=created; }
      else if (method === "PUT") { tasks=tasks.map(item => path.endsWith(`/${item.id}`) ? {...item,...body}:item); }
      else if (method === "DELETE") tasks=tasks.filter(item=>!path.endsWith(`/${item.id}`));
    } else if (path.endsWith("/codex/models")) data={status:"succeeded",supported:true,data:[]};
    else if (path.endsWith("/gemini/models")) data={models:[{id:"gemini-3.8-flash",name:"Gemini Flash"}]};
    return new Response(JSON.stringify(data),{status:200});
  }));
  const notify=vi.fn(), close=vi.fn();
  render(<I18nProvider><DataProvider revisions={{}}><WorkspaceScheduledTasks workspaceID="w" workspace={workspace} initialThread={initial ? sessions[1]:undefined} revision="0" notify={notify} onClose={close}/></DataProvider></I18nProvider>);
  return {user:userEvent.setup(),requests,notify,close};
}
afterEach(()=>{vi.unstubAllGlobals();localStorage.clear();clearResourceMemory();});

test("shows all three engines for this workspace and edits, toggles and deletes each",async()=>{
  const {user,requests}=setup();
  await screen.findByText("claude job");
  expect(screen.queryByText("Other workspace job")).not.toBeInTheDocument();
  for (const engine of engines) {
    const row=()=>screen.getByText(`${engine} job`).closest("tr")!;
    expect(within(row()).getByText(engine[0].toUpperCase()+engine.slice(1))).toBeInTheDocument();
    await user.click(within(row()).getByRole("button",{name:"Edit"}));
    const dialog=await screen.findByRole("dialog",{name:"Edit scheduled task"});
    expect(within(dialog).getByLabelText("Agent")).toBeDisabled();
    await user.clear(within(dialog).getByLabelText("Prompt"));
    await user.type(within(dialog).getByLabelText("Prompt"),`Updated ${engine}`);
    await user.click(within(dialog).getByRole("button",{name:"Save"}));
    await screen.findByText(`Updated ${engine}`);
    await user.click(within(row()).getByRole("button",{name:"Disable scheduled task"}));
    await waitFor(()=>expect(within(row()).getByRole("button",{name:"Enable scheduled task"})).toBeEnabled());
    await user.click(within(row()).getByRole("button",{name:"Enable scheduled task"}));
    await waitFor(()=>expect(within(row()).getByRole("button",{name:"Disable scheduled task"})).toBeEnabled());
    await user.click(within(row()).getByRole("button",{name:"Delete scheduled task"}));
    expect(requests.some(r=>r.method==="DELETE" && r.path===`/scheduled-tasks/task-${engine}`)).toBe(false);
    const confirmation=await screen.findByRole("dialog",{name:"Delete scheduled task"});
    expect(within(confirmation).getByRole("alert")).toHaveTextContent(`${engine} job`);
    await user.click(within(confirmation).getByRole("button",{name:"Delete scheduled task"}));
    await waitFor(()=>expect(screen.queryByText(`${engine} job`)).not.toBeInTheDocument());
    expect(requests).toContainEqual({path:`/scheduled-tasks/task-${engine}`,method:"PUT",body:{enabled:false}});
    expect(requests).toContainEqual({path:`/scheduled-tasks/task-${engine}`,method:"PUT",body:{enabled:true}});
  }
});

test("creates each engine with its native fields and equal session IDs across cursor pages",async()=>{
  const {user,requests}=setup({empty:true});
  for (const engine of engines) {
    await waitFor(()=>expect(screen.getByRole("button",{name:"New scheduled task"})).toBeEnabled());
    await user.click(screen.getByRole("button",{name:"New scheduled task"}));
    const dialog=await screen.findByRole("dialog",{name:"New scheduled task"}), form=within(dialog);
    await user.selectOptions(form.getByLabelText("Agent"),engine);
    await form.findByLabelText(engine === "codex" ? "Model override":engine === "gemini" ? "Gemini model":"Claude model or alias");
    expect(form.queryByRole("option",{name:/Wrong workspace/})).not.toBeInTheDocument();
    expect(form.getByRole("option",{name:new RegExp(`${engine} session`)})).toHaveValue("same");
    await user.selectOptions(form.getByLabelText("Session"),"same");
    await user.type(form.getByLabelText("Name"),`New ${engine}`);
    await user.type(form.getByLabelText("Prompt"),"Review changes");
    if (engine === "codex") await user.selectOptions(form.getByLabelText("Approve on request"),"never");
    else if (engine === "gemini") { await user.selectOptions(form.getByLabelText("Gemini model"),"gemini-3.8-flash"); await user.selectOptions(form.getByLabelText("Thinking level"),"high"); await user.selectOptions(form.getByLabelText("Tool permissions"),"yolo"); }
    else { await user.type(form.getByLabelText("Claude model or alias"),"custom-model"); await user.selectOptions(form.getByLabelText("Reasoning effort"),"max"); await user.selectOptions(form.getByLabelText("Tool permissions"),"plan"); }
    await user.click(form.getByRole("button",{name:"Save"}));
    await screen.findByText(`New ${engine}`);
    const write=requests.find(r=>r.method==="POST" && r.path==="/scheduled-tasks" && r.body?.agent_kind===engine)!;
    expect(write.body).toEqual(expect.objectContaining({agent_kind:engine,thread_id:"same",schedule:"0 9 * * *",enabled:true,approval_mode:{codex:"never",gemini:"yolo",claude:"plan"}[engine]}));
    if (engine === "gemini") expect(write.body).toEqual(expect.objectContaining({model:"gemini-3.8-flash",thinking_level:"high"}));
    else expect(write.body).not.toHaveProperty("thinking_level");
    if (engine === "claude") expect(write.body).toEqual(expect.objectContaining({model:"custom-model",reasoning_effort:"max"}));
  }
  expect(requests.some(r=>r.path.includes("cursor=next"))).toBe(true);
});

test("opens from a native session and retains its draft after a failed save",async()=>{
  const {user,notify,requests}=setup({initial:true,failWrite:true});
  const dialog=await screen.findByRole("dialog",{name:"New scheduled task"}), form=within(dialog);
  expect(form.getByLabelText("Agent")).toHaveValue("gemini");
  expect(form.getByLabelText("Session")).toHaveValue("same");
  await user.type(form.getByLabelText("Name"),"Retry draft");
  await user.type(form.getByLabelText("Prompt"),"Keep this prompt");
  await user.click(form.getByRole("button",{name:"Save"}));
  await waitFor(()=>expect(notify).toHaveBeenCalledWith("Save unavailable"));
  expect(form.getByLabelText("Prompt")).toHaveValue("Keep this prompt");
  await user.click(form.getByRole("button",{name:"Save"}));
  await screen.findByText("Retry draft");
  expect(requests.filter(r=>r.method==="POST" && r.path==="/scheduled-tasks")).toHaveLength(2);
});

test("retries task and session read failures without silently showing an empty list",async()=>{
  const {user}=setup({failRead:true,failSessions:true});
  await screen.findByText("Task list unavailable");
  await screen.findByText("Session list unavailable");
  expect(screen.getByRole("button",{name:"New scheduled task"})).toBeDisabled();
  for (const button of screen.getAllByRole("button",{name:"Retry"})) await user.click(button);
  await screen.findByText("claude job");
  await waitFor(()=>expect(screen.getByRole("button",{name:"New scheduled task"})).toBeEnabled());
});


test("explains that an empty workspace needs a session before scheduling",async()=>{
  setup({empty:true,noSessions:true});
  await screen.findByText("Create a session in this workspace before adding a scheduled task.");
  expect(screen.getByRole("button",{name:"New scheduled task"})).toBeDisabled();
});
