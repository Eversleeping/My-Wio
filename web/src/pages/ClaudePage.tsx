import { type CSSProperties, type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Archive, ArchiveRestore, ChevronDown, ChevronRight, Copy, EyeOff, Folder, Pin, PinOff, ArrowUp, Bot, LoaderCircle, MessageSquare, Pencil, Plus, Square, Trash2, UserRound } from "lucide-react";
import { api, patch, post, remove } from "../api";
import { Dialog, ErrorBanner, Field, WorkspaceFilesPanel, message, type FilePreviewSelection } from "../App";
import { ContextMenu, type ContextMenuAction } from "../ContextMenu";
import { HiddenProjectsButton } from "../components/HiddenProjectsButton";
import { SidebarHeightDivider, readSidebarHeight } from "../components/SidebarHeightDivider";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DialogActions } from "../components/Dialog";
import { Empty, ErrorState, Status } from "../components/PageUI";
import { SessionPreviewPanels, SessionPreviewTabs, useSessionPreviews } from "../components/SessionPreviews";
import { useI18n } from "../i18n";
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
  const [hiding, setHiding] = useState<ClaudeThread | null>(null);
  useEffect(() => { try { localStorage.setItem("wio_claude_sidebar_height", String(sidebarHeight)); } catch { /* Keep resizing available without storage. */ } }, [sidebarHeight]);
  const [tick, setTick] = useState(0);
  const [archived, setArchived] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [workspaceID, setWorkspaceID] = useState("");
  const [model, setModel] = useState("sonnet");
  const [effort, setEffort] = useState("");
  const [mode, setMode] = useState("default");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [renaming, setRenaming] = useState<ClaudeThread | null>(null);
  const [title, setTitle] = useState("");
  const [deleting, setDeleting] = useState<ClaudeThread | null>(null);
  const [mobile, setMobile] = useState("sessions");
  const dependency = `${realtime}:${tick}`;
  const threads = useData<ClaudeThread[]>(`/claude/threads?archived=${archived}`, dependency);
  const selected = useData<ClaudeThread>(selectedThreadID ? `/claude/threads/${selectedThreadID}` : null, dependency);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
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
    event.preventDefault(); setBusy(true); setError("");
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
  const newSession = (id = thread?.workspace_id ?? "") => { const previous = threads.data?.find(item => item.workspace_id === id); setWorkspaceID(id); setModel(modelForWorkspace(id)); setEffort(previous?.effort ?? ""); setMode(previous?.permission_mode ?? "default"); setError(""); setCreateOpen(true); };

  const groups = useMemo(() => {
    const result = new Map<string, { name: string; path: string; threads: ClaudeThread[] }>();
    const byID = new Map((workspaces.data ?? []).map(item => [item.id, item]));
    for (const item of threads.data ?? []) {
      if (item.project_hidden_at) continue;
      const workspace = byID.get(item.workspace_id);
      const path = workspace?.path || item.path;
      const identity = [workspace?.display_name?.trim(), workspace?.branch?.trim(), path.split(/[\\/]/).filter(Boolean).at(-1)].find(value => value && value !== item.project_name);
      const group = result.get(item.workspace_id) ?? { name: identity ? `${item.project_name} · ${identity}` : item.project_name, path, threads: [] };
      group.threads.push(item); result.set(item.workspace_id, group);
    }
    const query = search.trim().toLocaleLowerCase();
    return [...result.entries()].sort(([,a],[,b]) => Number(!a.threads[0].project_pinned_at) - Number(!b.threads[0].project_pinned_at) || (b.threads[0].project_pinned_at ?? "").localeCompare(a.threads[0].project_pinned_at ?? "")).flatMap(([id, group]) => {
      if (!query || `${group.name} ${group.path}`.toLocaleLowerCase().includes(query)) return [[id, group] as const];
      const matching = group.threads.filter(item => `${item.title} ${item.server_name} ${item.id} ${item.session_id}`.toLocaleLowerCase().includes(query));
      return matching.length ? [[id, { ...group, threads: matching }] as const] : [];
    });
  }, [search, threads.data, workspaces.data]);
  const updateProject = async (target: ClaudeThread, values: object) => {
    setBusy(true);
    try { await patch(`/projects/${target.project_id}`, { server_id: target.server_id, ...values }); setHiding(null); refresh(); }
    catch (err) { notify(message(err)); } finally { setBusy(false); }
  };
  const threadActions = (item: ClaudeThread): ContextMenuAction[] => [
    { id: "rename", label: t("gemini.rename"), icon: Pencil, disabled: busy || activeTurn(item) || item.recovery_pending, onSelect: () => { setRenaming(item); setTitle(item.title); } },
    { id: "archive", label: t(item.archived_at ? "gemini.restore" : "gemini.archive"), icon: item.archived_at ? ArchiveRestore : Archive, disabled: busy || activeTurn(item) || item.recovery_pending, onSelect: () => change(item, { archived: !item.archived_at }) },
    { id: "copy-path", label: t("codex.copyWorkingDirectory"), icon: Copy, separatorBefore: true, onSelect: () => { void navigator.clipboard.writeText(item.path).then(() => notify(t("codex.workingDirectoryCopied"))).catch(err => notify(message(err))); } },
  ];

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
          const first = group.threads[0];
          const heading = <button type="button" className="thread-project-toggle" aria-expanded={!collapsed.has(id)} title={`${t(collapsed.has(id) ? "codex.expandWorkspace" : "codex.collapseWorkspace")} · ${first.server_name} · ${group.path}`} onClick={() => setCollapsed(current => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; })}>{collapsed.has(id) ? <ChevronRight size={14} /> : <ChevronDown size={14} />}<Folder size={15} /><strong><span>{group.name}</span><small>{first.server_name}</small></strong>{first.project_pinned_at && <Pin className="pinned-icon" size={12} />}<span>{group.threads.length}</span></button>;
          return <section className="thread-project" key={id}>
            {archived ? <div className="thread-project-heading">{heading}</div> : <ContextMenu className="thread-project-heading" label={t("codex.workspaceMenu", { name: group.name })} actions={[
              { id: "new-session", label: t("codex.createSessionHere"), icon: Plus, disabled: busy, onSelect: () => newSession(id) },
              { id: "pin", label: t(first.project_pinned_at ? "codex.unpinProject" : "codex.pinProject"), icon: first.project_pinned_at ? PinOff : Pin, disabled: busy, onSelect: () => updateProject(first, { pinned: !first.project_pinned_at }) },
              { id: "hide", label: t("codex.hideProject"), icon: EyeOff, disabled: busy, danger: true, onSelect: () => setHiding(first) },
            ]}>{heading}</ContextMenu>}
            {!collapsed.has(id) && <div className="project-threads">{group.threads.map(item => <ContextMenu key={item.id} className={`thread ${thread?.id === item.id ? "active" : ""}`} label={t("codex.threadMenu", { name: item.title })} actions={threadActions(item)}>
              <button className="thread-select" onClick={() => { onSelectThread(item.id); setMobile("conversation"); }}><span><strong>{item.title}</strong><small>{item.server_name}</small></span></button>
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
      <SessionPreviewPanels previews={previews} workspaceID={thread.workspace_id} workspaceRoot={thread.path} writable realtime={realtime + tick} notify={notify} onOpenFile={openFile}>
        <ClaudeSession key={thread.id} thread={thread} revision={dependency} onChanged={refresh} onOpenFile={openFile} onArchive={() => void change(thread, { archived: !thread.archived_at })} />
      </SessionPreviewPanels>
    </> : selectedThreadID && selected.error ? <ErrorState error={selected.error} reload={selected.reload} /> : <div className="gemini-welcome"><Bot size={40} /><h2>Claude</h2><p>{t("claude.intro")}</p><button className="primary-button" onClick={() => newSession()}><Plus size={17} />{t("claude.newSession")}</button></div>}</div>
    <Dialog open={createOpen} title={t("claude.newSession")} onClose={() => { if (!busy) setCreateOpen(false); }}><form onSubmit={create}>
      {workspaces.error && <ErrorState error={workspaces.error} reload={workspaces.reload} />}
      <Field label={t("gemini.workspace")}><select required value={workspaceID} onChange={event => { setWorkspaceID(event.target.value); setModel(modelForWorkspace(event.target.value)); setEffort(""); }}><option value="">{t("gemini.selectWorkspace")}</option>{(workspaces.data ?? []).filter(item => item.status === "ready").map(item => <option key={item.id} value={item.id}>{item.project_name} · {item.server_name} · {item.path}</option>)}</select></Field>
      <ClaudeOptions model={model} mode={mode} effort={effort} setEffort={setEffort} setModel={setModel} setMode={setMode} disabled={busy} effortSupported={Boolean(servers.data?.find(item => item.id === workspaces.data?.find(workspace => workspace.id === workspaceID)?.server_id)?.claude_effort_supported)} />
      <p className="gemini-hint">{t("claude.permissionsHelp")}</p>{error && <ErrorBanner text={error} />}
      <DialogActions><button className="primary-button" disabled={busy || !workspaceID || !model.trim()}>{t("claude.newSession")}</button></DialogActions>
    </form></Dialog>
    <Dialog open={Boolean(renaming)} title={t("gemini.rename")} onClose={() => { if (!busy) setRenaming(null); }}><form onSubmit={event => { event.preventDefault(); if (renaming) void change(renaming, { title: title.trim() }); }}><Field label={t("gemini.title")}><input required maxLength={180} value={title} onChange={event => setTitle(event.target.value)} /></Field><DialogActions><button className="primary-button" disabled={busy || !title.trim()}>{t("common.save")}</button></DialogActions></form></Dialog>
    <ConfirmDialog open={Boolean(hiding)} danger title={t("codex.hideProject")} impact={t("codex.confirmHideProject", { name: hiding?.project_name ?? "" })} confirmLabel={t("codex.hideProject")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setHiding(null)} onConfirm={() => hiding ? updateProject(hiding, { hidden: true }) : undefined} />
    <ConfirmDialog open={Boolean(deleting)} danger title={t("claude.delete")} impact={t("claude.deleteImpact")} confirmLabel={t("common.delete")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setDeleting(null)} onConfirm={deleteSession} />
  </div>;
}

function ClaudeOptions({ model, mode, effort, setModel, setMode, setEffort, disabled, compact = false, effortSupported = true }: { model: string; mode: string; effort: string; setModel: (value: string) => void; setMode: (value: string) => void; setEffort: (value: string) => void; disabled: boolean; compact?: boolean; effortSupported?: boolean }) {
  const { t } = useI18n();
  const controls = [
    <input key="model" aria-label={t("claude.model")} title={model} required maxLength={128} value={model} disabled={disabled} onChange={event => setModel(event.target.value)} />,
    <select key="effort" aria-label={t("claude.effort")} title={t(effortSupported ? "claude.effort" : "claude.effortUpgrade")} value={effort} disabled={disabled} onChange={event => setEffort(event.target.value)}>{["", "low", "medium", "high", "xhigh", "max"].map(value => <option key={value} value={value} disabled={!!value && !effortSupported}>{t(`claude.effort.${value || "default"}`)}</option>)}</select>,
    <select key="mode" aria-label={t("claude.permission")} title={t("claude.permissionsHelp")} value={mode} disabled={disabled} onChange={event => setMode(event.target.value)}>{modes.map(value => <option key={value} value={value}>{t(`claude.permission.${value}`)}</option>)}</select>,
  ];
  return compact ? <div className="claude-composer-options">{controls}</div> : <div className="claude-options">{controls.map((control, index) => <Field key={control.key} label={t(["claude.model", "claude.effort", "claude.permission"][index])}>{control}</Field>)}</div>;
}

type DisplayItem = { id: string; role: "user" | "assistant" | "tool" | "notice"; text: string; detail?: string };
function displayEvents(events: StreamEvent[], denied: string): DisplayItem[] {
  const items: DisplayItem[] = [];
  for (const event of events) {
    const p = event.payload && typeof event.payload === "object" ? event.payload as Record<string, unknown> : {};
    const text = typeof p.text === "string" ? p.text : "";
    if (event.kind === "claude.message") {
      const previous = items.at(-1);
      if (previous?.role === "assistant") previous.text += text;
      else if (text) items.push({ id: event.event_id, role: "assistant", text });
    } else if (event.kind === "claude.user.message") items.push({ id: event.event_id, role: "user", text });
    else if (event.kind === "claude.tool_use") items.push({ id: event.event_id, role: "tool", text: String(p.name ?? "Tool"), detail: JSON.stringify(p.input ?? {}, null, 2) });
    else if (event.kind === "claude.tool_result") items.push({ id: event.event_id, role: "tool", text: "", detail: typeof p.content === "string" ? p.content : JSON.stringify(p.content ?? "", null, 2) });
    else if (event.kind === "claude.notice") items.push({ id: event.event_id, role: "notice", text });
    else if (event.kind === "claude.result") {
      if (Array.isArray(p.permission_denials) && p.permission_denials.length) items.push({ id: `${event.event_id}-denied`, role: "notice", text: denied });
      if (Array.isArray(p.errors) && p.errors.length) items.push({ id: `${event.event_id}-errors`, role: "notice", text: p.errors.join("\n") });
    } else if (event.kind === "claude.turn.completed" && typeof p.message === "string" && p.message) items.push({ id: event.event_id, role: "notice", text: p.message });
  }
  return items;
}

function ClaudeSession({ thread, revision, onChanged, onOpenFile, onArchive }: { thread: ClaudeThread; revision: string; onChanged: () => void; onOpenFile: (selection: FilePreviewSelection) => void; onArchive: () => void }) {
  const { t } = useI18n();
  const [prompt, setPrompt] = useState(""); const [model, setModel] = useState(thread.model); const [mode, setMode] = useState(thread.permission_mode); const [effort, setEffort] = useState(thread.effort ?? "");
  const [busy, setBusy] = useState(false); const [error, setError] = useState(""); const [confirming, setConfirming] = useState(false);
  const [allEvents, setAllEvents] = useState<StreamEvent[]>([]); const [historyBusy, setHistoryBusy] = useState(false);
  const allEventsRef = useRef<StreamEvent[]>([]);
  const stream = useRef<HTMLDivElement>(null); const follow = useRef(true);
  const throttledRevision = useThrottledValue(revision, 750);
  const events = useData<StreamEvent[]>(`/claude/threads/${thread.id}/events`, throttledRevision);
  const mergeEvents = useCallback((incoming: StreamEvent[]) => {
    const merged = new Map(allEventsRef.current.map(event => [event.event_id, event]));
    for (const event of incoming) merged.set(event.event_id, event);
    allEventsRef.current = [...merged.values()].sort((a, b) => a.sequence - b.sequence);
    setAllEvents(allEventsRef.current);
  }, []);
  useEffect(() => {
    if (!events.data?.length) return;
    const incoming = events.data;
    let cursor = allEventsRef.current.at(-1)?.sequence ?? 0;
    const first = incoming[0].sequence;
    if (!cursor || first <= cursor + 1) { mergeEvents(incoming); return; }
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
  }, [events.data, mergeEvents, thread.id]);
  const items = useMemo(() => displayEvents(allEvents, t("claude.denied")), [allEvents, t]);
  useEffect(() => { if (follow.current && stream.current) stream.current.scrollTop = stream.current.scrollHeight; }, [items]);
  const running = activeTurn(thread); const blocked = running || thread.recovery_pending || Boolean(thread.archived_at) || thread.server_status !== "online";
  const send = async (event: FormEvent) => {
    event.preventDefault(); if (blocked || busy || !prompt.trim()) return; setBusy(true); setError("");
    try { await post(`/claude/threads/${thread.id}/turns`, { prompt: prompt.trim(), model: model.trim(), permission_mode: mode, effort }); setPrompt(""); follow.current = true; onChanged(); }
    catch (err) { setError(message(err)); } finally { setBusy(false); }
  };
  const interrupt = async () => { setBusy(true); setError(""); try { await post(`/claude/threads/${thread.id}/interrupt`, {}); onChanged(); } catch (err) { setError(message(err)); } finally { setBusy(false); } };
  const confirmStopped = async () => { setBusy(true); try { await post(`/claude/threads/${thread.id}/confirm-stopped`, { operation_id: thread.operation_id, confirmed: true }); setConfirming(false); onChanged(); } catch (err) { setError(message(err)); } finally { setBusy(false); } };
  const earlier = async () => {
    if (!allEvents.length || historyBusy) return; setHistoryBusy(true);
    try { const older = await api<StreamEvent[]>(`/claude/threads/${thread.id}/events?before=${allEvents[0].sequence}`); mergeEvents(older); }
    catch (err) { setError(message(err)); } finally { setHistoryBusy(false); }
  };
  return <section className="session-panel gemini-session"><header className="session-header"><div><h2>{thread.title}</h2><span>{thread.server_name}<i />{thread.path}</span></div><div className="session-actions"><Status value={thread.status} /><button className="icon-button" title={t(thread.archived_at ? "gemini.showActive" : "gemini.showArchived")} aria-label={t(thread.archived_at ? "gemini.showActive" : "gemini.showArchived")} disabled={busy || running || thread.recovery_pending} onClick={onArchive}><Archive size={16} /></button></div></header>
    <div ref={stream} className="gemini-stream conversation-stream" role="log" aria-label={t("gemini.conversation")} onScroll={() => { if (stream.current) follow.current = stream.current.scrollHeight - stream.current.scrollTop - stream.current.clientHeight < 100; }}>
      {allEvents.length > 0 && allEvents[0].sequence > 1 && <button className="secondary-button" disabled={historyBusy} onClick={() => void earlier()}>{t("claude.loadEarlier")}</button>}
      {events.error && <ErrorState error={events.error} reload={events.reload} />}
      {!items.length && <Empty icon={<Bot size={30} />} text={t("claude.startPrompt")} />}
      {items.map(item => item.role === "tool" ? <details className="gemini-tool" key={item.id}><summary>{item.text ? `${t("claude.tool")}: ${item.text}` : t("claude.toolResult")}</summary><pre>{item.detail}</pre></details> : <article className={`message ${item.role === "notice" ? "error" : item.role}`} key={item.id}><header>{item.role === "user" ? <UserRound size={15} /> : <Bot size={15} />}<strong>{item.role === "user" ? t("codex.you") : "Claude"}</strong></header><MarkdownContent text={item.text} workspaceRoot={thread.path} onOpenFile={onOpenFile} /></article>)}
      {running && <p className="gemini-working" role="status"><LoaderCircle className="spin" size={16} />{t(thread.status === "interrupting" ? "claude.interrupting" : "claude.working")}</p>}
    </div>
    <form className="composer gemini-composer claude-composer" onSubmit={send}>{error && <ErrorBanner text={error} />}{thread.recovery_pending && <><p className="gemini-hint" role="status">{t("claude.recovery")}</p><button type="button" className="secondary-button" disabled={busy} onClick={() => setConfirming(true)}>{t("claude.confirmStopped")}</button></>}{thread.server_status !== "online" && <p className="gemini-hint">{t("gemini.offline")}</p>}{thread.archived_at ? <p className="gemini-hint">{t("gemini.archivedHint")}</p> : <><textarea aria-label={t("claude.prompt")} placeholder={t("claude.prompt")} value={prompt} disabled={busy || blocked} onChange={event => setPrompt(event.target.value)} onKeyDown={event => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} /><div className="composer-bar"><ClaudeOptions compact model={model} mode={mode} effort={effort} setEffort={setEffort} effortSupported={thread.effort_supported} setModel={setModel} setMode={setMode} disabled={busy || blocked} />{running ? <button type="button" className="secondary-button danger composer-action" aria-label={t("gemini.interrupt")} disabled={busy || thread.status === "interrupting" || thread.server_status !== "online"} onClick={() => void interrupt()}><Square size={15} /><span>{t("gemini.interrupt")}</span></button> : <button className="primary-button composer-action" disabled={busy || blocked || !prompt.trim() || !model.trim()}><ArrowUp size={16} /><span>{t("codex.send")}</span></button>}</div></>}</form>
    <ConfirmDialog open={confirming} danger title={t("claude.confirmStopped")} impact={t("claude.confirmStoppedImpact")} confirmLabel={t("claude.confirmStopped")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setConfirming(false)} onConfirm={confirmStopped} />
  </section>;
}
