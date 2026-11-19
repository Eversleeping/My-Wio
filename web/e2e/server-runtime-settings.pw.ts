import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(({ page }) => assertMockApplication(page));

test("configures server runtime access and global collaboration from Settings", async ({ page }, testInfo) => {
 const server = { id: "runtime-node", name: "Runtime node", hostname: "node.example.test", status: "online", agent_access_supported:true,agent_restart_supported:true,agent_version: "0.3.39", agent_target_version: "0.3.39", codex_ready: 1, codex_version: "1.0.0", codex_target_version: "1.0.0", address: "node.example.test", configuration: "", notes: "" };
 let collaboration = {enabled:false,target_modes:{}};
 let access = {allow_sudo:false,revision:"a".repeat(64),applied:true,restart_required:false,instance_id:"before"};
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
 await expect(page.getByText("Isolation unavailable",{exact:true})).toHaveCount(0);
 await page.getByRole("button",{name:"Enroll server",exact:true}).click();
 const dialog = page.getByRole("dialog",{name:"Enroll Linux server"});
 await expect(dialog.getByRole("checkbox",{name:/session isolation|passwordless sudo/i})).toHaveCount(0);
 await dialog.getByRole("button",{name:"Cancel",exact:true}).click();
 await page.goto("/?view=settings");
 const categories = page.getByRole("navigation",{name:"Settings categories",exact:true});
 await expect(categories.getByRole("link",{name:"Session collaboration",exact:true})).toHaveAttribute("aria-current","page");
 const enable = page.getByRole("checkbox",{name:"Allow creating and managing child sessions",exact:true});
 await expect(enable).toBeEnabled();
 await enable.check();
 await page.getByRole("checkbox",{name:"Allow creating separate worktrees",exact:true}).check();
 await page.getByRole("combobox",{name:"claude collaboration permissions"}).selectOption("default");
 await page.locator(".collaboration-settings").getByRole("button",{name:"Save",exact:true}).click();
 await expect.poll(() => writes).toContainEqual(expect.objectContaining({enabled:true,worktree:true,target_modes:{claude:"default"}}));
 await categories.getByRole("link",{name:"Server runtime",exact:true}).click();
 await expect(page).toHaveURL(/\?view=settings&section=servers$/);
 await expect(enable).toBeHidden();
 await page.getByRole("combobox",{name:"Select server",exact:true}).selectOption(server.id);
 await expect(page.getByRole("textbox",{name:"SSH host or IP",exact:true})).toHaveCount(0);
 await expect(page.getByRole("button",{name:"Check fingerprint",exact:true})).toHaveCount(0);
 const sudo = page.getByRole("checkbox",{name:/Allow passwordless sudo/});
 await expect(page.getByRole("checkbox",{name:/Enable session isolation/})).toHaveCount(0);
 await expect(page.getByRole("button",{name:"Check and install isolation environment",exact:true})).toHaveCount(0);
 await sudo.check();
 for (const theme of ["dark","light"]) {
  await page.evaluate(theme => {document.documentElement.setAttribute("data-theme",theme);document.body.setAttribute("data-theme",theme);},theme);
  await sudo.scrollIntoViewIfNeeded();
  await page.screenshot({path:testInfo.outputPath(`settings-runtime-${theme}.png`)});
 }
 await page.getByRole("button",{name:"Apply server settings",exact:true}).click();
 await expect(page.getByText(/Settings applied and confirmed by the Agent/)).toBeVisible();
 expect(writes).toContainEqual(expect.objectContaining({allow_sudo:true,revision:"a".repeat(64)}));
 await page.reload();
 // The category is part of the address: a reload returns to it, and Back leaves it again.
 await expect(page.getByRole("combobox",{name:"Select server",exact:true})).toHaveValue("");
 await expect(enable).toHaveCount(0);
 await categories.getByRole("link",{name:"Session collaboration",exact:true}).click();
 await expect(page).toHaveURL(/\?view=settings$/);
 await expect(enable).toBeChecked();
 await page.goBack();
 await expect(page).toHaveURL(/\?view=settings&section=servers$/);
 await expect(enable).toBeHidden();
 access={...access,restart_required:true};
 await page.getByRole("combobox",{name:"Select server",exact:true}).selectOption(server.id);
 await expect(page.getByText(/The previous change still needs a restart/)).toBeVisible();
 await expect(sudo).toBeDisabled();
 await page.getByRole("button",{name:"Apply server settings",exact:true}).click();
 await expect(page.getByText(/Settings applied and confirmed by the Agent/)).toBeVisible();
 await expect(sudo).toBeEnabled();
 await page.getByRole("button",{name:"Restart Agent",exact:true}).click();
 await expect(page.getByText("Agent restarted and reconnected",{exact:true})).toBeVisible();
 expect(writes).toContainEqual({action:"restart"});
});

test("opens a settings category from its address and loads the others on demand", async ({ page }, testInfo) => {
 const requested: string[] = [];
 await installMockApplication(page, {configured:true,onAPIRequest:request => {
  if (request.method !== "GET") return undefined;
  requested.push(request.path);
  if (["/settings/gemini-cli","/settings/claude-cli"].includes(request.path)) return {body:{target_version:"1.0.0",versions:["1.0.0"]}};
  if (["/servers","/credential-profiles","/secret-sets","/scheduled-tasks"].includes(request.path)) return {body:[]};
  if (request.path === "/sessions") return {body:{items:[],has_more:false,next:null}};
  return undefined;
 }});
 await page.goto("/?view=settings&section=cli");
 const categories = page.getByRole("navigation",{name:"Settings categories",exact:true});
 await expect(categories.getByRole("link")).toHaveText([/Session collaboration/,/Server runtime/,/CLI versions/,/Scheduled tasks/,/Credentials & secrets/]);
 await expect(categories.getByRole("link",{name:"CLI versions",exact:true})).toHaveAttribute("aria-current","page");
 for (const name of ["Codex CLI","Gemini CLI","Claude CLI"]) await expect(page.getByRole("group",{name,exact:true}).getByRole("button",{name:"Check for updates",exact:true})).toBeEnabled();
 expect(requested.filter(path => ["/settings/collaboration","/scheduled-tasks","/credential-profiles","/secret-sets"].includes(path))).toEqual([]);

 await categories.getByRole("link",{name:"Credentials & secrets",exact:true}).click();
 await expect(page).toHaveURL(/\?view=settings&section=credentials$/);
 await expect(page.getByRole("button",{name:"New credential profile",exact:true})).toBeVisible();
 await expect(page.getByRole("button",{name:"New secret set",exact:true})).toBeVisible();
 await expect(page.getByRole("group",{name:"Codex CLI",exact:true})).toBeHidden();
 const loads = requested.filter(path => path === "/credential-profiles").length;
 expect(loads).toBeGreaterThan(0);
 // The page itself never scrolls sideways; on a narrow screen the category strip does.
 expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);
 await expect(categories.getByRole("link",{name:"Credentials & secrets",exact:true})).toBeInViewport({ratio:1});

 await page.goBack();
 await expect(page).toHaveURL(/\?view=settings&section=cli$/);
 await expect(page.getByRole("group",{name:"Codex CLI",exact:true})).toBeVisible();
 await page.goForward();
 await expect(page.getByRole("button",{name:"New credential profile",exact:true})).toBeVisible();
 // A category that was already opened is shown again without loading it a second time.
 expect(requested.filter(path => path === "/credential-profiles")).toHaveLength(loads);

 if (testInfo.project.name === "desktop-chromium") {
  // Beside the console sidebar the list is a strip above the panel; a wider window puts it beside the panel.
  const hint = categories.getByText("API, Git and Vault secrets",{exact:true});
  const panels = page.locator(".settings-panels");
  await expect(hint).toBeHidden();
  let list = (await categories.boundingBox())!, panel = (await panels.boundingBox())!;
  expect(list.y + list.height).toBeLessThanOrEqual(panel.y);
  await page.setViewportSize({width:1600,height:900});
  await expect(hint).toBeVisible();
  list = (await categories.boundingBox())!; panel = (await panels.boundingBox())!;
  expect(list.x + list.width).toBeLessThanOrEqual(panel.x);
  expect(Math.abs(list.y - panel.y)).toBeLessThan(1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBe(0);
 }

 // The console's own Settings entry opens the first category again.
 if (testInfo.project.name === "mobile-chromium") await page.getByRole("button",{name:"Open navigation"}).click();
 await page.getByRole("navigation",{name:"Primary navigation"}).getByRole("button",{name:"Settings",exact:true}).click();
 await expect(page).toHaveURL(/\?view=settings$/);
 await expect(categories.getByRole("link",{name:"Session collaboration",exact:true})).toHaveAttribute("aria-current","page");
 await expect(page.getByRole("checkbox",{name:"Allow creating and managing child sessions",exact:true})).toBeVisible();
 await expect(page.getByRole("button",{name:"New credential profile",exact:true})).toBeHidden();
});
