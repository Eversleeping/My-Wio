import { useState } from "react";
import { Network, ExternalLink, Trash2 } from "lucide-react";
import { post, remove } from "../api";
import { useData } from "../useData";
import { useI18n } from "../i18n";
import { ConfirmDialog } from "./ConfirmDialog";
import { Status } from "./PageUI";

type Engine = "codex" | "claude" | "gemini";
type Ref = { engine: Engine; id: string };
type Grant = { enabled: boolean; workspace_ids: string[] | null; session_refs: Ref[] | null; all_project_sessions?: boolean; worktree: boolean; auto_continue: boolean; delegate: boolean; target_modes: Partial<Record<Engine, string>> | null; show_unread_results?: boolean; show_creation_results?: boolean };
type RelatedSession = Ref & { title: string; status: string; workspace_id: string; url: string; operation_id: string; recovery_pending: boolean; waiting_user?: boolean; archived_at?: string | null };
type Request = { id: string; request_id: string; tool: string; status: string; phase: string; target_engine: Engine; target_id: string; operation_id: string; error?: { code: string; message: string } };
type Link = { source_key: string; target_key: string; relation: string; session: RelatedSession; missing: boolean };
type State = { grant: Grant; supported: boolean; notifications?: { target_key: string; event_id: string }[]; links: Link[]; requests: Request[] };

// Each engine keeps its own delete endpoint and refuses sessions that are still running.
const deletePath: Record<Engine, string> = { codex: "/sessions/codex/", claude: "/sessions/claude/", gemini: "/sessions/gemini/" };
const activeStatuses = new Set(["queued", "running", "interrupting"]);
// Any other request status is a result: succeeded, failed, cancelled or unknown.
const pendingRequestStatuses = new Set(["accepted", "preparing", "queued", "running", "waiting_user"]);

export function SessionCollaboration({ engine, id, workspaceID, revision }: { engine: Engine; id: string; workspaceID: string; projectID: string; revision: unknown }) {
  const { language, t } = useI18n();
  const text = (zh: string, en: string) => language === "zh-CN" ? zh : en;
  const [expanded, setExpanded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState<RelatedSession | null>(null);
  const path = `/sessions/${engine}/${encodeURIComponent(id)}/collaboration`;
  const state = useData<State>(path, revision);
  if (!state.data && !state.error) return null;
  // Send-task receipts only repeat what the child session already shows.
  const requests = (state.data?.requests ?? []).filter(request => {
    if (request.tool === "wio_send_message") return false;
    const creationResult = (request.tool === "wio_create_session" || request.tool === "wio_fork_session") && !pendingRequestStatuses.has(request.status);
    return !creationResult || state.data?.grant?.show_creation_results;
  }).slice(0, 10);
  const notifications = state.data?.grant?.show_unread_results ? state.data.notifications ?? [] : [];
  // Deleted and archived sessions no longer occupy the panel.
  const links = (state.data?.links ?? []).filter(link => !link.missing && !link.session?.archived_at);
  const deleteSession = async () => {
    if (!deleting) return;
    setBusy(true); setError("");
    try { await remove(deletePath[deleting.engine] + encodeURIComponent(deleting.id)); setDeleting(null); state.reload(); }
    catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); setDeleting(null); }
    finally { setBusy(false); }
  };
  const toolLabel: Record<string, string> = { wio_create_session: text("创建会话", "Create session"), wio_fork_session: text("继续会话", "Continue session"), wio_send_message: text("发送任务", "Send task"), wio_steer_session: text("补充指令", "Add instruction"), wio_interrupt_session: text("中断任务", "Interrupt task"), wio_archive_session: text("归档会话", "Archive session"), wio_restore_session: text("恢复会话", "Restore session"), wio_update_session: text("更新会话", "Update session") };
  return <section className="session-collaboration" aria-label={text("会话协作", "Session collaboration")}>
    <div className="collaboration-heading">
      <button type="button" className="collaboration-toggle" aria-expanded={expanded} onClick={() => setExpanded(value => !value)}><Network size={15} />{text("会话协作", "Session collaboration")}{links.length > 0 && <span>{links.length}</span>}</button>
      {notifications.length > 0 && <span role="status">{notifications.length} {text("条未读结果", "unread results")}</span>}

    </div>
    {expanded && <div className="collaboration-content">
      {notifications.map(notification => <button type="button" className="secondary-button collaboration-read" key={notification.target_key} onClick={() => { void post(path + "/read", notification).then(() => state.reload()).catch(failure => setError(String(failure))); }}>{text("标为已读", "Mark as read")} · {state.data?.links?.find(link => link.target_key === notification.target_key)?.session?.title ?? notification.target_key}</button>)}
      {error && <p role="alert">{error}</p>}
      {state.error && <p role="alert">{state.error}<button type="button" onClick={state.reload}>{t("common.retry")}</button></p>}
      {state.data && !state.data.supported && <p>{text("请升级此服务器的 Agent 后使用协作工具。", "Update this server's Agent to use collaboration tools.")}</p>}
      {!state.data?.grant?.enabled && <p>{text("在全局设置页开启会话协作，对所有会话生效。", "Enable collaboration for all sessions on the global Settings page.")}</p>}
      {links.map(link => { const child = link.target_key !== `${engine}:${id}`; const running = activeStatuses.has(link.session.status); return <div className="collaboration-card" key={`${link.source_key}:${link.target_key}`}>
        {child && <button type="button" className="icon-button danger" aria-label={`${text("删除子会话", "Delete child session")} ${link.session.title}`} title={running ? text("运行中的会话需先停止才能删除", "Stop the running session before deleting it") : text("删除子会话", "Delete child session")} disabled={busy || running} onClick={() => setDeleting(link.session)}><Trash2 size={14} /></button>}
        <span>{child ? text("子会话", "Child session") : text("来源会话", "Source session")}</span>
        <><a href={link.session.url}>{link.session.title} · {link.session.engine}<ExternalLink size={12} /></a><Status value={link.session.recovery_pending ? "unknown" : link.session.waiting_user ? "waiting_user" : link.session.status} /><small>{link.session.workspace_id === workspaceID ? text("共享工作目录", "Shared working directory") : text("独立工作区", "Separate workspace")}</small></>
      </div>; })}
      {requests.map(request => <div className="collaboration-card" key={request.id}><span>{toolLabel[request.tool] ?? request.tool}</span><Status value={request.status} />{request.error && <small role="alert">{request.error.code} · {request.error.message}</small>}{request.phase === "await_source" && <small>{text("等待来源会话本轮结束", "Waiting for the source turn to end")}</small>}{request.target_id && <a href={`/?view=${request.target_engine}&thread=${encodeURIComponent(request.target_id)}`}>{text("打开会话", "Open session")}</a>}<small title={request.operation_id}>{request.request_id}</small></div>)}
    </div>}
    <ConfirmDialog open={Boolean(deleting)} danger title={text("删除子会话", "Delete child session")} description={deleting ? `${deleting.title} · ${deleting.engine}` : undefined} impact={text("将永久删除该会话及其消息记录，无法恢复。来源记录会保留。", "This permanently deletes the session and its messages. Provenance is retained.")} confirmLabel={t("common.delete")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setDeleting(null)} onConfirm={deleteSession} />
  </section>;
}
