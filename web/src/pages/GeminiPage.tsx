import { SessionUsage, formatUsageNumber } from "../components/SessionUsage";
import { GeminiTool, GeminiToolGroup } from "../components/GeminiTool";
import { MessageImages } from "../components/MessageImages";
import { compressImage } from "../imageCompression";
import { GeminiQuestion } from "../components/GeminiQuestion";
import { GeminiSkillsDialog } from "../components/GeminiSkillsDialog";
import { GeminiThinkingPicker, compatibleGeminiThinking } from "../components/GeminiThinkingPicker";
import { FormEvent, type CSSProperties, type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { Archive, ArchiveRestore, Boxes, CalendarClock, GitFork, Globe, Lightbulb, ListChecks, MoreHorizontal, Pin, ArrowUp, Bot, ChevronDown, ChevronRight, Copy, Download, Folder, LoaderCircle, MessageSquare, Pause, Pencil, Play, Plus, RefreshCw, Sparkles, Square, Target, Trash2, UserRound, Users, X } from "lucide-react";
import { api, patch, post, remove } from "../api";
import { Dialog, ErrorBanner, Field, message, WorkspaceFilesPanel, type FilePreviewSelection } from "../App";
import { SessionPreviewPanels, SessionPreviewTabs, useSessionPreviews } from "../components/SessionPreviews";
import { useGeminiManagement } from "./geminiManagement";
import { ContextMenu, type ContextMenuAction } from "../ContextMenu";
import { SlashCommandMenu, type SlashCommandItem } from "../SlashCommandMenu";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { HiddenProjectsButton } from "../components/HiddenProjectsButton";
import { SidebarHeightDivider, readSidebarHeight } from "../components/SidebarHeightDivider";
import { DialogActions } from "../components/Dialog";
import { Empty, ErrorState, Status } from "../components/PageUI";
import { geminiForkTargets, geminiMessages, groupGeminiMessages } from "../geminiContent";
import { formatTime } from "../format";
import { useI18n } from "../i18n";
import MarkdownContent from "../MarkdownContent";
import type { GeminiGoal, GeminiThread, StreamEvent, Workspace } from "../types";
import { useData } from "../useData";

type Props = { realtime: number; notify: (text: string) => void; selectedThreadID: string; onSelectThread: (id: string, replace?: boolean) => void };
const isActive = (thread?: GeminiThread) => Boolean(thread && ["queued", "running", "interrupting"].includes(thread.status));
const sidebarHeightKey = "wio_gemini_sidebar_height";

export default function GeminiPage({ realtime, notify, selectedThreadID, onSelectThread }: Props) {
  const { t } = useI18n();
  const [sidebarHeight, setSidebarHeight] = useState(() => readSidebarHeight(sidebarHeightKey));
  useEffect(() => {
    try { localStorage.setItem(sidebarHeightKey, String(sidebarHeight)); } catch { /* Resizing still works when storage is unavailable. */ }
  }, [sidebarHeight]);
  const [tick, setTick] = useState(0);
  const [archived, setArchived] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [newWorkspaceID, setNewWorkspaceID] = useState("");
  const [newModel, setNewModel] = useState("");
  const [newThinking, setNewThinking] = useState("");
  const [newMode, setNewMode] = useState("default");
  const [search, setSearch] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [deleting, setDeleting] = useState<GeminiThread | null>(null);
  const [busy, setBusy] = useState(false);
  const [rename, setRename] = useState<GeminiThread | null>(null);
  const [title, setTitle] = useState("");
  const [mobileView, setMobileView] = useState("sessions");
  const dependency = `${realtime}:${tick}`;
  const threads = useData<GeminiThread[]>(`/gemini/threads?archived=${archived}`, dependency);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const selected = useData<GeminiThread>(selectedThreadID ? `/gemini/threads/${selectedThreadID}` : null, dependency);
  const active = threads.data?.find(thread => thread.id === selectedThreadID) ?? selected.data;
  const previews = useSessionPreviews(`${selectedThreadID}:${active?.workspace_id ?? ""}`, active?.workspace_id ?? "");
  const preview = previews.preview;
  const openFile = (selection: FilePreviewSelection) => { previews.open(selection); setMobileView("conversation"); };
  useEffect(() => { const timer = window.setInterval(() => { if (!document.hidden) setTick(value => value + 1); }, 5000); return () => clearInterval(timer); }, []);
  useEffect(() => { if (!selectedThreadID) { const first=threads.data?.find(thread=>!thread.project_hidden_at);if(first) onSelectThread(first.id,true); } }, [onSelectThread, selectedThreadID, threads.data]);
  useEffect(() => { if (selected.data) setArchived(Boolean(selected.data.archived_at)); }, [selected.data?.id, selected.data?.archived_at]);
  useEffect(() => { if (selectedThreadID) setMobileView("conversation"); }, [selectedThreadID]);
  const groups = useMemo(() => {
    const result = new Map<string, { name: string; path: string; threads: GeminiThread[] }>();
    const byID = new Map((workspaces.data ?? []).map(workspace => [workspace.id, workspace]));
    for (const thread of threads.data ?? []) {
      if (thread.project_hidden_at) continue;
      const workspace = byID.get(thread.workspace_id);
      const path = workspace?.path || thread.path;
      const identity = [workspace?.display_name?.trim(), workspace?.branch?.trim(), path.split(/[\\/]/).filter(Boolean).at(-1)].find(value => value && value !== thread.project_name);
      const name = identity ? `${thread.project_name} · ${identity}` : thread.project_name;
      const group = result.get(thread.workspace_id) ?? { name, path, threads: [] };
      group.threads.push(thread); result.set(thread.workspace_id, group);
    }
    const query = search.trim().toLocaleLowerCase();
    return [...result.entries()].sort(([,a],[,b]) => Number(!a.threads[0].project_pinned_at) - Number(!b.threads[0].project_pinned_at) || (b.threads[0].project_pinned_at ?? "").localeCompare(a.threads[0].project_pinned_at ?? "")).flatMap(([id, group]) => {
      if (!query || `${group.name} ${group.path}`.toLocaleLowerCase().includes(query)) return [[id, group] as const];
      const matching = group.threads.filter(thread => `${thread.title} ${thread.server_name} ${thread.id} ${thread.session_id ?? ""}`.toLocaleLowerCase().includes(query));
      return matching.length ? [[id, { ...group, threads: matching }] as const] : [];
    });
  }, [search, threads.data, workspaces.data]);
  const openNewSession = (workspaceID = active?.workspace_id ?? "") => {
    const previous = active?.workspace_id === workspaceID ? active : threads.data?.find(thread => thread.workspace_id === workspaceID);
    setNewWorkspaceID(workspaceID); setNewModel(previous?.model ?? ""); setNewThinking(previous?.thinking_level ?? ""); setNewMode(previous?.approval_mode === "plan" ? "default" : previous?.approval_mode ?? "default"); setCreateOpen(true);
  };
  const toggleGroup = (id: string) => setCollapsed(current => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; });
  const refresh = () => { threads.reload(); selected.reload(); setTick(value => value + 1); };
  const management = useGeminiManagement({ workspaces: workspaces.data ?? [], busy, setBusy, refresh, select: onSelectThread, notify });
  const mutate = async (thread: GeminiThread, values: unknown) => {
    setBusy(true);
    try { await patch(`/gemini/threads/${thread.id}`, values); setRename(null); refresh(); }
    catch (error) { notify(message(error)); } finally { setBusy(false); }
  };
  const createSessionInWorkspace = async (workspaceID: string) => {
    if (busy) return;
    const previous = active?.workspace_id === workspaceID ? active : threads.data?.find(thread => thread.workspace_id === workspaceID);
    if (!previous?.model) { openNewSession(workspaceID); return; }
    setBusy(true);
    try {
      const created = await post<GeminiThread>("/gemini/threads", { workspace_id: workspaceID, model: previous.model, approval_mode: previous.approval_mode === "plan" ? "default" : previous.approval_mode, thinking_level: previous.thinking_level ?? "" });
      setArchived(false); onSelectThread(created.id); refresh();
    } catch (error) { notify(message(error)); } finally { setBusy(false); }
  };
  const threadMenuActions = (thread: GeminiThread): ContextMenuAction[] => {
    const actions=management.threadActions(thread);
    return [
    ...actions.filter(action=>action.id==="pin"),
    { id: "rename", label: t("gemini.rename"), icon: Pencil, disabled: isActive(thread) || thread.recovery_pending || busy, onSelect: () => { setRename(thread); setTitle(thread.title); } },
    ...actions.filter(action=>["schedule","fork","fork-worktree"].includes(action.id)),
    { id: "archive", label: t(thread.archived_at ? "gemini.restore" : "gemini.archive"), icon: thread.archived_at ? ArchiveRestore : Archive, disabled: isActive(thread) || thread.recovery_pending || busy, onSelect: () => mutate(thread, { archived: !thread.archived_at }) },
    ...actions.filter(action=>!["pin","schedule","fork","fork-worktree"].includes(action.id))
  ]; };
  const deleteThread = async () => {
    if (!deleting) return;
    setBusy(true);
    try { await remove(`/gemini/threads/${deleting.id}`); if (selectedThreadID === deleting.id) onSelectThread(""); setDeleting(null); refresh(); }
    catch (error) { notify(message(error)); } finally { setBusy(false); }
  };

  return <div className={`codex-layout gemini-layout gemini-mobile-${mobileView}`}>
    <div className="gemini-mobile-tabs">{["sessions", "files", "conversation"].map(view => <button key={view} className={mobileView === view ? "active" : ""} onClick={() => setMobileView(view)}>{t(`gemini.${view}`)}</button>)}</div>
    <aside className="codex-sidebar" style={{ "--thread-list-height": `${sidebarHeight}%` } as CSSProperties}>
      <div className="thread-list gemini-thread-list">
        <div className="panel-heading"><div><Sparkles size={18} /><h2>{t(archived ? "codex.archivedTasks" : "gemini.sessions")}</h2></div><div className="row-actions"><HiddenProjectsButton realtime={dependency} onRestored={refresh} notify={notify} /><button className={`icon-button ${archived ? "active" : ""}`} aria-pressed={archived} aria-label={t(archived ? "gemini.showActive" : "gemini.showArchived")} onClick={() => { setArchived(!archived); onSelectThread(""); }}><Archive size={18} /></button>{!archived && <button className="icon-button" aria-label={t("gemini.newSession")} onClick={() => openNewSession()}><Plus size={18} /></button>}</div></div>
        <input className="gemini-session-search" type="search" aria-label={t("gemini.searchSessions")} placeholder={t("gemini.searchSessions")} value={search} onChange={event => setSearch(event.target.value)} />
        <div className="thread-items">{threads.error ? <ErrorState error={threads.error} reload={threads.reload} /> : threads.loading ? <p className="gemini-hint">{t("common.loading")}</p> : groups.length ? groups.map(([id, group]) => {
          const heading = <button type="button" className="thread-project-toggle" aria-expanded={!collapsed.has(id)} title={`${t(collapsed.has(id) ? "codex.expandWorkspace" : "codex.collapseWorkspace")} · ${group.threads[0].server_name} · ${group.path}`} onClick={() => toggleGroup(id)}>{collapsed.has(id) ? <ChevronRight size={14} /> : <ChevronDown size={14} />}<Folder size={15} /><strong><span>{group.name}</span><small>{group.threads[0].server_name}</small></strong>{group.threads[0].project_pinned_at && <Pin className="pinned-icon" size={12} />}<span>{group.threads.length}</span></button>;
          return <section className="thread-project" key={id}>
            {archived ? <div className="thread-project-heading">{heading}</div> : <ContextMenu className="thread-project-heading" label={t("codex.workspaceMenu", { name: group.name })} actions={[{ id: "new-session", label: t("codex.createSessionHere"), icon: Plus, disabled: busy, onSelect: () => createSessionInWorkspace(id) }, ...management.projectActions(group.threads[0])]}>{heading}</ContextMenu>}
            {!collapsed.has(id) && <div className="project-threads">{group.threads.map(thread => <ContextMenu key={thread.id} className={`thread ${active?.id === thread.id ? "active" : ""}`} label={t("codex.threadMenu", { name: thread.title })} actions={threadMenuActions(thread)}>
              <button className="thread-select" onClick={() => { onSelectThread(thread.id); setMobileView("conversation"); }}><span><strong>{thread.pinned_at && <Pin size={12} />}{thread.title}</strong><small>{thread.server_name}</small></span></button>
              <div className="thread-actions"><Status value={thread.status} /><button className="icon-button danger thread-delete" disabled={isActive(thread) || thread.recovery_pending || busy} aria-label={t("gemini.delete")} onClick={() => setDeleting(thread)}><Trash2 size={14} /></button></div>
            </ContextMenu>)}</div>}
          </section>;
        }) : <Empty icon={<MessageSquare size={24} />} text={t("gemini.noSessions")} />}</div>
      </div>
      {active && <SidebarHeightDivider value={sidebarHeight} onChange={setSidebarHeight} />}
      {active && <WorkspaceFilesPanel workspaceID={active.workspace_id} taskID={active.operation_id} taskStatus={active.status} realtime={realtime + tick} activePath={preview?.path ?? ""} activeMode={preview?.mode === "diff" ? "diff" : "file"} onOpenFile={openFile} notify={notify} />}
    </aside>
    <div className="gemini-main">{active ? <>
      <SessionPreviewTabs previews={previews} />
      <SessionPreviewPanels previews={previews} workspaceID={active.workspace_id} workspaceRoot={active.path} writable realtime={realtime + tick} notify={notify} onOpenFile={openFile}>
        <GeminiSession key={active.id} thread={active} realtime={dependency} notify={notify} onChanged={refresh} onNewSession={() => openNewSession(active.workspace_id)} onOpenFile={openFile} onFork={eventID => void management.fork(active, eventID)} onSchedule={() => management.schedule(active)} />
      </SessionPreviewPanels>
    </> : selectedThreadID && selected.error ? <ErrorState error={selected.error} reload={selected.reload} /> : <div className="gemini-welcome"><Sparkles size={40} /><h2>{t("gemini.welcome")}</h2><p>{t("gemini.intro")}</p><button className="primary-button" onClick={() => openNewSession()}><Plus size={17} />{t("gemini.newSession")}</button></div>}</div>
    {management.dialogs}
    <Dialog open={createOpen} title={t("gemini.newSession")} onClose={() => setCreateOpen(false)}><CreateGeminiSession initialWorkspaceID={newWorkspaceID} initialModel={newModel} initialThinking={newThinking} initialMode={newMode} workspaces={workspaces.data ?? []} error={workspaces.error} onRetry={workspaces.reload} onCreated={thread => { setArchived(false); onSelectThread(thread.id); setCreateOpen(false); refresh(); }} /></Dialog>
    <Dialog open={Boolean(rename)} title={t("gemini.rename")} onClose={() => { if (!busy) setRename(null); }}><form onSubmit={event => { event.preventDefault(); if (rename) void mutate(rename, { title }); }}><Field label={t("gemini.title")}><input autoFocus required maxLength={180} value={title} onChange={event => setTitle(event.target.value)} /></Field><DialogActions><button className="primary-button" disabled={busy || !title.trim()}>{t("common.save")}</button></DialogActions></form></Dialog>
    <ConfirmDialog open={Boolean(deleting)} danger title={t("gemini.delete")} impact={t("gemini.deleteConfirm", { title: deleting?.title ?? "" })} confirmLabel={t("common.delete")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setDeleting(null)} onConfirm={deleteThread} />
  </div>;
}

type GeminiModels = { models: { id: string; name: string }[]; configured: boolean };

function useGeminiModels(workspaceID: string) {
  const [result, setResult] = useState<{ workspaceID: string; data: GeminiModels | null; error: string }>({ workspaceID: "", data: null, error: "" });
  useEffect(() => {
    if (!workspaceID) return;
    const controller = new AbortController();
    // Match Codex: defer the one refresh until StrictMode's mount replay has settled.
    const timer = window.setTimeout(() => {
      api<GeminiModels>(`/workspaces/${workspaceID}/gemini/models`, { signal: controller.signal })
        .then(data => { if (!controller.signal.aborted) setResult({ workspaceID, data, error: "" }); })
        .catch(error => { if (!controller.signal.aborted) setResult({ workspaceID, data: null, error: error instanceof Error ? error.message : "Request failed" }); });
    }, 0);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [workspaceID]);
  return result.workspaceID === workspaceID ? result : { data: null, error: "" };
}

function ModelOptions({ workspaceID, model, mode, thinking, setThinking, setModel, setMode, disabled = false, autoSelect = false, planSupported = false, actions }: { workspaceID: string; model: string; mode: string; thinking: string; setThinking: (value: string) => void; setModel: (value: string) => void; setMode: (value: string) => void; disabled?: boolean; autoSelect?: boolean; planSupported?: boolean; actions?: ReactNode }) {
  const { t } = useI18n();
  const models = useGeminiModels(workspaceID);
  const upstream = models.data?.models ?? [];
  const [customSelected, setCustomSelected] = useState(false);
  useEffect(() => { if (autoSelect && !customSelected && !model && upstream.length) setModel(upstream[0].id); }, [autoSelect, customSelected, model, setModel, upstream]);
  const custom = customSelected || Boolean(model) && !upstream.some(item => item.id === model);
  return <div className={`gemini-options${actions ? " with-actions" : ""}`}><Field label={t("gemini.model")}><select aria-label={t("gemini.model")} value={custom ? "__custom__" : model} disabled={disabled} onChange={event => { const custom = event.target.value === "__custom__"; setCustomSelected(custom); const nextModel=custom ? "" : event.target.value; setModel(nextModel); setThinking(compatibleGeminiThinking(nextModel,thinking)); }}><option value="" disabled>{t("gemini.selectModel")}</option>{upstream.map(item => <option value={item.id} key={item.id}>{item.name === item.id ? item.id : `${item.name} · ${item.id}`}</option>)}<option value="__custom__">{t("gemini.customModel")}</option></select>{custom && <input aria-label={t("gemini.customModelName")} placeholder={t("gemini.customModelName")} required maxLength={128} value={model} disabled={disabled} onChange={event => { setCustomSelected(true); setModel(event.target.value); setThinking(compatibleGeminiThinking(event.target.value,thinking)); }} />}{models.error && <small className="gemini-model-error" role="alert">{models.error}</small>}{models.data?.configured === false && <small className="muted">{t("gemini.modelsUnconfigured")}</small>}</Field><GeminiThinkingPicker model={model} value={thinking} onChange={setThinking} disabled={disabled} /><Field label={t("gemini.permissions")}><select aria-label={t("gemini.permissions")} value={mode} disabled={disabled} onChange={event => setMode(event.target.value)}><option value="default">{t("gemini.defaultMode")}</option><option value="auto_edit">{t("gemini.autoEditMode")}</option>{(planSupported || mode === "plan") && <option value="plan">{t("gemini.planMode")}</option>}<option value="yolo">{t("gemini.neverApprove")}</option></select></Field>{actions && <div className="gemini-options-actions">{actions}</div>}</div>;
}

function CreateGeminiSession({ initialWorkspaceID, initialModel, initialThinking, initialMode, workspaces, error, onRetry, onCreated }: { initialWorkspaceID: string; initialModel: string; initialThinking: string; initialMode: string; workspaces: Workspace[]; error: string; onRetry: () => void; onCreated: (thread: GeminiThread) => void }) {
  const { t } = useI18n();
  const [workspaceID, setWorkspaceID] = useState(initialWorkspaceID);
  const [model, setModel] = useState(initialModel);
  const [thinking, setThinking]=useState(initialThinking);
  const [mode, setMode] = useState(initialMode);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault(); if (busy || !workspaceID || !model.trim()) return; setBusy(true); setFailure("");
    try { onCreated(await post<GeminiThread>("/gemini/threads", { workspace_id: workspaceID, model: model.trim(), approval_mode: mode, thinking_level: thinking })); }
    catch (error) { setFailure(message(error)); } finally { setBusy(false); }
  };
  return <form onSubmit={submit}>{error && <ErrorState error={error} reload={onRetry} />}<Field label={t("gemini.workspace")}><select required disabled={busy} value={workspaceID} onChange={event => { setWorkspaceID(event.target.value); setModel(""); setThinking(""); }}><option value="">{t("gemini.selectWorkspace")}</option>{workspaces.filter(workspace => workspace.status === "ready").map(workspace => <option key={workspace.id} value={workspace.id}>{workspace.project_name} · {workspace.server_name} · {workspace.path}</option>)}</select></Field><ModelOptions key={workspaceID} autoSelect workspaceID={workspaceID} model={model} mode={mode} thinking={thinking} setThinking={setThinking} setModel={setModel} setMode={setMode} disabled={busy} /><p className="gemini-hint">{t("gemini.permissionsHelp")}</p>{failure && <ErrorBanner text={failure} />}<DialogActions><button className="primary-button" disabled={busy || !workspaceID || !model.trim()}>{busy ? <LoaderCircle className="spin" size={16} /> : <Plus size={16} />}{t("gemini.newSession")}</button></DialogActions></form>;
}

function GeminiSession({ thread, realtime, notify, onChanged, onNewSession, onOpenFile, onFork, onSchedule }: { thread: GeminiThread; realtime: string; notify: (text: string) => void; onChanged: () => void; onNewSession: () => void; onOpenFile: (value: FilePreviewSelection) => void; onFork: (eventID?: string) => void; onSchedule: () => void }) {
  const { t, language } = useI18n();
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const cursor = useRef(0);
  const historyRevision = useRef(0);
  const [hasEarlier, setHasEarlier] = useState(false);
  const [historyBusy, setHistoryBusy] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [error, setError] = useState("");
  const [prompt, setPrompt] = useState("");
  const [editingEventID, setEditingEventID] = useState("");
  const [images, setImages] = useState<{id:string;dataURL:string}[]>([]);
  const [imageBusy, setImageBusy] = useState(false);
  const imageWork = useRef<AbortController | null>(null);
  useEffect(() => () => imageWork.current?.abort(), [thread.id]);
  const addImages = async (files: File[]) => {
    if (running || recoveryPending || busy || thread.archived_at) return;
    if (!thread.input_supported) { notify(t("gemini.inputAgentUpdate")); return; }
    const available = 4 - images.length;
    if (available <= 0) { notify(t("codex.imageLimit")); return; }
    imageWork.current?.abort();
    const controller = new AbortController(); imageWork.current = controller; setImageBusy(true);
    try {
      const selected = files.slice(0, available);
      const prepared = new Array<{id:string;dataURL:string}>(selected.length);
      let next = 0;
      const worker = async () => { while (next < selected.length) { const index = next++; prepared[index] = { id: crypto.randomUUID(), dataURL: await compressImage(selected[index], { signal: controller.signal }) }; } };
      await Promise.all(Array.from({length: Math.min(2, selected.length)}, worker));
      if (!controller.signal.aborted) setImages(current => [...current, ...prepared].slice(0, 4));
      if (files.length > available) notify(t("codex.imageLimit"));
    } catch { if (!controller.signal.aborted) notify(t("codex.imageFailed")); controller.abort(); }
    finally { if (imageWork.current === controller) { imageWork.current = null; setImageBusy(false); } }
  };

  const promptRef = useRef<HTMLTextAreaElement>(null);
  const slashKeyboardRef = useRef<((event: React.KeyboardEvent<HTMLTextAreaElement>) => boolean) | null>(null);
  const [slashDismissedValue, setSlashDismissedValue] = useState("");
  const [skillsOpen, setSkillsOpen] = useState(false);
  const [browserOpen, setBrowserOpen] = useState(false);
  const [model, setModel] = useState(thread.model);
  const [thinking, setThinking]=useState(thread.thinking_level ?? "");
  useEffect(() => setThinking(thread.thinking_level ?? ""), [thread.thinking_level]);
  const [mode, setMode] = useState(thread.approval_mode);
  useEffect(() => setMode(thread.approval_mode), [thread.approval_mode]);
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);
  const [goalRounds, setGoalRounds] = useState(20);
  const [goalMinutes, setGoalMinutes] = useState(60);
  const goalData = useData<GeminiGoal | null>(`/gemini/threads/${thread.id}/goal`, `${realtime}:${reload}:${thread.status}:${thread.operation_id}`);
  const goal = goalData.data && !Array.isArray(goalData.data) && goalData.data.id ? goalData.data : null;
  useEffect(() => { if (goal) { setGoalRounds(goal.max_continuations); setGoalMinutes(goal.max_seconds / 60); } }, [goal?.id]);
  const [confirmingOperation, setConfirmingOperation] = useState("");
  const stream = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  const running = isActive(thread);
  const recoveryPending = Boolean(thread.recovery_pending);
  const blocked = running || recoveryPending;
  const goalStopPending = Boolean(goal && goal.operation_id === thread.operation_id && (thread.status === "interrupting" || recoveryPending) && (goal.status === "paused" || goal.status === "cancelled"));
  const goalStatus = recoveryPending && goal?.operation_id === thread.operation_id ? "unknown" : goalStopPending ? "stopping" : goal?.status;
  const slashOpen = !editingEventID && !running && /^\/[A-Za-z0-9_-]*$/.test(prompt) && prompt !== slashDismissedValue;
  const finishSlash = (action: () => void, refocus = true) => { setPrompt(""); setSlashDismissedValue(""); action(); if (refocus) requestAnimationFrame(() => promptRef.current?.focus()); };
  const draftSlash = (text: string) => finishSlash(() => setPrompt(text));
  const nativeCommands = useMemo(() => {
    const event = [...events].reverse().find(item => item.kind === "gemini.commands");
    const commands = (event?.payload as {commands?: unknown} | undefined)?.commands;
    if (!Array.isArray(commands)) return [];
    return commands.filter((item): item is {name:string;description:string} => typeof item?.name === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(item.name) && typeof item.description === "string");
  }, [events]);
  const slashItems: SlashCommandItem[] = [
    { id: "goal", name: "/goal", description: t("gemini.slashGoal"), icon: Target, onSelect: () => thread.goal_supported ? draftSlash("/goal ") : finishSlash(() => notify(t("gemini.goalUpdate"))) },
    { id: "schedule", name: "/schedule", description: t("gemini.slashSchedule"), icon: CalendarClock, onSelect: () => finishSlash(onSchedule, false) },
    { id: "plan", name: "/plan", description: t("gemini.slashPlan"), icon: ListChecks, onSelect: () => finishSlash(() => thread.plan_supported ? setMode("plan") : notify(t("gemini.planUpdate"))) },
    { id: "grill-me", name: "/grill-me", description: t("gemini.slashInterview"), icon: MessageSquare, onSelect: () => thread.input_supported ? draftSlash(t("gemini.interviewDraft")) : finishSlash(() => notify(t("gemini.inputAgentUpdate"))) },
    { id: "learn", name: "/learn", description: t("gemini.slashLearn"), icon: Lightbulb, onSelect: () => draftSlash(t("gemini.learnDraft")) },
    { id: "skills", name: "/skills", description: t("gemini.slashSkills"), icon: Boxes, onSelect: () => finishSlash(() => setSkillsOpen(true), false) },
    { id: "browser", name: "/browser", description: t("gemini.slashBrowser"), icon: Globe, onSelect: () => finishSlash(() => setBrowserOpen(true), false) },
    { id: "teamwork", name: "/teamwork", description: t("gemini.slashTeamwork"), icon: Users, onSelect: () => draftSlash(t("gemini.teamworkDraft")) },
    ...nativeCommands.filter(item => !["goal", "schedule", "plan", "grill-me", "learn", "skills", "browser", "teamwork"].includes(item.name)).map(item => ({ id: `native-${item.name}`, name: `/${item.name}`, description: item.description, section: t("gemini.nativeCommands"), icon: Sparkles, onSelect: () => mode === "plan" && !["help", "about"].includes(item.name) ? finishSlash(() => notify(t("gemini.planCommandUnavailable"))) : draftSlash(`/${item.name} `) })),
  ];
  const messages = useMemo(() => groupGeminiMessages(geminiMessages(events)), [events]);
  const forkTargets = useMemo(() => geminiForkTargets(events), [events]);
  const merge = (incoming: StreamEvent[]) => setEvents(current => [...new Map([...current, ...incoming].map(event => [event.event_id, event])).values()].sort((a, b) => a.sequence - b.sequence));
  useEffect(() => {
    const controller = new AbortController();
    (async () => {
      let initial = cursor.current === 0;
      while (!controller.signal.aborted) {
        const next = await api<StreamEvent[]>(`/gemini/threads/${thread.id}/events${initial ? "" : `?after=${cursor.current}`}`, { signal: controller.signal });
        if (controller.signal.aborted) return;
        const rewritten = !initial && next.some(event => event.kind === "gemini.user.message" && (event.payload as { history_rewritten?: boolean })?.history_rewritten);
        if (rewritten) {
          const recent = await api<StreamEvent[]>(`/gemini/threads/${thread.id}/events`, { signal: controller.signal });
          if (controller.signal.aborted) return;
          historyRevision.current += 1;
          setEvents(recent); setHasEarlier(recent.length === 500);
          cursor.current = recent.at(-1)?.sequence ?? 0;
          setError(""); break;
        }
        merge(next);
        if (initial) setHasEarlier(next.length === 500);
        cursor.current = Math.max(cursor.current, next.at(-1)?.sequence ?? 0);
        setError("");
        if (initial || next.length < 500) break;
        initial = false;
      }
    })().catch(error => { if (!controller.signal.aborted) setError(message(error)); });
    return () => controller.abort();
  }, [realtime, reload, thread.id]);
  useEffect(() => { if (follow.current && stream.current) stream.current.scrollTop = stream.current.scrollHeight; }, [messages, running]);
  const earlier = async () => {
    setHistoryBusy(true);
    const element = stream.current;
    const height = element?.scrollHeight ?? 0;
    const revision = historyRevision.current;
    follow.current = false;
    try { const next = await api<StreamEvent[]>(`/gemini/threads/${thread.id}/events?before=${events[0]?.sequence ?? 0}`); if (revision !== historyRevision.current) return; merge(next); setHasEarlier(next.length === 500); requestAnimationFrame(() => { if (element) element.scrollTop += element.scrollHeight - height; }); }
    catch (error) { notify(message(error)); } finally { setHistoryBusy(false); }
  };
  const steerReady = thread.status === "running" && thread.steer_supported && (thread.steer_ready || events.some(event => event.kind === "gemini.steer.ready" && (event.payload as {operation_id?:string}).operation_id === thread.operation_id));
  const showInterrupt = recoveryPending || (running && (!steerReady || !prompt.trim() || images.length > 0 || Boolean(editingEventID)));
  const actionLabel = recoveryPending ? "gemini.verifyStop" : showInterrupt ? "gemini.interrupt" : running ? "codex.steer" : "gemini.send";
  const steeringRequest = useRef<{operation:string; prompt:string; id:string}>();
  const goalDraft = !running && !editingEventID && /^\/goal(?:\s|$)/i.test(prompt);
  const goalObjective = goalDraft ? prompt.replace(/^\/goal\s*/i, "").trim() : "";
  const goalLimitsValid = Number.isInteger(goalRounds) && goalRounds >= 1 && goalRounds <= 100 && Number.isInteger(goalMinutes) && goalMinutes >= 1 && goalMinutes <= 1440;
  const submit = async (event: FormEvent) => {
    event.preventDefault(); if (slashOpen || busy || recoveryPending || (running && !steerReady) || imageBusy || (!prompt.trim() && !images.length) || thread.server_status !== "online") return;
    setBusy(true); setError("");
    try {
      if (goalDraft && goalData.error) { setError(t("gemini.goalLoadError")); return; }
      if (goalDraft && (!thread.goal_supported || !goalObjective || !goalLimitsValid)) {
        setError(t(!thread.goal_supported ? "gemini.goalUpdate" : !goalObjective ? "gemini.goalObjectiveRequired" : "gemini.goalLimitsInvalid"));
        return;
      }
      if (running) {
        if (images.length || editingEventID) return;
        if (!steeringRequest.current || steeringRequest.current.operation !== thread.operation_id || steeringRequest.current.prompt !== prompt) steeringRequest.current = {operation:thread.operation_id,prompt,id:crypto.randomUUID()};
        await post(`/gemini/threads/${thread.id}/steer`, {prompt,operation_id:thread.operation_id,request_id:steeringRequest.current.id});
        steeringRequest.current = undefined;
      } else await post(editingEventID ? `/gemini/threads/${thread.id}/events/${editingEventID}/rewrite` : `/gemini/threads/${thread.id}/turns`, { prompt: goalDraft ? goalObjective : prompt, model: model.trim(), approval_mode: mode, thinking_level: thinking, ...(goalDraft ? { goal: {max_continuations:goalRounds,max_seconds:goalMinutes*60} } : {}), ...(images.length ? { images: images.map(image => ({data_url:image.dataURL})) } : {}) });
      setPrompt(""); setImages([]); setEditingEventID(""); follow.current = true; onChanged(); setReload(value => value + 1);
    }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  };
  const controlGoal = async (action: "pause" | "resume" | "cancel") => {
    if (!goal || busy) return;
    if (action === "resume" && !goalLimitsValid) { setError(t("gemini.goalLimitsInvalid")); return; }
    setBusy(true); setError("");
    try {
      await post(`/gemini/threads/${thread.id}/goals/${goal.id}/${action}`, action === "resume" ? {max_continuations:goalRounds,max_seconds:goalMinutes*60} : {});
      onChanged(); setReload(value => value + 1);
    } catch (error) { setError(message(error)); } finally { setBusy(false); }
  };
  const exportSession = async () => {
    if (exporting || running) return;
    setExporting(true);
    try {
      const all: StreamEvent[] = [];
      let after = 0;
      while (true) {
        const page = await api<StreamEvent[]>(`/gemini/threads/${thread.id}/events?after=${after}`);
        all.push(...page);
        if (page.length < 500) break;
        const next = page.at(-1)?.sequence ?? 0;
        if (next <= after) throw new Error(t("gemini.exportFailed"));
        after = next;
      }
      const transcript = [`# ${thread.title}`, `${thread.server_name} · ${thread.path}`, ...geminiMessages(all).map(item => item.stats ? t("gemini.usage", { input: formatUsageNumber(item.stats.input_tokens, language), output: formatUsageNumber(item.stats.output_tokens, language), tools: formatUsageNumber(item.stats.tool_calls, language) }) : `## ${item.title || item.role}\n\n${item.text}`)].join("\n\n");
      const url = URL.createObjectURL(new Blob([transcript], { type: "text/markdown;charset=utf-8" }));
      try { const link = document.createElement("a"); link.href = url; link.download = `gemini-${thread.id}.md`; document.body.append(link); link.click(); link.remove(); }
      finally { URL.revokeObjectURL(url); }
    } catch (error) { notify(message(error)); } finally { setExporting(false); }
  };
  const interrupt = async () => {
    setBusy(true);
    try { await post(`/gemini/threads/${thread.id}/interrupt`, {}); onChanged(); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  };
  const confirmStopped = async () => {
    if (!confirmingOperation || busy) return;
    setBusy(true); setError("");
    try {
      await post(`/gemini/threads/${thread.id}/confirm-stopped`, { operation_id: confirmingOperation, confirmed: true });
      setConfirmingOperation(""); onChanged(); setReload(value => value + 1);
    } catch (error) { setError(message(error)); setConfirmingOperation(""); }
    finally { setBusy(false); }
  };
  return <section className="session-panel gemini-session"><header className="session-header"><div className="session-heading"><h2>{thread.title}</h2><span className="session-meta" title={`${thread.server_name} · ${thread.path}`}>{thread.server_name}<i />{thread.path}</span></div><div className="session-actions"><Status value={thread.status} /><ContextMenu label={t("codex.messageMenu")} triggerIcon={MoreHorizontal} actions={[{ id: "fork", label: t("codex.continueInNewTask"), icon: GitFork, disabled: blocked || busy || !thread.fork_supported || !thread.session_id || Boolean(thread.archived_at) || thread.server_status !== "online", onSelect: () => onFork() }]}>{null}</ContextMenu><button className="icon-button" aria-label={t("gemini.newSessionHere")} title={t("gemini.newSessionHere")} onClick={onNewSession}><Plus size={16} /></button><button className="icon-button" aria-label={t("gemini.export")} title={t("gemini.export")} disabled={running || exporting} onClick={() => void exportSession()}>{exporting ? <LoaderCircle className="spin" size={16} /> : <Download size={16} />}</button><button className="icon-button" aria-label={t("common.refresh")} onClick={() => { onChanged(); setReload(value => value + 1); }}><RefreshCw size={16} /></button></div><SessionUsage provider="gemini" threadID={thread.id} revision={`${realtime}:${reload}`} running={running} /></header>
    <div className="gemini-stream conversation-stream" ref={stream} role="log" aria-label={t("gemini.conversation")} onScroll={() => { const el = stream.current; if (el) follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100; }}>
      {hasEarlier && <button className="secondary-button" disabled={historyBusy} onClick={() => void earlier()}>{t("gemini.loadEarlier")}</button>}
      {!messages.length && <Empty icon={<Sparkles size={30} />} text={t("gemini.startPrompt")} />}
      {messages.map(item => item.role === "commandGroup" ? <GeminiToolGroup key={item.id} items={item.items} uncertainOperationID={recoveryPending ? thread.operation_id : undefined} /> : item.role === "question" ? <GeminiQuestion key={item.id} item={item} threadID={thread.id} readOnly={!running || thread.status === "interrupting" || item.operationID !== thread.operation_id || Boolean(thread.archived_at) || thread.server_status !== "online"} onAnswered={() => { onChanged(); setReload(value => value + 1); }} /> : item.role === "tool" ? <GeminiTool key={item.id} item={item} uncertain={recoveryPending && item.operationID === thread.operation_id} /> : item.role === "status" ? <p className="gemini-usage" key={item.id}>{item.text === "interrupted" ? t("status.interrupted") : item.stats ? t("gemini.usage", { input: formatUsageNumber(item.stats.input_tokens, language), output: formatUsageNumber(item.stats.output_tokens, language), tools: formatUsageNumber(item.stats.tool_calls, language) }) : item.text}</p> : <article className={`message ${item.role === "error" ? "error" : item.role === "steer" ? "user" : item.role}`} key={item.id}><header>{(item.role === "user" || item.role === "steer") ? <UserRound size={15} /> : <Bot size={15} />}<strong>{(item.role === "user" || item.role === "steer") ? t("codex.you") : "Gemini"}</strong><time>{formatTime(item.time)}</time>{item.role === "steer" && <small>{t(`gemini.steer.${item.steerStatus ?? "queued"}`)}</small>}</header>{!!item.images?.length && <MessageImages sources={item.images.map(image => image.data_url)} />}<MarkdownContent text={item.text} workspaceRoot={thread.path} onOpenFile={onOpenFile} />{item.role === "user" && <div className="message-actions"><button type="button" className="message-action" title={t("codex.copyMessage")} aria-label={t("codex.copyMessage")} onClick={() => { void navigator.clipboard.writeText(item.text).then(() => notify(t("codex.messageCopied"))).catch(error => notify(message(error))); }}><Copy size={14} /></button>{!thread.archived_at && <button type="button" className="message-action" disabled={busy || blocked || thread.server_status !== "online" || !thread.rewrite_supported} title={t(thread.rewrite_supported ? "codex.editMessage" : "gemini.rewriteAgentUpdate")} aria-label={t("codex.editMessage")} onClick={() => { imageWork.current?.abort(); setImageBusy(false); setPrompt(item.text); setImages((item.images ?? []).map(image => ({id:crypto.randomUUID(),dataURL:image.data_url}))); setEditingEventID(item.id); requestAnimationFrame(() => { promptRef.current?.focus(); promptRef.current?.scrollIntoView({ block: "nearest" }); }); }}><Pencil size={14} /></button>}</div>}{item.role === "assistant" && <div className="message-actions"><ContextMenu className="message-menu" label={t("codex.messageMenu")} triggerIcon={MoreHorizontal} actions={[{ id: "fork", label: t("codex.branchToNewChat"), icon: GitFork, disabled: blocked || busy || !forkTargets.has(item.id) || !thread.fork_supported || Boolean(thread.archived_at) || thread.server_status !== "online", onSelect: () => onFork(item.id) }]}>{null}</ContextMenu></div>}</article>)}
      {running && <p className="gemini-working" role="status"><LoaderCircle className="spin" size={16} />{t(thread.status === "interrupting" ? "gemini.interrupting" : "gemini.working")}</p>}
    </div>
    <form className={`composer gemini-composer ${goalDraft ? "goal-mode" : ""}`} onSubmit={submit}>
      {goalData.error && <div className="gemini-goal-error" role="alert"><span>{t("gemini.goalLoadError")} {goalData.error}</span><button type="button" className="text-button" onClick={goalData.reload}>{t("common.retry")}</button></div>}
      {goal && <div className="gemini-goal">
        <div className="goal-progress-row"><Target size={16} /><span title={goal.objective}><strong>{goal.objective}</strong><small>{t("gemini.goalProgress", {count:goal.continuations})} · {t(`gemini.goalStatus.${goalStatus}`)}</small></span>
          <div>
            {goal.status === "active" && <button type="button" className="icon-button" aria-label={t("gemini.goalPause")} title={t("gemini.goalPause")} disabled={busy || Boolean(goalData.error) || Boolean(thread.archived_at) || thread.status === "interrupting" || recoveryPending} onClick={() => void controlGoal("pause")}><Pause size={14} /></button>}
            {(goal.status === "paused" || goal.status === "blocked") && <button type="button" className="icon-button" aria-label={t("gemini.goalResume")} title={t("gemini.goalResume")} disabled={busy || Boolean(goalData.error) || Boolean(thread.archived_at) || blocked || thread.server_status !== "online" || !thread.goal_supported} onClick={() => void controlGoal("resume")}><Play size={14} /></button>}
            {["active", "paused", "blocked"].includes(goal.status) && <button type="button" className="icon-button danger" aria-label={t("gemini.goalCancel")} title={t("gemini.goalCancel")} disabled={busy || Boolean(goalData.error) || Boolean(thread.archived_at) || thread.status === "interrupting" || recoveryPending} onClick={() => void controlGoal("cancel")}><X size={14} /></button>}
          </div>
        </div>
        {(goal.message || goal.evidence) && <details className="gemini-goal-evidence"><summary>{t("gemini.goalEvidence")}</summary><p>{goal.message || goal.evidence}</p></details>}
      </div>}
      {(goalDraft || (goal && (goal.status === "paused" || goal.status === "blocked"))) && <div className="gemini-goal-limits"><label>{t("gemini.goalRounds")}<input aria-label={t("gemini.goalRounds")} type="number" min="1" max="100" value={goalRounds} onChange={event => setGoalRounds(Number(event.target.value))} disabled={busy} /></label><label>{t("gemini.goalMinutes")}<input aria-label={t("gemini.goalMinutes")} type="number" min="1" max="1440" value={goalMinutes} onChange={event => setGoalMinutes(Number(event.target.value))} disabled={busy} /></label>{goalDraft && <button type="button" className="icon-button" aria-label={t("gemini.goalExit")} title={t("gemini.goalExit")} onClick={() => setPrompt(goalObjective)}><X size={14} /></button>}</div>}
      {goalDraft && goal && (goal.status === "paused" || goal.status === "blocked") && <p className="gemini-hint">{t("gemini.goalReplacement")}</p>}
      {editingEventID && <div className="composer-editing"><Pencil size={14} /><span>{t("codex.editingMessage")}</span><button type="button" className="icon-button" title={t("codex.cancelEdit")} aria-label={t("codex.cancelEdit")} disabled={busy} onClick={() => { setEditingEventID(""); setPrompt(""); setImages([]); }}><X size={14} /></button></div>}
      {images.length > 0 && <div className="composer-images">{images.map(image => <figure key={image.id}><img src={image.dataURL} alt="" /><button type="button" aria-label={t("common.close")} disabled={busy} onClick={() => setImages(current => current.filter(item => item.id !== image.id))}><X size={13} /></button></figure>)}</div>}
      {imageBusy && <p className="gemini-hint" role="status"><LoaderCircle className="spin" size={14} />{t("gemini.preparingImages")}</p>}
      {error && <ErrorBanner text={error} />}
      {recoveryPending && <><p className="gemini-hint" role="status">{t("gemini.recoveryPending")}</p><button type="button" className="secondary-button" disabled={busy} onClick={() => setConfirmingOperation(thread.operation_id)}>{t("gemini.confirmStopped")}</button></>}
      {running && <p className="gemini-hint">{t(steerReady ? "gemini.steerHint" : "gemini.steerUnavailable")}</p>}
      {thread.server_status !== "online" && <p className="gemini-hint">{t("gemini.offline")}</p>}
      {slashOpen && <SlashCommandMenu items={slashItems} query={prompt.slice(1)} label={t("codex.slashMenu")} onDismiss={() => setSlashDismissedValue(prompt)} keyboardRef={slashKeyboardRef} />}
      {thread.archived_at ? <p className="gemini-hint">{t("gemini.archivedHint")}</p> : <><textarea ref={promptRef} onPaste={event => { const files = Array.from(event.clipboardData.items).filter(item => item.type.startsWith("image/")).map(item => item.getAsFile()).filter((file): file is File => file !== null); if (files.length) { event.preventDefault(); void addImages(files); } }} aria-label={t("gemini.prompt")} placeholder={t(running ? "codex.steerPlaceholder" : "gemini.prompt")} value={prompt} disabled={busy || recoveryPending || (running && !steerReady)} onChange={event => { setPrompt(event.target.value); if (event.target.value !== slashDismissedValue) setSlashDismissedValue(""); }} onKeyDown={event => { if (slashOpen && slashKeyboardRef.current?.(event)) return; if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} /><ModelOptions workspaceID={thread.workspace_id} planSupported={thread.plan_supported} model={model} mode={mode} thinking={thinking} setThinking={setThinking} setModel={setModel} setMode={setMode} disabled={blocked || busy} actions={<button type={showInterrupt ? "button" : "submit"} className={showInterrupt ? "secondary-button danger composer-action" : "primary-button composer-action"} aria-label={t(actionLabel)} disabled={busy || (showInterrupt ? thread.status === "interrupting" || (recoveryPending && thread.server_status !== "online") : slashOpen || imageBusy || (!prompt.trim() && !images.length) || !model.trim() || thread.server_status !== "online" || (running && (!steerReady || !!images.length || !!editingEventID)))} onClick={showInterrupt ? () => void interrupt() : undefined}>{busy ? <LoaderCircle className="spin" size={15} /> : showInterrupt ? <Square size={14} /> : <ArrowUp size={15} />}<span className="composer-action-full">{t(actionLabel)}</span><span className="composer-action-compact">{t(recoveryPending ? "gemini.verifyStop" : showInterrupt ? "codex.interruptShort" : running ? "codex.steerShort" : "gemini.send")}</span></button>} /></>}
    </form>
    <GeminiSkillsDialog open={skillsOpen} thread={thread} onClose={() => setSkillsOpen(false)} onUse={name => { setSkillsOpen(false); setPrompt(t("gemini.skillDraft", { name })); requestAnimationFrame(() => promptRef.current?.focus()); }} />
    <Dialog open={browserOpen} title={t("gemini.browserTitle")} onClose={() => setBrowserOpen(false)}>
      <p>{t("gemini.browserPrerequisites")}</p>
      <pre>{'{\n  "agents": { "overrides": { "browser_agent": { "enabled": true } } }\n}'}</pre>
      <p><a href="https://geminicli.com/docs/core/subagents/#browser-agent" target="_blank" rel="noreferrer">{t("gemini.browserDocumentation")}</a></p>
      <DialogActions><button type="button" className="primary-button" onClick={() => { setBrowserOpen(false); setPrompt(t("gemini.browserDraft")); requestAnimationFrame(() => promptRef.current?.focus()); }}>{t("gemini.browserPrepare")}</button></DialogActions>
    </Dialog>
    <ConfirmDialog open={Boolean(confirmingOperation)} danger title={t("gemini.confirmStoppedTitle")} impact={t("gemini.confirmStoppedDetail", {server: thread.server_name})} confirmLabel={t("gemini.confirmStoppedAction")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setConfirmingOperation("")} onConfirm={() => void confirmStopped()} />
  </section>;
}
