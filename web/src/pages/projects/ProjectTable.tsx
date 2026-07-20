import { EyeOff } from "lucide-react";
import type { ReactNode } from "react";
import { deriveProjectLifecycleState, type ProjectLifecycleState, type ProjectListRecord } from "./model";
import type { ProjectTableSlots } from "./slots";

export interface ProjectTableLabels {
  project: string;
  remote: string;
  workspaces: string;
  status: string;
  updated: string;
  actions: string;
  empty: string;
  local: string;
  hidden: string;
  targetServer: (server: string) => string;
  awaitingWorkspace: string;
}

export interface ProjectTableProps {
  projects: ProjectListRecord[];
  labels: ProjectTableLabels;
  slots: ProjectTableSlots;
  formatTime: (value: string) => string;
  formatImportMessage?: (project: ProjectListRecord) => string;
  renderActions?: (project: ProjectListRecord, state: ProjectLifecycleState) => ReactNode;
  onSelect?: (project: ProjectListRecord) => void;
}

export function ProjectTable({ projects, labels, slots, formatTime, formatImportMessage, renderActions, onSelect }: ProjectTableProps) {
  const { DataTable, Status } = slots;
  return <DataTable headers={[labels.project, labels.remote, labels.workspaces, labels.status, labels.updated, labels.actions]} empty={labels.empty}>
    {projects.map(project => {
      const state = deriveProjectLifecycleState(project);
      const importMessage = formatImportMessage?.(project) ?? (project.provision_error || project.import_message);
      return <tr key={project.id} className={project.hidden_at ? "project-hidden-row" : ""}>
        <td><div className="cell-main"><span className="inline"><button type="button" className="table-link" onClick={() => onSelect?.(project)}>{project.name}</button>{project.hidden_at && <span className="status-tag neutral"><EyeOff size={12} />{labels.hidden}</span>}</span>{project.import_server_name && <small>{labels.targetServer(project.import_server_name)}</small>}</div></td>
        <td><code className="truncate-code">{project.remote_url || labels.local}</code></td>
        <td>{project.workspace_count}</td>
        <td><div className="project-import-state"><Status value={state} />{state === "syncing" && <small className="project-import-message syncing">{labels.awaitingWorkspace}</small>}{(state === "failed" || state === "partial" || state === "deletion-failed") && importMessage && <small className="project-import-message" title={importMessage}>{importMessage}</small>}</div></td>
        <td>{formatTime(project.updated_at)}</td>
        <td><div className="row-actions">{renderActions?.(project, state) ?? <span className="muted">-</span>}</div></td>
      </tr>;
    })}
  </DataTable>;
}
