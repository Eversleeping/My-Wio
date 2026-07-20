import { AlertTriangle, Database, HardDrive, LoaderCircle, ShieldCheck, Trash2 } from "lucide-react";
import { useEffect, useState, type ComponentType, type FormEvent } from "react";
import type { Project, ProjectDeletionPlan } from "../../types";
import type { DialogSlotProps } from "./slots";

export type ProjectDeletionMode = "metadata-only" | "managed-files";

export interface ProjectDeletionLabels {
  title: string;
  loading: string;
  metadataOnly: string;
  metadataDescription: string;
  managedFiles: string;
  managedDescription: string;
  workspaces: string;
  managed: string;
  observed: string;
  dirty: string;
  activeOperations: string;
  activeTasks: string;
  activeDeployments: string;
  remotePreserved: string;
  blockers: string;
  noBlockers: string;
  confirmLabel: string;
  confirmPlaceholder: string;
  cancel: string;
  deleting: string;
  deleteMetadata: string;
  deleteFiles: string;
}

export function ProjectDeletionDialog({ open, project, plan, loading, busy, error, labels, Dialog, onClose, onSubmit }: {
  open: boolean;
  project: Project | null;
  plan: ProjectDeletionPlan | null;
  loading: boolean;
  busy: boolean;
  error: string;
  labels: ProjectDeletionLabels;
  Dialog: ComponentType<DialogSlotProps>;
  onClose: () => void;
  onSubmit: (mode: ProjectDeletionMode) => void | Promise<void>;
}) {
  const [mode, setMode] = useState<ProjectDeletionMode>("metadata-only");
  const [confirmation, setConfirmation] = useState("");
  useEffect(() => {
    if (!open) return;
    setMode("metadata-only");
    setConfirmation("");
  }, [open, project?.id]);
  const modeBlockers = plan?.blockers.filter(blocker => blocker.modes.includes(mode)) ?? [];
  const allowed = mode === "metadata-only" ? plan?.can_delete_metadata : plan?.can_delete_managed_files;
  const confirmed = Boolean(project && confirmation === project.name);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!busy && allowed && confirmed) void onSubmit(mode);
  };
  return <Dialog open={open} title={project ? `${labels.title}: ${project.name}` : labels.title} onClose={() => { if (!busy) onClose(); }} wide>
    <form className="deletion-dialog" onSubmit={submit}>
      {loading && <div className="empty-state"><LoaderCircle className="spin" size={18} />{labels.loading}</div>}
      {error && <div className="error-banner" role="alert"><AlertTriangle size={16} />{error}</div>}
      {plan && <>
        <div className="segmented-control deletion-mode" role="tablist">
          <button type="button" role="tab" aria-selected={mode === "metadata-only"} className={mode === "metadata-only" ? "active" : ""} onClick={() => setMode("metadata-only")}><Database size={15} />{labels.metadataOnly}</button>
          <button type="button" role="tab" aria-selected={mode === "managed-files"} className={mode === "managed-files" ? "active" : ""} onClick={() => setMode("managed-files")}><HardDrive size={15} />{labels.managedFiles}</button>
        </div>
        <p className="deletion-description">{mode === "metadata-only" ? labels.metadataDescription : labels.managedDescription}</p>
        <div className="deletion-summary-grid">
          <DeletionStat label={labels.workspaces} value={plan.workspace_count} />
          <DeletionStat label={labels.managed} value={plan.managed_workspace_count} />
          <DeletionStat label={labels.observed} value={plan.observed_workspace_count} />
          <DeletionStat label={labels.dirty} value={plan.dirty_managed_workspaces} danger={plan.dirty_managed_workspaces > 0} />
          <DeletionStat label={labels.activeOperations} value={plan.active_agent_operations} danger={plan.active_agent_operations > 0} />
          <DeletionStat label={labels.activeTasks} value={plan.active_codex_tasks} danger={plan.active_codex_tasks > 0} />
          <DeletionStat label={labels.activeDeployments} value={plan.active_deployments} danger={plan.active_deployments > 0} />
        </div>
        {plan.workspaces.length > 0 && <div className="deletion-workspace-list"><strong>{labels.workspaces}</strong>{plan.workspaces.map(workspace => <div key={workspace.id}><span><b>{workspace.server_name}</b><small>{workspace.management_mode === "managed" ? labels.managed : labels.observed}{workspace.dirty ? ` · ${labels.dirty}` : ""}</small></span><code>{workspace.path}</code><span className={`status-tag ${workspace.server_status}`}>{workspace.server_status}</span></div>)}</div>}
        <div className="preserved-notice"><ShieldCheck size={16} /><span>{labels.remotePreserved}</span></div>
        <div className={`deletion-blockers ${modeBlockers.length ? "blocked" : "ready"}`}>
          <strong>{labels.blockers}</strong>
          {modeBlockers.length ? modeBlockers.map(blocker => <span key={blocker.code}><AlertTriangle size={14} />{blocker.message} ({blocker.count})</span>) : <span><ShieldCheck size={14} />{labels.noBlockers}</span>}
        </div>
        <label className="field"><span>{labels.confirmLabel}</span><input value={confirmation} disabled={busy || !allowed} onChange={event => setConfirmation(event.target.value)} placeholder={labels.confirmPlaceholder.replace("{name}", project?.name ?? "")} autoComplete="off" /></label>
      </>}
      <div className="dialog-actions"><button type="button" className="secondary-button" disabled={busy} onClick={onClose}>{labels.cancel}</button><button className="secondary-button danger" disabled={busy || !allowed || !confirmed}>{busy ? <LoaderCircle className="spin" size={16} /> : <Trash2 size={16} />}{busy ? labels.deleting : mode === "metadata-only" ? labels.deleteMetadata : labels.deleteFiles}</button></div>
    </form>
  </Dialog>;
}

function DeletionStat({ label, value, danger = false }: { label: string; value: number; danger?: boolean }) {
  return <div className={danger ? "danger" : ""}><small>{label}</small><strong>{value}</strong></div>;
}
