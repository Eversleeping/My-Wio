import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ServersPage } from "./pages/ServersPage";
import { I18nProvider } from "./i18n";

const profile = { id:"gemini-1", kind:"gemini", name:"Gemini", model:"flash" };
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), {status, headers:{"Content-Type":"application/json"}});
function setup(result = "success", supported = true, geminiOnly = false) {
  window.localStorage.setItem("wio_language", "en");
  const requests: Array<{url:string; body:unknown}> = [];
  const server = {id:"server-1",name:"Production",status:"online",gemini_credentials_supported:supported,gemini_profile_id:"gemini-1",gemini_profile_name:"Gemini",codex_profile_id:geminiOnly ? "" : "codex-1",codex_profile_name:geminiOnly ? "" : "Codex",git_profiles:[{id:"git-1",name:"GitHub",username:"user"}]};
  vi.stubGlobal("fetch",vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (init.method === "POST") {
      requests.push({url,body:JSON.parse(String(init.body))});
      if (url.endsWith("/gemini-credential-profile") && result === "rejected") return response({error:"finish active Gemini turns"},409);
      return response({operation_id:"apply-1"},202);
    }
    if (url === "/api/servers") return response([server]);
    if (url === "/api/credential-profiles") return response(geminiOnly ? [profile] : [profile,{id:"codex-1",kind:"codex",name:"Codex",model:"gpt"},{id:"git-1",kind:"git",name:"GitHub",username:"user",commit_name:"User",commit_email:"user@example.com"}]);
    if (url === "/api/operations/apply-1") return response({id:"apply-1",status:result === "failed" ? "failed" : result === "pending" ? "running" : "succeeded",result:result === "failed" ? "Gemini configuration failed" : result === "pending" ? "" : "Gemini credentials configured"});
    return response([]);
  }));
  render(<I18nProvider><ServersPage realtime={0} notify={vi.fn()} /></I18nProvider>);
  return {user:userEvent.setup(),requests};
}
afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test.each(["success","rejected","failed"])("applies Gemini through the server credential button and reports %s",async result => {
  const {user,requests} = setup(result);
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog",{name:"Change server credentials"});
  await user.click(within(dialog).getByRole("tab",{name:"Gemini API"}));
  expect(within(dialog).queryByRole("combobox",{name:"Target server"})).not.toBeInTheDocument();
  expect(within(dialog).getByRole("combobox",{name:"Gemini API"})).toHaveValue(profile.id);
  await user.click(within(dialog).getByRole("button",{name:"Apply credentials"}));
  await screen.findByText(result === "rejected" ? "finish active Gemini turns" : result === "failed" ? /Gemini configuration failed/ : /Gemini credentials configured/);
  expect(requests).toEqual([{url:"/api/servers/server-1/gemini-credential-profile",body:{profile_id:profile.id}}]);
  expect(screen.getAllByText("Gemini",{exact:true})).toHaveLength(2); // column heading and bound profile
  expect(screen.queryByText("Gemini · Gemini")).not.toBeInTheDocument();
});

test("preserves Codex and Git application in the same dialog",async () => {
  const {user,requests} = setup();
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByRole("combobox",{name:"Codex API profile"})).toHaveValue("codex-1");
  await user.click(within(dialog).getByRole("button",{name:"Apply credentials"}));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(requests).toEqual([{url:"/api/servers/server-1/credential-profiles",body:{codex_profile_id:"codex-1",git_profile_ids:["git-1"]}}]);
});

test("blocks Gemini on an unsupported Agent",async () => {
  const {user,requests} = setup("success",false);
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("tab",{name:"Gemini API"}));
  expect(within(dialog).getByRole("combobox",{name:"Gemini API"})).toBeDisabled();
  expect(within(dialog).getByRole("button",{name:"Apply credentials"})).toBeDisabled();
  expect(requests).toEqual([]);
});

test("prevents duplicate submissions while Gemini credentials are being applied",async () => {
  const {user,requests} = setup("pending");
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("tab",{name:"Gemini API"}));
  await user.dblClick(within(dialog).getByRole("button",{name:"Apply credentials"}));
  expect(await within(dialog).findByRole("button",{name:"Applying"})).toBeDisabled();
  expect(within(dialog).getByRole("tab",{name:"Codex / Git"})).toBeDisabled();
  expect(requests).toHaveLength(1);
});

test("can apply Gemini without any Codex profile",async () => {
  const {user,requests} = setup("success",true,true);
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByRole("tab",{name:"Gemini API"})).toHaveAttribute("aria-selected","true");
  await user.click(within(dialog).getByRole("button",{name:"Apply credentials"}));
  await screen.findByText(/Gemini credentials configured/);
  expect(requests).toEqual([{url:"/api/servers/server-1/gemini-credential-profile",body:{profile_id:profile.id}}]);
});
