import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(({ page }) => assertMockApplication(page));

test("configures server runtime access and global collaboration from Settings", async ({ page }, testInfo) => {
 const server = { id: "isolated-node", name: "Isolated node", hostname: "node.example.test", status: "online", agent_version: "0.3.32", agent_target_version: "0.3.32", codex_ready: 1, codex_version: "1.0.0", codex_target_version: "1.0.0", address: "node.example.test", configuration: "", notes: "", session_isolation: { mode: "unsupported", reason: "Required namespaces are unavailable" } };
 let collaboration = {enabled:false,target_modes:{}};
 let access = {allow_sudo:false,session_isolation:false,revision:"a".repeat(64)};
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
  if (request.path === "/servers/ssh/probe" && request.method === "POST") return {body:{fingerprint:"SHA256:synthetic-node",key_type:"ssh-ed25519"}};
  if (request.path.endsWith("/ssh/access-settings")) {
   const input = JSON.parse(request.body || "{}");
   if (input.settings) { access = input.settings; writes.push(access); }
   return {body:access};
  }
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
 await page.getByRole("combobox",{name:"Authentication",exact:true}).selectOption("none");
 await page.getByRole("button",{name:"Check fingerprint",exact:true}).click();
 await page.getByRole("button",{name:"Confirm fingerprint and read settings",exact:true}).click();
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
 await expect(page.getByText(/Settings applied and Agent restarted/)).toBeVisible();
 expect(writes).toContainEqual({allow_sudo:false,session_isolation:true,revision:"a".repeat(64)});
 await page.reload();
 await expect(page.getByRole("checkbox",{name:"Allow creating and managing child sessions",exact:true})).toBeChecked();
});
