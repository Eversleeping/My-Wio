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
 const { t } = useI18n();
 const state = useResource(endpoints.collaboration());
 const [draft, setDraft] = useState<CollaborationGrant | null>(null);
 const grant = { ...emptyGrant, ...state.data, ...draft };
 const setGrant = setDraft;
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState("");
 const save = async () => {
  setBusy(true); setError("");
  try { await endpoints.saveCollaboration(grant); setDraft(null); notify(t("collaboration.settingsSaved")); }
  catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
  finally { setBusy(false); }
 };
  const modes: Record<Engine, string[]> = { codex: ["untrusted", "on-request", "never"], claude: ["plan", "default", "acceptEdits", "bypassPermissions"], gemini: ["plan", "default", "auto_edit", "yolo"] };
  const modeLabel = (mode: string) => ({ plan: t("collaboration.planMode"), default: t("collaboration.defaultPermissions"), acceptEdits: t("collaboration.allowFileEdits"), auto_edit: t("collaboration.allowFileEdits"), bypassPermissions: t("collaboration.bypassPermissions"), yolo: t("collaboration.automaticallyApprove"), untrusted: t("collaboration.approveUntrusted"), "on-request": t("collaboration.approveOnRequest"), never: t("collaboration.neverApprove") }[mode] ?? mode);
 return <Section title={t("collaboration.title")} icon={<Network size={18} />}><div className="settings-content">
 <p>{t("collaboration.settingsScope")}</p>
 {state.error && <p role="alert">{state.error}<button onClick={state.reload}>{t("common.retry")}</button></p>}
 <fieldset className="settings-fields" disabled={busy || !state.data}>
      <form className="collaboration-settings" onSubmit={event => { event.preventDefault(); void save(); }}>
        <label><input type="checkbox" checked={grant.enabled} onChange={event => setGrant({ ...grant, enabled: event.target.checked })} />{t("collaboration.allowChildren")}</label>
        <p>{t("collaboration.authorizedTask")}</p>
        <fieldset disabled={busy}>
          <legend>{t("collaboration.informationDisplay")}</legend>
          <label><input type="checkbox" checked={grant.show_unread_results ?? false} onChange={event => setGrant({ ...grant, show_unread_results: event.target.checked })} />{t("collaboration.showUnread")}</label>
          <label><input type="checkbox" checked={grant.show_creation_results ?? false} onChange={event => setGrant({ ...grant, show_creation_results: event.target.checked })} />{t("collaboration.showCreation")}</label>
        </fieldset>
        <fieldset disabled={!grant.enabled || busy}>
          <legend>{t("collaboration.scope")}</legend>
          <label><input type="checkbox" checked={grant.worktree} onChange={event => setGrant({ ...grant, worktree: event.target.checked })} />{t("collaboration.allowWorktrees")}</label>
          <label><input type="checkbox" checked={grant.auto_continue} onChange={event => setGrant({ ...grant, auto_continue: event.target.checked })} />{t("collaboration.autoContinue")}</label>
          <label><input type="checkbox" checked={grant.delegate} onChange={event => setGrant({ ...grant, delegate: event.target.checked })} />{t("collaboration.allowDelegation")}</label>
          <p>{t("collaboration.crossModelPermissions")}</p>
          {(["codex", "claude", "gemini"] as const).map(value => <label key={value}>{value}<select aria-label={`${value} ${t("collaboration.permissions")}`} value={grant.target_modes?.[value] ?? ""} onChange={event => setGrant({ ...grant, target_modes: { ...grant.target_modes, [value]: event.target.value || undefined } })}><option value="">{t("collaboration.notGranted")}</option>{modes[value].map(mode => <option key={mode} value={mode}>{modeLabel(mode)}</option>)}</select></label>)}
          <label><input type="checkbox" checked={grant.all_project_sessions ?? false} onChange={event => setGrant({ ...grant, all_project_sessions: event.target.checked })} />{t("collaboration.allowOtherSessions")}</label>
          <p>{t("collaboration.authorizeWorkspaces")}</p>
        </fieldset>
        {error && <p className="error-banner" role="alert">{error}</p>}
        <DialogActions><button className="primary-button" disabled={busy || !state.data}>{busy ? t("common.loading") : t("common.save")}</button></DialogActions>
      </form>
 </fieldset>
 </div></Section>;
}
