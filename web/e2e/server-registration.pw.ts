import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(async ({page}) => assertMockApplication(page));

test("generates a temporary Linux registration, tracks progress, and reopens history", async ({page}, testInfo) => {
  const registration = {id: "11111111-1111-4111-8111-111111111111", name: "command-node", state: "pending", server_id: "", expires_at: "2099-01-01T00:15:00Z", session_expires_at: "2099-01-01T01:00:00Z", error_code: "", progress: [] as Array<{step: string}>, result: {} as {warnings?: string[]}};
  let issued = false;
  const writes: Array<{path: string; body: unknown}> = [];
  await installMockApplication(page, {configured: true, onAPIRequest: request => {
    if (request.method === "GET" && request.path === "/servers") return {body: []};
    if (request.method === "GET" && request.path === "/credential-profiles") return {body: [{id: "codex", kind: "codex", name: "Codex", model: "gpt-6.1-sol"}]};
    if (request.method === "GET" && request.path === "/servers/registrations") return {body: issued ? [registration] : []};
    if (request.method === "POST" && request.path === "/servers/registrations") {
      writes.push({path: request.path, body: JSON.parse(request.body!)}); issued = true;
      return {status: 201, body: {registration, command: "curl -fsSL https://wio.example.test/api/agent/install.sh | sudo sh -s -- --url https://wio.example.test --registration 11111111-1111-4111-8111-111111111111", code: "synthetic-one-time-code"}};
    }
    return undefined;
  }});
  await page.goto("/?view=servers");
  await page.getByRole("button", {name: "Enroll server", exact: true}).click();
  const form = page.getByRole("dialog", {name: "Enroll server"});
  await form.getByLabel("Registration method").selectOption("command");
  await expect(form.getByLabel("SSH host or IP")).toHaveCount(0);
  await form.getByLabel("Server name").fill("command-node");
  await form.getByRole("button", {name: "Generate registration command"}).click();
  const dialog = page.getByRole("dialog", {name: "Registration progress"});
  await expect(dialog.getByLabel("One-time registration code")).toHaveValue("synthetic-one-time-code");
  await expect(dialog.getByLabel("Enrollment command")).not.toHaveValue(/synthetic-one-time-code/);
  await page.screenshot({path: testInfo.outputPath("registration-command.png"), fullPage: true});
  expect(writes).toEqual([{path: "/servers/registrations", body: {os: "linux", name: "command-node", scan_roots: ["/srv", "/opt", "/home"], configuration: "", notes: "", codex_profile_id: "codex", git_profile_id: ""}}]);
  registration.state = "waiting_online"; registration.server_id = "server-1"; registration.progress = [{step: "starting_service"}];
  await dialog.getByRole("button", {name: "Refresh", exact: true}).click();
  await expect(dialog.getByText(/Waiting for Agent heartbeat/)).toBeVisible();
  await expect(dialog.getByLabel("One-time registration code")).toHaveCount(0);
  registration.state = "complete"; registration.result = {warnings: ["docker_unavailable"]};
  await dialog.getByRole("button", {name: "Refresh", exact: true}).click();
  await expect(dialog.getByText(/Complete and online/)).toBeVisible();
  await dialog.getByRole("button", {name: "Close", exact: true}).last().click();
  await page.getByRole("button", {name: /command-node.*Complete and online/}).click();
  await expect(dialog.getByText(/Complete and online/)).toBeVisible();
  await expect(dialog.getByLabel("One-time registration code")).toHaveCount(0);
  await page.screenshot({path: testInfo.outputPath("registration-complete.png"), fullPage: true});
});

for (const os of ["darwin", "windows"] as const) {
 test(`generates a ${os} command with matching scan roots`, async ({page},testInfo)=>{
  let received:unknown;
  const registration={id:"33333333-3333-4333-8333-333333333333",name:"platform-node",state:"pending",server_id:"",expires_at:"2099-01-01T00:15:00Z",session_expires_at:"2099-01-01T01:00:00Z",error_code:"",progress:[],result:{}};
  await installMockApplication(page,{configured:true,onAPIRequest:request=>{
   if(request.method==="GET"&&request.path==="/servers")return {body:[]};
   if(request.method==="GET"&&request.path==="/servers/registrations")return {body:[]};
   if(request.method==="GET"&&request.path==="/credential-profiles")return {body:[{id:"codex",kind:"codex",name:"Codex",model:"gpt-6.1-sol"}]};
   if(request.method==="POST"&&request.path==="/servers/registrations"){received=JSON.parse(request.body!);return {status:201,body:{registration,command:os==="windows"?"Invoke-WebRequest https://wio.example.test/api/agent/install.ps1":"curl https://wio.example.test/api/agent/install.sh | sudo sh",code:"platform-once"}};}
   return undefined;
  }});
  await page.goto("/?view=servers");
  await page.getByRole("button",{name:"Enroll server",exact:true}).click();
  const form=page.getByRole("dialog",{name:"Enroll server"});
  await form.getByLabel("Operating system").selectOption(os);
  await form.getByLabel("Registration method").selectOption("command");
  await expect(form.getByLabel("Repository scan roots")).toHaveValue(os==="windows"?"C:\\Projects":"/Users, /opt");
  await form.getByLabel("Server name").fill("platform-node");
  await form.getByRole("button",{name:"Generate registration command"}).click();
  const progress=page.getByRole("dialog",{name:"Registration progress"});
  await expect(progress.getByLabel("One-time registration code")).toHaveValue("platform-once");
  expect(received).toMatchObject({os,scan_roots:os==="windows"?["C:\\Projects"]:["/Users","/opt"]});
  await page.screenshot({path:testInfo.outputPath(`${os}-registration.png`),fullPage:true});
 });
}
