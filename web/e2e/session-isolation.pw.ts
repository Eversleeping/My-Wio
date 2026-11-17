import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(({ page }) => assertMockApplication(page));

test("configures server runtime access and global collaboration from Settings", async ({ page }, testInfo) => {
 const server = { id: "isolated-node", name: "Isolated node", hostname: "node.example.test", status: "online", agent_access_supported:true,agent_restart_supported:true,agent_version: "0.3.35", agent_target_version: "0.3.35", codex_ready: 1, codex_version: "1.0.0", codex_target_version: "1.0.0", address: "node.example.test", configuration: "", notes: "", session_isolation: { mode: "unsupported", reason: "Required namespaces are unavailable" } };
 let collaboration = {enabled:false,target_modes:{}};
 let access = {allow_sudo:false,session_isolation:false,revision:"a".repeat(64),applied:true,restart_required:false,instance_id:"before",isolation_support:{supported:true,reason:""}};
 let restartReceipt=access;
 const writes: unknown[] = [];
 await installMockApplication(page, {configured:true,onAPIRequest:request => {
  if (request.path === "/servers" && request.method === "GET") return {body:[server]};
  if (request.path === "/settings/collaboration") {
   if (request.method === "PUT") { collaboration = JSON.parse(request.body || "{}"); writes.push(collaboration); }
   return {body:collaboration};
  }
  if (["/settings/gemini-cli","/settings/claude-cli"].includes(request.path)) return {body:{target_version:"1.0.0",versions:["1.0.0"]}};
  if (["/credential-profiles","/secret-sets","/scheduled-tasks","/projects","/workspaces"].includes(request.path) && request.method === "GET") return {body:[]};
  if (request.path === "/sessions" && request.method === "GET") return {body:{items:[],has_more:false,next:null}};
  if (request.path.endsWith("/access-settings/read"))return {status:202,body:{operation_id:"read"}};
  if (request.path.endsWith("/agent-restart")) {restartReceipt={...access};access={...access,instance_id:"after"};writes.push({action:"restart"});return {status:202,body:{operation_id:"restart"}};}
  if (request.path.endsWith("/access-settings") && request.method==="PUT") {const next=JSON.parse(request.body||"{}");writes.push(next);access={...next,restart_required:false,applied:true};return {status:202,body:{operation_id:"apply"}};}
  if (request.path.includes("/access-settings/operations/"))return {body:{status:"succeeded",settings:request.path.endsWith("/restart")?restartReceipt:access}};
  return undefined;
 }});
 await page.goto("/?view=servers");
 await expect(page.getByText("Isolation unavailable",{exact:true})).toBeVisible();
 await page.getByRole("button",{name:"Enroll server",exact:true}).click();
 const dialog = page.getByRole("dialog",{name:"Enroll Linux server"});
 await expect(dialog.getByRole("checkbox",{name:/session isolation|passwordless sudo/i})).toHaveCount(0);
 await dialog.getByRole("button",{name:"Cancel",exact:true}).click();
 await page.goto("/?view=settings");
 const enable = page.getByRole("checkbox",{name:"Allow creating and managing child sessions",exact:true});
 await expect(enable).toBeEnabled();
 await enable.check();
 await page.getByRole("checkbox",{name:"Allow creating separate worktrees",exact:true}).check();
 await page.getByRole("combobox",{name:"claude collaboration permissions"}).selectOption("default");
 await page.locator(".collaboration-settings").getByRole("button",{name:"Save",exact:true}).click();
 await expect.poll(() => writes).toContainEqual(expect.objectContaining({enabled:true,worktree:true,target_modes:{claude:"default"}}));
 await page.getByRole("combobox",{name:"Select server",exact:true}).selectOption(server.id);
 await expect(page.getByRole("textbox",{name:"SSH host or IP",exact:true})).toHaveCount(0);
 await expect(page.getByRole("button",{name:"Check fingerprint",exact:true})).toHaveCount(0);
 const isolation = page.getByRole("checkbox",{name:/Enable session isolation/});
 const sudo = page.getByRole("checkbox",{name:/Allow passwordless sudo/});
 await expect(isolation).not.toBeChecked();
 await isolation.check();
 await expect(sudo).toBeDisabled();
 await isolation.uncheck();
 await sudo.check();
 await expect(isolation).toBeDisabled();
 await sudo.uncheck();
 await isolation.check();
 for (const theme of ["dark","light"]) {
  await page.evaluate(theme => {document.documentElement.setAttribute("data-theme",theme);document.body.setAttribute("data-theme",theme);},theme);
  await isolation.scrollIntoViewIfNeeded();
  await page.screenshot({path:testInfo.outputPath(`settings-runtime-${theme}.png`)});
 }
 await page.getByRole("button",{name:"Apply server settings",exact:true}).click();
 await expect(page.getByText(/Settings applied and confirmed by the Agent/)).toBeVisible();
 expect(writes).toContainEqual(expect.objectContaining({allow_sudo:false,session_isolation:true,revision:"a".repeat(64)}));
 await page.reload();
 await expect(page.getByRole("checkbox",{name:"Allow creating and managing child sessions",exact:true})).toBeChecked();
 access={...access,restart_required:true};
 await page.getByRole("combobox",{name:"Select server",exact:true}).selectOption(server.id);
 await expect(page.getByText(/The previous change still needs a restart/)).toBeVisible();
 await expect(isolation).toBeDisabled();
 await expect(sudo).toBeDisabled();
 await page.getByRole("button",{name:"Apply server settings",exact:true}).click();
 await expect(page.getByText(/Settings applied and confirmed by the Agent/)).toBeVisible();
 await expect(isolation).toBeEnabled();
 await page.getByRole("button",{name:"Restart Agent",exact:true}).click();
 await expect(page.getByText("Agent restarted and reconnected",{exact:true})).toBeVisible();
 expect(writes).toContainEqual({action:"restart"});
 access={...access,isolation_support:{supported:false,reason:"Landlock ABI 9 is required"}};
 await page.getByRole("button",{name:"Read settings again",exact:true}).click();
 await expect(page.getByText(/Turn isolation off and apply settings to restore sessions/)).toBeVisible();
 await isolation.uncheck();
 await page.getByRole("button",{name:"Apply server settings",exact:true}).click();
 await expect(page.getByText(/Settings applied and confirmed by the Agent/)).toBeVisible();
 await expect(isolation).toBeDisabled();
 expect(writes.at(-1)).toEqual(expect.objectContaining({session_isolation:false}));
});
