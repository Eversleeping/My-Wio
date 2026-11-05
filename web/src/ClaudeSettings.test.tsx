import { screen, waitFor, within, render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "./i18n";
import { SettingsPage } from "./pages/SettingsPage";

const profile = { id:"claude-1", kind:"claude", name:"Claude Studio", endpoint:"https://api.anthropic.com", model:"flash", username:"", commit_name:"", commit_email:"", updated_at:"2026-10-25T00:00:00Z" };
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), {status, headers:{"Content-Type":"application/json"}});

function setup() {
  window.localStorage.setItem("wio_language","en");
  const requests: Array<{url:string; body:unknown}> = [];
  let target = "2.1.318";
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (init.method === "POST") {
      const body = JSON.parse(String(init.body)); requests.push({url,body});
      if (url.endsWith("/select-version")) { target = body.version; return response({target_version:target}); }
      if (url === "/api/credential-profiles") return response(profile,201);
    }
    if (url === "/api/settings/claude-cli") return response({target_version:target, versions:["2.1.318","2.1.317"]});
    if (url === "/api/settings/gemini-cli") return response({target_version:"1.0.0", versions:["1.0.0"]});
    if (url === "/api/settings/codex-cli") return response({target_version:"1.0.0", versions:["1.0.0"]});
    if (url === "/api/credential-profiles") return response([profile]);
    if (url === "/api/servers") return response([
      {id:"server-1",name:"Production",status:"online",claude_credentials_supported:true},
      {id:"server-2",name:"Old Agent",status:"online",claude_credentials_supported:false},
      {id:"server-3",name:"Offline",status:"offline",claude_credentials_supported:true}
    ]);
    if (url === "/api/operations/apply-1") return response({id:"apply-1",status:"succeeded",result:"Claude credentials configured"});
    if (url.startsWith("/api/audit")) return response({items:[],has_more:false,next:null});
    return response([]);
  }));
  render(<I18nProvider><SettingsPage realtime={0} notify={vi.fn()} /></I18nProvider>);
  return { user:userEvent.setup(), requests };
}
afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test("selects Claude target independently and saves Claude API profiles with masked keys", async () => {
  const {user,requests} = setup();
  const version = await screen.findByRole("combobox",{name:"Select a Claude CLI version"});
  await waitFor(() => expect(version).toHaveValue("2.1.318"));
  await user.selectOptions(version,"2.1.317");
  const section = version.closest("section")!;
  await user.click(within(section).getByRole("button",{name:"Apply version"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/settings/claude-cli/select-version",body:{version:"2.1.317"}}));
  expect(within(section).queryByText("Claude API")).not.toBeInTheDocument();
  expect(screen.queryByRole("button",{name:"Apply Claude API to server"})).not.toBeInTheDocument();
  await user.click(screen.getByRole("button",{name:"New credential profile"}));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("tab",{name:"Claude API"}));
  expect(within(dialog).getByRole("tab",{name:"Claude API"})).toHaveAttribute("aria-selected","true");
  expect(within(dialog).getByLabelText("Service URL")).toHaveValue(profile.endpoint);
  const key = within(dialog).getByLabelText("Claude API key");
  expect(key).toHaveAttribute("type","password");
  await user.type(within(dialog).getByLabelText("Name"),"New Claude");
  await user.type(key,"synthetic-api-key");
  await user.click(within(dialog).getByRole("button",{name:"Encrypt and save"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/credential-profiles",body:expect.objectContaining({kind:"claude",name:"New Claude",secret:"synthetic-api-key",model:"sonnet"})}));
});
