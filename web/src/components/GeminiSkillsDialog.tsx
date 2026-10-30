import { useEffect, useState } from "react";
import { LoaderCircle, RefreshCw } from "lucide-react";
import { api } from "../api";
import { useI18n } from "../i18n";
import type { GeminiThread } from "../types";
import { Dialog, DialogActions } from "./Dialog";

type Skill = { name: string; description: string; enabled: boolean; builtin: boolean };
type Discovery = { status: string; error?: string; skills?: Skill[] };

export function GeminiSkillsDialog({ open, thread, onClose, onUse }: { open: boolean; thread: GeminiThread; onClose: () => void; onUse: (name: string) => void }) {
  const { t } = useI18n();
  const [revision, setRevision] = useState(0);
  const [skills, setSkills] = useState<Skill[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open || !thread.skills_supported || thread.server_status !== "online") return;
    const controller = new AbortController();
    let timer = 0;
    setSkills(null); setError(""); setLoading(true);
    const root = `/workspaces/${encodeURIComponent(thread.workspace_id)}/gemini/skills`;
    (async () => {
      const started = await api<{ operation_id: string }>(root, { method: "POST", body: "{}", signal: controller.signal });
      const deadline = Date.now() + 60000;
      while (!controller.signal.aborted && Date.now() < deadline) {
        const result = await api<Discovery>(`${root}/${encodeURIComponent(started.operation_id)}`, { signal: controller.signal });
        if (result.status === "succeeded") {
          if (!Array.isArray(result.skills)) throw new Error(t("gemini.actionFailed"));
          setSkills(result.skills); return;
        }
        if (["failed", "cancelled", "canceled"].includes(result.status)) throw new Error(result.error || t("gemini.actionFailed"));
        await new Promise<void>(resolve => { timer = window.setTimeout(resolve, 1000); });
      }
      if (!controller.signal.aborted) throw new Error(t("gemini.skillsTimeout"));
    })().catch(value => { if (!controller.signal.aborted) setError(value instanceof Error ? value.message : t("gemini.actionFailed")); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => { controller.abort(); window.clearTimeout(timer); };
  }, [open, revision, thread.workspace_id, thread.skills_supported, thread.server_status, t]);

  return <Dialog open={open} title={t("gemini.skillsTitle")} onClose={onClose} wide closeLabel={t("common.close")}>
    <p className="muted">{t("gemini.skillsHint")}</p>
    {!thread.skills_supported ? <p>{t("gemini.skillsUpdate")}</p> : thread.server_status !== "online" ? <p>{t("gemini.offline")}</p> : <>
      {loading && <p role="status"><LoaderCircle className="spin" size={16} /> {t("gemini.skillsLoading")}</p>}
      {error && <p role="alert">{error}</p>}
      {skills && !skills.length && <p>{t("gemini.skillsNone")}</p>}
      {skills && skills.length > 0 && <div className="native-list">{skills.map(skill => <article key={skill.name}>
        <header><strong>{skill.name}</strong>{skill.builtin && <small>{t("gemini.skillsBuiltin")}</small>}{!skill.enabled && <small>{t("gemini.skillsDisabled")}</small>}</header>
        <p>{skill.description}</p>
        <button type="button" className="secondary-button" disabled={!skill.enabled} aria-label={`${t("gemini.skillsUse")} ${skill.name}`} onClick={() => onUse(skill.name)}>{t("gemini.skillsUse")}</button>
      </article>)}</div>}
    </>}
    <DialogActions><button type="button" className="secondary-button" disabled={loading || !thread.skills_supported || thread.server_status !== "online"} onClick={() => setRevision(value => value + 1)}><RefreshCw size={16} />{t("common.refresh")}</button></DialogActions>
  </Dialog>;
}
