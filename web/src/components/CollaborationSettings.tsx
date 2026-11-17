import { useState } from "react";
import { Network } from "lucide-react";
import { useResource } from "../data/resources";
import { settings as endpoints, type CollaborationGrant } from "../data/settings";
import { useI18n } from "../i18n";
import { Section } from "./PageUI";
import { DialogActions } from "./Dialog";
type Engine = "codex" | "claude" | "gemini";
const emptyGrant: CollaborationGrant = { enabled: false, workspace_ids: [], session_refs: [], all_project_sessions: false, worktree: false, auto_continue: false, delegate: false, target_modes: {}, show_unread_results: false, show_creation_results: false };
export function CollaborationSettings({ notify }: { notify: (message: string) => void }) {
 const { language, t } = useI18n();
 const text = (zh: string, en: string) => language === "zh-CN" ? zh : en;
 const state = useResource(endpoints.collaboration());
 const [draft, setDraft] = useState<CollaborationGrant | null>(null);
 const grant = { ...emptyGrant, ...state.data, ...draft };
 const setGrant = setDraft;
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState("");
 const save = async () => {
  setBusy(true); setError("");
  try { await endpoints.saveCollaboration(grant); setDraft(null); notify(text("会话协作设置已应用到所有会话", "Collaboration settings applied to all sessions")); }
  catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
  finally { setBusy(false); }
 };
  const modes: Record<Engine, string[]> = { codex: ["untrusted", "on-request", "never"], claude: ["plan", "default", "acceptEdits", "bypassPermissions"], gemini: ["plan", "default", "auto_edit", "yolo"] };
  const modeLabel = (mode: string) => ({ plan: text("计划模式", "Plan mode"), default: text("默认权限", "Default permissions"), acceptEdits: text("允许编辑文件", "Allow file edits"), auto_edit: text("允许编辑文件", "Allow file edits"), bypassPermissions: text("跳过权限审批", "Bypass permissions"), yolo: text("自动批准", "Automatically approve"), untrusted: text("不可信命令需审批", "Approve untrusted commands"), "on-request": text("按需审批", "Approve on request"), never: text("永不审批", "Never approve") }[mode] ?? mode);
 return <Section title={text("会话协作", "Session collaboration")} icon={<Network size={18} />}><div className="settings-content">
 <p>{text("保存后统一应用于所有已有和新建会话，替代原来的逐会话设置。协作仍限定在来源会话所在的项目内。", "Saving applies to all existing and new sessions and replaces per-session settings. Collaboration remains within the source session's project.")}</p>
 {state.error && <p role="alert">{state.error}<button onClick={state.reload}>{t("common.retry")}</button></p>}
 <fieldset className="settings-fields" disabled={busy || !state.data}>
      <form className="collaboration-settings" onSubmit={event => { event.preventDefault(); void save(); }}>
        <label><input type="checkbox" checked={grant.enabled} onChange={event => setGrant({ ...grant, enabled: event.target.checked })} />{text("允许模型创建和管理子会话", "Allow creating and managing child sessions")}</label>
        <p>{text("工具仅在用户授权的任务范围内使用。新建空会话不调用模型；共享工作目录中的修改会影响其他会话。", "Tools operate within the user's authorized task. Empty sessions do not call a model. Changes in a shared directory affect other sessions.")}</p>
        <fieldset disabled={busy}>
          <legend>{text("信息显示", "Information display")}</legend>
          <label><input type="checkbox" checked={grant.show_unread_results ?? false} onChange={event => setGrant({ ...grant, show_unread_results: event.target.checked })} />{text("显示未读结果", "Show unread results")}</label>
          <label><input type="checkbox" checked={grant.show_creation_results ?? false} onChange={event => setGrant({ ...grant, show_creation_results: event.target.checked })} />{text("显示创建会话结果（成功、失败或取消）", "Show session creation results (success, failure or cancellation)")}</label>
        </fieldset>
        <fieldset disabled={!grant.enabled || busy}>
          <legend>{text("协作范围", "Collaboration scope")}</legend>
          <label><input type="checkbox" checked={grant.worktree} onChange={event => setGrant({ ...grant, worktree: event.target.checked })} />{text("允许创建独立 worktree", "Allow creating separate worktrees")}</label>
          <label><input type="checkbox" checked={grant.auto_continue} onChange={event => setGrant({ ...grant, auto_continue: event.target.checked })} />{text("允许任务完成后自动续聊（会调用模型）", "Allow automatic continuation on completion (calls the model)")}</label>
          <label><input type="checkbox" checked={grant.delegate} onChange={event => setGrant({ ...grant, delegate: event.target.checked })} />{text("允许子会话进一步委派（最多两层）", "Allow child sessions to delegate (up to two levels)")}</label>
          <p>{text("跨模型执行权限（不得高于来源会话）：", "Cross-model execution permissions (cannot exceed the source session):")}</p>
          {(["codex", "claude", "gemini"] as const).map(value => <label key={value}>{value}<select aria-label={`${value} ${text("协作权限", "collaboration permissions")}`} value={grant.target_modes?.[value] ?? ""} onChange={event => setGrant({ ...grant, target_modes: { ...grant.target_modes, [value]: event.target.value || undefined } })}><option value="">{text("未授权", "Not granted")}</option>{modes[value].map(mode => <option key={mode} value={mode}>{modeLabel(mode)}</option>)}</select></label>)}
          <label><input type="checkbox" checked={grant.all_project_sessions ?? false} onChange={event => setGrant({ ...grant, all_project_sessions: event.target.checked })} />{text("允许管理本项目其他 Agent 的已有会话", "Allow managing other agents' existing sessions in this project")}</label>
          <p>{text("开启后同时授权本项目的全部工作区（包括 worktree），可原生分叉 worktree 中的会话。", "Also grants every workspace in this project, including worktrees, so their sessions can be natively forked.")}</p>
        </fieldset>
        {error && <p className="error-banner" role="alert">{error}</p>}
        <DialogActions><button className="primary-button" disabled={busy || !state.data}>{busy ? t("common.loading") : t("common.save")}</button></DialogActions>
      </form>
 </fieldset>
 </div></Section>;
}
