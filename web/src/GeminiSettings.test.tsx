import { screen, waitFor, within, render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { I18nProvider } from "./i18n";
import { SettingsPage } from "./pages/SettingsPage";

const profile = { id:"gemini-1", kind:"gemini", name:"Gemini Studio", endpoint:"https://generativelanguage.googleapis.com", model:"flash", username:"", commit_name:"", commit_email:"", updated_at:"2026-10-25T00:00:00Z" };
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), {status, headers:{"Content-Type":"application/json"}});

function setup() {
  window.localStorage.setItem("wio_language","en");
  const requests: Array<{url:string; body:unknown}> = [];
  let target = "0.65.0";
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (init.method === "POST") {
      const body = JSON.parse(String(init.body)); requests.push({url,body});
      if (url.endsWith("/select-version")) { target = body.version; return response({target_version:target}); }
      if (url === "/api/credential-profiles") return response(profile,201);
    }
    if (url === "/api/settings/gemini-cli") return response({target_version:target, versions:["0.65.0","0.64.0"]});
    if (url === "/api/settings/codex-cli") return response({target_version:"1.0.0", versions:["1.0.0"]});
    if (url === "/api/credential-profiles") return response([profile]);
    if (url === "/api/servers") return response([
      {id:"server-1",name:"Production",status:"online",gemini_credentials_supported:true},
      {id:"server-2",name:"Old Agent",status:"online",gemini_credentials_supported:false},
      {id:"server-3",name:"Offline",status:"offline",gemini_credentials_supported:true}
    ]);
    if (url === "/api/operations/apply-1") return response({id:"apply-1",status:"succeeded",result:"Gemini credentials configured"});
    if (url.startsWith("/api/audit")) return response({items:[],has_more:false,next:null});
    return response([]);
  }));
  render(<I18nProvider><SettingsPage realtime={0} notify={vi.fn()} /></I18nProvider>);
  return { user:userEvent.setup(), requests };
}
afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear(); });

test("selects Gemini target independently and saves Gemini API profiles with masked keys", async () => {
  const {user,requests} = setup();
  const version = await screen.findByRole("combobox",{name:"Select a Gemini CLI version"});
  await waitFor(() => expect(version).toHaveValue("0.65.0"));
  await user.selectOptions(version,"0.64.0");
  const section = version.closest("section")!;
  await user.click(within(section).getByRole("button",{name:"Apply version"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/settings/gemini-cli/select-version",body:{version:"0.64.0"}}));
  expect(within(section).queryByText("Gemini API")).not.toBeInTheDocument();
  expect(screen.queryByRole("button",{name:"Apply Gemini API to server"})).not.toBeInTheDocument();
  await user.click(screen.getByRole("button",{name:"New credential profile"}));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("tab",{name:"Gemini API"}));
  expect(within(dialog).getByRole("tab",{name:"Gemini API"})).toHaveAttribute("aria-selected","true");
  expect(within(dialog).getByLabelText("Service URL")).toHaveValue(profile.endpoint);
  const key = within(dialog).getByLabelText("Gemini API key");
  expect(key).toHaveAttribute("type","password");
  await user.type(within(dialog).getByLabelText("Name"),"New Gemini");
  await user.type(key,"synthetic-api-key");
  await user.click(within(dialog).getByRole("button",{name:"Encrypt and save"}));
  await waitFor(() => expect(requests).toContainEqual({url:"/api/credential-profiles",body:expect.objectContaining({kind:"gemini",name:"New Gemini",secret:"synthetic-api-key",model:"auto"})}));
});
