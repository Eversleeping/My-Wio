import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { MessageSquare, Pin, Plus, Trash2 } from "lucide-react";
import { Dialog, Field } from "../components/FormUI";
import { Empty, ErrorState, RefreshWarning, PageLoading, Status } from "../components/PageUI";
import { useResourceRevision } from "../data/resources";
import { relative } from "../format";
import { useI18n } from "../i18n";
import { isEngine, type SessionEntry } from "../navigation";
import type { CodexPageProps, Server, Workspace } from "../types";
import { useData } from "../useData";
import { CreateSessionDialog } from "./CreateSessionDialog";
import { sessionPath } from "./api";
import { engineModule, engines } from "./engineModules";
import { SessionPage } from "./SessionPage";
import { Sidebar } from "./Sidebar";
import { sessionKey, type AISession, type Engine, type SessionRef } from "./types";
import { includeSelection, unifiedGroups } from "./unifiedGroups";
import { useSessionList } from "./useSessionList";
import { sessionBusy, useUnifiedManagement } from "./useUnifiedManagement";

const WorkspaceScheduledTasks = lazy(() => import("./WorkspaceScheduledTasks"));
const ApprovalPanel = lazy(() => import("./engines/codex/ApprovalPanel"));

type Props = Omit<CodexPageProps, "selectedThreadID" | "onSelectThread"> & { entry?: SessionEntry; onEntryConsumed?: () => void; initialFilter?: {engine: Engine}; selected?: SessionRef; onSelect: (ref: SessionRef | null, replace?: boolean) => void };
export default function UnifiedSessionsPage({ selected, initialFilter, entry, onEntryConsumed, onSelect, ...props }: Props) {
  const { t } = useI18n();
  const [filter, setFilter] = useState<Engine | "">(() => {
    if (initialFilter) return initialFilter.engine;
    try { const value = localStorage.getItem("wio_sessions_engine"); return isEngine(value) ? value : ""; } catch { return ""; }
  });
  useEffect(() => { if (initialFilter) setFilter(initialFilter.engine); }, [initialFilter]);
  const [search, setSearch] = useState("");
  const [archived, setArchived] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const [tick, setTick] = useState(0);
  const union = useResourceRevision(["codex", "gemini", "claude", "projects", "servers"]);
  const revision = `${union}:${props.realtime}:${tick}`;
  const source = useSessionList({ engine: filter || undefined, archived, q: search }, revision);
  const workspaces = useData<Workspace[]>("/workspaces", revision);
  const servers = useData<Server[]>("/servers", revision);
  const detail = useData<AISession>(selected ? sessionPath(selected.engine, selected.id) : null, revision);
  const listed = source.data?.find(item => selected && sessionKey(item) === sessionKey(selected));
  const active = detail.data ?? listed ?? null;
  const [scheduling, setScheduling] = useState<{workspaceID:string; thread?:AISession} | null>(null);
  const [creation, setCreation] = useState<{ workspaceID: string } | null>(null);
  const appliedEntry = useRef<SessionEntry>();
  const focusedEntry = useRef<SessionEntry>();
  const [focusWorkspaceID, setFocusWorkspaceID] = useState("");
  useEffect(() => {
    if (!entry || !workspaces.data || appliedEntry.current === entry) return;
    appliedEntry.current = entry;
    const workspace = workspaces.data.find(item => item.id === entry.workspaceID);
    if (!workspace) props.notify(t("sessions.workspaceMissing"));
    else {
      setFilter(""); setArchived(false); setShowAll(true); setSearch(workspace.path); setFocusWorkspaceID(workspace.id);
      if (workspace.project_hidden_at) props.notify(t("sessions.workspaceHidden"));
      if (entry.create && workspace.status === "ready") setCreation({workspaceID:workspace.id});
    }
    onEntryConsumed?.();
  }, [entry, workspaces.data, onEntryConsumed, props.notify, t]);
  const refresh = () => setTick(value => value + 1);
  const openNew = (workspaceID = active?.workspace_id ?? "") => setCreation({ workspaceID });
  useEffect(() => { try { localStorage.setItem("wio_sessions_engine", filter); } catch { /* Optional preference. */ } }, [filter]);
  useEffect(() => { if (detail.data) setArchived(Boolean(detail.data.archived_at)); }, [detail.data?.engine, detail.data?.id, detail.data?.archived_at]);
  const items = includeSelection(source.data ?? [], active, archived, filter, search, workspaces.data ?? []);
  const groups = unifiedGroups(items, workspaces.data ?? [], source.groups, showAll, search);
  useEffect(() => {
    if (!entry || focusedEntry.current === entry || !focusWorkspaceID || entry.create) return;
    const group = document.getElementById(`session-workspace-${focusWorkspaceID}`);
    if (group) { group.focus(); group.scrollIntoView?.({block:"nearest"}); focusedEntry.current = entry; }
  }, [entry, focusWorkspaceID, groups]);
  const management = useUnifiedManagement({ workspaces: workspaces.data ?? [], online: new Set((servers.data ?? []).filter(server => server.status === "online").map(server => server.id)), refresh, select: ref => { if (ref) setArchived(false); onSelect(ref); }, notify: props.notify, onNew: openNew, onSchedule: thread => setScheduling({workspaceID:thread.workspace_id,thread}), onWorkspaceSchedule: workspaceID => setScheduling({workspaceID}) });
  const engine = selected?.engine ?? "codex";
  const Conversation = engineModule(engine).Conversation;
  const selectKey = (key: string) => { const item = items.find(item => sessionKey(item) === key); if (item) onSelect({ engine: item.engine, id: item.id }); };
  return <SessionPage engine={engine} thread={active} isolation={servers.data?.find(server => server.id === active?.server_id)?.session_isolation} selectedID={selected ? sessionKey(selected) : ""} onSelect={selectKey} revision={props.realtime + tick} collaborationRevision={props.realtime + tick} notify={props.notify} writable={workspaces.data?.find(w => w.id === active?.workspace_id)?.management_mode === "managed"} error={detail.error} reload={detail.reload}
    sidebar={select => <Sidebar focusWorkspaceID={focusWorkspaceID} icon={MessageSquare} archived={archived} onArchived={() => { setArchived(!archived); onSelect(null); }} search={search} onSearch={setSearch} revision={revision} refresh={refresh} notify={props.notify} groups={groups} selectedID={selected?.id} selectedEngine={selected?.engine} source={source} onNew={openNew} groupActions={group => management.groupActions(group, archived)} threadActions={management.threadActions}
      controls={<div className="session-filters"><Field label={t("sessions.engine")}><select aria-label={t("sessions.engine")} value={filter} onChange={event => setFilter(event.target.value as Engine | "")}><option value="">{t("sessions.allEngines")}</option>{engines.map(module => <option key={module.id} value={module.id}>{module.label}</option>)}</select></Field><label><input type="checkbox" checked={showAll} onChange={event => setShowAll(event.target.checked)} />{t("sessions.showAll")}</label>{workspaces.error && (workspaces.data !== null && workspaces.retrying ? <RefreshWarning error={workspaces.error} reload={workspaces.reload} /> : <ErrorState error={workspaces.error} reload={workspaces.reload} />)}</div>}
      renderRow={thread => { const module = engineModule(thread.engine); const Icon = module.Icon; return <><button type="button" className="thread-select" onClick={() => select(sessionKey(thread))}><span><strong><span className="session-engine"><Icon size={13} />{module.label}</span>{thread.pinned_at && <Pin size={12} />}{thread.title}</strong><small>{thread.server_name} · {relative(thread.updated_at)}</small></span></button><div className="thread-actions"><Status value={thread.status} /><button className="icon-button danger thread-delete" aria-label={t("codex.deleteSession")} disabled={management.busy || sessionBusy(thread)} onClick={() => management.delete(thread)}><Trash2 size={14} /></button></div></>; }} />}
    welcome={<div className="gemini-welcome"><MessageSquare size={40} /><h2>{t("sessions.welcome")}</h2><p>{t("sessions.intro")}</p><button className="primary-button" onClick={() => openNew()}><Plus size={17} />{t("sessions.newSession")}</button></div>}
    dialogs={<>{scheduling && <Suspense fallback={<PageLoading />}><WorkspaceScheduledTasks workspaceID={scheduling.workspaceID} workspace={workspaces.data?.find(item => item.id === scheduling.workspaceID)} initialThread={scheduling.thread} revision={revision} notify={props.notify} onClose={() => setScheduling(null)} /></Suspense>}<Suspense fallback={null}><ApprovalPanel {...props} /></Suspense>{management.dialogs}{creation && <CreateSessionDialog initialWorkspaceID={creation.workspaceID} currentSession={active} workspaces={workspaces.data ?? []} onClose={() => setCreation(null)} onCreated={thread => { setCreation(null); setArchived(false); refresh(); onSelect({ engine: thread.engine, id: thread.id }); }} />}</>}>
    {openFile => selected ? active ? <Suspense fallback={<PageLoading />}><Conversation key={sessionKey(active)} {...props} thread={active} revision={revision} onChanged={refresh} onOpenFile={openFile} onNew={() => openNew(active.workspace_id)} onFork={eventID => management.fork(active, eventID)} onSchedule={() => management.schedule(active)} onArchive={() => void management.archive(active)} busy={management.busy} /></Suspense> : detail.error ? <ErrorState error={detail.error} reload={detail.reload} /> : <PageLoading /> : <div className="gemini-welcome"><MessageSquare size={40} /><h2>{t("sessions.welcome")}</h2><p>{t("sessions.intro")}</p><button className="primary-button" onClick={() => openNew()}><Plus size={17} />{t("sessions.newSession")}</button></div>}
  </SessionPage>;
}
