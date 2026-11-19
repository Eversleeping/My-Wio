import { expect, test } from "@playwright/test";
import { assertMockApplication, installMockApplication } from "./support/mock-api";

test.afterEach(({ page }) => assertMockApplication(page));

for (const engine of ["codex", "claude", "gemini"]) {
  test(`${engine} shows native subagent status, live content and persisted history`, async ({ page }, testInfo) => {
    const workspace = { id:"workspace",project_id:"project",server_id:"server",path:"/srv/project",status:"ready",management_mode:"managed",project_name:"Project",server_name:"Server" };
    const session = { engine,id:"parent",workspace_id:workspace.id,project_id:"project",server_id:"server",path:workspace.path,project_name:"Project",server_name:"Server",title:"Parent task",status:"idle",server_status:"online",native_id:"native-parent",session_id:"native-parent",codex_thread_id:"native-parent",model:"test-model",mode:"default",approval_mode:"default",permission_mode:"default",capabilities:{},created_at:"2026-11-19T00:00:00Z",updated_at:"2026-11-19T00:00:00Z",archived_at:null };
    let complete = false;
    let refresh = () => {};
    await installMockApplication(page,{configured:true,onAPIRequest:request => {
      if (request.path === "/workspaces") return {body:[workspace]};
      if (request.path === "/servers") return {body:[{id:"server",status:"online"}]};
      if (request.path === "/sessions") return {body:{items:[session],next_cursor:null,groups:[{workspace_id:workspace.id,matched_count:1,project_pinned_at:null}]}};
      if (request.path === `/sessions/${engine}/parent`) return {body:session};
      if (request.path === `/sessions/${engine}/parent/subagents`) return {body:{items:[{id:"child",name:"Review code",prompt:"Inspect this change",status:complete?"succeeded":"running",detail:"activity",updated_at:"2026-11-19T00:00:00Z"}]}};
      if (request.path === `/sessions/${engine}/parent/subagents/child/events`) {
        const after = Number(new URLSearchParams(request.search).get("after") || "0");
        const events = [{sequence:1,event_id:"tool",payload:{entry:{id:"read",kind:"tool",tool:"Read",status:"running",input:{path:"README.md"}}}},...(complete?[{sequence:2,event_id:"result",payload:{entry:{id:"read",kind:"tool",status:"succeeded",output:"File inspected"}}},{sequence:3,event_id:"answer",payload:{entry:{id:"answer",kind:"message",text:"Review finished"}}}]:[])];
        const items=events.filter(event=>event.sequence>after);
        return {body:{items,next:items.at(-1)?.sequence??after,has_more:false}};
      }
      if (request.path.endsWith("/events")) return {body:[]};
      if (request.path.endsWith("/goal")) return {body:engine==="codex"?{status:"succeeded",supported:true,data:null,error:""}:null};
      if (request.path === "/workspaces/workspace/files") return {body:{files:[],status:"succeeded",error:"",truncated:false}};
      if (/\/workspaces\/workspace\/(gemini|claude)\/models$/.test(request.path)) return {body:{configured:true,models:[{id:"test-model",name:"Test model"}],default_model:"test-model"}};
      return undefined;
    }});
    await page.routeWebSocket("**/api/ws", socket => { refresh=()=>socket.send(JSON.stringify({kind:"session.subagent.updated",stream_id:"parent"})); });
    await page.goto(`/?view=sessions&engine=${engine}&thread=parent`);
    const panel=page.getByRole("region",{name:"Subagents",exact:true});
    await expect(panel.getByText("1 running",{exact:true})).toBeVisible();
    await panel.getByRole("button",{name:/Review code/}).click();
    const dialog=page.getByRole("dialog",{name:"Review code"});
    await expect(dialog.getByText("Read",{exact:true})).toBeVisible();
    complete=true;
    refresh();
    await expect(dialog.getByText("Review finished",{exact:true})).toBeVisible();
    await expect(dialog.locator(".subagent-transcript article")).toHaveCount(2);
    await page.screenshot({path:testInfo.outputPath(`subagents-${engine}.png`)});
    await dialog.getByRole("button",{name:"Close",exact:true}).click();
    await page.reload();
    await panel.getByRole("button",{name:/Review code/}).click();
    await expect(page.getByRole("dialog").getByText("Review finished",{exact:true})).toBeVisible();
    expect(await page.evaluate(()=>document.documentElement.scrollWidth-document.documentElement.clientWidth)).toBe(0);
  });
}
