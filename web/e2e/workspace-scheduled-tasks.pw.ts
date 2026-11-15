import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";
test.afterEach(({page})=>assertMockApplication(page));

test("workspace scheduled tasks manage all engines together",async({page},testInfo)=>{
  const errors:string[]=[];
  page.on("pageerror",error=>errors.push(error.message));
  const workspace={id:"w",project_id:"p",project_name:"Project",server_id:"s",server_name:"Server",path:"/srv/project",display_name:"Main",status:"ready",management_mode:"managed"};
  const engines=["codex","gemini","claude"];
  const sessions=engines.map(engine=>({...workspace,id:"same",workspace_id:"w",engine,title:`${engine} session`,native_id:`native-${engine}`,model:"",status:"idle",archived_at:null,created_at:"2026-11-16T00:00:00Z",updated_at:"2026-11-16T00:00:00Z"}));
  const writes:{method:string;body:Record<string,unknown>}[]=[];
  let tasks:Record<string,unknown>[]=[{id:"other",workspace_id:"other",agent_kind:"codex",name:"Foreign job"}];
  await installMockApplication(page,{configured:true,onAPIRequest:request=>{
    if(request.path==="/workspaces") return {body:[workspace]};
    if(request.path==="/servers") return {body:[{id:"s",name:"Server",status:"online"}]};
    if(request.path==="/sessions") return {body:{items:sessions,next_cursor:null,groups:[{workspace_id:"w",matched_count:3,project_pinned_at:null}]}};
    if(request.path==="/workspaces/w/gemini/models") return {body:{models:[{id:"gemini-flash",name:"Flash"}]}};
    if(request.path==="/scheduled-tasks" && request.method==="GET") return {body:tasks};
    if(request.path.startsWith("/scheduled-tasks")) {
      const input=request.body ? JSON.parse(request.body):{};
      writes.push({method:request.method,body:input});
      if(request.method==="POST") { const created={...input,id:`task-${input.agent_kind}`,workspace_id:"w",thread_title:`${input.agent_kind} session`}; tasks.push(created); return {body:created,status:201}; }
      if(request.method==="PUT") tasks=tasks.map(task=>request.path.endsWith(`/${task.id}`) ? {...task,...input}:task);
      if(request.method==="DELETE") tasks=tasks.filter(task=>!request.path.endsWith(`/${task.id}`));
      return {body:{}};
    }
    return undefined;
  }});
  await page.goto("/?view=sessions");
  await page.getByRole("button",{name:/Workspace menu for Project/}).click();
  await page.getByRole("menuitem",{name:"Scheduled tasks",exact:true}).click();
  const list=page.getByRole("dialog",{name:"Scheduled tasks",exact:true});
  await expect(list).toBeVisible();
  await expect(list.getByText("Foreign job")).toHaveCount(0);
  for(const engine of engines) {
    await list.getByRole("button",{name:"New scheduled task"}).click();

    await expect(page.getByRole("dialog")).toHaveAccessibleName("New scheduled task");
    const form=page.getByRole("dialog",{name:"New scheduled task",exact:true});
    await form.getByRole("combobox",{name:"Agent",exact:true}).selectOption(engine);
    await expect(form.getByRole(engine==="claude" ? "textbox":"combobox",{name:engine==="codex" ? "Model override":engine==="gemini" ? "Gemini model":"Claude model or alias",exact:true})).toBeVisible();
    await form.getByRole("combobox",{name:"Session",exact:true}).selectOption("same");
    await form.getByRole("textbox",{name:"Name",exact:true}).fill(`${engine} job`);
    await form.getByRole("textbox",{name:"Prompt",exact:true}).fill("Review this workspace");
    if(engine==="claude") await form.getByRole("combobox",{name:"Tool permissions",exact:true}).selectOption("plan");
    await form.getByRole("button",{name:"Save",exact:true}).click();
    await expect(list.getByText(`${engine} job`,{exact:true})).toBeVisible();
  }
  await page.screenshot({path:testInfo.outputPath("workspace-scheduled-tasks.png"),fullPage:true});
  for(const engine of engines) {
    const row=list.getByRole("row").filter({hasText:`${engine} job`});
    await row.getByRole("button",{name:"Edit",exact:true}).click();
    const form=page.getByRole("dialog",{name:"Edit scheduled task"});
    await expect(form.getByRole("combobox",{name:"Agent",exact:true})).toBeDisabled();
    await form.getByRole("textbox",{name:"Prompt",exact:true}).fill(`Updated ${engine}`);
    await form.getByRole("button",{name:"Save",exact:true}).click();
    await expect(row.getByText(`Updated ${engine}`,{exact:true})).toBeVisible();
    await row.getByRole("button",{name:"Disable scheduled task"}).click();
    await row.getByRole("button",{name:"Enable scheduled task"}).click();
    await expect(row.getByRole("button",{name:"Disable scheduled task"})).toBeEnabled();
    await row.getByRole("button",{name:"Delete scheduled task"}).click();
    const confirmation=page.getByRole("dialog",{name:"Delete scheduled task"});
    await expect(confirmation.getByRole("alert")).toContainText(`${engine} job`);
    await confirmation.getByRole("button",{name:"Delete scheduled task"}).click();
    await expect(row).toHaveCount(0);
  }
  expect(writes.filter(item=>item.method==="POST").map(item=>[item.body.agent_kind,item.body.thread_id])).toEqual(engines.map(engine=>[engine,"same"]));
  expect(writes.filter(item=>item.method==="PUT")).toHaveLength(9);
  expect(writes.filter(item=>item.method==="DELETE")).toHaveLength(3);
  expect(errors).toEqual([]);
});
