import { ScheduledTaskDialog } from "./ScheduledTaskDialog";
import { useState } from "react";
import { Archive, CalendarClock, Copy, ExternalLink, EyeOff, FolderOpen, GitBranch, GitFork, Link, LoaderCircle, Pencil, Pin, PinOff } from "lucide-react";
import { api, patch, post } from "../api";
import { Dialog, Field, message, locationFor } from "../App";
import { DialogActions } from "../components/Dialog";
import { ConfirmDialog } from "../components/ConfirmDialog";
import type { ContextMenuAction } from "../ContextMenu";
import type { GeminiThread, Operation, Workspace } from "../types";
import { useI18n } from "../i18n";

type Props = { workspaces: Workspace[]; busy: boolean; setBusy: (busy: boolean) => void; refresh: () => void; select: (id: string) => void; notify: (text: string) => void };
export function useGeminiManagement({ workspaces, busy, setBusy, refresh, select, notify }: Props) {
 const { t } = useI18n();
 const [scheduled,setScheduled]=useState<GeminiThread|null>(null);
 const [rename, setRename] = useState<{ id: string; name: string; serverID: string } | null>(null);
 const [name, setName] = useState("");
 const [confirmation, setConfirmation] = useState<{ kind: "hide" | "archive"; thread: GeminiThread } | null>(null);
 const [worktree, setWorktree] = useState<{ workspaceID: string; thread?: GeminiThread } | null>(null);
 const [form, setForm] = useState({ branch: "", path: "", base_ref: "HEAD" });
 const [error, setError] = useState("");
 const action = async (fn: () => Promise<unknown>) => {
  if (busy) return; setBusy(true);
  try { await fn(); refresh(); } catch (e) { notify(message(e)); } finally { setBusy(false); }
 };
 const copy = (value: string, label: string) => { void navigator.clipboard.writeText(value).then(() => notify(t(label))).catch(e => notify(message(e))); };
 const link = (thread: GeminiThread) => `${window.location.origin}${locationFor("gemini", thread.id)}`;
 const wait = async (operationID: string) => {
  for (let i = 0; i < 180; i++) {
   const op = await api<Operation>(`/operations/${operationID}`);
   if (op.status === "succeeded") return;
   if (["failed", "cancelled"].includes(op.status)) throw new Error(op.result || t("gemini.actionFailed"));
   await new Promise(resolve => setTimeout(resolve, 1000));
  }
  throw new Error(t("codex.worktreeTimeout"));
 };
 const fork = (thread: GeminiThread, eventID?: string) => action(async () => {
  const result = await post<{ operation_id: string; target_thread_id: string }>(`/gemini/threads/${thread.id}/fork`, eventID ? { event_id: eventID } : {});
  await wait(result.operation_id); select(result.target_thread_id); notify(t("codex.continueInNewTask"));
 });
 const openWorktree = (workspaceID: string, thread?: GeminiThread) => { setError(""); setForm({ branch: "", path: "", base_ref: "HEAD" }); setWorktree({ workspaceID, thread }); };
 const projectActions = (thread: GeminiThread): ContextMenuAction[] => [
  { id: "pin", label: t(thread.project_pinned_at ? "codex.unpinProject" : "codex.pinProject"), icon: thread.project_pinned_at ? PinOff : Pin, disabled: busy, onSelect: () => action(() => patch(`/projects/${thread.project_id}`, { pinned: !thread.project_pinned_at, server_id: thread.server_id })) },
  { id: "rename", label: t("codex.renameProject"), icon: Pencil, disabled: busy, onSelect: () => { setRename({ id: thread.project_id, name: thread.project_name, serverID: thread.server_id }); setName(thread.project_name); } },
  { id: "worktree", label: t("codex.createPermanentWorktree"), icon: GitBranch, disabled: busy || thread.server_status !== "online", onSelect: () => openWorktree(thread.workspace_id) },
  { id: "archive-all", label: t("codex.archiveProjectThreads"), icon: Archive, disabled: busy, danger: true, separatorBefore: true, onSelect: () => setConfirmation({ kind: "archive", thread }) },
  { id: "hide", label: t("codex.hideProject"), icon: EyeOff, disabled: busy, danger: true, separatorBefore: true, onSelect: () => setConfirmation({ kind: "hide", thread }) }
 ];
 const threadActions = (thread: GeminiThread): ContextMenuAction[] => [
  ...(!thread.archived_at ? [
   { id: "pin", label: t(thread.pinned_at ? "codex.unpinThread" : "codex.pinThread"), icon: thread.pinned_at ? PinOff : Pin, disabled: busy, onSelect: () => action(() => patch(`/gemini/threads/${thread.id}`, { pinned: !thread.pinned_at })) },
   { id: "schedule", label:t("codex.createScheduledTask"),icon:CalendarClock,disabled:busy,onSelect:()=>setScheduled(thread) },
   { id: "fork", label: t("codex.continueInNewTask"), icon: GitFork, disabled: busy || thread.recovery_pending || !thread.fork_supported || !thread.session_id || ["queued", "running", "interrupting"].includes(thread.status) || thread.server_status !== "online", onSelect: () => fork(thread) },
   { id: "fork-worktree", label: t("codex.continueInNewWorktree"), icon: GitBranch, disabled: busy || thread.recovery_pending || !thread.fork_supported || !thread.session_id || ["queued", "running", "interrupting"].includes(thread.status) || thread.server_status !== "online", onSelect: () => openWorktree(thread.workspace_id, thread) }
  ] satisfies ContextMenuAction[] : []),
  { id: "copy-path", label: t("codex.copyWorkingDirectory"), icon: FolderOpen, separatorBefore: true, onSelect: () => copy(thread.path, "codex.workingDirectoryCopied") },
  { id: "copy-wio-id", label: t("codex.copyWioSessionID"), icon: Copy, onSelect: () => copy(thread.id, "codex.wioSessionIDCopied") },
  { id: "copy-gemini-id", label: t("gemini.copySessionID"), icon: Copy, disabled: !thread.session_id, onSelect: () => copy(thread.session_id, "gemini.sessionIDCopied") },
  { id: "copy-link", label: t("codex.copyDeepLink"), icon: Link, onSelect: () => copy(link(thread), "codex.deepLinkCopied") },
  { id: "new-window", label: t("codex.openNewWindow"), icon: ExternalLink, separatorBefore: true, onSelect: () => { window.open(link(thread), "_blank", "noopener,noreferrer"); } }
 ];
 const dialogs = <>
  <ScheduledTaskDialog open={Boolean(scheduled)} initialThreadID={scheduled?.id} agentKind="gemini" lockThread threads={[]} geminiThreads={scheduled?[scheduled]:[]} notify={notify} onClose={()=>setScheduled(null)} onSaved={refresh} />
  <Dialog open={Boolean(rename)} title={t("codex.renameProject")} onClose={() => { if (!busy) setRename(null); }}><form onSubmit={e => { e.preventDefault(); if (rename) void action(async () => { await patch(`/projects/${rename.id}`, { name, server_id: rename.serverID }); setRename(null); }); }}><Field label={t("settings.name")}><input autoFocus required maxLength={180} value={name} disabled={busy} onChange={e => setName(e.target.value)} /></Field><DialogActions><button className="primary-button" disabled={busy || !name.trim()}>{t("common.save")}</button></DialogActions></form></Dialog>
  <ConfirmDialog open={Boolean(confirmation)} danger title={t(confirmation?.kind === "hide" ? "codex.hideProject" : "codex.archiveProjectThreads")} impact={t(confirmation?.kind === "hide" ? "codex.confirmHideProject" : "codex.confirmArchiveProject", { name: confirmation?.thread.project_name ?? "" })} confirmLabel={t(confirmation?.kind === "hide" ? "codex.hideProject" : "codex.archiveProjectThreads")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => { if (!busy) setConfirmation(null); }} onConfirm={() => action(async () => { if (!confirmation) return; if (confirmation.kind === "hide") await patch(`/projects/${confirmation.thread.project_id}`, { hidden: true, server_id: confirmation.thread.server_id }); else await post(`/projects/${confirmation.thread.project_id}/gemini/threads/archive?server_id=${encodeURIComponent(confirmation.thread.server_id)}`, {}); setConfirmation(null); select(""); })} />
  <Dialog open={Boolean(worktree)} wide title={t(worktree?.thread ? "codex.continueInNewWorktree" : "codex.createPermanentWorktree")} onClose={() => { if (!busy) setWorktree(null); }}><form onSubmit={e => { e.preventDefault(); if (!worktree || busy) return; setBusy(true); setError(""); void (async () => { try { const result = await post<{ operation_id: string; target_thread_id?: string }>(worktree.thread ? `/gemini/threads/${worktree.thread.id}/fork-worktree` : `/workspaces/${worktree.workspaceID}/worktrees`, form); await wait(result.operation_id); setWorktree(null); refresh(); if (result.target_thread_id) select(result.target_thread_id); notify(t("codex.worktreeCreated")); } catch (e) { setError(message(e)); } finally { setBusy(false); } })(); }}>
   <p className="muted">{workspaces.find(w => w.id === worktree?.workspaceID)?.path}</p>
   <Field label={t("codex.worktreeBranch")}><input required disabled={busy} value={form.branch} onChange={e => setForm({ ...form, branch: e.target.value })} /></Field>
   <Field label={t("codex.worktreeBaseRef")}><input required disabled={busy} value={form.base_ref} onChange={e => setForm({ ...form, base_ref: e.target.value })} /></Field>
   <Field label={t("codex.worktreePath")}><input disabled={busy} placeholder={t("codex.worktreePathPlaceholder")} value={form.path} onChange={e => setForm({ ...form, path: e.target.value })} /></Field>
   <p className="muted">{t(worktree?.thread ? "codex.continueWorktreeDescription" : "codex.createWorktreeDescription")}</p>{error && <p role="alert">{error}</p>}<DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={() => setWorktree(null)}>{t("common.cancel")}</button><button className="primary-button" disabled={busy || !form.branch.trim()}>{busy && <LoaderCircle className="spin" size={16} />}{t("common.save")}</button></DialogActions>
  </form></Dialog>
 </>;
 return { projectActions, threadActions, fork, schedule: setScheduled, dialogs };
}
