import { Archive, EyeOff, GitBranch, History, LoaderCircle, Pin, Save } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import type { ProjectDetail } from "../../types";
import type { CreateProjectDialogSlots } from "./slots";

export interface ProjectEditValue {
  name: string;
  description: string;
  defaultBranch: string;
  pinned: boolean;
  hidden: boolean;
  archived: boolean;
}

export interface ProjectDetailsLabels {
  title: string;
  overview: string;
  history: string;
  name: string;
  description: string;
  defaultBranch: string;
  pinned: string;
  hidden: string;
  archived: string;
  remote: string;
  noRemote: string;
  operation: string;
  state: string;
  time: string;
  result: string;
  noOperations: string;
  cancel: string;
  save: string;
  saving: string;
  loading: string;
}

export function ProjectDetailsDialog({ open, detail, loading, busy, error, labels, slots, onClose, onSubmit }: {
  open: boolean;
  detail: ProjectDetail | null;
  loading: boolean;
  busy: boolean;
  error: string;
  labels: ProjectDetailsLabels;
  slots: CreateProjectDialogSlots;
  onClose: () => void;
  onSubmit: (value: ProjectEditValue) => void | Promise<void>;
}) {
  const { Dialog, Field, DialogActions } = slots;
  const remotes = detail?.remotes ?? [];
  const operations = detail?.operations ?? [];
  const [tab, setTab] = useState<"overview" | "history">("overview");
  const [value, setValue] = useState<ProjectEditValue>({ name: "", description: "", defaultBranch: "main", pinned: false, hidden: false, archived: false });
  useEffect(() => {
    if (!detail) return;
    setValue({ name: detail.project.name, description: detail.project.description, defaultBranch: detail.project.default_branch, pinned: Boolean(detail.project.pinned_at), hidden: Boolean(detail.project.hidden_at), archived: Boolean(detail.project.archived_at) });
    setTab("overview");
  }, [detail?.project.id]);
  const update = <Key extends keyof ProjectEditValue>(key: Key, next: ProjectEditValue[Key]) => setValue(current => ({ ...current, [key]: next }));
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!busy && value.name.trim() && value.defaultBranch.trim()) void onSubmit(value);
  };
  return <Dialog open={open} title={detail?.project.name || labels.title} onClose={() => { if (!busy) onClose(); }} wide>
    {loading && <div className="empty-state"><LoaderCircle className="spin" size={20} />{labels.loading}</div>}
    {error && <div className="error-banner" role="alert">{error}</div>}
    {detail && <form onSubmit={submit}>
      <div className="segmented-control project-detail-tabs" role="tablist">
        <button type="button" role="tab" aria-selected={tab === "overview"} className={tab === "overview" ? "active" : ""} onClick={() => setTab("overview")}><GitBranch size={15} />{labels.overview}</button>
        <button type="button" role="tab" aria-selected={tab === "history"} className={tab === "history" ? "active" : ""} onClick={() => setTab("history")}><History size={15} />{labels.history}</button>
      </div>
      {tab === "overview" ? <>
        <div className="form-grid">
          <Field label={labels.name}><input value={value.name} disabled={busy} onChange={event => update("name", event.target.value)} required /></Field>
          <Field label={labels.defaultBranch}><input value={value.defaultBranch} disabled={busy} onChange={event => update("defaultBranch", event.target.value)} required /></Field>
        </div>
        <Field label={labels.description}><textarea rows={3} value={value.description} disabled={busy} onChange={event => update("description", event.target.value)} /></Field>
        <div className="project-preference-grid">
          <label className="toggle-row"><input type="checkbox" checked={value.pinned} disabled={busy} onChange={event => update("pinned", event.target.checked)} /><Pin size={15} /><span>{labels.pinned}</span></label>
          <label className="toggle-row"><input type="checkbox" checked={value.hidden} disabled={busy} onChange={event => update("hidden", event.target.checked)} /><EyeOff size={15} /><span>{labels.hidden}</span></label>
          <label className="toggle-row"><input type="checkbox" checked={value.archived} disabled={busy} onChange={event => update("archived", event.target.checked)} /><Archive size={15} /><span>{labels.archived}</span></label>
        </div>
        <div className="project-remote-list"><strong>{labels.remote}</strong>{remotes.length === 0 ? <span className="muted">{labels.noRemote}</span> : remotes.map(remote => <div className="project-remote-row" key={remote.id}><span>{remote.name}</span><code className="truncate-code">{remote.fetch_url}</code><span className="status-tag neutral">{remote.provider || remote.mode}</span></div>)}</div>
      </> : <div className="project-operation-list">
        <div className="project-operation-header"><span>{labels.operation}</span><span>{labels.state}</span><span>{labels.time}</span><span>{labels.result}</span></div>
        {operations.length === 0 ? <div className="empty-state">{labels.noOperations}</div> : operations.map(operation => <div className="project-operation-row" key={operation.id}><code>{operation.kind}</code><span className={`status-tag ${operation.status === "failed" ? "failed" : "neutral"}`}>{operation.status}</span><span>{new Date(operation.created_at).toLocaleString()}</span><span className="truncate-text" title={operation.result}>{operation.result || "-"}</span></div>)}
      </div>}
      <DialogActions><button type="button" className="secondary-button" disabled={busy} onClick={onClose}>{labels.cancel}</button>{tab === "overview" && <button className="primary-button" disabled={busy || !value.name.trim() || !value.defaultBranch.trim()}>{busy ? <LoaderCircle className="spin" size={16} /> : <Save size={16} />}{busy ? labels.saving : labels.save}</button>}</DialogActions>
    </form>}
  </Dialog>;
}
