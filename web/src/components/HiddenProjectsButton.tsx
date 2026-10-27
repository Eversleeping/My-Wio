import { useState } from "react";
import { createPortal } from "react-dom";
import { Eye, EyeOff, LoaderCircle } from "lucide-react";
import { patch } from "../api";
import { useData } from "../useData";
import { useI18n } from "../i18n";
import { Dialog } from "./Dialog";
import { Empty, ErrorState } from "./PageUI";

type HiddenProject = { project_id: string; server_id: string; project_name: string; server_name: string; hidden_at: string };

export function HiddenProjectsButton({ realtime, onRestored, notify }: { realtime: unknown; onRestored: () => void; notify: (message: string) => void }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [restoring, setRestoring] = useState("");
  const hidden = useData<HiddenProject[]>(open ? "/projects/hidden" : null, realtime);
  const restore = async (project: HiddenProject) => {
    if (restoring) return;
    setRestoring(`${project.project_id}:${project.server_id}`);
    try {
      await patch(`/projects/${project.project_id}`, { server_id: project.server_id, hidden: false });
      hidden.reload();
      onRestored();
      notify(t("codex.projectShown"));
    } catch (error) {
      notify(error instanceof Error ? error.message : String(error));
    } finally {
      setRestoring("");
    }
  };
  return <>
    <button type="button" className="icon-button" title={t("codex.hiddenProjects")} aria-label={t("codex.hiddenProjects")} onClick={() => setOpen(true)}><EyeOff size={18} /></button>
    {createPortal(<Dialog open={open} title={t("codex.hiddenProjects")} closeLabel={t("common.close")} dismissDisabled={Boolean(restoring)} onClose={() => setOpen(false)}>
      {hidden.error ? <ErrorState error={hidden.error} reload={hidden.reload} /> : hidden.loading ? <div className="page-loading"><LoaderCircle className="spin" size={20} /></div> : hidden.data?.length ? <div className="hidden-project-list">{hidden.data.map(project => {
        const key = `${project.project_id}:${project.server_id}`;
        return <div className="hidden-project-row" key={key}><div><strong>{project.project_name}</strong><small>{project.server_name}</small></div><button type="button" className="secondary-button small" disabled={Boolean(restoring)} onClick={() => void restore(project)}>{restoring === key ? <LoaderCircle className="spin" size={14} /> : <Eye size={14} />}{t("codex.showProject")}</button></div>;
      })}</div> : <Empty icon={<Eye size={24} />} text={t("codex.noHiddenProjects")} />}
    </Dialog>, document.body)}
  </>;
}
