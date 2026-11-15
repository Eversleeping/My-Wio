import { Suspense, useState } from "react";
import { Archive, ArchiveRestore, CalendarClock, Copy, ExternalLink, EyeOff, FolderOpen, GitBranch, GitFork, Link, Pencil, Pin, PinOff, Plus } from "lucide-react";
import type { ContextMenuAction } from "../ContextMenu";
import { api, patch, post, remove } from "../api";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DialogActions } from "../components/Dialog";
import { Dialog, ErrorBanner, Field } from "../components/FormUI";
import { PageLoading } from "../components/PageUI";
import { waitForBackoff, waitForResult } from "../data/waitFor";
import { useI18n } from "../i18n";
import { locationFor } from "../navigation";
import { copyText, message } from "../text";
import type { Operation, Workspace } from "../types";
import { sessionAPI, sessionPath } from "./api";
import { engineModule, engines } from "./engineModules";
import type { SessionGroup } from "./groups";
import { sessionKey, type AISession, type SessionRef } from "./types";

export function sessionBusy(thread: AISession) { return ["queued", "running", "interrupting"].includes(thread.status) || Boolean("recovery_pending" in thread && thread.recovery_pending); }

type ProjectContext = Pick<Workspace, "project_id" | "server_id" | "project_name" | "id"> & { pinned_at?: string | null };
export function useUnifiedManagement({ workspaces, online, refresh, select, notify, onNew }: {
  workspaces: Workspace[]; online: Set<string>; refresh: () => void; select: (ref: SessionRef | null) => void;
  notify: (text: string) => void; onNew: (workspaceID?: string) => void;
}) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [rename, setRename] = useState<{ thread?: AISession; project?: ProjectContext; name: string } | null>(null);
  const [confirmation, setConfirmation] = useState<{ kind: "hide" | "archive"; project: ProjectContext } | { kind: "delete"; thread: AISession } | null>(null);
  const [worktree, setWorktree] = useState<{ workspaceID: string; thread?: AISession } | null>(null);
  const [form, setForm] = useState({ branch: "", path: "", base_ref: "HEAD" });
  const [error, setError] = useState("");
  const [scheduled, setScheduled] = useState<AISession | null>(null);
  const action = async (fn: () => Promise<unknown>) => {
    if (busy) return; setBusy(true);
    try { await fn(); } catch (error) { notify(message(error)); } finally { refresh(); setBusy(false); }
  };
  const wait = async (id: string) => waitForResult(async () => {
    const operation = await api<Operation>(`/operations/${encodeURIComponent(id)}`);
    if (operation.status === "succeeded") return true;
    if (["failed", "cancelled"].includes(operation.status)) throw new Error(operation.result || t("gemini.actionFailed"));
  }, { attempts: 180, pause: attempt => waitForBackoff(attempt, 1000, 1000), onTimeout: () => { throw new Error(t("codex.worktreeTimeout")); } });
  const selectFork = async (source: AISession, targetID: string) => {
    const module = await engineModule(source.engine).load(); module.afterFork?.(source.id, targetID);
    select({ engine: source.engine, id: targetID });
  };
  const fork = (thread: AISession, eventID?: string) => action(async () => {
    const result = await sessionAPI.action<{ operation_id: string; target_thread_id: string }>(thread.engine, thread.id, "fork", eventID ? { event_id: eventID } : {});
    await wait(result.operation_id); await selectFork(thread, result.target_thread_id);
  });
  const archive = (thread: AISession) => action(async () => { await sessionAPI.update(thread.engine, thread.id, { archived: !thread.archived_at }); select(null); });
  const openWorktree = (workspaceID: string, thread?: AISession) => { setError(""); setForm({ branch: "", path: "", base_ref: "HEAD" }); setWorktree({ workspaceID, thread }); };
  const copy = (value: string, label: string) => { void copyText(value).then(() => notify(t(label))).catch(error => notify(message(error))); };
  const link = (thread: AISession) => `${window.location.origin}${locationFor("sessions", thread.id, thread.engine)}`;
  const threadActions = (thread: AISession): ContextMenuAction[] => [
    ...(!thread.archived_at ? [
      { id: "pin", label: t(thread.pinned_at ? "codex.unpinThread" : "codex.pinThread"), icon: thread.pinned_at ? PinOff : Pin, disabled: busy, onSelect: () => action(() => sessionAPI.update(thread.engine, thread.id, { pinned: !thread.pinned_at })) },
    ] satisfies ContextMenuAction[] : []),
    { id: "rename", label: t("gemini.rename"), icon: Pencil, disabled: busy || sessionBusy(thread), onSelect: () => setRename({ thread, name: thread.title }) },
    ...(!thread.archived_at ? [
      { id: "schedule", label: t("codex.createScheduledTask"), icon: CalendarClock, disabled: busy, onSelect: () => setScheduled(thread) },
      { id: "fork", label: t("codex.continueInNewTask"), icon: GitFork, disabled: busy || sessionBusy(thread) || !thread.native_id || !thread.capabilities?.fork || thread.server_status !== "online", onSelect: () => fork(thread) },
      { id: "fork-worktree", label: t("codex.continueInNewWorktree"), icon: GitBranch, disabled: busy || sessionBusy(thread) || !thread.native_id || !thread.capabilities?.fork_worktree || thread.server_status !== "online", onSelect: () => openWorktree(thread.workspace_id, thread) },
    ] satisfies ContextMenuAction[] : []),
    { id: "archive", label: t(thread.archived_at ? "gemini.restore" : "gemini.archive"), icon: thread.archived_at ? ArchiveRestore : Archive, disabled: busy || sessionBusy(thread), onSelect: () => archive(thread) },
    { id: "copy-path", label: t("codex.copyWorkingDirectory"), icon: FolderOpen, separatorBefore: true, onSelect: () => copy(thread.path, "codex.workingDirectoryCopied") },
    { id: "copy-wio-id", label: t("codex.copyWioSessionID"), icon: Copy, onSelect: () => copy(thread.id, "codex.wioSessionIDCopied") },
    { id: "copy-native-id", label: t("sessions.copyNativeID", { engine: engineModule(thread.engine).label }), icon: Copy, disabled: !thread.native_id, onSelect: () => copy(thread.native_id, "sessions.nativeIDCopied") },
    { id: "copy-link", label: t("codex.copyDeepLink"), icon: Link, onSelect: () => copy(link(thread), "codex.deepLinkCopied") },
    { id: "new-window", label: t("codex.openNewWindow"), icon: ExternalLink, onSelect: () => { window.open(link(thread), "_blank", "noopener,noreferrer"); } },
  ];
  const groupActions = (group: SessionGroup<AISession>, archived: boolean): ContextMenuAction[] => {
    const workspace = workspaces.find(w => w.id === group.id);
    const context = group.context;
    if (!workspace && !context) return [];
    const project: ProjectContext = { id: group.id, project_id: workspace?.project_id ?? context!.project_id, server_id: workspace?.server_id ?? context!.server_id, project_name: workspace?.project_display_name ?? context?.project_name ?? workspace!.project_name, pinned_at: group.pinnedAt };
    return [
      ...(!archived ? [{ id: "new-session", label: t("codex.createSessionHere"), icon: Plus, disabled: busy, onSelect: () => onNew(group.id) }] satisfies ContextMenuAction[] : []),
      { id: "pin", label: t(group.pinnedAt ? "codex.unpinProject" : "codex.pinProject"), icon: group.pinnedAt ? PinOff : Pin, disabled: busy, onSelect: () => action(() => patch(`/projects/${project.project_id}`, { server_id: project.server_id, pinned: !group.pinnedAt })) },
      { id: "rename", label: t("codex.renameProject"), icon: Pencil, disabled: busy, onSelect: () => setRename({ project, name: project.project_name }) },
      ...(!archived ? [
        { id: "worktree", label: t("codex.createPermanentWorktree"), icon: GitBranch, disabled: busy || !workspace || workspace.status !== "ready" || !online.has(project.server_id), onSelect: () => openWorktree(group.id) },
        { id: "archive", label: t("codex.archiveProjectThreads"), icon: Archive, disabled: busy, onSelect: () => setConfirmation({ kind: "archive", project }) },
      ] satisfies ContextMenuAction[] : []),
      { id: "hide", label: t("codex.hideProject"), icon: EyeOff, danger: true, disabled: busy, onSelect: () => setConfirmation({ kind: "hide", project }) },
    ];
  };
  const Schedule = scheduled ? engineModule(scheduled.engine).Schedule : null;
  const dialogs = <>
    <Dialog open={Boolean(rename)} title={t(rename?.project ? "codex.renameProject" : "gemini.rename")} onClose={() => { if (!busy) setRename(null); }}><form onSubmit={event => { event.preventDefault(); if (rename) void action(async () => { if (rename.thread) await sessionAPI.update(rename.thread.engine, rename.thread.id, { title: rename.name.trim() }); else await patch(`/projects/${rename.project!.project_id}`, { name: rename.name.trim(), server_id: rename.project!.server_id }); setRename(null); }); }}><Field label={t("settings.name")}><input required autoFocus maxLength={180} disabled={busy} value={rename?.name ?? ""} onChange={event => setRename(current => current && { ...current, name: event.target.value })} /></Field><DialogActions><button className="primary-button" disabled={busy || !rename?.name.trim()}>{t("common.save")}</button></DialogActions></form></Dialog>
    <ConfirmDialog open={Boolean(confirmation)} danger title={t(confirmation?.kind === "delete" ? "codex.deleteSession" : confirmation?.kind === "hide" ? "codex.hideProject" : "codex.archiveProjectThreads")} impact={confirmation?.kind === "delete" ? t("codex.confirmDeleteSession", { title: confirmation.thread.title }) : t(confirmation?.kind === "hide" ? "codex.confirmHideProject" : "codex.confirmArchiveProject", { name: confirmation?.project.project_name ?? "" })} confirmLabel={t(confirmation?.kind === "delete" ? "codex.deleteSession" : confirmation?.kind === "hide" ? "codex.hideProject" : "codex.archiveProjectThreads")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => { if (!busy) setConfirmation(null); }} onConfirm={() => action(async () => {
      if (!confirmation) return;
      if (confirmation.kind === "delete") { const thread = confirmation.thread; await remove(sessionPath(thread.engine, thread.id)); (await engineModule(thread.engine).load()).afterDelete?.(thread.id); }
      else if (confirmation.kind === "hide") await patch(`/projects/${confirmation.project.project_id}`, { hidden: true, server_id: confirmation.project.server_id });
      else for (const module of engines) await post(`${module.archiveProjectPath(confirmation.project.project_id)}?server_id=${encodeURIComponent(confirmation.project.server_id)}`, {});
      setConfirmation(null); select(null);
    })} />
    <Dialog open={Boolean(worktree)} title={t(worktree?.thread ? "codex.continueInNewWorktree" : "codex.createPermanentWorktree")} onClose={() => { if (!busy) setWorktree(null); }}><form onSubmit={event => { event.preventDefault(); if (!worktree || busy) return; setBusy(true); setError(""); void (async () => {
      try { const result = await post<{ operation_id: string; target_thread_id?: string }>(worktree.thread ? sessionPath(worktree.thread.engine, worktree.thread.id, "fork-worktree") : `/workspaces/${worktree.workspaceID}/worktrees`, form); await wait(result.operation_id); if (worktree.thread && result.target_thread_id) await selectFork(worktree.thread, result.target_thread_id); setWorktree(null); refresh(); }
      catch (error) { setError(message(error)); } finally { setBusy(false); }
    })(); }}>
      <Field label={t("codex.worktreeBranch")}><input required value={form.branch} disabled={busy} onChange={event => setForm({ ...form, branch: event.target.value })} /></Field>
      <Field label={t("codex.worktreeBaseRef")}><input required value={form.base_ref} disabled={busy} onChange={event => setForm({ ...form, base_ref: event.target.value })} /></Field>
      <Field label={t("codex.worktreePath")}><input value={form.path} disabled={busy} onChange={event => setForm({ ...form, path: event.target.value })} /></Field>
      {error && <ErrorBanner text={error} />}<DialogActions><button className="primary-button" disabled={busy || !form.branch.trim()}>{t("common.save")}</button></DialogActions>
    </form></Dialog>
    {scheduled && Schedule && <Suspense fallback={<PageLoading />}><Schedule thread={scheduled} notify={notify} onClose={() => setScheduled(null)} onSaved={refresh} /></Suspense>}
  </>;
  return { busy, groupActions, threadActions, fork, archive, schedule: setScheduled, delete: (thread: AISession) => setConfirmation({ kind: "delete", thread }), dialogs };
}
