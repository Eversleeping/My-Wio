import { FormEvent, useEffect, useMemo, useState } from "react";
import {
  Archive,
  ArchiveRestore,
  AlertTriangle,
  ArrowDownToLine,
  CalendarClock,
  Check,
  ChevronDown,
  ChevronRight,
  Code2,
  Copy,
  EyeOff,
  ExternalLink,
  FileCode2,
  FileDiff,
  Folder,
  FolderOpen,
  FolderTree,
  GitBranch,
  GitFork,
  Link,
  LoaderCircle,
  MessageSquare,
  Pencil,
  Pin,
  PinOff,
  Plus,
  ShieldCheck,
  SquareTerminal,
  Trash2
} from "lucide-react";
import { patch, post, remove } from "../api";
import { ContextMenu, type ContextMenuAction } from "../ContextMenu";
import { DialogActions } from "../components/Dialog";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Empty, ErrorState, Status } from "../components/PageUI";
import { clearCodexComposerPreferences } from "../codexComposerPreferences";
import { relative } from "../format";
import { useI18n } from "../i18n";
import { useData } from "../useData";
import type { Approval, Project, Thread, Workspace } from "../types";
import {
  ApprovalActions,
  clearCodexSessionMemory,
  copyText,
  CreateThread,
  Dialog,
  ErrorBanner,
  Field,
  FileDiffPane,
  FilePreviewPane,
  groupThreadsByWorkspace,
  locationFor,
  mergeThreads,
  message,
  SessionView,
  useThreadList,
  waitForThread,
  waitForWorkspace,
  WorkspaceFilesPanel,
  type CodexPageProps,
  type FilePreviewSelection,
  type ThreadGroup
} from "../App";
import { ScheduledTaskDialog } from "./ScheduledTaskDialog";

function pretty(value: unknown) {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function approvalDetail(detail: unknown) {
  if (!detail || typeof detail !== "object" || Array.isArray(detail)) return pretty(detail);
  const value = detail as Record<string, unknown>;
  for (const key of ["command", "reason", "message", "question"]) {
    if (typeof value[key] === "string" && value[key]) return value[key] as string;
  }
  return pretty(detail);
}

function readableKind(kind: string) {
  return kind.replace(/^codex\./, "").replaceAll(".", " / ").replaceAll("/", " / ");
}

export default function CodexPage({ realtime, streamRevisions, approvals, approvalSignal, reloadApprovals, notify, selectedThreadID, onSelectThread }: CodexPageProps) {
  const { t } = useI18n();
  const threads = useThreadList(false, realtime);
  const archivedThreads = useThreadList(true, realtime);
  const workspaces = useData<Workspace[]>("/workspaces", realtime);
  const [selected, setSelected] = useState(selectedThreadID);
  const [createOpen, setCreateOpen] = useState(false);
  const [approvalOpen, setApprovalOpen] = useState(approvals.length > 0);
  const [collapsedWorkspaces, setCollapsedWorkspaces] = useState<Set<string>>(new Set());
  const [deletingThread, setDeletingThread] = useState("");
  const [threadAction, setThreadAction] = useState("");
  const [showArchived, setShowArchived] = useState(false);
  const [pendingThread, setPendingThread] = useState<Thread | null>(null);
  const [renameTarget, setRenameTarget] = useState<{ kind: "project" | "thread"; id: string; name: string } | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [renameBusy, setRenameBusy] = useState(false);
  const [worktreeTarget, setWorktreeTarget] = useState<{ kind: "project"; projectID: string; projectName: string } | { kind: "thread"; thread: Thread } | null>(null);
  const [worktreeForm, setWorktreeForm] = useState({ workspace_id: "", branch: "", path: "", base_ref: "HEAD" });
  const [worktreeBusy, setWorktreeBusy] = useState(false);
  const [worktreeError, setWorktreeError] = useState("");
  const [scheduledThread, setScheduledThread] = useState<Thread | null>(null);
  const [codexConfirmation, setCodexConfirmation] = useState<{ type: "archive-project" | "hide-project"; group: ThreadGroup } | { type: "delete-thread"; thread: Thread } | null>(null);
  const [preview, setPreview] = useState<FilePreviewSelection | null>(null);
  const [activePane, setActivePane] = useState<"conversation" | "preview">("conversation");
  const [mobileView, setMobileView] = useState<"sessions" | "files" | "conversation">(selectedThreadID ? "conversation" : "sessions");
  const listedActiveThreads = threads.data ?? [];
  const listedArchivedThreads = archivedThreads.data ?? [];
  const knownSelectedThread = pendingThread?.id === selectedThreadID ? pendingThread : [...listedActiveThreads, ...listedArchivedThreads].find(thread => thread.id === selectedThreadID);
  const deepLinkedThread = useData<Thread>(selectedThreadID && !knownSelectedThread ? `/threads/${selectedThreadID}` : null, `${realtime}:${selectedThreadID}`);
  const requestedThread = knownSelectedThread ?? deepLinkedThread.data;
  const requestedThreadMatchesView = requestedThread && Boolean(requestedThread.archived_at) === showArchived ? requestedThread : null;
  const activeThreads = mergeThreads(
    !showArchived && pendingThread ? [pendingThread] : [],
    requestedThreadMatchesView ? [requestedThreadMatchesView] : [],
    showArchived ? listedArchivedThreads : listedActiveThreads
  );
  const activeThreadSource = showArchived ? archivedThreads : threads;
  const visibleThreads = useMemo(() => activeThreads.filter(thread => !thread.project_hidden_at), [activeThreads]);
  const active = visibleThreads.find(thread => thread.id === selected) ?? visibleThreads[0];
  const activeWorkspace = workspaces.data?.find(workspace => workspace.id === active?.workspace_id);
  const threadGroups = useMemo(() => groupThreadsByWorkspace(visibleThreads, workspaces.data ?? []), [visibleThreads, workspaces.data]);
  const approvalKey = approvals.map(item => item.id).join(",");
  useEffect(() => {
    if (selectedThreadID && requestedThread && Boolean(requestedThread.archived_at) !== showArchived) setShowArchived(Boolean(requestedThread.archived_at));
  }, [requestedThread, selectedThreadID, showArchived]);
  useEffect(() => {
    if (!(showArchived ? archivedThreads.data : threads.data)) return;
    if (selectedThreadID && !visibleThreads.some(thread => thread.id === selectedThreadID) && (deepLinkedThread.loading || (requestedThread && Boolean(requestedThread.archived_at) !== showArchived))) return;
    const requested = selectedThreadID && visibleThreads.some(thread => thread.id === selectedThreadID) ? selectedThreadID : "";
    const next = requested || (visibleThreads.some(thread => thread.id === selected) ? selected : visibleThreads[0]?.id ?? "");
    if (next !== selected) setSelected(next);
    if (next !== selectedThreadID) onSelectThread(next, true);
  }, [archivedThreads.data, deepLinkedThread.loading, onSelectThread, requestedThread, selected, selectedThreadID, showArchived, threads.data, visibleThreads]);
  useEffect(() => { setApprovalOpen(Boolean(approvalKey)); }, [approvalKey, approvalSignal]);
  useEffect(() => { if (pendingThread && listedActiveThreads.some(thread => thread.id === pendingThread.id)) setPendingThread(null); }, [listedActiveThreads, pendingThread]);
  useEffect(() => { setPreview(null); setActivePane("conversation"); if (active) setMobileView("conversation"); }, [active?.workspace_id]);
  const selectThread = (threadID: string) => { setSelected(threadID); setMobileView(threadID ? "conversation" : "sessions"); onSelectThread(threadID); };
  const toggleWorkspace = (workspaceID: string) => setCollapsedWorkspaces(current => { const next = new Set(current); if (next.has(workspaceID)) next.delete(workspaceID); else next.add(workspaceID); return next; });
  const openFile = (selection: FilePreviewSelection) => { setPreview(selection); setActivePane("preview"); setMobileView("conversation"); };
  const copyValue = async (value: string, success: string) => { try { await copyText(value); notify(success); } catch (error) { notify(message(error)); } };
  const deepLink = (threadID: string) => `${window.location.origin}${locationFor("codex", threadID)}`;
  const beginRename = (kind: "project" | "thread", id: string, name: string) => { setRenameTarget({ kind, id, name }); setRenameValue(name); };
  const submitRename = async (event: FormEvent) => {
    event.preventDefault();
    const value = renameValue.trim();
    if (!renameTarget || !value || renameBusy) return;
    setRenameBusy(true);
    try {
      if (renameTarget.kind === "project") await patch<Project>(`/projects/${renameTarget.id}`, { name: value });
      else await patch<Thread>(`/threads/${renameTarget.id}`, { title: value });
      threads.reload();
      notify(t(renameTarget.kind === "project" ? "codex.projectRenamed" : "codex.threadRenamed"));
      setRenameTarget(null);
    } catch (error) { notify(message(error)); } finally { setRenameBusy(false); }
  };
  const reloadThreadLists = () => { threads.reload(); archivedThreads.reload(); };
  const setThreadArchived = async (thread: Thread, archived: boolean) => {
    if (threadAction) return;
    setThreadAction(`archive:${thread.id}`);
    try {
      await patch<Thread>(`/threads/${thread.id}`, { archived });
      const remaining = visibleThreads.find(item => item.id !== thread.id);
      if (active?.id === thread.id) selectThread(remaining?.id ?? "");
      reloadThreadLists();
      notify(t(archived ? "codex.threadArchived" : "codex.threadRestored"));
    } catch (error) { notify(message(error)); } finally { setThreadAction(""); }
  };
  const archiveProjectThreads = async (group: ThreadGroup) => {
    if (threadAction) return;
    setThreadAction(`project:${group.projectID}`);
    try {
      const result = await post<{ archived: number }>(`/projects/${group.projectID}/threads/archive`, {});
      if (active?.project_id === group.projectID) selectThread(visibleThreads.find(item => item.project_id !== group.projectID)?.id ?? "");
      reloadThreadLists();
      notify(t("codex.projectThreadsArchived", { count: result.archived }));
      setCodexConfirmation(null);
    } catch (error) { notify(message(error)); } finally { setThreadAction(""); }
  };
  const hideProject = async (group: ThreadGroup) => {
    if (threadAction) return;
    setThreadAction(`hide:${group.projectID}`);
    try {
      await patch<Project>(`/projects/${group.projectID}`, { hidden: true });
      if (active?.project_id === group.projectID) selectThread(visibleThreads.find(thread => thread.project_id !== group.projectID)?.id ?? "");
      threads.reload();
      notify(t("codex.projectHidden"));
      setCodexConfirmation(null);
    } catch (error) { notify(message(error)); } finally { setThreadAction(""); }
  };
  const continueInNewTask = async (thread: Thread) => {
    if (threadAction) return;
    setThreadAction(`fork:${thread.id}`);
    try {
      const result = await post<{ operation_id: string; target_thread_id: string }>(`/threads/${thread.id}/fork`, {});
      const created = await waitForThread(result.target_thread_id, t("codex.threadForkTimeout"));
      setPendingThread(created);
      reloadThreadLists();
      setShowArchived(false);
      selectThread(created.id);
      notify(t("codex.threadForked"));
    } catch (error) { notify(message(error)); } finally { setThreadAction(""); }
  };
  const createSessionInWorkspace = async (group: ThreadGroup) => {
    if (threadAction) return;
    setThreadAction(`create:${group.workspaceID}`);
    try {
      const created = await post<Thread>("/threads", { workspace_id: group.workspaceID });
      setPendingThread(created);
      setShowArchived(false);
      selectThread(created.id);
      threads.reload();
      notify(t("codex.sessionCreated"));
    } catch (error) { notify(message(error)); } finally { setThreadAction(""); }
  };
  const openWorktreeDialog = (target: { kind: "project"; projectID: string; projectName: string } | { kind: "thread"; thread: Thread }) => {
    const candidates = target.kind === "project" ? (workspaces.data ?? []).filter(workspace => workspace.project_id === target.projectID) : (workspaces.data ?? []).filter(workspace => workspace.id === target.thread.workspace_id);
    setWorktreeTarget(target);
    setWorktreeForm({ workspace_id: candidates[0]?.id ?? "", branch: "", path: "", base_ref: "HEAD" });
    setWorktreeError("");
  };
  const closeWorktreeDialog = () => { if (!worktreeBusy) { setWorktreeTarget(null); setWorktreeError(""); } };
  const submitWorktree = async (event: FormEvent) => {
    event.preventDefault();
    if (!worktreeTarget || worktreeBusy || !worktreeForm.workspace_id || !worktreeForm.branch.trim()) return;
    setWorktreeBusy(true);
    setWorktreeError("");
    const payload = { branch: worktreeForm.branch.trim(), path: worktreeForm.path.trim(), base_ref: worktreeForm.base_ref.trim() || "HEAD" };
    try {
      if (worktreeTarget.kind === "project") {
        const result = await post<{ operation_id: string; workspace_id: string }>(`/workspaces/${worktreeForm.workspace_id}/worktrees`, payload);
        await waitForWorkspace(result.workspace_id, t("codex.worktreeTimeout"));
        workspaces.reload();
        notify(t("codex.worktreeCreated"));
      } else {
        const result = await post<{ operation_id: string; workspace_id: string; target_thread_id: string }>(`/threads/${worktreeTarget.thread.id}/fork-worktree`, payload);
        const [, created] = await Promise.all([waitForWorkspace(result.workspace_id, t("codex.worktreeTimeout")), waitForThread(result.target_thread_id, t("codex.worktreeTaskTimeout"))]);
        setPendingThread(created);
        workspaces.reload();
        reloadThreadLists();
        setShowArchived(false);
        selectThread(created.id);
        notify(t("codex.continuedInWorktree"));
      }
      setWorktreeTarget(null);
    } catch (error) { setWorktreeError(message(error)); } finally { setWorktreeBusy(false); }
  };
  const projectMenuActions = (group: ThreadGroup): ContextMenuAction[] => [
    ...(!showArchived ? [{ id: "new-session", label: t("codex.createSessionHere"), icon: Plus, disabled: threadAction !== "", onSelect: () => createSessionInWorkspace(group) } satisfies ContextMenuAction] : []),
    { id: "pin", label: t(group.pinnedAt ? "codex.unpinProject" : "codex.pinProject"), icon: group.pinnedAt ? PinOff : Pin, onSelect: async () => { try { await patch<Project>(`/projects/${group.projectID}`, { pinned: !group.pinnedAt }); threads.reload(); notify(t(group.pinnedAt ? "codex.projectUnpinned" : "codex.projectPinned")); } catch (error) { notify(message(error)); } } },
    { id: "rename", label: t("codex.renameProject"), icon: Pencil, onSelect: () => beginRename("project", group.projectID, group.projectName) },
    ...(!showArchived ? [{ id: "create-worktree", label: t("codex.createPermanentWorktree"), icon: GitBranch, disabled: threadAction !== "" || !(workspaces.data ?? []).some(workspace => workspace.project_id === group.projectID), onSelect: () => openWorktreeDialog({ kind: "project", projectID: group.projectID, projectName: group.projectName }) } satisfies ContextMenuAction] : []),
    ...(!showArchived ? [{ id: "archive-tasks", label: t("codex.archiveProjectThreads"), icon: Archive, danger: true, separatorBefore: true, disabled: threadAction !== "", onSelect: () => setCodexConfirmation({ type: "archive-project", group }) } satisfies ContextMenuAction] : []),
    { id: "hide", label: t("codex.hideProject"), icon: EyeOff, danger: true, separatorBefore: true, disabled: threadAction !== "", onSelect: () => setCodexConfirmation({ type: "hide-project", group }) }
  ];
  const threadMenuActions = (thread: Thread): ContextMenuAction[] => [
    ...(showArchived ? [{ id: "restore", label: t("codex.restoreThread"), icon: ArchiveRestore, disabled: threadAction !== "", onSelect: () => setThreadArchived(thread, false) } satisfies ContextMenuAction] : [
    { id: "pin", label: t(thread.pinned_at ? "codex.unpinThread" : "codex.pinThread"), icon: thread.pinned_at ? PinOff : Pin, onSelect: async () => { try { await patch<Thread>(`/threads/${thread.id}`, { pinned: !thread.pinned_at }); threads.reload(); notify(t(thread.pinned_at ? "codex.threadUnpinned" : "codex.threadPinned")); } catch (error) { notify(message(error)); } } },
    { id: "rename", label: t("codex.renameThread"), icon: Pencil, onSelect: () => beginRename("thread", thread.id, thread.title) },
    { id: "schedule", label: t("codex.createScheduledTask"), icon: CalendarClock, onSelect: () => setScheduledThread(thread) },
    { id: "fork", label: t("codex.continueInNewTask"), icon: GitFork, disabled: threadAction !== "" || !thread.codex_thread_id, onSelect: () => continueInNewTask(thread) },
    { id: "fork-worktree", label: t("codex.continueInNewWorktree"), icon: GitBranch, disabled: threadAction !== "" || !thread.codex_thread_id || thread.status === "queued" || thread.status === "running", onSelect: () => openWorktreeDialog({ kind: "thread", thread }) },
    { id: "archive", label: t("codex.archiveThread"), icon: Archive, danger: true, separatorBefore: true, disabled: threadAction !== "" || thread.status === "queued" || thread.status === "running", onSelect: () => setThreadArchived(thread, true) }
    ]),
    { id: "copy-path", label: t("codex.copyWorkingDirectory"), icon: FolderOpen, separatorBefore: true, disabled: !thread.path, onSelect: () => copyValue(thread.path, t("codex.workingDirectoryCopied")) },
    { id: "copy-wio-id", label: t("codex.copyWioSessionID"), icon: Copy, onSelect: () => copyValue(thread.id, t("codex.wioSessionIDCopied")) },
    { id: "copy-codex-id", label: t("codex.copyCodexSessionID"), icon: Copy, disabled: !thread.codex_thread_id, onSelect: () => copyValue(thread.codex_thread_id, t("codex.codexSessionIDCopied")) },
    { id: "copy-link", label: t("codex.copyDeepLink"), icon: Link, onSelect: () => copyValue(deepLink(thread.id), t("codex.deepLinkCopied")) },
    { id: "new-window", label: t("codex.openNewWindow"), icon: ExternalLink, separatorBefore: true, onSelect: () => { window.open(deepLink(thread.id), "_blank", "noopener,noreferrer"); } }
  ];
  const deleteSession = async (thread: Thread) => {
    if (thread.status === "queued" || thread.status === "running") return;
    setDeletingThread(thread.id);
    try {
      await remove(`/threads/${thread.id}`);
      clearCodexComposerPreferences(thread.id);
      clearCodexSessionMemory(thread.id);
      const next = visibleThreads.find(item => item.id !== thread.id);
      if (active?.id === thread.id) selectThread(next?.id ?? "");
      threads.reload();
      notify(t("codex.sessionDeleted"));
      setCodexConfirmation(null);
    } catch (error) {
      notify(message(error));
    } finally {
      setDeletingThread("");
    }
  };
  return <div className="codex-layout">
    <div className="codex-mobile-tabs" role="tablist" aria-label={t("codex.sessionViews")}><button type="button" role="tab" aria-selected={mobileView === "sessions"} className={mobileView === "sessions" ? "active" : ""} onClick={() => setMobileView("sessions")}><Code2 size={15} />{t("codex.sessions")}</button><button type="button" role="tab" aria-selected={mobileView === "files"} className={mobileView === "files" ? "active" : ""} onClick={() => setMobileView("files")}><FolderTree size={15} />{t("codex.projectFiles")}</button><button type="button" role="tab" aria-selected={mobileView === "conversation"} className={mobileView === "conversation" ? "active" : ""} onClick={() => setMobileView("conversation")}><MessageSquare size={15} />{t("codex.conversation")}</button></div>
    <aside className={`codex-sidebar mobile-${mobileView}`}><section className="thread-list"><div className="panel-heading"><div><Code2 size={18} /><h2>{t(showArchived ? "codex.archivedTasks" : "codex.sessions")}</h2></div><div className="row-actions"><button className={`icon-button ${showArchived ? "active" : ""}`} aria-pressed={showArchived} title={t(showArchived ? "codex.showActiveTasks" : "codex.showArchivedTasks")} onClick={() => { setShowArchived(value => !value); selectThread(""); }}><Archive size={18} /></button>{!showArchived && <button className="icon-button" title={t("codex.newSession")} onClick={() => setCreateOpen(true)}><Plus size={18} /></button>}</div></div><div className="thread-items">{activeThreadSource.loading ? <div className="page-loading"><LoaderCircle className="spin" size={20} /></div> : <>{threadGroups.length === 0 ? <Empty icon={showArchived ? <Archive size={23} /> : <Code2 size={23} /> } text={t(showArchived ? "codex.noArchivedTasks" : "codex.noSessions")} /> : threadGroups.map(group => {
      const collapsed = collapsedWorkspaces.has(group.workspaceID);
      return <section className="thread-project" key={group.workspaceID}>
        <ContextMenu className="thread-project-heading" label={t("codex.workspaceMenu", { name: group.label })} actions={projectMenuActions(group)}>
          <button type="button" className="thread-project-toggle" aria-expanded={!collapsed} title={`${t(collapsed ? "codex.expandWorkspace" : "codex.collapseWorkspace")} · ${group.path}`} onClick={() => toggleWorkspace(group.workspaceID)}>
            {collapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} />}<Folder size={15} /><strong>{group.label}</strong>{group.pinnedAt && <Pin className="pinned-icon" size={12} />}<span>{group.threads.length}</span>
          </button>
        </ContextMenu>
        {!collapsed && <div className="project-threads">{group.threads.map(thread => {
          const activeThread = thread.status === "queued" || thread.status === "running";
          const deleting = deletingThread === thread.id;
          const acting = threadAction.endsWith(`:${thread.id}`);
          return <ContextMenu key={thread.id} className={active?.id === thread.id ? "thread active" : "thread"} label={t("codex.threadMenu", { name: thread.title })} actions={threadMenuActions(thread)}><button type="button" className="thread-select" onClick={() => selectThread(thread.id)}><span><strong>{thread.title}</strong><small>{thread.server_name}</small></span>{thread.pinned_at && <Pin className="pinned-icon" size={12} />}</button><div className="thread-actions">{acting ? <LoaderCircle className="spin" size={14} /> : <Status value={thread.status} />}{!showArchived && <button type="button" className="icon-button danger thread-delete" disabled={activeThread || deleting || !!threadAction} title={activeThread ? t("codex.deleteActiveSession") : t("codex.deleteSession")} onClick={() => setCodexConfirmation({ type: "delete-thread", thread })}>{deleting ? <LoaderCircle className="spin" size={14} /> : <Trash2 size={14} />}</button>}</div></ContextMenu>;
        })}</div>}
      </section>;
    })}{activeThreadSource.loadError && <div className="snapshot-notice warning"><AlertTriangle size={15} />{activeThreadSource.loadError}</div>}{activeThreadSource.hasMore && <div className="history-loader"><button type="button" className="secondary-button small" disabled={activeThreadSource.loadingMore} onClick={() => void activeThreadSource.loadMore()}>{activeThreadSource.loadingMore ? <LoaderCircle className="spin" size={15} /> : <ArrowDownToLine size={15} />}{t(activeThreadSource.loadingMore ? "codex.loadingMoreSessions" : "codex.loadMoreSessions")}</button></div>}</>}</div></section><WorkspaceFilesPanel workspaceID={active?.workspace_id ?? null} taskID={active?.id ?? ""} taskStatus={active?.status ?? ""} realtime={realtime} notify={notify} writable={activeWorkspace?.management_mode === "managed"} activePath={preview?.path ?? ""} activeMode={preview?.mode ?? "file"} onOpenFile={openFile} /></aside>
    <section className={`session-area ${mobileView === "conversation" ? "mobile-active" : "mobile-hidden"}`}>{preview && <div className="session-pane-tabs" role="tablist" aria-label={t("codex.sessionViews")}><button type="button" role="tab" aria-selected={activePane === "conversation"} className={activePane === "conversation" ? "active" : ""} onClick={() => setActivePane("conversation")}><MessageSquare size={15} />{t("codex.conversation")}</button><button type="button" role="tab" aria-selected={activePane === "preview"} className={activePane === "preview" ? "active" : ""} onClick={() => setActivePane("preview")}>{preview.mode === "diff" ? <FileDiff size={15} /> : <FileCode2 size={15} />}{t(preview.mode === "diff" ? "codex.fileReview" : "codex.filePreview")}</button></div>}<div className={`session-panes ${preview ? `has-preview ${activePane}-active` : ""}`}><section className="session-panel">{activeThreadSource.error && !activeThreadSource.data ? <ErrorState error={activeThreadSource.error} reload={activeThreadSource.reload} /> : active ? <SessionView key={active.id} thread={active} approvals={approvals.filter(item => item.thread_id === active.id)} realtime={streamRevisions[active.id]?.revision ?? 0} globalStreamRevision={streamRevisions["*"]?.revision ?? 0} streamRevision={streamRevisions[active.id]?.revision ?? 0} invalidationSequence={streamRevisions[active.id]?.minimumSequence} reloadApprovals={reloadApprovals} notify={notify} onOpenFile={openFile} onNewTask={() => setCreateOpen(true)} /> : <Empty icon={<SquareTerminal size={28} />} text={t("codex.selectWorkspace")} />}</section>{active && preview && (preview.mode === "diff" ? <FileDiffPane workspaceID={active.workspace_id} selection={preview} realtime={realtime} writable={activeWorkspace?.management_mode === "managed"} notify={notify} onClose={() => { setPreview(null); setActivePane("conversation"); }} /> : <FilePreviewPane workspaceID={active.workspace_id} selection={preview} realtime={realtime} onClose={() => { setPreview(null); setActivePane("conversation"); }} />)}</div></section>
    <button className={`approval-drawer-button ${approvals.length ? "visible" : ""}`} onClick={() => setApprovalOpen(true)}><ShieldCheck size={17} />{t("codex.approvalCount", { count: approvals.length })}</button>
    <Dialog open={createOpen} title={t("codex.newSession")} onClose={() => setCreateOpen(false)}><CreateThread workspaces={workspaces.data ?? []} onCreated={thread => { setPendingThread(thread); selectThread(thread.id); setCreateOpen(false); threads.reload(); notify(t("codex.sessionCreated")); }} /></Dialog>
    <Dialog open={renameTarget !== null} title={t(renameTarget?.kind === "project" ? "codex.renameProject" : "codex.renameThread")} onClose={() => { if (!renameBusy) setRenameTarget(null); }}><form onSubmit={submitRename}><Field label={t(renameTarget?.kind === "project" ? "project.name" : "codex.threadName")}><input autoFocus maxLength={180} value={renameValue} onChange={event => setRenameValue(event.target.value)} required /></Field><DialogActions><button type="button" className="secondary-button" disabled={renameBusy} onClick={() => setRenameTarget(null)}>{t("common.cancel")}</button><button className="primary-button" disabled={renameBusy || !renameValue.trim()}>{renameBusy ? <LoaderCircle className="spin" size={16} /> : <Check size={16} />}{t("common.save")}</button></DialogActions></form></Dialog>
    <Dialog open={worktreeTarget !== null} title={t(worktreeTarget?.kind === "thread" ? "codex.continueInNewWorktree" : "codex.createPermanentWorktree")} onClose={closeWorktreeDialog}><form onSubmit={submitWorktree}>{worktreeError && <ErrorBanner text={worktreeError} />}<p className="security-notice">{t(worktreeTarget?.kind === "thread" ? "codex.continueWorktreeDescription" : "codex.createWorktreeDescription")}</p>{worktreeTarget?.kind === "project" && <Field label={t("codex.sourceWorkspace")}><select value={worktreeForm.workspace_id} disabled={worktreeBusy} onChange={event => setWorktreeForm({ ...worktreeForm, workspace_id: event.target.value })} required><option value="">{t("codex.selectWorkspaceOption")}</option>{(workspaces.data ?? []).filter(workspace => workspace.project_id === worktreeTarget.projectID).map(workspace => <option key={workspace.id} value={workspace.id}>{workspace.server_name} · {workspace.branch || t("project.detached")} · {workspace.path}</option>)}</select></Field>}<div className="form-grid"><Field label={t("codex.worktreeBranch")}><input autoFocus value={worktreeForm.branch} disabled={worktreeBusy} onChange={event => setWorktreeForm({ ...worktreeForm, branch: event.target.value })} placeholder="feature/my-change" required /></Field><Field label={t("codex.worktreeBaseRef")}><input value={worktreeForm.base_ref} disabled={worktreeBusy} onChange={event => setWorktreeForm({ ...worktreeForm, base_ref: event.target.value })} placeholder="HEAD" required /></Field></div><Field label={t("codex.worktreePath")}><input value={worktreeForm.path} disabled={worktreeBusy} onChange={event => setWorktreeForm({ ...worktreeForm, path: event.target.value })} placeholder={t("codex.worktreePathPlaceholder")} /></Field><DialogActions><button type="button" className="secondary-button" disabled={worktreeBusy} onClick={closeWorktreeDialog}>{t("common.cancel")}</button><button className="primary-button" disabled={worktreeBusy || !worktreeForm.workspace_id || !worktreeForm.branch.trim()}>{worktreeBusy ? <LoaderCircle className="spin" size={16} /> : worktreeTarget?.kind === "thread" ? <GitFork size={16} /> : <GitBranch size={16} />}{t(worktreeBusy ? "codex.creatingWorktree" : worktreeTarget?.kind === "thread" ? "codex.continueInNewWorktree" : "codex.createPermanentWorktree")}</button></DialogActions></form></Dialog>
    <ScheduledTaskDialog open={scheduledThread !== null} initialThreadID={scheduledThread?.id} lockThread threads={scheduledThread ? [scheduledThread] : []} notify={notify} onClose={() => setScheduledThread(null)} onSaved={() => undefined} />
    <Dialog open={approvalOpen} title={t("codex.pendingApprovals")} onClose={() => setApprovalOpen(false)} wide><div className="approval-list">{approvals.length === 0 ? <Empty icon={<ShieldCheck size={24} />} text={t("codex.noApprovals")} /> : approvals.map(item => <div className="approval-item" key={item.id}><div className="approval-meta"><Status value="pending" /><span>{item.title}</span><time>{relative(item.expires_at)}</time></div><strong>{readableKind(item.kind)}</strong><pre>{approvalDetail(item.detail)}</pre><ApprovalActions item={item} onDecided={reloadApprovals} notify={notify} /></div>)}</div></Dialog>
    {codexConfirmation?.type === "archive-project" ? <ConfirmDialog open danger title={t("codex.archiveProjectThreads")} impact={t("codex.confirmArchiveProject", { name: codexConfirmation.group.projectName })} confirmLabel={t("codex.archiveProjectThreads")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={threadAction !== ""} onClose={() => setCodexConfirmation(null)} onConfirm={() => archiveProjectThreads(codexConfirmation.group)} /> : codexConfirmation?.type === "hide-project" ? <ConfirmDialog open danger title={t("codex.hideProject")} impact={t("codex.confirmHideProject", { name: codexConfirmation.group.projectName })} confirmLabel={t("codex.hideProject")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={threadAction !== ""} onClose={() => setCodexConfirmation(null)} onConfirm={() => hideProject(codexConfirmation.group)} /> : codexConfirmation?.type === "delete-thread" ? <ConfirmDialog open danger title={t("codex.deleteSession")} impact={t("codex.confirmDeleteSession", { title: codexConfirmation.thread.title })} confirmLabel={t("codex.deleteSession")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={deletingThread !== ""} onClose={() => setCodexConfirmation(null)} onConfirm={() => deleteSession(codexConfirmation.thread)} /> : null}
  </div>;
}
