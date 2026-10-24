import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { Archive, ArchiveRestore, ArrowUp, Bot, FileCode2, Folder, HelpCircle, LoaderCircle, MessageSquare, Pencil, Plus, RefreshCw, Sparkles, Square, SquareTerminal, Trash2, UserRound } from "lucide-react";
import { api, patch, post, remove } from "../api";
import { Dialog, ErrorBanner, Field, FileDiffPane, FilePreviewPane, message, WorkspaceFilesPanel, type FilePreviewSelection } from "../App";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DialogActions } from "../components/Dialog";
import { Empty, ErrorState, Status } from "../components/PageUI";
import { geminiMessages } from "../geminiContent";
import { formatTime, relative } from "../format";
import { useI18n } from "../i18n";
import MarkdownContent from "../MarkdownContent";
import type { GeminiThread, StreamEvent, Workspace } from "../types";
import { useData } from "../useData";

type Props = { realtime: number; notify: (text: string) => void; selectedThreadID: string; onSelectThread: (id: string, replace?: boolean) => void };
const isActive = (thread?: GeminiThread) => Boolean(thread && ["queued", "running", "interrupting"].includes(thread.status));

export default function GeminiPage({ realtime, notify, selectedThreadID, onSelectThread }: Props) {
  const { t } = useI18n();
  const [tick, setTick] = useState(0);
  const [archived, setArchived] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const [deleting, setDeleting] = useState<GeminiThread | null>(null);
  const [busy, setBusy] = useState(false);
  const [rename, setRename] = useState<GeminiThread | null>(null);
  const [title, setTitle] = useState("");
  const [mobileView, setMobileView] = useState("sessions");
  const [preview, setPreview] = useState<FilePreviewSelection | null>(null);
  const dependency = `${realtime}:${tick}`;
  const threads = useData<GeminiThread[]>(`/gemini/threads?archived=${archived}`, dependency);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const selected = useData<GeminiThread>(selectedThreadID ? `/gemini/threads/${selectedThreadID}` : null, dependency);
  const active = threads.data?.find(thread => thread.id === selectedThreadID) ?? selected.data;
  useEffect(() => { const timer = window.setInterval(() => { if (!document.hidden) setTick(value => value + 1); }, 5000); return () => clearInterval(timer); }, []);
  useEffect(() => { if (!selectedThreadID && threads.data?.length) onSelectThread(threads.data[0].id, true); }, [onSelectThread, selectedThreadID, threads.data]);
  useEffect(() => { if (selected.data) setArchived(Boolean(selected.data.archived_at)); }, [selected.data?.id, selected.data?.archived_at]);
  useEffect(() => { setPreview(null); if (selectedThreadID) setMobileView("conversation"); }, [selectedThreadID]);
  const groups = useMemo(() => {
    const result = new Map<string, { name: string; threads: GeminiThread[] }>();
    for (const thread of threads.data ?? []) {
      const group = result.get(thread.workspace_id) ?? { name: `${thread.project_name} · ${thread.server_name}`, threads: [] };
      group.threads.push(thread); result.set(thread.workspace_id, group);
    }
    return [...result.entries()];
  }, [threads.data]);
  const refresh = () => { threads.reload(); selected.reload(); setTick(value => value + 1); };
  const mutate = async (thread: GeminiThread, values: unknown) => {
    setBusy(true);
    try { await patch(`/gemini/threads/${thread.id}`, values); setRename(null); refresh(); }
    catch (error) { notify(message(error)); } finally { setBusy(false); }
  };
  const deleteThread = async () => {
    if (!deleting) return;
    setBusy(true);
    try { await remove(`/gemini/threads/${deleting.id}`); if (selectedThreadID === deleting.id) onSelectThread(""); setDeleting(null); refresh(); }
    catch (error) { notify(message(error)); } finally { setBusy(false); }
  };

  return <div className={`codex-layout gemini-layout gemini-mobile-${mobileView}`}>
    <div className="gemini-mobile-tabs">{["sessions", "files", "conversation"].map(view => <button key={view} className={mobileView === view ? "active" : ""} onClick={() => setMobileView(view)}>{t(`gemini.${view}`)}</button>)}</div>
    <aside className="codex-sidebar">
      <div className="thread-list gemini-thread-list">
        <header className="gemini-sidebar-heading"><strong><Sparkles size={16} />Gemini</strong><div><button className={`icon-button ${archived ? "active" : ""}`} aria-label={t(archived ? "gemini.showActive" : "gemini.showArchived")} onClick={() => { setArchived(!archived); onSelectThread(""); }}><Archive size={16} /></button><button className="icon-button" aria-label={t("gemini.newSession")} onClick={() => setCreateOpen(true)}><Plus size={18} /></button></div></header>
        <div className="thread-items">{threads.error ? <ErrorState error={threads.error} reload={threads.reload} /> : threads.loading ? <p className="gemini-hint">{t("common.loading")}</p> : groups.length ? groups.map(([id, group]) => <section className="thread-project" key={id}><div className="thread-project-toggle"><Folder size={14} /><strong title={group.name}>{group.name}</strong><span>{group.threads.length}</span></div>{group.threads.map(thread => <div key={thread.id} className={`thread ${active?.id === thread.id ? "active" : ""}`}><button className="thread-select" onClick={() => { onSelectThread(thread.id); setMobileView("conversation"); }}><span><strong>{thread.title}</strong><small>{relative(thread.updated_at)} · {t(`status.${thread.status}`)}</small></span></button><div className="thread-actions"><button className="icon-button" disabled={isActive(thread) || busy} aria-label={t("gemini.rename")} onClick={() => { setRename(thread); setTitle(thread.title); }}><Pencil size={13} /></button><button className="icon-button" disabled={isActive(thread) || busy} aria-label={t(thread.archived_at ? "gemini.restore" : "gemini.archive")} onClick={() => void mutate(thread, { archived: !thread.archived_at })}>{thread.archived_at ? <ArchiveRestore size={13} /> : <Archive size={13} />}</button><button className="icon-button danger" disabled={isActive(thread) || busy} aria-label={t("gemini.delete")} onClick={() => setDeleting(thread)}><Trash2 size={13} /></button></div></div>)}</section>) : <Empty icon={<MessageSquare size={24} />} text={t("gemini.noSessions")} />}</div>
      </div>
      {active && <WorkspaceFilesPanel workspaceID={active.workspace_id} taskID={active.operation_id} taskStatus={active.status} realtime={realtime + tick} activePath={preview?.path ?? ""} activeMode={preview?.mode === "diff" ? "diff" : "file"} onOpenFile={value => { setPreview(value); setMobileView("conversation"); }} notify={notify} />}
      <button className="text-button gemini-help" onClick={() => setHelpOpen(true)}><HelpCircle size={15} />{t("gemini.setup")}</button>
    </aside>
    <div className="gemini-main">{active ? <>
      {preview && <div className="gemini-preview-tabs"><button className="secondary-button" onClick={() => setPreview(null)}><MessageSquare size={15} />{t("gemini.conversation")}</button><span><FileCode2 size={14} />{preview.path}</span></div>}
      <div className={preview ? "gemini-conversation-hidden" : "gemini-conversation"}><GeminiSession key={active.id} thread={active} realtime={dependency} notify={notify} onChanged={refresh} onOpenFile={setPreview} /></div>
      {preview && (preview.mode === "diff" ? <FileDiffPane workspaceID={active.workspace_id} selection={preview} realtime={realtime + tick} onClose={() => setPreview(null)} writable notify={notify} /> : <FilePreviewPane workspaceID={active.workspace_id} selection={preview} realtime={realtime + tick} onClose={() => setPreview(null)} writable />)}
    </> : selectedThreadID && selected.error ? <ErrorState error={selected.error} reload={selected.reload} /> : <div className="gemini-welcome"><Sparkles size={40} /><h2>{t("gemini.welcome")}</h2><p>{t("gemini.intro")}</p><button className="primary-button" onClick={() => setCreateOpen(true)}><Plus size={17} />{t("gemini.newSession")}</button><button className="text-button" onClick={() => setHelpOpen(true)}>{t("gemini.setup")}</button></div>}</div>
    <Dialog open={createOpen} title={t("gemini.newSession")} onClose={() => setCreateOpen(false)}><CreateGeminiSession workspaces={workspaces.data ?? []} error={workspaces.error} onRetry={workspaces.reload} onCreated={thread => { setArchived(false); onSelectThread(thread.id); setCreateOpen(false); refresh(); }} /></Dialog>
    <Dialog open={helpOpen} title={t("gemini.setup")} onClose={() => setHelpOpen(false)}><div className="gemini-setup"><p>{t("gemini.setupIntro")}</p><pre>npm install -g @google/gemini-cli</pre><p>{t("gemini.authHelp")}</p><p>{t("gemini.permissionsHelp")}</p><p>{t("gemini.timeoutHelp")}</p><a href="https://geminicli.com/docs/cli/headless/" target="_blank" rel="noreferrer">{t("gemini.docs")}</a></div></Dialog>
    <Dialog open={Boolean(rename)} title={t("gemini.rename")} onClose={() => { if (!busy) setRename(null); }}><form onSubmit={event => { event.preventDefault(); if (rename) void mutate(rename, { title }); }}><Field label={t("gemini.title")}><input autoFocus required maxLength={180} value={title} onChange={event => setTitle(event.target.value)} /></Field><DialogActions><button className="primary-button" disabled={busy || !title.trim()}>{t("common.save")}</button></DialogActions></form></Dialog>
    <ConfirmDialog open={Boolean(deleting)} danger title={t("gemini.delete")} impact={t("gemini.deleteConfirm", { title: deleting?.title ?? "" })} confirmLabel={t("common.delete")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setDeleting(null)} onConfirm={deleteThread} />
  </div>;
}

function ModelOptions({ workspaceID, model, mode, setModel, setMode, disabled = false }: { workspaceID: string; model: string; mode: string; setModel: (value: string) => void; setMode: (value: string) => void; disabled?: boolean }) {
  const { t } = useI18n();
  const models = useData<{ models: { id: string; name: string }[]; configured: boolean }>(workspaceID ? `/workspaces/${workspaceID}/gemini/models` : null, workspaceID);
  const upstream = models.data?.models ?? [];
  const [customSelected, setCustomSelected] = useState(false);
  const custom = customSelected || Boolean(model) && !upstream.some(item => item.id === model);
  return <div className="gemini-options"><Field label={t("gemini.model")}><div className="gemini-model-input"><select aria-label={t("gemini.model")} value={custom ? "__custom__" : model} disabled={disabled} onChange={event => { const custom = event.target.value === "__custom__"; setCustomSelected(custom); setModel(custom ? "" : event.target.value); }}><option value="" disabled>{t("gemini.selectModel")}</option>{upstream.map(item => <option value={item.id} key={item.id}>{item.name === item.id ? item.id : `${item.name} · ${item.id}`}</option>)}<option value="__custom__">{t("gemini.customModel")}</option></select><button type="button" className="icon-button" aria-label={t("gemini.refreshModels")} title={t("gemini.refreshModels")} disabled={!workspaceID || disabled || models.loading} onClick={models.reload}>{models.loading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button></div>{custom && <input aria-label={t("gemini.customModelName")} placeholder={t("gemini.customModelName")} required maxLength={128} value={model} disabled={disabled} onChange={event => { setCustomSelected(true); setModel(event.target.value); }} />}{models.error && <small className="gemini-model-error" role="alert">{models.error}</small>}{models.data?.configured === false && <small className="muted">{t("gemini.modelsUnconfigured")}</small>}</Field><Field label={t("gemini.permissions")}><select aria-label={t("gemini.permissions")} value={mode} disabled={disabled} onChange={event => setMode(event.target.value)}><option value="default">{t("gemini.defaultMode")}</option><option value="auto_edit">{t("gemini.autoEditMode")}</option></select></Field></div>;
}

function CreateGeminiSession({ workspaces, error, onRetry, onCreated }: { workspaces: Workspace[]; error: string; onRetry: () => void; onCreated: (thread: GeminiThread) => void }) {
  const { t } = useI18n();
  const [workspaceID, setWorkspaceID] = useState("");
  const [model, setModel] = useState("");
  const [mode, setMode] = useState("default");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault(); setBusy(true); setFailure("");
    try { onCreated(await post<GeminiThread>("/gemini/threads", { workspace_id: workspaceID, model: model.trim(), approval_mode: mode })); }
    catch (error) { setFailure(message(error)); } finally { setBusy(false); }
  };
  return <form onSubmit={submit}>{error && <ErrorState error={error} reload={onRetry} />}<Field label={t("gemini.workspace")}><select required value={workspaceID} onChange={event => setWorkspaceID(event.target.value)}><option value="">{t("gemini.selectWorkspace")}</option>{workspaces.filter(workspace => workspace.status === "ready").map(workspace => <option key={workspace.id} value={workspace.id}>{workspace.project_name} · {workspace.server_name} · {workspace.path}</option>)}</select></Field><ModelOptions workspaceID={workspaceID} model={model} mode={mode} setModel={setModel} setMode={setMode} disabled={busy} /><p className="gemini-hint">{t("gemini.permissionsHelp")}</p>{failure && <ErrorBanner text={failure} />}<DialogActions><button className="primary-button" disabled={busy || !workspaceID || !model.trim()}>{busy ? <LoaderCircle className="spin" size={16} /> : <Plus size={16} />}{t("gemini.newSession")}</button></DialogActions></form>;
}

function GeminiSession({ thread, realtime, notify, onChanged, onOpenFile }: { thread: GeminiThread; realtime: string; notify: (text: string) => void; onChanged: () => void; onOpenFile: (value: FilePreviewSelection) => void }) {
  const { t } = useI18n();
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const cursor = useRef(0);
  const [hasEarlier, setHasEarlier] = useState(false);
  const [historyBusy, setHistoryBusy] = useState(false);
  const [error, setError] = useState("");
  const [prompt, setPrompt] = useState("");
  const [model, setModel] = useState(thread.model);
  const [mode, setMode] = useState(thread.approval_mode);
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);
  const stream = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  const running = isActive(thread);
  const messages = useMemo(() => geminiMessages(events), [events]);
  const merge = (incoming: StreamEvent[]) => setEvents(current => [...new Map([...current, ...incoming].map(event => [event.event_id, event])).values()].sort((a, b) => a.sequence - b.sequence));
  useEffect(() => {
    const controller = new AbortController();
    (async () => {
      let initial = cursor.current === 0;
      while (!controller.signal.aborted) {
        const next = await api<StreamEvent[]>(`/gemini/threads/${thread.id}/events${initial ? "" : `?after=${cursor.current}`}`, { signal: controller.signal });
        if (controller.signal.aborted) return;
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
    follow.current = false;
    try { const next = await api<StreamEvent[]>(`/gemini/threads/${thread.id}/events?before=${events[0]?.sequence ?? 0}`); merge(next); setHasEarlier(next.length === 500); requestAnimationFrame(() => { if (element) element.scrollTop += element.scrollHeight - height; }); }
    catch (error) { notify(message(error)); } finally { setHistoryBusy(false); }
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault(); if (busy || running || !prompt.trim()) return;
    setBusy(true); setError("");
    try { await post(`/gemini/threads/${thread.id}/turns`, { prompt, model: model.trim(), approval_mode: mode }); setPrompt(""); follow.current = true; onChanged(); setReload(value => value + 1); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  };
  const interrupt = async () => {
    setBusy(true);
    try { await post(`/gemini/threads/${thread.id}/interrupt`, {}); onChanged(); }
    catch (error) { setError(message(error)); } finally { setBusy(false); }
  };
  return <section className="session-panel gemini-session"><header className="session-header"><div><h2>{thread.title}</h2><span>{thread.server_name}<i />{thread.path}</span></div><Status value={thread.status} /><button className="icon-button" aria-label={t("common.refresh")} onClick={() => { onChanged(); setReload(value => value + 1); }}><RefreshCw size={16} /></button></header>
    <div className="gemini-stream" ref={stream} role="log" aria-label={t("gemini.conversation")} onScroll={() => { const el = stream.current; if (el) follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100; }}>
      {hasEarlier && <button className="secondary-button" disabled={historyBusy} onClick={() => void earlier()}>{t("gemini.loadEarlier")}</button>}
      {!messages.length && <Empty icon={<Sparkles size={30} />} text={t("gemini.startPrompt")} />}
      {messages.map(item => item.role === "tool" ? <details className="gemini-tool" key={item.id}><summary><SquareTerminal size={15} />{item.title || t("gemini.tool")}</summary><pre>{item.text}</pre></details> : item.role === "status" ? <p className="gemini-usage" key={item.id}>{item.text === "interrupted" ? t("status.interrupted") : item.stats ? t("gemini.usage", { input: String(item.stats.input_tokens ?? 0), output: String(item.stats.output_tokens ?? 0), tools: String(item.stats.tool_calls ?? 0) }) : item.text}</p> : <article className={`message ${item.role === "error" ? "error" : item.role}`} key={item.id}><header>{item.role === "user" ? <UserRound size={15} /> : <Bot size={15} />}<strong>{item.role === "user" ? t("codex.you") : "Gemini"}</strong><time>{formatTime(item.time)}</time></header><MarkdownContent text={item.text} workspaceRoot={thread.path} onOpenFile={onOpenFile} /></article>)}
      {running && <p className="gemini-working" role="status"><LoaderCircle className="spin" size={16} />{t(thread.status === "interrupting" ? "gemini.interrupting" : "gemini.working")}</p>}
    </div>
    <form className="composer gemini-composer" onSubmit={submit}>
      {error && <ErrorBanner text={error} />}
      {thread.server_status !== "online" && <p className="gemini-hint">{t("gemini.offline")}</p>}
      {thread.archived_at ? <p className="gemini-hint">{t("gemini.archivedHint")}</p> : <><textarea aria-label={t("gemini.prompt")} placeholder={t("gemini.prompt")} value={prompt} disabled={running || busy} onChange={event => setPrompt(event.target.value)} onKeyDown={event => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} /><ModelOptions workspaceID={thread.workspace_id} model={model} mode={mode} setModel={setModel} setMode={setMode} disabled={running || busy} /><div className="gemini-composer-footer"><small>{t("gemini.composerHint")}</small>{running ? <button type="button" className="secondary-button danger" disabled={busy || thread.status === "interrupting"} onClick={() => void interrupt()}><Square size={14} />{t("gemini.interrupt")}</button> : <button className="primary-button" aria-label={t("gemini.send")} disabled={busy || !prompt.trim() || !model.trim() || thread.server_status !== "online"}>{busy ? <LoaderCircle className="spin" size={15} /> : <ArrowUp size={15} />}{t("gemini.send")}</button>}</div></>}
    </form>
  </section>;
}
