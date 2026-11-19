import { useEffect, useState } from "react";
import { Network, ExternalLink, Trash2 } from "lucide-react";
import { post, remove } from "../api";
import { SubagentDetails, type NativeSubagent } from "./SubagentDetails";
import { useData, useThrottledValue } from "../useData";
import { useI18n } from "../i18n";
import { locationFor } from "../navigation";
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
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState<RelatedSession | null>(null);
  const path = `/sessions/${engine}/${encodeURIComponent(id)}/collaboration`;
  const dependency = useThrottledValue(revision, 500);
  const state = useData<State>(path, dependency);
  const nativePath = `/sessions/${engine}/${encodeURIComponent(id)}/subagents`;
  const native = useData<{ items: NativeSubagent[] }>(nativePath, dependency);
  const agents = native.data?.items ?? [];
  const activeAgent = agents.find(agent => agent.id === selected);
  const visibleRecords = agents.length + (state.data?.links ?? []).filter(link => !link.missing && !link.session?.archived_at).length + (state.data?.requests ?? []).filter(request => pendingRequestStatuses.has(request.status)).length + (state.data?.grant?.show_unread_results ? state.data.notifications?.length ?? 0 : 0);
  useEffect(() => { if (visibleRecords > 0) setExpanded(true); }, [visibleRecords]);
  if (!state.data && !state.error && agents.length === 0 && !native.error) return null;
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
  const toolLabel: Record<string, string> = { wio_create_session: t("collaboration.createSession"), wio_fork_session: t("collaboration.continueSession"), wio_send_message: t("collaboration.sendTask"), wio_steer_session: t("collaboration.addInstruction"), wio_interrupt_session: t("collaboration.interruptTask"), wio_archive_session: t("collaboration.archiveSession"), wio_restore_session: t("collaboration.restoreSession"), wio_update_session: t("collaboration.updateSession") };
  const children = links.filter(link => link.target_key !== `${engine}:${id}`);
  const running = agents.filter(agent => activeStatuses.has(agent.status || "")).length + children.filter(link => activeStatuses.has(link.session.status) && !link.session.waiting_user && !link.session.recovery_pending).length;
  const count = agents.length + children.length;
  return <section className="session-collaboration" aria-label={t(count ? "subagents.title" : "collaboration.title")}>
    <div className="collaboration-heading">
      <button type="button" className="collaboration-toggle" aria-expanded={expanded} onClick={() => setExpanded(value => !value)}><Network size={15} />{t(count ? "subagents.title" : "collaboration.title")}{count > 0 && <span>{count}</span>}</button>
      {running > 0 && <span role="status">{t("subagents.running", { count: running })}</span>}
      {notifications.length > 0 && <span role="status">{notifications.length} {t("collaboration.unreadResults")}</span>}

    </div>
    {expanded && <div className="collaboration-content">
      {notifications.map(notification => <button type="button" className="secondary-button collaboration-read" key={notification.target_key} onClick={() => { void post(path + "/read", notification).then(() => state.reload()).catch(failure => setError(String(failure))); }}>{t("collaboration.markRead")} · {state.data?.links?.find(link => link.target_key === notification.target_key)?.session?.title ?? notification.target_key}</button>)}
      {error && <p role="alert">{error}</p>}
      {state.error && <p role="alert">{state.error}<button type="button" onClick={state.reload}>{t("common.retry")}</button></p>}
      {native.error && <p role="alert">{native.error}<button type="button" onClick={native.reload}>{t("common.retry")}</button></p>}
      {agents.map(agent => <div className="collaboration-card native-subagent-card" key={agent.id}>
        <button type="button" className="subagent-open" onClick={() => setSelected(agent.id)}><strong>{agent.name || t("subagents.agent")}</strong><small>{agent.prompt || agent.native_id || agent.id}</small></button>
        <Status value={agent.status || "unknown"} /><small>{engine}{agent.parent_id && agents.some(parent => parent.id === agent.parent_id) && ` · ${t("subagents.child")}`}</small><time dateTime={agent.updated_at} title={new Date(agent.updated_at).toLocaleString()}>{new Date(agent.updated_at).toLocaleTimeString()}</time>
      </div>)}
      {agents.length === 0 && state.data && !state.data.supported && <p>{t("collaboration.updateAgent")}</p>}
      {agents.length === 0 && !state.data?.grant?.enabled && <p>{t("collaboration.enableAll")}</p>}
      {links.map(link => { const child = link.target_key !== `${engine}:${id}`; const running = activeStatuses.has(link.session.status); return <div className="collaboration-card" key={`${link.source_key}:${link.target_key}`}>
        {child && <button type="button" className="icon-button danger" aria-label={`${t("collaboration.deleteChild")} ${link.session.title}`} title={running ? t("collaboration.stopBeforeDelete") : t("collaboration.deleteChild")} disabled={busy || running} onClick={() => setDeleting(link.session)}><Trash2 size={14} /></button>}
        <span>{child ? t("collaboration.child") : t("collaboration.source")}</span>
        <><a href={link.session.url}>{link.session.title} · {link.session.engine}<ExternalLink size={12} /></a><Status value={link.session.recovery_pending ? "unknown" : link.session.waiting_user ? "waiting_user" : link.session.status} /><small>{link.session.workspace_id === workspaceID ? t("collaboration.sharedDirectory") : t("collaboration.separateWorkspace")}</small></>
      </div>; })}
      {requests.map(request => <div className="collaboration-card" key={request.id}><span>{toolLabel[request.tool] ?? request.tool}</span><Status value={request.status} />{request.error && <small role="alert">{request.error.code} · {request.error.message}</small>}{request.phase === "await_source" && <small>{t("collaboration.waitingForSource")}</small>}{request.target_id && <a href={locationFor("sessions", request.target_id, request.target_engine)}>{t("collaboration.openSession")}</a>}<small title={request.operation_id}>{request.request_id}</small></div>)}
    </div>}
    {activeAgent && <SubagentDetails key={activeAgent.id} agent={activeAgent} path={nativePath} revision={dependency} onClose={() => setSelected(null)} />}
    <ConfirmDialog open={Boolean(deleting)} danger title={t("collaboration.deleteChild")} description={deleting ? `${deleting.title} · ${deleting.engine}` : undefined} impact={t("collaboration.deleteImpact")} confirmLabel={t("common.delete")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={busy} onClose={() => setDeleting(null)} onConfirm={deleteSession} />
  </section>;
}
