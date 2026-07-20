import { AlertTriangle, FolderPlus, GitBranch, LoaderCircle, Search, Server as ServerIcon } from "lucide-react";
import type { FormEvent, ReactNode } from "react";
import {
  toCreateProjectRequest,
  validateCreateProjectForm,
  type BlankProjectRemoteMode,
  type CreateProjectFormValue,
  type CreateProjectMode,
  type CreateProjectRequest,
  type CreateProjectValidationLabels,
  type ProjectServerOption
} from "./model";
import type { CreateProjectDialogSlots } from "./slots";

export interface CreateProjectDialogLabels extends CreateProjectValidationLabels {
  title: string;
  modeLabel: string;
  blankMode: string;
  cloneMode: string;
  discoverMode: string;
  projectName: string;
  targetServer: string;
  selectServer: string;
  offline: string;
  destination: string;
  optional: string;
  initialBranch: string;
  remoteSetup: string;
  remoteNone: string;
  remoteExisting: string;
  remoteCreate: string;
  remoteURL: string;
  remoteProvider: string;
  remoteNamespace: string;
  remoteRepository: string;
  remoteVisibility: string;
  comingSoon: string;
  visibilityPrivate: string;
  visibilityInternal: string;
  visibilityPublic: string;
  initializeReadme: string;
  existingServer: string;
  cancel: string;
  working: string;
  create: string;
  clone: string;
  discover: string;
}

export interface CreateProjectDialogProps {
  open: boolean;
  value: CreateProjectFormValue;
  servers: ProjectServerOption[];
  labels: CreateProjectDialogLabels;
  slots: CreateProjectDialogSlots;
  busy?: boolean;
  error?: string;
  onChange: (value: CreateProjectFormValue) => void;
  onClose: () => void;
  onSubmit: (request: CreateProjectRequest) => void | Promise<void>;
}

export function CreateProjectDialog({
  open,
  value,
  servers,
  labels,
  slots,
  busy = false,
  error = "",
  onChange,
  onClose,
  onSubmit
}: CreateProjectDialogProps) {
  const { Dialog, Field, DialogActions } = slots;
  const errors = validateCreateProjectForm(value, labels);
  const invalid = Object.keys(errors).length > 0;
  const update = <Key extends keyof CreateProjectFormValue>(key: Key, next: CreateProjectFormValue[Key]) => {
    onChange({ ...value, [key]: next });
  };
  const setMode = (mode: CreateProjectMode) => update("mode", mode);
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy || invalid) return;
    void onSubmit(toCreateProjectRequest(value));
  };

  return <Dialog open={open} title={labels.title} onClose={() => { if (!busy) onClose(); }} wide>
    <form onSubmit={submit}>
      {error && <div className="error-banner" role="alert"><AlertTriangle size={16} />{error}</div>}
      <div className="segmented-control project-create-modes" role="tablist" aria-label={labels.modeLabel}>
        <ModeButton active={value.mode === "blank"} disabled={busy} icon={<FolderPlus size={15} />} label={labels.blankMode} onClick={() => setMode("blank")} />
        <ModeButton active={value.mode === "clone"} disabled={busy} icon={<GitBranch size={15} />} label={labels.cloneMode} onClick={() => setMode("clone")} />
        <ModeButton active={value.mode === "discover"} disabled={busy} icon={<ServerIcon size={15} />} label={labels.discoverMode} onClick={() => setMode("discover")} />
      </div>

      {value.mode === "blank" && <BlankProjectFields value={value} labels={labels} servers={servers} busy={busy} Field={Field} update={update} />}
      {value.mode === "clone" && <CloneProjectFields value={value} labels={labels} servers={servers} busy={busy} Field={Field} update={update} />}
      {value.mode === "discover" && <ServerField label={labels.existingServer} value={value.serverID} servers={servers} labels={labels} busy={busy} Field={Field} onChange={serverID => update("serverID", serverID)} />}

      <DialogActions>
        <button type="button" className="secondary-button" disabled={busy} onClick={onClose}>{labels.cancel}</button>
        <button className="primary-button" disabled={busy || invalid}>
          {busy ? <LoaderCircle className="spin" size={16} /> : submitIcon(value.mode)}
          {busy ? labels.working : submitLabel(value.mode, labels)}
        </button>
      </DialogActions>
    </form>
  </Dialog>;
}

function BlankProjectFields({ value, labels, servers, busy, Field, update }: FieldGroupProps) {
  return <>
    <div className="form-grid">
      <Field label={labels.projectName}><input autoFocus value={value.name} disabled={busy} onChange={event => update("name", event.target.value)} required /></Field>
      <ServerField label={labels.targetServer} value={value.serverID} servers={servers} labels={labels} busy={busy} Field={Field} onChange={serverID => update("serverID", serverID)} />
    </div>
    <div className="form-grid">
      <Field label={labels.destination}><input value={value.destination} disabled={busy} onChange={event => update("destination", event.target.value)} placeholder={labels.optional} /></Field>
      <Field label={labels.initialBranch}><input value={value.initialBranch} disabled={busy} onChange={event => update("initialBranch", event.target.value)} placeholder="main" required /></Field>
    </div>
    <Field label={labels.remoteSetup}>
      <select value={value.remoteMode} disabled={busy} onChange={event => update("remoteMode", event.target.value as BlankProjectRemoteMode)}>
        <option value="none">{labels.remoteNone}</option>
        <option value="existing" disabled>{labels.remoteExisting} ({labels.comingSoon})</option>
        <option value="create" disabled>{labels.remoteCreate} ({labels.comingSoon})</option>
      </select>
    </Field>
    <label className="inline"><input type="checkbox" style={{ width: "auto", minHeight: "auto" }} checked={value.initializeReadme} disabled={busy} onChange={event => update("initializeReadme", event.target.checked)} /><strong>{labels.initializeReadme}</strong></label>
  </>;
}

function CloneProjectFields({ value, labels, servers, busy, Field, update }: FieldGroupProps) {
  return <>
    <Field label={labels.remoteURL}><input autoFocus value={value.remoteURL} disabled={busy} onChange={event => update("remoteURL", event.target.value)} placeholder="https://git.example.com/team/project.git" required /></Field>
    <div className="form-grid">
      <Field label={labels.projectName}><input value={value.name} disabled={busy} onChange={event => update("name", event.target.value)} placeholder={labels.optional} /></Field>
      <ServerField label={labels.targetServer} value={value.serverID} servers={servers} labels={labels} busy={busy} Field={Field} onChange={serverID => update("serverID", serverID)} />
    </div>
    <Field label={labels.destination}><input value={value.destination} disabled={busy} onChange={event => update("destination", event.target.value)} placeholder={labels.optional} /></Field>
  </>;
}

type FieldComponent = CreateProjectDialogSlots["Field"];
type UpdateForm = <Key extends keyof CreateProjectFormValue>(key: Key, value: CreateProjectFormValue[Key]) => void;

interface FieldGroupProps {
  value: CreateProjectFormValue;
  labels: CreateProjectDialogLabels;
  servers: ProjectServerOption[];
  busy: boolean;
  Field: FieldComponent;
  update: UpdateForm;
}

function ServerField({ label, value, servers, labels, busy, Field, onChange }: {
  label: string;
  value: string;
  servers: ProjectServerOption[];
  labels: CreateProjectDialogLabels;
  busy: boolean;
  Field: FieldComponent;
  onChange: (serverID: string) => void;
}) {
  return <Field label={label}><select value={value} disabled={busy} onChange={event => onChange(event.target.value)} required><option value="">{labels.selectServer}</option>{servers.map(server => <option key={server.id} value={server.id} disabled={server.status !== "online"}>{server.name}{server.status !== "online" ? ` (${labels.offline})` : ""}</option>)}</select></Field>;
}

function ModeButton({ active, disabled, icon, label, onClick }: { active: boolean; disabled: boolean; icon: ReactNode; label: string; onClick: () => void }) {
  return <button type="button" role="tab" aria-selected={active} className={active ? "active" : ""} disabled={disabled} onClick={onClick}>{icon}{label}</button>;
}

function submitIcon(mode: CreateProjectMode) {
  if (mode === "blank") return <FolderPlus size={16} />;
  if (mode === "clone") return <GitBranch size={16} />;
  return <Search size={16} />;
}

function submitLabel(mode: CreateProjectMode, labels: CreateProjectDialogLabels) {
  if (mode === "blank") return labels.create;
  if (mode === "clone") return labels.clone;
  return labels.discover;
}
