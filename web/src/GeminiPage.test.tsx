import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import GeminiPage from "./pages/GeminiPage";
import { I18nProvider } from "./i18n";

const thread = {id:"thread-1",workspace_id:"workspace-1",project_name:"Project",server_name:"Production",server_status:"online",path:"/project",title:"Gemini",status:"idle",model:"auto",approval_mode:"default",archived_at:null,updated_at:"2026-10-25T00:00:00Z",created_at:"2026-10-25T00:00:00Z"};
function setup(selected = false, model = "auto") {
  window.localStorage.setItem("wio_language","en");
  const requests: Array<{url:string; body:unknown}> = [];
  vi.stubGlobal("fetch",vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    let body: unknown = [];
    if (init.method === "POST") { requests.push({url,body:JSON.parse(String(init.body))}); body = {...thread,model}; }
    else if (url === "/api/workspaces") body = [{id:"workspace-1",project_name:"Project",server_name:"Production",path:"/project",status:"ready"}];
    else if (url.includes("/gemini/threads?") && selected) body = [{...thread,model}];
    else if (url === "/api/gemini/threads/thread-1") body = {...thread,model};
    return new Response(JSON.stringify(body),{status:200,headers:{"Content-Type":"application/json"}});
  }));
  render(<I18nProvider><GeminiPage realtime={0} notify={vi.fn()} selectedThreadID={selected ? thread.id : ""} onSelectThread={vi.fn()} /></I18nProvider>);
  return {user:userEvent.setup(),requests};
}
afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test("shows every model alias and creates a session with a selected model",async () => {
  const {user,requests} = setup();
  await user.click(screen.getAllByRole("button",{name:"New Gemini session"})[0]);
  const dialog = screen.getByRole("dialog");
  const models = within(dialog).getByRole("combobox",{name:"Gemini model"});
  expect(within(models).getAllByRole("option").map(option => option.textContent)).toEqual(["auto","pro","flash","flash-lite","Custom model"]);
  await user.selectOptions(models,"pro");
  await user.selectOptions(within(dialog).getByRole("combobox",{name:"Workspace"}),"workspace-1");
  await user.click(within(dialog).getByRole("button",{name:"New Gemini session"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads",body:{workspace_id:"workspace-1",model:"pro",approval_mode:"default"}}));
});

test("switches an existing session model for the next turn",async () => {
  const {user,requests} = setup(true);
  const models = await screen.findByRole("combobox",{name:"Gemini model"});
  await user.selectOptions(models,"flash");
  await user.type(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"}),"Hello");
  await user.click(screen.getByRole("button",{name:"Send"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/gemini/threads/thread-1/turns",body:{prompt:"Hello",model:"flash",approval_mode:"default"}}));
});

test("preserves and edits a custom model name",async () => {
  const {user,requests} = setup(true,"gemini-custom-model");
  expect(await screen.findByRole("combobox",{name:"Gemini model"})).toHaveValue("custom");
  const name = screen.getByRole("textbox",{name:"Model name"});
  expect(name).toHaveValue("gemini-custom-model");
  await user.clear(name); await user.type(name,"gemini-custom-next");
  await user.type(screen.getByRole("textbox",{name:"Ask Gemini to work on this project…"}),"Hello");
  await user.click(screen.getByRole("button",{name:"Send"}));
  await waitFor(() => expect(requests[0].body).toMatchObject({model:"gemini-custom-next"}));
});
