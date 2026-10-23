import { useState } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { CodexPage, SessionView, waitForThread } from "./App";
import { I18nProvider } from "./i18n";
import { messageForkTargets } from "./messageForks";
import { loadCodexComposerPreferences, saveCodexComposerPreferences } from "./codexComposerPreferences";
import type { StreamEvent, Thread } from "./types";

const source: Thread = { id:"fork-source",workspace_id:"fork-workspace",project_id:"fork-project",codex_thread_id:"native-source",title:"Original chat",status:"idle",path:"/srv/project",server_id:"server",server_name:"Server",project_name:"Project",created_at:"2026-10-23T00:00:00Z",updated_at:"2026-10-23T00:00:00Z",pinned_at:null,archived_at:null,project_pinned_at:null,project_hidden_at:null };
const branch: Thread = { ...source,id:"fork-branch",codex_thread_id:"native-branch",title:"Original chat (branch)" };
function event(sequence: number,kind: string,payload: unknown): StreamEvent { return {event_id:`event-${sequence}`,stream_id:source.id,sequence,kind,payload,occurred_at:"2026-10-23T00:00:00Z"}; }
const events = [
  event(1,"user.message",{text:"Original question"}),
  event(2,"codex.item.completed",{turnId:"turn-1",item:{id:"commentary",type:"agentMessage",phase:"commentary",text:"Working"}}),
  event(3,"codex.item.completed",{turnId:"turn-1",item:{id:"answer",type:"agentMessage",phase:"final_answer",text:"Selected reply"}}),
  event(4,"codex.turn.completed",{turn:{id:"turn-1",status:"completed"}}),
  event(5,"user.message",{text:"Later question"}),
  event(6,"codex.item.completed",{turnId:"turn-2",item:{id:"answer-2",type:"agentMessage",text:"Later reply"}}),
  event(7,"codex.turn.completed",{turn:{id:"turn-2",status:"completed"}})
];
const response = (body: unknown,status=200) => new Response(JSON.stringify(body),{status,headers:{"Content-Type":"application/json"}});
beforeEach(() => { localStorage.clear(); localStorage.setItem("wio_language","en"); });
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); localStorage.clear(); });

test("branches from the selected reply and opens the finished chat with inherited preferences", async () => {
  const user = userEvent.setup();
  const posts: unknown[] = [];
  const notify = vi.fn();
  let created = false;
  saveCodexComposerPreferences(source.id,{model:"custom-model",reasoningEffort:"high",approvalMode:"never"});
  vi.stubGlobal("fetch",vi.fn(async (input: RequestInfo | URL,init?:RequestInit) => {
    const url = new URL(String(input),location.origin);
    if (url.pathname.endsWith("/fork") && init?.method==="POST") { posts.push(JSON.parse(String(init.body)));created=true;return response({operation_id:"fork-op",target_thread_id:branch.id},202); }
    if (url.pathname==="/api/operations/fork-op") return response({status:"succeeded",result:""});
    if (url.pathname===`/api/threads/${branch.id}`) return response(branch);
    if (url.pathname==="/api/threads") return response({items:url.searchParams.has("archived")?[]:created?[branch,source]:[source],has_more:false,next:null});
    if (url.pathname==="/api/workspaces") return response([{id:source.workspace_id,project_id:source.project_id,server_id:source.server_id,path:source.path,display_name:"Project",management_mode:"managed",project_name:"Project",server_name:"Server"}]);
    if (url.pathname.endsWith("/events")) return response(url.pathname.includes(branch.id)?events.slice(0,4):events);
    if (url.pathname.endsWith("/goal")) return response({status:"idle",supported:true,data:{}});
    if (url.pathname.endsWith("/files")) return response({files:[],status:"succeeded"});
    return response([]);
  }));
  function Page() { const [selected,setSelected] = useState(source.id);return <I18nProvider><CodexPage realtime={0} streamRevisions={{}} approvals={[]} approvalSignal={0} reloadApprovals={vi.fn()} notify={notify} selectedThreadID={selected} onSelectThread={setSelected} /></I18nProvider>; }
  render(<Page />);
  const selected = (await screen.findByText("Selected reply")).closest("article")!;
  await user.click(within(selected).getByRole("button",{name:"Message actions"}));
  const menu = screen.getByRole("menu",{name:"Message actions"});
  expect(menu.parentElement).toBe(document.body);
  await user.click(within(menu).getByRole("menuitem",{name:"Branch to new chat"}));
  await screen.findByRole("heading",{name:branch.title});
  expect(posts).toEqual([{event_id:"event-3"}]);
  expect(notify).toHaveBeenCalledWith("Branched to a new chat");
  expect(loadCodexComposerPreferences(branch.id)).toEqual(loadCodexComposerPreferences(source.id));
  await waitFor(() => expect(screen.queryByText("Later reply")).not.toBeInTheDocument());
});

test("offers branching only at finished turn boundaries and disables it while busy", async () => {
  const user = userEvent.setup();
  const fork = vi.fn();
  vi.stubGlobal("fetch",vi.fn(async input=>response(String(input).includes("/events")?events:[])));
  render(<I18nProvider><SessionView thread={{...source,status:"running"}} approvals={[]} realtime={0} reloadApprovals={vi.fn()} notify={vi.fn()} onOpenFile={vi.fn()} onNewTask={vi.fn()} onFork={fork} /></I18nProvider>);
  const commentary = (await screen.findByText("Working")).closest("article")!;
  expect(within(commentary).queryByRole("button",{name:"Message actions"})).not.toBeInTheDocument();
  const selected = screen.getByText("Selected reply").closest("article")!;
  await user.click(within(selected).getByRole("button",{name:"Message actions"}));
  expect(screen.getByRole("menuitem",{name:"Branch to new chat"})).toBeDisabled();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(fork).not.toHaveBeenCalled();
  expect(messageForkTargets(events.slice(0,3),events.slice(0,3)).size).toBe(0);
});

test("surfaces a failed fork operation immediately without waiting for a missing chat", async () => {
  const fetch = vi.fn(async()=>response({status:"failed",result:"Codex could not locate the selected turn"}));
  vi.stubGlobal("fetch",fetch);
  await expect(waitForThread("missing","Timed out","failed-op")).rejects.toThrow("Codex could not locate the selected turn");
  expect(fetch).toHaveBeenCalledTimes(1);
});
