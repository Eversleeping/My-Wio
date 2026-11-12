import { SessionCollaboration } from "../components/SessionCollaboration";
import { ClaudeQuestion } from "../components/ClaudeQuestion";
import { type CSSProperties, type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Copy, MoreHorizontal, Archive, ArchiveRestore, ChevronDown, ChevronRight, Folder, Pin, ArrowUp, Bot, CalendarClock, GitFork, ListChecks, Settings2, Gauge, X, LoaderCircle, MessageSquare, Pencil, Plus, Square, Trash2, UserRound, Workflow } from "lucide-react";
import { api, patch, post, remove } from "../api";
import { Dialog, ErrorBanner, Field, WorkspaceFilesPanel, message, type FilePreviewSelection } from "../App";
import { SlashCommandMenu, type SlashCommandItem } from "../SlashCommandMenu";
import { CollaborationTaskDialog, useSubmitPrompt } from "../components/CollaborationTaskDialog";
import { compressImage } from "../imageCompression";
import { MessageImages } from "../components/MessageImages";
import { ComposerImagePicker } from "../components/ComposerImagePicker";
import { SessionUsage } from "../components/SessionUsage";
import { ClaudeModelPicker } from "../components/ClaudeModelPicker";
import { ClaudeToolGroup } from "../components/ClaudeToolGroup";
import { ClaudeCompactionEvent } from "../components/ClaudeCompactionEvent";
import { claudeDisplayEvents } from "../claudeContent";
import { useClaudeSkills, validClaudeSkillName, type ClaudeSkill } from "../claudeSkills";
import { useClaudeModels, type ClaudeModel } from "../claudeModels";
import { useClaudeManagement } from "./claudeManagement";
import { ContextMenu, type ContextMenuAction } from "../ContextMenu";
import { HiddenProjectsButton } from "../components/HiddenProjectsButton";
import { SidebarHeightDivider, readSidebarHeight } from "../components/SidebarHeightDivider";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DialogActions } from "../components/Dialog";
import { Empty, ErrorState, Status } from "../components/PageUI";
import { SessionPreviewPanels, SessionPreviewTabs, useSessionPreviews } from "../components/SessionPreviews";
import { useI18n } from "../i18n";
import { formatDate, formatTime } from "../format";
import MarkdownContent from "../MarkdownContent";
import type { ClaudeThread, Server, StreamEvent, Workspace } from "../types";
import { useData, useThrottledValue } from "../useData";

type Props = { realtime: number; notify: (text: string) => void; selectedThreadID: string; onSelectThread: (id: string, replace?: boolean) => void };
const modes = ["default", "acceptEdits", "plan", "bypassPermissions"];
const activeTurn = (thread: ClaudeThread) => ["queued", "running", "interrupting"].includes(thread.status);

export default function ClaudePage({ realtime, notify, selectedThreadID, onSelectThread }: Props) {
  const { t } = useI18n();
  const [sidebarHeight, setSidebarHeight] = useState(() => readSidebarHeight("wio_claude_sidebar_height"));
  const [search, setSearch] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  useEffect(() => { try { localStorage.setItem("wio_claude_sidebar_height", String(sidebarHeight)); } catch { /* Keep resizing available without storage. */ } }, [sidebarHeight]);
  const [tick, setTick] = useState(0);
  const [archived, setArchived] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [workspaceID, setWorkspaceID] = useState("");
  const [model, updateModel] = useState("sonnet");
  const modelSelected = useRef(false);
  const setModel = (value: string) => { modelSelected.current = true; updateModel(value); };
  const [effort, setEffort] = useState("");
  const [mode, setMode] = useState("default");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [renaming, setRenaming] = useState<ClaudeThread | null>(null);
  const [title, setTitle] = useState("");
  const [deleting, setDeleting] = useState<ClaudeThread | null>(null);
  const [mobile, setMobile] = useState("sessions");
  const newModels = useClaudeModels(createOpen ? workspaceID : "", "create");
  useEffect(() => {
    if (!modelSelected.current && newModels.data?.default_model) updateModel(newModels.data.default_model);
  }, [newModels.data]);
  const dependency = `${realtime}:${tick}`;
  const threads = useData<ClaudeThread[]>(`/claude/threads?archived=${archived}`, dependency);
  const selected = useData<ClaudeThread>(selectedThreadID ? `/claude/threads/${selectedThreadID}` : null, dependency);
  const workspaces = useData<Workspace[]>("/workspaces", dependency);
  const servers = useData<Server[]>("/servers", realtime);
  const thread = threads.data?.find(item => item.id === selectedThreadID) ?? selected.data;
  const previews = useSessionPreviews(`${selectedThreadID}:${thread?.workspace_id ?? ""}`, thread?.workspace_id ?? "");
  const openFile = (selection: FilePreviewSelection) => { previews.open(selection); setMobile("conversation"); };
  const refresh = () => setTick(value => value + 1);

  useEffect(() => { const timer = window.setInterval(() => { if (!document.hidden) refresh(); }, 5000); return () => clearInterval(timer); }, []);
  useEffect(() => { if (!selectedThreadID) { const first = threads.data?.find(item => !item.project_hidden_at); if (first) onSelectThread(first.id, true); } }, [selectedThreadID, threads.data, onSelectThread]);
  useEffect(() => { if (selected.data) setArchived(Boolean(selected.data.archived_at)); }, [selected.data?.id, selected.data?.archived_at]);
  useEffect(() => { if (selectedThreadID) setMobile("conversation"); }, [selectedThreadID]);

  const create = async (event: FormEvent) => {
    event.preventDefault(); if (newModels.loading) return; setBusy(true); setError("");
    try {
      const created = await post<ClaudeThread>("/claude/threads", { workspace_id: workspaceID, model: model.trim(), permission_mode: mode, effort });
      setArchived(false); setCreateOpen(false); onSelectThread(created.id); refresh();
    } catch (err) { setError(message(err)); } finally { setBusy(false); }
  };
  const change = async (target: ClaudeThread, data: object) => {
    setBusy(true); setError("");
    try { await patch(`/claude/threads/${target.id}`, data); setRenaming(null); if ("archived" in data) onSelectThread(""); refresh(); }
    catch (err) { notify(message(err)); } finally { setBusy(false); }
  };
  const deleteSession = async () => {
    if (!deleting) return; setBusy(true);
    try { await remove(`/claude/threads/${deleting.id}`); if (selectedThreadID === deleting.id) onSelectThread(""); setDeleting(null); refresh(); }
    catch (err) { notify(message(err)); } finally { setBusy(false); }
  };
  const modelForWorkspace = (id: string) => { const workspace = workspaces.data?.find(item => item.id === id); return servers.data?.find(item => item.id === workspace?.server_id)?.claude_profile_model || "sonnet"; };
  const newSession = (id = thread?.workspace_id ?? "") => { const previous = threads.data?.find(item => item.workspace_id === id); setWorkspaceID(id); updateModel(modelForWorkspace(id)); modelSelected.current = false; setEffort(previous?.effort ?? ""); setMode(previous?.permission_mode ?? "default"); setError(""); setCreateOpen(true); };

  const groups = useMemo(() => groupClaudeThreads(threads.data ?? [], workspaces.data ?? [], search), [search, threads.data, workspaces.data]);
  const management = useClaudeManagement({ workspaces: workspaces.data ?? [], busy, setBusy, refresh, select: onSelectThread, notify });
  const threadActions = (item: ClaudeThread): ContextMenuAction[] => { const actions=management.threadActions(item); return [
    ...actions.filter(action => action.id === "pin"),
    { id: "rename", label: t("gemini.rename"), icon: Pencil, disabled: busy || activeTurn(item) || item.recovery_pending, onSelect: () => { setRenaming(item); setTitle(item.title); } },
    ...actions.filter(action => ["schedule","fork","fork-worktree"].includes(action.id)),
    { id: "archive", danger: !item.archived_at, separatorBefore: true, label: t(item.archived_at ? "gemini.restore" : "gemini.archive"), icon: item.archived_at ? ArchiveRestore : Archive, disabled: busy || activeTurn(item) || item.recovery_pending, onSelect: () => change(item, { archived: !item.archived_at }) },
    ...actions.filter(action => action.id.startsWith("copy-") || action.id === "new-window"),
  ]; };


  return <div className={`codex-layout gemini-layout gemini-mobile-${mobile}`}>
    <div className="gemini-mobile-tabs">{["sessions", "files", "conversation"].map(view => <button key={view} className={mobile === view ? "active" : ""} onClick={() => setMobile(view)}>{t(`gemini.${view}`)}</button>)}</div>
    <aside className="codex-sidebar" style={{ "--thread-list-height": `${sidebarHeight}%` } as CSSProperties}>
      <div className="thread-list gemini-thread-list">
        <div className="panel-heading"><div><Bot size={18} /><h2>{t(archived ? "codex.archivedTasks" : "gemini.sessions")}</h2></div><div className="row-actions">
          <HiddenProjectsButton realtime={dependency} onRestored={refresh} notify={notify} />
          <button className={`icon-button ${archived ? "active" : ""}`} aria-pressed={archived} aria-label={t(archived ? "gemini.showActive" : "gemini.showArchived")} onClick={() => { setArchived(!archived); onSelectThread(""); }}><Archive size={18} /></button>
          {!archived && <button className="icon-button" aria-label={t("claude.newSession")} onClick={() => newSession()}><Plus size={18} /></button>}
        </div></div>
        <input className="gemini-session-search" type="search" aria-label={t("gemini.searchSessions")} placeholder={t("gemini.searchSessions")} value={search} onChange={event => setSearch(event.target.value)} />
        <div className="thread-items">{threads.error ? <ErrorState error={threads.error} reload={threads.reload} /> : threads.loading ? <p className="gemini-hint">{t("common.loading")}</p> : groups.length ? groups.map(([id, group]) => {
          const first = group.context;
          const heading = <button type="button" className="thread-project-toggle" aria-expanded={!collapsed.has(id)} title={`${t(collapsed.has(id) ? "codex.expandWorkspace" : "codex.collapseWorkspace")} · ${first.server_name} · ${group.path}`} onClick={() => setCollapsed(current => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; })}>{collapsed.has(id) ? <ChevronRight size={14} /> : <ChevronDown size={14} />}<Folder size={15} /><strong><span>{group.name}</span><small>{first.server_name}</small></strong>{first.project_pinned_at && <Pin className="pinned-icon" size={12} />}<span>{group.threads.length}</span></button>;
          return <section className="thread-project" key={id}>
            {archived ? <div className="thread-project-heading">{heading}</div> : <ContextMenu className="thread-project-heading" label={t("codex.workspaceMenu", { name: group.name })} actions={[
              { id: "new-session", label: t("codex.createSessionHere"), icon: Plus, disabled: busy, onSelect: () => newSession(id) },
              ...management.projectActions(first),
            ]}>{heading}</ContextMenu>}
            {!collapsed.has(id) && <div className="project-threads">{[...group.threads].sort((a,b)=>Number(!a.pinned_at)-Number(!b.pinned_at) || (b.pinned_at ?? "").localeCompare(a.pinned_at ?? "")).map(item => <ContextMenu key={item.id} className={`thread ${thread?.id === item.id ? "active" : ""}`} label={t("codex.threadMenu", { name: item.title })} actions={threadActions(item)}>
              <button className="thread-select" onClick={() => { onSelectThread(item.id); setMobile("conversation"); }}><span><strong>{item.pinned_at && <Pin size={12} />}{item.title}</strong><small>{item.server_name}</small></span></button>
              <div className="thread-actions"><Status value={item.status} /><button className="icon-button danger thread-delete" aria-label={t("claude.delete")} disabled={busy || activeTurn(item) || item.recovery_pending} onClick={() => setDeleting(item)}><Trash2 size={14} /></button></div>
            </ContextMenu>)}</div>}
          </section>;
        }) : <Empty icon={<MessageSquare size={24} />} text={t("claude.noSessions")} />}</div>
      </div>
      {thread && <SidebarHeightDivider value={sidebarHeight} onChange={setSidebarHeight} />}
      {thread && <WorkspaceFilesPanel workspaceID={thread.workspace_id} taskID={thread.operation_id} taskStatus={thread.status} realtime={realtime + tick} activePath={previews.preview?.path ?? ""} activeMode={previews.preview?.mode === "diff" ? "diff" : "file"} onOpenFile={openFile} notify={notify} />}
    </aside>
    <div className="gemini-main">{thread ? <>
      <SessionPreviewTabs previews={previews} />
      {thread && <SessionCollaboration key={"collaboration:"+thread.id} engine="claude" id={thread.id} workspaceID={thread.workspace_id} projectID={thread.project_id} revision={realtime} />}
      <SessionPreviewPanels previews={previews} workspaceID={thread.workspace_id} workspaceRoot={thread.path} writable realtime={realtime + tick} notify={notify} onOpenFile={openFile}>
        <ClaudeSession key={thread.id} thread={thread} revision={dependency} onChanged={refresh} onOpenFile={openFile} onArchive={() => void change(thread, { archived: !thread.archived_at })} onSchedule={() => management.schedule(thread)} onFork={eventID => management.fork(thread, eventID)} notify={notify} onNew={() => newSession(thread.workspace_id)} />
      </SessionPreviewPanels>
    </> : selectedThreadID && selected.error ? <ErrorState error={selected.error} reload={selected.reload} /> : <div className="gemini-welcome"><Bot size={40} /><h2>Claude</h2><p>{t("claude.intro")}</p><button className="primary-button" onClick={() => newSession()}><Plus size={17} />{t("claude.newSession")}</button></div>}</div>
    <Dialog open={createOpen} title={t("claude.newSession")} onClose={() => { if (!busy) setCreateOpen(false); }}><form onSubmit={create}>
      {workspaces.error && <ErrorState error={workspaces.error} reload={workspaces.reload} />}
      <Field label={t("gemini.workspace")}><select required value={workspaceID} onChange={event => { setWorkspaceID(event.target.value); updateModel(modelForWorkspace(event.target.value)); modelSelected.current = false; setEffort(""); }}><option value="">{t("gemini.selectWorkspace")}</option>{(workspaces.data ?? []).filter(item => item.status === "ready").map(item => <option key={item.id} value={item.id}>{item.project_name} · {item.server_name} · {item.path}</option>)}</select></Field>
      <ClaudeOptions availableModels={newModels.models} model={model} mode={mode} effort={effort} setEffort={setEffort} setModel={setModel} setMode={setMode} disabled={busy} effortSupported={Boolean(servers.data?.find(item => item.id === workspaces.data?.find(workspace => workspace.id === workspaceID)?.server_id)?.claude_effort_supported)} />
      <p className="gemini-hint">{t("claude.permissionsHelp")}</p>{error && <ErrorBanner text={error} />}
      <DialogActions><button className="primary-button" disabled={busy || newModels.loading || !workspaceID || !model.trim()}>{t("claude.newSession")}</button></DialogActions>
    </form></Dialog>
    <Dialog open={Boolean(renaming)} title={t("gemini.rename")} onClose={() => { if (!busy) setRenaming(null); }}><form onSubmit={event => { event.preventDefault(); if (renaming) void change(renaming, { title: title.trim() }); }}><Field label={t("gemini.title")}><input required maxLength={180} value={title} onChange={event => setTitle(event.target.value)} /></Field><DialogActions><button className="primary-button" disabled={busy || !title.trim()}>{t("common.save")}</button></DialogActions></form></Dialog>
    {management.dialogs}
    <ConfirmDialog open={Boolean(deleting)} danger title={t("claude.delete")} impact={t("claude.deleteImpact")} confirmLabel={t("common.delete")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setDeleting(null)} onConfirm={deleteSession} />
  </div>;
}

function ClaudeOptions({ model, mode, effort, setModel, setMode, setEffort, disabled, compact = false, effortSupported = true, availableModels = [], requestCustom = 0 }: { model: string; mode: string; effort: string; setModel: (value: string) => void; setMode: (value: string) => void; setEffort: (value: string) => void; disabled: boolean; compact?: boolean; effortSupported?: boolean; availableModels?: ClaudeModel[]; requestCustom?: number }) {
  const { t } = useI18n();
  const controls = [
    <ClaudeModelPicker key="model" value={model} onChange={setModel} models={availableModels} disabled={disabled} requestCustom={requestCustom} />,
    <select className="claude-effort-select" key="effort" aria-label={t("claude.effort")} title={t(effortSupported ? "claude.effort" : "claude.effortUpgrade")} value={effort} disabled={disabled} onChange={event => setEffort(event.target.value)}>{["", "low", "medium", "high", "xhigh", "max"].map(value => <option key={value} value={value} disabled={!!value && !effortSupported}>{t(`claude.effort.${value || "default"}`)}</option>)}</select>,
    <select className="claude-permission-select" key="mode" aria-label={t("claude.permission")} title={t("claude.permissionsHelp")} value={mode} disabled={disabled} onChange={event => setMode(event.target.value)}>{modes.map(value => <option key={value} value={value}>{t(`claude.permission.${value}`)}</option>)}</select>,
  ];
  return compact ? <div className="claude-composer-options">{controls}</div> : <div className="claude-options">{controls.map((control, index) => <Field key={control.key} label={t(["claude.model", "claude.effort", "claude.permission"][index])}>{control}</Field>)}</div>;
}

function ClaudeSession({ thread, revision, onChanged, onOpenFile, onArchive, onSchedule, onFork, onNew, notify }: { thread: ClaudeThread; revision: string; onChanged: () => void; onOpenFile: (selection: FilePreviewSelection) => void; onArchive: () => void; onSchedule: () => void; onFork: (eventID?: string) => void; onNew: () => void; notify: (text: string) => void }) {
  const { t } = useI18n();
  const availableModels = useClaudeModels(thread.workspace_id, thread.id);
  const skillDiscovery = useClaudeSkills(thread.workspace_id, thread.id, Boolean(thread.skills_supported) && thread.server_status === "online");
  const [requestCustomModel, setRequestCustomModel] = useState(0);
  const [prompt, setPrompt] = useState(""); const [model, updateModel] = useState(thread.model); const [mode, setMode] = useState(thread.permission_mode); const [effort, setEffort] = useState(thread.effort ?? "");
  const modelSelected = useRef(false);
  const handledModelRevision = useRef("");
  const setModel = useCallback((value: string) => { modelSelected.current = true; updateModel(value); }, []);
  const [editingEventID, setEditingEventID] = useState("");
  const [busy, setBusy] = useState(false); const [error, setError] = useState(""); const [confirming, setConfirming] = useState(false);
  const [allEvents, setAllEvents] = useState<StreamEvent[]>([]); const [historyBusy, setHistoryBusy] = useState(false);
  const allEventsRef = useRef<StreamEvent[]>([]);
  const stream = useRef<HTMLDivElement>(null); const follow = useRef(true);
  const throttledRevision = useThrottledValue(revision, 750);
  const historySession = useRef(thread.session_id);
  useEffect(() => {
    if (historySession.current === thread.session_id) return;
    historySession.current = thread.session_id;
    allEventsRef.current = []; setAllEvents([]);
  }, [thread.session_id]);
  const events = useData<StreamEvent[]>(`/claude/threads/${thread.id}/events?session=${encodeURIComponent(thread.session_id)}`, throttledRevision);
  const mergeEvents = useCallback((incoming: StreamEvent[]) => {
    const lastSequence = allEventsRef.current.at(-1)?.sequence ?? 0;
    const replacement = [...incoming].reverse().find(event => event.sequence > lastSequence && (event.payload as {history_rewritten?: boolean}).history_rewritten);
    const cutoff = (replacement?.payload as {replaced_sequence?: number} | undefined)?.replaced_sequence;
    const retained = cutoff ? allEventsRef.current.filter(event => event.sequence < cutoff) : allEventsRef.current;
    const merged = new Map(retained.map(event => [event.event_id, event]));
    for (const event of incoming) merged.set(event.event_id, event);
    allEventsRef.current = [...merged.values()].sort((a, b) => a.sequence - b.sequence);
    setAllEvents(allEventsRef.current);
  }, []);
  useEffect(() => {
    if (!events.data?.length) return;
    const incoming = events.data;
    let cursor = allEventsRef.current.at(-1)?.sequence ?? 0;
    const first = incoming[0].sequence;
    if (!cursor || first <= cursor + 1 || incoming.some(event => (event.payload as {history_rewritten?:boolean}).history_rewritten)) { mergeEvents(incoming); return; }
    // A recent-events refresh can skip more than one page while the tab is
    // inactive. Fill the gap before appending the recent tail.
    const controller = new AbortController();
    void (async () => {
      while (cursor < first - 1) {
        const page = await api<StreamEvent[]>(`/claude/threads/${thread.id}/events?after=${cursor}`, { signal: controller.signal });
        if (controller.signal.aborted) return;
        const next = page.at(-1)?.sequence ?? cursor;
        if (next <= cursor) throw new Error("Could not load missing Claude events");
        mergeEvents(page);
        cursor = next;
      }
      mergeEvents(incoming);
    })().catch(err => { if (!controller.signal.aborted) setError(message(err)); });
    return () => controller.abort();
  }, [events.data, mergeEvents, thread.id, thread.session_id]);
  const running = activeTurn(thread);
  const steerState = [...allEvents].reverse().find(event => ["claude.steer.ready", "claude.steer.closed"].includes(event.kind) && (event.payload as {operation_id?:string}).operation_id === thread.operation_id);
  const steerReady = thread.status === "running" && Boolean(thread.steer_supported) && (steerState ? steerState.kind === "claude.steer.ready" : Boolean(thread.steer_ready));
  const blocked = (running && (!steerReady || Boolean(editingEventID))) || thread.recovery_pending || Boolean(thread.archived_at) || thread.server_status !== "online";
  useEffect(() => {
    const data = availableModels.data;
    const revision = data?.credential_revision;
    if (running || blocked || busy || !revision || !data.default_model || handledModelRevision.current === revision) return;
    handledModelRevision.current = revision;
    if (!modelSelected.current && thread.model_credential_revision !== revision) updateModel(data.default_model);
  }, [availableModels.data, running, blocked, busy, thread.model_credential_revision]);
  const steeringRequest = useRef<{operation:string;prompt:string;images:string;id:string}>();
  const [images,setImages]=useState<{id:string;dataURL:string}[]>([]);
  const [imageBusy,setImageBusy]=useState(false);
  const imageWork=useRef<AbortController|null>(null);
  const promptRef=useRef<HTMLTextAreaElement>(null);
  const slashKeyboardRef=useRef<((event:React.KeyboardEvent<HTMLTextAreaElement>)=>boolean)|null>(null);
  const [slashDismissed,setSlashDismissed]=useState("");
  const [slashSection,setSlashSection]=useState("");
  const [helpOpen,setHelpOpen]=useState(false);
  const [collabOpen,setCollabOpen]=useState(false);
  const composerFormRef=useRef<HTMLFormElement>(null);
  const submitPrompt=useSubmitPrompt(composerFormRef,prompt,setPrompt);
  useEffect(()=>()=>imageWork.current?.abort(),[]);
  const addImages=async(files:File[])=>{
    if (blocked || busy || imageBusy) return;
    if (!thread.input_supported) {setError(t("claude.inputUpgrade"));return;}
    const available=4-images.length;if(available<=0){setError(t("codex.imageLimit"));return;}
    const controller=new AbortController();imageWork.current=controller;setImageBusy(true);setError("");
    try {const prepared:{id:string;dataURL:string}[]=[];for(const file of files.slice(0,available)){prepared.push({id:crypto.randomUUID(),dataURL:await compressImage(file,{signal:controller.signal})});}
      if(!controller.signal.aborted){setImages(current=>[...current,...prepared].slice(0,4));if(files.length>available)setError(t("codex.imageLimit"));}
    } catch {if(!controller.signal.aborted)setError(t("codex.imageFailed"));}
    finally {if(imageWork.current===controller){imageWork.current=null;setImageBusy(false);}}
  };
  const slashOpen=!editingEventID && !running && !blocked && !busy && /^\/[A-Za-z0-9_.:/-]*$/.test(prompt) && prompt!==slashDismissed;
  const finishSlash=(action:()=>void)=>{setPrompt("");setSlashSection("");action();};
  const latestInit = [...allEvents].reverse().find(event => event.kind === "claude.init");
  const historicalSkills = (latestInit?.payload as { skills?: unknown[] })?.skills;
  const nativeSkills: ClaudeSkill[] = skillDiscovery.skills ?? [...new Set(Array.isArray(historicalSkills) ? historicalSkills.filter((name): name is string => typeof name === "string" && validClaudeSkillName(name)) : [])].map(name => ({ name, description: t("claude.nativeSkill") }));
  const skillItem = (skill: ClaudeSkill): SlashCommandItem => ({ id: `skill:${skill.name}`, name: `/${skill.name}`, description: skill.description || t("claude.nativeSkill"), detail: skill.argument_hint, icon: ListChecks, onSelect: () => { setSlashSection(""); setPrompt(`/${skill.name} `); promptRef.current?.focus(); } });
  const skillsStatus = skillDiscovery.loading ? t("claude.skillsLoading") : skillDiscovery.error ? `${t("claude.skillsFailed")} ${skillDiscovery.error}` : !thread.skills_supported ? t("claude.skillsUpgrade") : thread.server_status !== "online" ? t("claude.skillsOffline") : !nativeSkills.length ? t("claude.skillsEmpty") : "";
  const slashItems:SlashCommandItem[]=slashSection==="skills" ? [...nativeSkills.map(skillItem), ...(thread.skills_supported && thread.server_status === "online" ? [{ id: "refresh-skills", name: t("claude.skillsRefresh"), description: "", icon: ListChecks, onSelect: () => { if (!skillDiscovery.loading) skillDiscovery.reload(); } }] : [])] : slashSection==="effort" ? ["","low","medium","high","xhigh","max"].filter(value=>!value || thread.effort_supported).map(value=>({id:value||"default",name:t(`claude.effort.${value||"default"}`),description:"",icon:Gauge,selected:effort===value,onSelect:()=>finishSlash(()=>setEffort(value))})) : slashSection==="permissions" ? modes.map(value=>({id:value,name:t(`claude.permission.${value}`),description:"",icon:Settings2,selected:mode===value,onSelect:()=>finishSlash(()=>setMode(value))})) : slashSection==="model" ? [...new Set([model,thread.model,...availableModels.models.map(item=>item.id),"sonnet","opus","haiku"])].filter(Boolean).map(value=>({id:value,name:value,description:availableModels.models.find(item=>item.id===value)?.name ?? "",icon:Bot,selected:model===value,onSelect:()=>finishSlash(()=>setModel(value))})).concat([{id:"custom",name:t("gemini.customModel"),description:"",icon:Bot,selected:false,onSelect:()=>finishSlash(()=>setRequestCustomModel(value=>value+1))}]) : [
    {id:"model",name:"/model",description:t("claude.model"),icon:Bot,onSelect:()=>{setSlashSection("model");setPrompt("/");}},
    {id:"effort",name:"/effort",description:t("claude.effort"),icon:Gauge,onSelect:()=>{setSlashSection("effort");setPrompt("/");}},
    {id:"permissions",name:"/permissions",description:t("claude.permission"),icon:Settings2,onSelect:()=>{setSlashSection("permissions");setPrompt("/");}},
    { id: "skill", name: "/skill", description: t("codex.slashSkillsDescription"), icon: ListChecks, onSelect: () => { setSlashSection("skills"); setPrompt("/"); } },
    {id:"plan",name:"/plan",description:t("claude.permission.plan"),icon:ListChecks,onSelect:()=>finishSlash(()=>setMode("plan"))},
    {id:"new",name:"/new",description:t("claude.newSession"),icon:Plus,onSelect:()=>finishSlash(onNew)},
    {id:"schedule",name:"/schedule",description:t("codex.createScheduledTask"),icon:CalendarClock,onSelect:()=>finishSlash(onSchedule)},
    ...(thread.input_supported && thread.session_id ? [{id:"fork",name:"/fork",description:t("codex.continueInNewTask"),icon:GitFork,onSelect:()=>finishSlash(onFork)}] : []),
    {id:"collab",name:"/collab",description:t("codex.slashCollabDescription"),icon:Workflow,onSelect:()=>finishSlash(()=>setCollabOpen(true))},
    {id:"help",name:"/help",description:t("claude.commandsHelp"),icon:MessageSquare,onSelect:()=>finishSlash(()=>setHelpOpen(true))},
    ...nativeSkills.filter(skill=>!["model","effort","permissions","skill","skills","plan","new","schedule","fork","collab","help"].includes(skill.name)).map(skillItem),
  ];
  const showInterrupt = running && (!steerReady || (!prompt.trim() && !images.length));
  const queuedInstructions = allEvents.filter(event=>event.kind==="claude.steer.queued").flatMap(event=>{
    const payload=event.payload as {request_id:string;text:string;images?:{data_url:string}[];operation_id:string};
    if(allEvents.some(item=>item.kind==="claude.steer.applied" && (item.payload as {request_id?:string}).request_id===payload.request_id))return [];
    const receipt=[...allEvents].reverse().find(item=>["claude.steer.accepted","claude.steer.applied","claude.steer.rejected","claude.steer.unconfirmed"].includes(item.kind) && (item.payload as {request_id?:string}).request_id===payload.request_id);
    if(receipt?.kind==="claude.steer.applied")return [];
    return [{...payload,id:event.event_id,state:receipt?.kind.split(".").at(-1) ?? "queued"}];
  });
  const latestMessage = [...allEvents].reverse().find(event => ["claude.message", "claude.user.message", "claude.steer.applied"].includes(event.kind));
  const items = useMemo(() => claudeDisplayEvents(allEvents, t("claude.denied")), [allEvents, t]);
  useEffect(() => { if (follow.current && stream.current) stream.current.scrollTop = stream.current.scrollHeight; }, [items]);
  const send = async (event: FormEvent) => {
    event.preventDefault(); if (blocked || busy || (!running && availableModels.loading) || imageBusy || slashOpen || (!prompt.trim() && !images.length)) return;
    const option=!editingEventID && !running && /^\/(model|effort|permissions)\s+(\S+)\s*$/.exec(prompt);
    if(option){const [,command,value]=option;if(command==="model")setModel(value);else if(command==="effort" && ["default","low","medium","high","xhigh","max"].includes(value) && (value==="default" || thread.effort_supported))setEffort(value==="default"?"":value);else if(command==="permissions" && modes.includes(value))setMode(value);else {setError(t("claude.invalidCommandOption"));return;}setPrompt("");setError("");return;}
    setBusy(true); setError("");
    try {
      const imagePayload = images.map(image => ({ data_url: image.dataURL }));
      if (running) {
        if (!steerReady) return;
        const imageIDs = images.map(image=>image.id).join(",");
        if (!steeringRequest.current || steeringRequest.current.operation!==thread.operation_id || steeringRequest.current.prompt!==prompt || steeringRequest.current.images!==imageIDs) steeringRequest.current={operation:thread.operation_id,prompt,images:imageIDs,id:crypto.randomUUID()};
        await post(`/claude/threads/${thread.id}/steer`, {prompt:prompt.trim(),operation_id:thread.operation_id,request_id:steeringRequest.current.id,...(images.length?{images:imagePayload}:{})});
        steeringRequest.current=undefined;
      } else {
        if (steeringRequest.current) { setError(t("claude.steerTaskEnded")); return; }
        await post(editingEventID ? `/claude/threads/${thread.id}/events/${editingEventID}/rewrite` : `/claude/threads/${thread.id}/turns`, { prompt: prompt.trim(), model: model.trim(), permission_mode: mode, effort, ...(images.length ? {images:imagePayload} : {}) });
      }
      setPrompt(""); setImages([]); setEditingEventID(""); follow.current=true; onChanged();
    }
    catch (err) { setError(message(err)); } finally { setBusy(false); }
  };
  const interrupt = async () => { setBusy(true); setError(""); try { await post(`/claude/threads/${thread.id}/interrupt`, {}); onChanged(); } catch (err) { setError(message(err)); } finally { setBusy(false); } };
  const confirmStopped = async () => { setBusy(true); try { await post(`/claude/threads/${thread.id}/confirm-stopped`, { operation_id: thread.operation_id, confirmed: true }); setConfirming(false); onChanged(); } catch (err) { setError(message(err)); } finally { setBusy(false); } };
  const earlier = async () => {
    if (!allEvents.length || historyBusy) return; setHistoryBusy(true);
    try { const session = historySession.current; const older = await api<StreamEvent[]>(`/claude/threads/${thread.id}/events?before=${allEvents[0].sequence}`); if (historySession.current === session) mergeEvents(older); }
    catch (err) { setError(message(err)); } finally { setHistoryBusy(false); }
  };
  return <section className="session-panel gemini-session claude-session"><header className="session-header"><div className="session-heading"><h2>{thread.title}</h2><span className="session-meta" title={`${thread.server_name} · ${thread.path}`}>{thread.server_name}<i />{thread.path}</span></div><div className="session-actions"><Status value={thread.status} /><button className="icon-button" title={t(thread.archived_at ? "gemini.showActive" : "gemini.showArchived")} aria-label={t(thread.archived_at ? "gemini.showActive" : "gemini.showArchived")} disabled={busy || running || thread.recovery_pending} onClick={onArchive}><Archive size={16} /></button></div><SessionUsage provider="claude" threadID={thread.id} revision={revision} running={running} /></header>
    <div ref={stream} className="gemini-stream conversation-stream" role="log" aria-label={t("gemini.conversation")} onScroll={() => { if (stream.current) follow.current = stream.current.scrollHeight - stream.current.scrollTop - stream.current.clientHeight < 100; }}>
      {allEvents.length > 0 && allEvents[0].sequence > 1 && <button className="secondary-button" disabled={historyBusy} onClick={() => void earlier()}>{t("claude.loadEarlier")}</button>}
      {events.error && <ErrorState error={events.error} reload={events.reload} />}
      {!items.length && <Empty icon={<Bot size={30} />} text={t("claude.startPrompt")} />}
      {items.map(item => item.role === "compaction" ? <ClaudeCompactionEvent key={item.id} item={item} activeOperationID={running && !thread.recovery_pending && thread.server_status === "online" ? thread.operation_id : undefined} /> : item.role === "question" ? <ClaudeQuestion key={item.id} item={item} threadID={thread.id} readOnly={!thread.questions_supported || thread.status !== "running" || item.operationID !== thread.operation_id || !!item.cancelled || !!thread.archived_at || thread.server_status !== "online" || thread.recovery_pending} onAnswered={onChanged} /> : item.role === "toolGroup" ? <ClaudeToolGroup key={item.id} tools={item.tools ?? []} activeOperationID={running && !thread.recovery_pending && thread.server_status === "online" ? thread.operation_id : undefined} /> : <article className={`message ${item.role === "notice" ? "error" : item.role}`} key={item.id}><header>{item.role === "user" ? <UserRound size={15} /> : <Bot size={15} />}<strong>{item.role === "user" ? t("codex.you") : "Claude"}</strong>{item.occurredAt && <time dateTime={item.occurredAt} title={formatDate(item.occurredAt)}>{formatTime(item.occurredAt)}</time>}</header>{!!item.images?.length && <MessageImages sources={item.images} />}<MarkdownContent text={item.text} workspaceRoot={thread.path} onOpenFile={onOpenFile} />{(item.role === "user" || item.role === "assistant") && <div className="message-actions">
        <button type="button" className="message-action" title={t("codex.copyMessage")} aria-label={t("codex.copyMessage")} onClick={() => { void navigator.clipboard.writeText(item.text).then(() => notify(t("codex.messageCopied"))).catch(err => notify(message(err))); }}><Copy size={14} /></button>
        {item.role === "user" && !thread.archived_at && <button type="button" className="message-action" title={t(thread.rewrite_supported ? "codex.editMessage" : "claude.rewriteAgentUpdate")} aria-label={t("codex.editMessage")} disabled={busy || blocked || running || !thread.rewrite_supported || !thread.session_id} onClick={() => { imageWork.current?.abort(); imageWork.current = null; setImageBusy(false); setPrompt(item.text); setImages((item.images ?? []).map(dataURL => ({ id: crypto.randomUUID(), dataURL }))); setEditingEventID(item.id); steeringRequest.current = undefined; setSlashSection(""); requestAnimationFrame(() => { promptRef.current?.focus(); promptRef.current?.scrollIntoView({ block: "nearest" }); }); }}><Pencil size={14} /></button>}
        {item.role === "assistant" && item.endEventID === latestMessage?.event_id && <ContextMenu className="message-menu" label={t("codex.messageMenu")} triggerIcon={MoreHorizontal} actions={[{ id: "fork", label: t("codex.branchToNewChat"), icon: GitFork, disabled: busy || blocked || running || !thread.input_supported || !thread.session_id, onSelect: () => onFork(item.endEventID) }]}>{null}</ContextMenu>}
      </div>}</article>)}
      {queuedInstructions.map(instruction=><div key={instruction.id} className="claude-steer-instruction" role="status"><strong>{t(`claude.steer.${instruction.state}`)}</strong><p>{instruction.text}</p>{!!instruction.images?.length && <MessageImages sources={instruction.images.map(image=>image.data_url)} />}{instruction.state==="rejected" && <button type="button" className="text-button" disabled={busy || Boolean(prompt.trim()) || images.length>0} onClick={()=>{setPrompt(instruction.text);setImages((instruction.images??[]).map(image=>({id:crypto.randomUUID(),dataURL:image.data_url})));steeringRequest.current=undefined;promptRef.current?.focus();}}>{t("claude.steerRestore")}</button>}</div>)}
      {running && <p className="gemini-working" role="status"><LoaderCircle className="spin" size={16} />{t(thread.status === "interrupting" ? "claude.interrupting" : "claude.working")}</p>}
    </div>
    <form ref={composerFormRef} className="composer gemini-composer claude-composer" onSubmit={send}>{error && <ErrorBanner text={error} />}{thread.recovery_pending && <><p className="gemini-hint" role="status">{t("claude.recovery")}</p><button type="button" className="secondary-button" disabled={busy} onClick={() => setConfirming(true)}>{t("claude.confirmStopped")}</button></>}{thread.server_status !== "online" && <p className="gemini-hint">{t("gemini.offline")}</p>}{thread.archived_at ? <p className="gemini-hint">{t("gemini.archivedHint")}</p> : <>
      {running && thread.status!=="interrupting" && <p className="gemini-hint" role="status">{t(steerReady ? "claude.steerHint" : thread.steer_supported ? "claude.steerStarting" : "claude.steerUpgrade")}</p>}
      {editingEventID && <div className="composer-editing"><Pencil size={14} /><span>{t("codex.editingMessage")}</span><button type="button" className="icon-button" title={t("codex.cancelEdit")} aria-label={t("codex.cancelEdit")} disabled={busy} onClick={() => { imageWork.current?.abort(); imageWork.current = null; setImageBusy(false); setEditingEventID(""); setPrompt(""); setImages([]); }}><X size={14} /></button></div>}
      {images.length>0 && <div className="composer-images">{images.map(image=><figure key={image.id}><img src={image.dataURL} alt=""/><button type="button" aria-label={t("common.close")} disabled={busy} onClick={()=>setImages(current=>current.filter(item=>item.id!==image.id))}><X size={13}/></button></figure>)}</div>}
      {imageBusy && <p className="gemini-hint" role="status">{t("gemini.preparingImages")}</p>}
      {slashOpen && <SlashCommandMenu status={slashSection === "skills" ? skillsStatus : undefined} items={slashItems} query={slashSection ? "" : prompt === "/skills" ? "skill" : prompt.slice(1)} label={t("codex.slashMenu")} backLabel={t("codex.slashBackToCommands")} onBack={slashSection?()=>{setSlashSection("");setPrompt("/");}:undefined} onDismiss={()=>{setSlashDismissed(prompt);setSlashSection("");}} keyboardRef={slashKeyboardRef}/>}
      {availableModels.error && <small className="gemini-model-error" role="status">{t("claude.modelsRefreshFailed")} {availableModels.error}</small>}
      <div className="composer-input"><ComposerImagePicker disabled={busy || blocked || imageBusy || images.length >= 4} onSelect={addImages} /><textarea ref={promptRef} onPaste={event=>{const files=Array.from(event.clipboardData.items).filter(item=>item.type.startsWith("image/")).map(item=>item.getAsFile()).filter((file):file is File=>file!==null);if(files.length){event.preventDefault();void addImages(files);}}} aria-label={t("claude.prompt")} placeholder={t("claude.prompt")} value={prompt} disabled={busy || blocked} onChange={event => {setPrompt(event.target.value);if(steeringRequest.current && event.target.value!==steeringRequest.current.prompt)steeringRequest.current=undefined;setSlashDismissed("");if(!event.target.value.startsWith("/"))setSlashSection("");}} onKeyDown={event => { if(slashOpen && slashKeyboardRef.current?.(event))return; if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} /></div><div className="composer-bar"><ClaudeOptions compact requestCustom={requestCustomModel} availableModels={availableModels.models} model={model} mode={mode} effort={effort} setEffort={setEffort} effortSupported={thread.effort_supported} setModel={setModel} setMode={setMode} disabled={busy || blocked || running} />{showInterrupt ? <button type="button" className="secondary-button danger composer-action" aria-label={t("gemini.interrupt")} disabled={busy || thread.status === "interrupting" || thread.server_status !== "online"} onClick={() => void interrupt()}><Square size={15} /><span>{t("gemini.interrupt")}</span></button> : <button className="primary-button composer-action" aria-label={t(running ? "codex.steer" : "codex.send")} disabled={busy || blocked || (!running && availableModels.loading) || imageBusy || slashOpen || (!prompt.trim() && !images.length) || !model.trim()}><ArrowUp size={16} /><span>{t(running ? "codex.steer" : "codex.send")}</span></button>}</div></>}</form>
    <CollaborationTaskDialog open={collabOpen} engine="claude" sessionID={thread.id} workspaceID={thread.workspace_id} currentModel={model} onClose={()=>setCollabOpen(false)} onSubmit={text=>{setCollabOpen(false);submitPrompt(text);}} />
    <Dialog open={helpOpen} title={t("claude.commandsHelp")} onClose={()=>setHelpOpen(false)}><p>{t("claude.commandsDescription")}</p><p><a href="https://code.claude.com/docs/en/headless" target="_blank" rel="noreferrer">Claude Code</a></p></Dialog>
    <ConfirmDialog open={confirming} danger title={t("claude.confirmStopped")} impact={t("claude.confirmStoppedImpact")} confirmLabel={t("claude.confirmStopped")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setConfirming(false)} onConfirm={confirmStopped} />
  </section>;
}

// Groups only workspaces that still contain sessions; an empty workspace would
// show as a "(0)" project entry in the sidebar.
export function groupClaudeThreads(threads: ClaudeThread[], workspaces: Workspace[], search: string) {
  const result = new Map<string, { name: string; path: string; context: ClaudeThread; threads: ClaudeThread[] }>();
  const byID = new Map(workspaces.map(item => [item.id, item]));
  for (const item of threads) {
    if (item.project_hidden_at) continue;
    const workspace = byID.get(item.workspace_id);
    const path = workspace?.path || item.path;
    const identity = [workspace?.display_name?.trim(), workspace?.branch?.trim(), path.split(/[\\/]/).filter(Boolean).at(-1)].find(value => value && value !== item.project_name);
    const group = result.get(item.workspace_id) ?? { name: identity ? `${item.project_name} · ${identity}` : item.project_name, path, context: item, threads: [] };
    group.threads.push(item); result.set(item.workspace_id, group);
  }
  const query = search.trim().toLocaleLowerCase();
  return [...result.entries()].sort(([,a],[,b]) => Number(!a.context.project_pinned_at) - Number(!b.context.project_pinned_at) || (b.context.project_pinned_at ?? "").localeCompare(a.context.project_pinned_at ?? "")).flatMap(([id, group]) => {
    if (!query || `${group.name} ${group.path}`.toLocaleLowerCase().includes(query)) return [[id, group] as const];
    const matching = group.threads.filter(item => `${item.title} ${item.server_name} ${item.id} ${item.session_id}`.toLocaleLowerCase().includes(query));
    return matching.length ? [[id, { ...group, threads: matching }] as const] : [];
  });
}
