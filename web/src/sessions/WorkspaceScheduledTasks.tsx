import { useEffect, useState } from "react";
import { Pause, Pencil, Play, Plus, Trash2 } from "lucide-react";
import { Dialog } from "../components/FormUI";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DataTable, ErrorState, PageLoading, Status } from "../components/PageUI";
import { useResource, useResourceRevision } from "../data/resources";
import { settings } from "../data/settings";
import { useI18n } from "../i18n";
import { ScheduledTaskDialog } from "../pages/ScheduledTaskDialog";
import { message } from "../text";
import type { ScheduledTask, Workspace } from "../types";
import { sessionAPI } from "./api";
import { rememberedEngine } from "./CreateSessionDialog";
import { engineModule } from "./engineModules";
import { workspaceTitle } from "./unifiedGroups";
import type { AISession, SessionListPage } from "./types";

// Fetch every matching cursor window before offering session choices. Paths can
// be shared by several servers; identity is filtered by workspace ID afterwards.
function useWorkspaceSessions(workspaceID: string, path: string | undefined, revision: string) {
  const [state, setState] = useState<{data:AISession[]; loading:boolean; error:string}>({data:[],loading:true,error:""});
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setState({data:[],loading:true,error:""});
    void (async () => {
      try {
        const items:AISession[] = [], cursors = new Set<string>();
        let cursor: string | null = null;
        do {
          const page: SessionListPage = await sessionAPI.list({q:path,archived:false,limit:100,cursor},{signal:controller.signal});
          items.push(...page.items.filter(item => item.workspace_id === workspaceID));
          cursor=page.next_cursor;
          if (cursor && cursors.has(cursor)) throw new Error("Unable to load sessions. Please try again.");
          if (cursor) cursors.add(cursor);
        } while (cursor && !controller.signal.aborted);
        if (!controller.signal.aborted) setState({data:items,loading:false,error:""});
      } catch (error) { if (!controller.signal.aborted) setState({data:[],loading:false,error:message(error)}); }
    })();
    return () => controller.abort();
  },[workspaceID,path,revision,retry]);
  return {...state,reload:()=>setRetry(value=>value+1)};
}

export default function WorkspaceScheduledTasks({workspaceID, workspace, initialThread, revision, notify, onClose}: {workspaceID:string; workspace?:Workspace; initialThread?:AISession; revision:string; notify:(text:string)=>void; onClose:()=>void}) {
  const {t}=useI18n();
  const shared = useResourceRevision(["codex","gemini","claude","projects"]);
  const options=useWorkspaceSessions(workspaceID,workspace?.path,`${revision}:${shared}`);
  const tasks=useResource(settings.scheduledTasks());
  const [editor,setEditor]=useState<{task?:ScheduledTask; thread?:AISession} | null>(() => initialThread ? {thread:initialThread} : null);
  const [deleting,setDeleting]=useState<ScheduledTask | null>(null);
  const [busy,setBusy]=useState(false);
  const sessions = initialThread && !options.data.some(item=>item.engine===initialThread.engine && item.id===initialThread.id) ? [...options.data,initialThread] : options.data;
  const change=async (action:()=>Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    try { await action(); tasks.reload(); } catch (error) { notify(message(error)); } finally { setBusy(false); }
  };
  if (editor) return <ScheduledTaskDialog open task={editor.task} agentKind={editor.task?.agent_kind ?? editor.thread?.engine ?? rememberedEngine(workspaceID)} initialThreadID={editor.thread?.id} threads={[]} sessions={sessions} sessionNotice={options.error ? <ErrorState error={options.error} reload={options.reload} /> : options.loading ? <PageLoading /> : null} notify={notify} onClose={()=>setEditor(null)} onSaved={tasks.reload} />;
  return <Dialog open title={t("settings.scheduledTasks")} wide dismissDisabled={busy} onClose={onClose}>
    <div>
    <p>{workspace ? `${workspaceTitle(workspace)} · ${workspace.server_name} · ${workspace.path}` : workspaceID}</p>
    {options.error && <ErrorState error={options.error} reload={options.reload} />}
    <div className="row-actions"><button className="primary-button" disabled={options.loading || Boolean(options.error) || !sessions.length} onClick={()=>setEditor({})}><Plus size={17}/>{t("settings.newScheduledTask")}</button></div>
    {!options.loading && !options.error && !sessions.length && <p>{t("sessions.scheduleNeedsSession")}</p>}
    {tasks.error ? <ErrorState error={tasks.error} reload={tasks.reload} /> : tasks.loading ? <PageLoading/> : <DataTable headers={[t("settings.name"),t("sessions.engine"),t("settings.scheduledThread"),t("settings.schedule"),t("column.state"),t("common.actions")]} empty={t("settings.noScheduledTasks")}>
      {(tasks.data ?? []).filter(task=>task.workspace_id===workspaceID).map(task=><tr key={task.id}>
        <td><div className="cell-main"><strong>{task.name}</strong><small title={task.prompt}>{task.prompt}</small></div></td>
        <td>{engineModule(task.agent_kind ?? "codex").label}</td><td>{task.thread_title}</td><td><code title={task.timezone}>{task.schedule}</code></td><td><Status value={task.enabled ? "enabled":"disabled"}/></td>
        <td><div className="row-actions"><button className="icon-button" title={t("common.edit")} disabled={busy} onClick={()=>setEditor({task})}><Pencil size={15}/></button><button className="icon-button" title={t(task.enabled ? "settings.disableScheduledTask":"settings.enableScheduledTask")} disabled={busy} onClick={()=>void change(()=>settings.saveScheduledTask(task.id,{enabled:!task.enabled}))}>{task.enabled ? <Pause size={15}/>:<Play size={15}/>}</button><button className="icon-button danger" title={t("settings.deleteScheduledTask")} disabled={busy} onClick={()=>setDeleting(task)}><Trash2 size={15}/></button></div></td>
      </tr>)}
    </DataTable>}
    </div>
    <ConfirmDialog open={Boolean(deleting)} danger title={t("settings.deleteScheduledTask")} impact={t("settings.confirmDeleteScheduledTask",{name:deleting?.name ?? ""})} confirmLabel={t("settings.deleteScheduledTask")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={()=>setDeleting(null)} onConfirm={()=>change(async()=>{if (deleting) await settings.removeScheduledTask(deleting.id);setDeleting(null);})}/>
  </Dialog>;
}
