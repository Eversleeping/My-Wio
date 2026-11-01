import { useEffect, useRef, useState } from "react";
import { LoaderCircle, RefreshCw } from "lucide-react";
import { api, APIError } from "../api";
import { useI18n } from "../i18n";
import type { GeminiThread } from "../types";
import { Dialog, DialogActions } from "./Dialog";
import { ConfirmDialog } from "./ConfirmDialog";

type Skill = { name: string; description: string; enabled: boolean; builtin: boolean; scope?: string };
type Discovery = { status: string; error?: string; skills?: Skill[] };
type Change = { action: "install" | "uninstall" | "enable" | "disable"; scope: string; name?: string; source?: string; path?: string; request_id: string };
type Pending = { workspace: string; change: Change; operation?: string };

export function GeminiSkillsDialog({ open, thread, onClose, onUse }: { open: boolean; thread: GeminiThread; onClose: () => void; onUse: (name: string) => void }) {
  const { t } = useI18n();
  const [revision, setRevision] = useState(0);
  const [skills, setSkills] = useState<Skill[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [source, setSource] = useState("");
  const [subpath, setSubpath] = useState("");
  const [scope, setScope] = useState("workspace");
  const [removing, setRemoving] = useState<Skill | null>(null);
  const pending = useRef<Pending>();
  const active = ["queued", "running", "interrupting"].includes(thread.status) || thread.recovery_pending;
  const canManage = Boolean(thread.skills_management_supported && !thread.archived_at && !active && thread.server_status === "online");
  const root = `/workspaces/${encodeURIComponent(thread.workspace_id)}/gemini/skills`;

  useEffect(() => {
    if (!open || !thread.skills_supported || thread.server_status !== "online") return;
    if (pending.current?.workspace !== thread.workspace_id) pending.current = undefined;
    const controller = new AbortController();
    let timer = 0;
    const mutation = pending.current;
    setError(""); setLoading(true);
    if (!mutation) setSkills(null);
    (async () => {
      let operation = mutation?.operation;
      if (!operation) {
        const started = await api<{ operation_id: string }>(mutation ? `${root}/manage` : root, { method: "POST", body: JSON.stringify(mutation?.change ?? {}), signal: controller.signal });
        operation = started.operation_id;
        if (mutation) mutation.operation = operation;
      }
      const deadline = Date.now() + (mutation ? 150000 : 60000);
      while (!controller.signal.aborted && Date.now() < deadline) {
        const result = await api<Discovery>(`${root}/${encodeURIComponent(operation)}`, { signal: controller.signal });
        if (result.status === "succeeded") {
          if (!Array.isArray(result.skills)) throw new Error(t("gemini.actionFailed"));
          setSkills(result.skills);
          if (mutation) { pending.current = undefined; setNotice(t("gemini.skillsChanged")); if (mutation.change.action === "install") { setSource(""); setSubpath(""); } }
          return;
        }
        if (["failed", "cancelled", "canceled"].includes(result.status)) {
          if (mutation) pending.current = undefined;
          throw new Error(result.error || t("gemini.actionFailed"));
        }
        await new Promise<void>(resolve => { timer = window.setTimeout(resolve, 1000); });
      }
      if (!controller.signal.aborted) throw new Error(t(mutation ? "gemini.skillsChangePending" : "gemini.skillsTimeout"));
    })().catch(value => {
      if (!controller.signal.aborted) {
        // A definitive validation/busy rejection did not enqueue the change.
        // Network and server errors keep the same idempotency key for retries.
        if (mutation && !mutation.operation && value instanceof APIError && [400, 403, 404, 409].includes(value.status)) pending.current = undefined;
        setError(value instanceof Error ? value.message : t("gemini.actionFailed"));
      }
    })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [open, revision, thread.workspace_id, thread.skills_supported, thread.server_status, root, t]);

  const change = (value: Omit<Change, "request_id">) => {
    if (!canManage || loading || pending.current) return;
    pending.current = { workspace: thread.workspace_id, change: { ...value, request_id: crypto.randomUUID() } };
    setNotice(""); setLoading(true); setRevision(value => value + 1);
  };

  return <>
    <Dialog open={open} title={t("gemini.skillsTitle")} onClose={onClose} wide closeLabel={t("common.close")}>
      <p className="muted">{t("gemini.skillsHint")}</p>
      {!thread.skills_supported ? <p>{t("gemini.skillsUpdate")}</p> : thread.server_status !== "online" ? <p>{t("gemini.offline")}</p> : <>
        {loading && <p role="status"><LoaderCircle className="spin" size={16} /> {t(pending.current ? "gemini.skillsChanging" : "gemini.skillsLoading")}</p>}
        {error && <p role="alert">{error}</p>}
        {notice && <p role="status">{notice}</p>}
        {pending.current && !loading && <button type="button" className="secondary-button" onClick={() => setRevision(value => value + 1)}>{t("gemini.skillsRetry")}</button>}
        {!thread.skills_management_supported ? <p className="muted">{t("gemini.skillsManageUpdate")}</p> : <>
          {active && <p className="muted">{t("gemini.skillsBusy")}</p>}
          <form className="gemini-skill-install" onSubmit={event => { event.preventDefault(); if (source.trim()) change({ action: "install", source: source.trim(), ...(subpath.trim() ? {path:subpath.trim()} : {}), scope }); }}>
            <label className="field"><span>{t("gemini.skillScope")}</span><select value={scope} disabled={loading || Boolean(pending.current)} onChange={event => setScope(event.target.value)}><option value="workspace">{t("gemini.skillScope.workspace")}</option><option value="user">{t("gemini.skillScope.user")}</option></select></label>
            <label className="field"><span>{t("gemini.skillSource")}</span><input value={source} maxLength={2048} placeholder="https://github.com/owner/skills.git" disabled={!canManage || loading || Boolean(pending.current)} onChange={event => setSource(event.target.value)} /></label>
            <label className="field"><span>{t("gemini.skillSubpath")}</span><input value={subpath} maxLength={1024} disabled={!canManage || loading || Boolean(pending.current)} onChange={event => setSubpath(event.target.value)} /></label>
            <p className="muted">{t("gemini.skillInstallHint")}</p>
            <button type="submit" className="primary-button" disabled={!canManage || loading || Boolean(pending.current) || !source.trim()}>{t("gemini.skillInstall")}</button>
          </form>
        </>}
        {skills && !skills.length && <p>{t("gemini.skillsNone")}</p>}
        {skills && skills.length > 0 && <div className="native-list">{skills.map(skill => <article key={skill.name}>
          <header><strong>{skill.name}</strong>{skill.builtin && <small>{t("gemini.skillsBuiltin")}</small>}{!skill.enabled && <small>{t("gemini.skillsDisabled")}</small>}{["workspace", "user"].includes(skill.scope ?? "") && <small>{t(`gemini.skillScope.${skill.scope}`)}</small>}</header>
          <p>{skill.description}</p>
          <div className="gemini-skill-actions">
            <button type="button" className="secondary-button" disabled={!skill.enabled || loading} aria-label={`${t("gemini.skillsUse")} ${skill.name}`} onClick={() => onUse(skill.name)}>{t("gemini.skillsUse")}</button>
            {thread.skills_management_supported && <button type="button" className="secondary-button" disabled={!canManage || loading || Boolean(pending.current)} aria-label={`${t(skill.enabled ? "gemini.skillDisable" : "gemini.skillEnable")} ${skill.name}`} onClick={() => change({ action: skill.enabled ? "disable" : "enable", name:skill.name, scope })}>{t(skill.enabled ? "gemini.skillDisable" : "gemini.skillEnable")}</button>}
            {thread.skills_management_supported && !skill.builtin && ["workspace", "user"].includes(skill.scope ?? "") && <button type="button" className="secondary-button danger" disabled={!canManage || loading || Boolean(pending.current)} aria-label={`${t("gemini.skillUninstall")} ${skill.name}`} onClick={() => setRemoving(skill)}>{t("gemini.skillUninstall")}</button>}
          </div>
        </article>)}</div>}
      </>}
      <DialogActions><button type="button" className="secondary-button" disabled={loading || Boolean(pending.current) || !thread.skills_supported || thread.server_status !== "online"} onClick={() => { setNotice(""); setRevision(value => value + 1); }}><RefreshCw size={16} />{t("common.refresh")}</button></DialogActions>
    </Dialog>
    <ConfirmDialog open={Boolean(removing)} danger title={t("gemini.skillUninstall")} impact={t("gemini.skillUninstallImpact", {name:removing?.name ?? "",scope:t(`gemini.skillScope.${removing?.scope ?? "workspace"}`)})} confirmLabel={t("gemini.skillUninstall")} cancelLabel={t("common.cancel")} closeLabel={t("common.close")} busy={loading} onClose={() => setRemoving(null)} onConfirm={() => { if (removing) change({action:"uninstall",name:removing.name,scope:removing.scope ?? "workspace"}); setRemoving(null); }} />
  </>;
}
