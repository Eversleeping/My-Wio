import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { ServersPage } from "./pages/ServersPage";
import { I18nProvider } from "./i18n";

const profile = { id:"claude-1", kind:"claude", name:"Claude", model:"flash" };
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), {status, headers:{"Content-Type":"application/json"}});
function setup(result = "success", supported = true, claudeOnly = false) {
  window.localStorage.setItem("wio_language", "en");
  const requests: Array<{url:string; body:unknown}> = [];
  const server = {id:"server-1",name:"Production",status:"online",scoped_credentials_supported:true,claude_credentials_supported:supported,claude_profile_id:"claude-1",claude_profile_name:"Claude",codex_profile_id:claudeOnly ? "" : "codex-1",codex_profile_name:claudeOnly ? "" : "Codex",git_profiles:[{id:"git-1",name:"GitHub",username:"user"}]};
  vi.stubGlobal("fetch",vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (init.method === "POST") {
      requests.push({url,body:JSON.parse(String(init.body))});
      if (url.endsWith("/claude-credential-profile") && result === "rejected") return response({error:"finish active Claude turns"},409);
      return response({operation_id:"apply-1"},202);
    }
    if (url === "/api/servers") return response([server]);
    if (url === "/api/credential-profiles") return response(claudeOnly ? [profile] : [profile,{id:"codex-1",kind:"codex",name:"Codex",model:"gpt"},{id:"git-1",kind:"git",name:"GitHub",username:"user",commit_name:"User",commit_email:"user@example.com"}]);
    if (url === "/api/operations/apply-1") return response({id:"apply-1",status:result === "failed" ? "failed" : result === "pending" ? "running" : "succeeded",result:result === "failed" ? "Claude configuration failed" : result === "pending" ? "" : "Claude credentials configured"});
    return response([]);
  }));
  render(<I18nProvider><ServersPage realtime={0} notify={vi.fn()} /></I18nProvider>);
  return {user:userEvent.setup(),requests};
}
afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test.each(["success","rejected","failed"])("applies Claude through the server credential button and reports %s",async result => {
  const {user,requests} = setup(result);
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog",{name:"Change server credentials"});
  await user.click(within(dialog).getByRole("tab",{name:"Claude API"}));
  expect(within(dialog).queryByRole("combobox",{name:"Target server"})).not.toBeInTheDocument();
  expect(within(dialog).getByRole("combobox",{name:"Claude API"})).toHaveValue(profile.id);
  await user.click(within(dialog).getByRole("button",{name:"Apply credentials"}));
  await screen.findByText(result === "rejected" ? "finish active Claude turns" : result === "failed" ? /Claude configuration failed/ : /Claude credentials configured/);
  expect(requests).toEqual([{url:"/api/servers/server-1/claude-credential-profile",body:{profile_id:profile.id}}]);
  expect(screen.getAllByText("Claude",{exact:true})).toHaveLength(2); // column heading and bound profile
  expect(screen.queryByText("Claude · Claude")).not.toBeInTheDocument();
});

test("applies only Codex from its own tab",async () => {
  const {user,requests} = setup();
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByRole("combobox",{name:"Codex API profile"})).toHaveValue("codex-1");
  await user.click(within(dialog).getByRole("button",{name:"Apply credentials"}));
  await waitFor(() => expect(requests).toHaveLength(1));
  expect(requests).toEqual([{url:"/api/servers/server-1/credential-profiles",body:{scope:"codex",codex_profile_id:"codex-1"}}]);
});

test("blocks Claude on an unsupported Agent",async () => {
  const {user,requests} = setup("success",false);
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("tab",{name:"Claude API"}));
  expect(within(dialog).getByRole("combobox",{name:"Claude API"})).toBeDisabled();
  expect(within(dialog).getByRole("button",{name:"Apply credentials"})).toBeDisabled();
  expect(requests).toEqual([]);
});

test("prevents duplicate submissions while Claude credentials are being applied",async () => {
  const {user,requests} = setup("pending");
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("tab",{name:"Claude API"}));
  await user.dblClick(within(dialog).getByRole("button",{name:"Apply credentials"}));
  expect(await within(dialog).findByRole("button",{name:"Applying"})).toBeDisabled();
  expect(within(dialog).getByRole("tab",{name:"Codex API"})).toBeDisabled();
  expect(requests).toHaveLength(1);
});

test("can apply Claude without any Codex profile",async () => {
  const {user,requests} = setup("success",true,true);
  await user.click(await screen.findByTitle("Change server credentials"));
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByRole("tab",{name:"Claude API"})).toHaveAttribute("aria-selected","true");
  await user.click(within(dialog).getByRole("button",{name:"Apply credentials"}));
  await screen.findByText(/Claude credentials configured/);
  expect(requests).toEqual([{url:"/api/servers/server-1/claude-credential-profile",body:{profile_id:profile.id}}]);
});
