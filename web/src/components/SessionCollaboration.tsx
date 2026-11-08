import { useEffect, useState } from "react";
import { Network, Settings2, ExternalLink } from "lucide-react";
import { api, put, post } from "../api";
import { useData } from "../useData";
import { useI18n } from "../i18n";
import { Dialog, DialogActions } from "./Dialog";
import { Status } from "./PageUI";
import type { Workspace } from "../types";

type Engine = "codex" | "claude" | "gemini";
type Ref = { engine: Engine; id: string };
type Grant = { enabled: boolean; workspace_ids: string[] | null; session_refs: Ref[] | null; worktree: boolean; auto_continue: boolean; delegate: boolean; target_modes: Partial<Record<Engine, string>> | null };
type RelatedSession = Ref & { title: string; status: string; workspace_id: string; url: string; operation_id: string; recovery_pending: boolean; waiting_user?: boolean; archived_at?: string | null };
type Request = { id: string; request_id: string; tool: string; status: string; phase: string; target_engine: Engine; target_id: string; operation_id: string; error?: { code: string; message: string } };
type State = { grant: Grant; supported: boolean; notifications?: { target_key: string; event_id: string }[]; links: { source_key: string; target_key: string; relation: string; session: RelatedSession; missing: boolean }[]; requests: Request[] };

const emptyGrant: Grant = { enabled: false, workspace_ids: [], session_refs: [], worktree: false, auto_continue: false, delegate: false, target_modes: {} };

export function SessionCollaboration({ engine, id, workspaceID, projectID, revision }: { engine: Engine; id: string; workspaceID: string; projectID: string; revision: unknown }) {
  const { language, t } = useI18n();
  const text = (zh: string, en: string) => language === "zh-CN" ? zh : en;
  const [tick, setTick] = useState(0);
  const [settings, setSettings] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [grant, setGrant] = useState<Grant>(emptyGrant);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const path = `/sessions/${engine}/${encodeURIComponent(id)}/collaboration`;
  const state = useData<State>(path, `${revision}:${tick}`);
  const workspaces = useData<Workspace[]>(settings ? "/workspaces" : null, settings);
  const [otherSessions, setOtherSessions] = useState<RelatedSession[]>([]);
  useEffect(() => {
    if (!state.data?.grant?.enabled && !state.data?.links?.length && !expanded) return;
    const timer = window.setInterval(() => { if (document.visibilityState !== "hidden") setTick(value => value + 1); }, 3000);
    return () => window.clearInterval(timer);
  }, [state.data?.grant?.enabled, state.data?.links?.length, expanded]);
  useEffect(() => {
    if (!settings) return;
    let cancelled = false;
    Promise.allSettled([api<RelatedSession[]>("/threads"), api<RelatedSession[]>("/claude/threads"), api<RelatedSession[]>("/gemini/threads")]).then(results => {
      if (cancelled) return;
      setOtherSessions(results.flatMap((result, index) => result.status === "fulfilled" && Array.isArray(result.value) ? result.value.filter(item => "project_id" in item && item.project_id === projectID && item.id !== id).map(item => ({ ...item, engine: (["codex", "claude", "gemini"] as const)[index] })) : []));
    });
    return () => { cancelled = true; };
  }, [settings, projectID, id]);
  // Older controllers can omit this optional surface without breaking chat.
  if (!state.data && !state.error) return null;
  const openSettings = () => { setGrant({ ...emptyGrant, ...state.data?.grant }); setError(""); setSettings(true); };
  const save = async () => {
    setBusy(true); setError("");
    try { await put(path, grant); setSettings(false); state.reload(); }
    catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
    finally { setBusy(false); }
  };
  const modes: Record<Engine, string[]> = { codex: ["untrusted", "on-request", "never"], claude: ["plan", "default", "acceptEdits", "bypassPermissions"], gemini: ["plan", "default", "auto_edit", "yolo"] };
  const modeLabel = (mode: string) => ({ plan: text("计划模式", "Plan mode"), default: text("默认权限", "Default permissions"), acceptEdits: text("允许编辑文件", "Allow file edits"), auto_edit: text("允许编辑文件", "Allow file edits"), bypassPermissions: text("跳过权限审批", "Bypass permissions"), yolo: text("自动批准", "Automatically approve"), untrusted: text("不可信命令需审批", "Approve untrusted commands"), "on-request": text("按需审批", "Approve on request"), never: text("永不审批", "Never approve") }[mode] ?? mode);
  const toolLabel: Record<string, string> = { wio_create_session: text("创建会话", "Create session"), wio_fork_session: text("继续会话", "Continue session"), wio_send_message: text("发送任务", "Send task"), wio_steer_session: text("补充指令", "Add instruction"), wio_interrupt_session: text("中断任务", "Interrupt task"), wio_archive_session: text("归档会话", "Archive session"), wio_restore_session: text("恢复会话", "Restore session"), wio_update_session: text("更新会话", "Update session") };
  return <section className="session-collaboration" aria-label={text("会话协作", "Session collaboration")}>
    <div className="collaboration-heading">
      <button type="button" className="collaboration-toggle" aria-expanded={expanded} onClick={() => setExpanded(value => !value)}><Network size={15} />{text("会话协作", "Session collaboration")}{Boolean(state.data?.links?.length) && <span>{state.data?.links.length}</span>}</button>
      {Boolean(state.data?.notifications?.length) && <span role="status">{state.data?.notifications?.length} {text("条未读结果", "unread results")}</span>}
      <button type="button" className="icon-button" aria-label={text("会话协作设置", "Collaboration settings")} onClick={openSettings}><Settings2 size={15} /></button>
    </div>
    {expanded && <div className="collaboration-content">
      {(state.data?.notifications ?? []).map(notification => <button type="button" className="secondary-button collaboration-read" key={notification.target_key} onClick={() => { void post(path + "/read", notification).then(() => state.reload()).catch(failure => setError(String(failure))); }}>{text("标为已读", "Mark as read")} · {state.data?.links?.find(link => link.target_key === notification.target_key)?.session?.title ?? notification.target_key}</button>)}
      {error && <p role="alert">{error}</p>}
      {state.error && <p role="alert">{state.error}<button type="button" onClick={state.reload}>{t("common.retry")}</button></p>}
      {state.data && !state.data.supported && <p>{text("请升级此服务器的 Agent 后使用协作工具。", "Update this server's Agent to use collaboration tools.")}</p>}
      {!state.data?.grant?.enabled && <p>{text("在设置中允许模型创建和管理子会话。", "Allow the model to create and manage child sessions in settings.")}</p>}
      {(state.data?.links ?? []).map(link => <div className="collaboration-card" key={`${link.source_key}:${link.target_key}`}>
        <span>{link.target_key === `${engine}:${id}` ? text("来源会话", "Source session") : text("子会话", "Child session")}</span>
        {link.missing ? <small>{text("会话已删除或正在准备，来源记录已保留。", "Session deleted or preparing; provenance retained.")}</small> : <><a href={link.session.url}>{link.session.title} · {link.session.engine}<ExternalLink size={12} /></a><Status value={link.session.archived_at ? "archived" : link.session.recovery_pending ? "unknown" : link.session.waiting_user ? "waiting_user" : link.session.status} /><small>{link.session.workspace_id === workspaceID ? text("共享工作目录", "Shared working directory") : text("独立工作区", "Separate workspace")}</small></>}
      </div>)}
      {(state.data?.requests ?? []).slice(0, 10).map(request => <div className="collaboration-card" key={request.id}><span>{toolLabel[request.tool] ?? request.tool}</span><Status value={request.status} />{request.error && <small role="alert">{request.error.code} · {request.error.message}</small>}{request.phase === "await_source" && <small>{text("等待来源会话本轮结束", "Waiting for the source turn to end")}</small>}{request.target_id && <a href={`/?view=${request.target_engine}&thread=${encodeURIComponent(request.target_id)}`}>{text("打开会话", "Open session")}</a>}<small title={request.operation_id}>{request.request_id}</small></div>)}
    </div>}
    <Dialog open={settings} title={text("会话协作设置", "Collaboration settings")} onClose={() => { if (!busy) setSettings(false); }} dismissDisabled={busy} closeLabel={t("common.close")}>
      <form className="collaboration-settings" onSubmit={event => { event.preventDefault(); void save(); }}>
        <label><input type="checkbox" checked={grant.enabled} onChange={event => setGrant({ ...grant, enabled: event.target.checked })} />{text("允许模型创建和管理子会话", "Allow creating and managing child sessions")}</label>
        <p>{text("工具仅在用户授权的任务范围内使用。新建空会话不调用模型；共享工作目录中的修改会影响其他会话。", "Tools operate within the user's authorized task. Empty sessions do not call a model. Changes in a shared directory affect other sessions.")}</p>
        <fieldset disabled={!grant.enabled || busy}>
          <legend>{text("协作范围", "Collaboration scope")}</legend>
          <label><input type="checkbox" checked={grant.worktree} onChange={event => setGrant({ ...grant, worktree: event.target.checked })} />{text("允许创建独立 worktree", "Allow creating separate worktrees")}</label>
          <label><input type="checkbox" checked={grant.auto_continue} onChange={event => setGrant({ ...grant, auto_continue: event.target.checked })} />{text("允许任务完成后自动续聊（会调用模型）", "Allow automatic continuation on completion (calls the model)")}</label>
          <label><input type="checkbox" checked={grant.delegate} onChange={event => setGrant({ ...grant, delegate: event.target.checked })} />{text("允许子会话进一步委派（最多两层）", "Allow child sessions to delegate (up to two levels)")}</label>
          <p>{text("当前工作区始终可用。可额外选择本项目工作区：", "The current workspace is available. Optionally grant other workspaces in this project:")}</p>
          {workspaces.error && <p role="alert">{workspaces.error}</p>}
          {(workspaces.data ?? []).filter(workspace => workspace.project_id === projectID && workspace.id !== workspaceID && workspace.status === "ready").map(workspace => <label key={workspace.id}><input type="checkbox" checked={grant.workspace_ids?.includes(workspace.id) ?? false} onChange={event => setGrant({ ...grant, workspace_ids: event.target.checked ? [...(grant.workspace_ids ?? []), workspace.id] : (grant.workspace_ids ?? []).filter(value => value !== workspace.id) })} />{workspace.server_name} · {workspace.path}</label>)}
          <p>{text("跨模型执行权限（不得高于来源会话）：", "Cross-model execution permissions (cannot exceed the source session):")}</p>
          {(["codex", "claude", "gemini"] as const).filter(value => value !== engine).map(value => <label key={value}>{value}<select aria-label={`${value} ${text("协作权限", "collaboration permissions")}`} value={grant.target_modes?.[value] ?? ""} onChange={event => setGrant({ ...grant, target_modes: { ...grant.target_modes, [value]: event.target.value || undefined } })}><option value="">{text("未授权", "Not granted")}</option>{modes[value].map(mode => <option key={mode} value={mode}>{modeLabel(mode)}</option>)}</select></label>)}
          <p>{text("额外允许管理已有会话：", "Optionally allow managing existing sessions:")}</p>
          {otherSessions.map(session => <label key={`${session.engine}:${session.id}`}><input type="checkbox" checked={grant.session_refs?.some(ref => ref.engine === session.engine && ref.id === session.id) ?? false} onChange={event => setGrant({ ...grant, session_refs: event.target.checked ? [...(grant.session_refs ?? []), { engine: session.engine, id: session.id }] : (grant.session_refs ?? []).filter(ref => ref.engine !== session.engine || ref.id !== session.id) })} />{session.engine} · {session.title}</label>)}
        </fieldset>
        {error && <p className="error-banner" role="alert">{error}</p>}
        <DialogActions><button className="primary-button" disabled={busy}>{busy ? t("common.loading") : t("common.save")}</button></DialogActions>
      </form>
    </Dialog>
  </section>;
}
