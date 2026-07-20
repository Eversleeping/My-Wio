import { AlertTriangle, Copy, FolderInput, LoaderCircle, Pencil, Server as ServerIcon, ShieldCheck, Trash2 } from "lucide-react";
import { useEffect, useState, type ComponentType, type FormEvent } from "react";
import type { Server, Workspace, WorkspaceDeletionPlan } from "../../types";
import type { DialogSlotProps } from "./slots";

type WorkspaceManagerTab = "rename" | "move" | "copy" | "delete";
export type WorkspaceDeletionMode = "metadata" | "files";

export interface WorkspaceManagerLabels {
  title: string;
  rename: string;
  move: string;
  copy: string;
  delete: string;
  displayName: string;
  currentPath: string;
  targetPath: string;
  targetServer: string;
  sameServer: string;
  managedOnly: string;
  save: string;
  moving: string;
  copying: string;
  loadingPlan: string;
  metadataOnly: string;
  deleteFiles: string;
  metadataDescription: string;
  filesDescription: string;
  dirty: string;
  activeOperations: string;
  threads: string;
  childWorkspaces: string;
  force: string;
  blockers: string;
  noBlockers: string;
  confirmLabel: string;
  confirmPlaceholder: string;
  deleting: string;
  cancel: string;
}

export function WorkspaceManagerDialog({ open, workspace, servers, plan, planLoading, busy, error, labels, Dialog, onClose, onRename, onMove, onCopy, onLoadDeletionPlan, onDelete }: {
  open: boolean;
  workspace: Workspace | null;
  servers: Server[];
  plan: WorkspaceDeletionPlan | null;
  planLoading: boolean;
  busy: boolean;
  error: string;
  labels: WorkspaceManagerLabels;
  Dialog: ComponentType<DialogSlotProps>;
  onClose: () => void;
  onRename: (name: string) => void | Promise<void>;
  onMove: (path: string) => void | Promise<void>;
  onCopy: (serverID: string, path: string) => void | Promise<void>;
  onLoadDeletionPlan: (force: boolean) => void | Promise<void>;
  onDelete: (mode: WorkspaceDeletionMode, force: boolean) => void | Promise<void>;
}) {
  const [tab, setTab] = useState<WorkspaceManagerTab>("rename");
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [targetServerID, setTargetServerID] = useState("");
  const [deleteMode, setDeleteMode] = useState<WorkspaceDeletionMode>("metadata");
  const [force, setForce] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  useEffect(() => {
    if (!workspace) return;
    setTab("rename");
    setName(workspace.display_name || workspace.project_name);
    setPath("");
    setTargetServerID(workspace.server_id);
    setDeleteMode("metadata");
    setForce(false);
    setConfirmation("");
  }, [workspace?.id, open]);
  const managed = workspace?.management_mode === "managed";
  const workspaceName = workspace?.display_name || workspace?.project_name || "";
  const selectTab = (next: WorkspaceManagerTab) => {
    setTab(next);
    if (next === "delete") void onLoadDeletionPlan(force);
  };
  const loadWithForce = (next: boolean) => {
    setForce(next);
    if (tab === "delete") void onLoadDeletionPlan(next);
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!workspace || busy) return;
    if (tab === "rename" && name.trim()) void onRename(name.trim());
    if (tab === "move" && managed && path.trim()) void onMove(path.trim());
    if (tab === "copy" && managed && path.trim() && targetServerID) void onCopy(targetServerID, path.trim());
    if (tab === "delete" && plan && confirmation === workspaceName && (deleteMode === "metadata" ? plan.can_remove_record : plan.can_delete_files)) void onDelete(deleteMode, force);
  };
  const deleteAllowed = deleteMode === "metadata" ? plan?.can_remove_record : plan?.can_delete_files;
  const modeBlockers = deleteMode === "metadata" ? (plan?.record_blockers ?? plan?.blockers ?? []) : (plan?.file_blockers ?? plan?.blockers ?? []);
  return <Dialog open={open} title={workspace ? `${labels.title}: ${workspace.display_name || workspace.project_name}` : labels.title} onClose={() => { if (!busy) onClose(); }} wide>
    <form className="workspace-manager" onSubmit={submit}>
      {error && <div className="error-banner" role="alert"><AlertTriangle size={16} />{error}</div>}
      <div className="segmented-control workspace-manager-tabs" role="tablist">
        <button type="button" role="tab" aria-selected={tab === "rename"} className={tab === "rename" ? "active" : ""} onClick={() => selectTab("rename")}><Pencil size={14} />{labels.rename}</button>
        <button type="button" role="tab" aria-selected={tab === "move"} className={tab === "move" ? "active" : ""} disabled={!managed} title={!managed ? labels.managedOnly : ""} onClick={() => selectTab("move")}><FolderInput size={14} />{labels.move}</button>
        <button type="button" role="tab" aria-selected={tab === "copy"} className={tab === "copy" ? "active" : ""} disabled={!managed} title={!managed ? labels.managedOnly : ""} onClick={() => selectTab("copy")}><Copy size={14} />{labels.copy}</button>
        <button type="button" role="tab" aria-selected={tab === "delete"} className={tab === "delete" ? "active" : ""} onClick={() => selectTab("delete")}><Trash2 size={14} />{labels.delete}</button>
      </div>
      {workspace && tab === "rename" && <label className="field"><span>{labels.displayName}</span><input autoFocus value={name} disabled={busy} onChange={event => setName(event.target.value)} required /></label>}
      {workspace && tab === "move" && <><ReadOnlyPath label={labels.currentPath} path={workspace.path} /><label className="field"><span>{labels.targetPath}</span><input autoFocus value={path} disabled={busy} onChange={event => setPath(event.target.value)} required /></label></>}
      {workspace && tab === "copy" && <><ReadOnlyPath label={labels.currentPath} path={workspace.path} /><div className="form-grid"><label className="field"><span>{labels.targetServer}</span><select value={targetServerID} disabled={busy} onChange={event => setTargetServerID(event.target.value)} required>{servers.map(server => <option key={server.id} value={server.id} disabled={server.status !== "online"}>{server.id === workspace.server_id ? labels.sameServer : server.name}{server.status !== "online" ? " (offline)" : ""}</option>)}</select></label><label className="field"><span>{labels.targetPath}</span><input value={path} disabled={busy} onChange={event => setPath(event.target.value)} required /></label></div></>}
      {workspace && tab === "delete" && <div className="workspace-delete-panel">
        <div className="segmented-control deletion-mode" role="tablist"><button type="button" role="tab" aria-selected={deleteMode === "metadata"} className={deleteMode === "metadata" ? "active" : ""} onClick={() => setDeleteMode("metadata")}><ServerIcon size={14} />{labels.metadataOnly}</button><button type="button" role="tab" aria-selected={deleteMode === "files"} className={deleteMode === "files" ? "active" : ""} disabled={!managed} onClick={() => setDeleteMode("files")}><Trash2 size={14} />{labels.deleteFiles}</button></div>
        <p className="deletion-description">{deleteMode === "metadata" ? labels.metadataDescription : labels.filesDescription}</p>
        {planLoading && <div className="empty-state"><LoaderCircle className="spin" size={17} />{labels.loadingPlan}</div>}
        {plan && !planLoading && <><div className="deletion-summary-grid workspace"><DeletionValue label={labels.dirty} value={plan.dirty ? 1 : 0} danger={plan.dirty} /><DeletionValue label={labels.activeOperations} value={plan.active_operations} danger={plan.active_operations > 0} /><DeletionValue label={labels.threads} value={plan.thread_count} danger={plan.thread_count > 0} /><DeletionValue label={labels.childWorkspaces} value={plan.child_workspaces} danger={plan.child_workspaces > 0} /></div><label className="toggle-row force-toggle"><input type="checkbox" checked={force} disabled={busy} onChange={event => loadWithForce(event.target.checked)} /><AlertTriangle size={15} /><span>{labels.force}</span></label><div className={`deletion-blockers ${modeBlockers.length ? "blocked" : "ready"}`}><strong>{labels.blockers}</strong>{modeBlockers.length ? modeBlockers.map(blocker => <span key={blocker}><AlertTriangle size={14} />{blocker}</span>) : <span><ShieldCheck size={14} />{labels.noBlockers}</span>}</div><label className="field"><span>{labels.confirmLabel}</span><input value={confirmation} disabled={busy || !deleteAllowed} onChange={event => setConfirmation(event.target.value)} placeholder={labels.confirmPlaceholder.replace("{name}", workspaceName)} autoComplete="off" /></label></>}
      </div>}
      <div className="dialog-actions"><button type="button" className="secondary-button" disabled={busy} onClick={onClose}>{labels.cancel}</button><button className={tab === "delete" ? "secondary-button danger" : "primary-button"} disabled={busy || (tab === "rename" && !name.trim()) || ((tab === "move" || tab === "copy") && !path.trim()) || (tab === "delete" && (!deleteAllowed || confirmation !== workspaceName))}>{busy ? <LoaderCircle className="spin" size={16} /> : tab === "rename" ? <Pencil size={16} /> : tab === "move" ? <FolderInput size={16} /> : tab === "copy" ? <Copy size={16} /> : <Trash2 size={16} />}{busy ? tab === "move" ? labels.moving : tab === "copy" ? labels.copying : labels.deleting : tab === "rename" ? labels.save : tab === "move" ? labels.move : tab === "copy" ? labels.copy : labels.delete}</button></div>
    </form>
  </Dialog>;
}

function ReadOnlyPath({ label, path }: { label: string; path: string }) { return <label className="field"><span>{label}</span><code className="readonly-path">{path}</code></label>; }
function DeletionValue({ label, value, danger }: { label: string; value: number; danger: boolean }) { return <div className={danger ? "danger" : ""}><small>{label}</small><strong>{value}</strong></div>; }
