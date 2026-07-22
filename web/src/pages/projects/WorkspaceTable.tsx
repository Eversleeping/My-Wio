import { GitBranch } from "lucide-react";
import type { ReactNode } from "react";
import type { WorkspaceListRecord } from "./model";
import type { ProjectTableSlots } from "./slots";

export interface WorkspaceTableLabels {
  project: string;
  server: string;
  path: string;
  branch: string;
  commit: string;
  state: string;
  actions: string;
  empty: string;
  detached: string;
}

export interface WorkspaceTableProps {
  workspaces: WorkspaceListRecord[];
  labels: WorkspaceTableLabels;
  slots: ProjectTableSlots;
  formatCommit: (value: string) => string;
  renderActions?: (workspace: WorkspaceListRecord) => ReactNode;
}

export function WorkspaceTable({ workspaces, labels, slots, formatCommit, renderActions }: WorkspaceTableProps) {
  const { DataTable, Status } = slots;
  const headers = [labels.project, labels.server, labels.path, labels.branch, labels.commit, labels.state];
  if (renderActions) headers.push(labels.actions);

  return <DataTable headers={headers} empty={labels.empty}>
    {workspaces.map(workspace => <tr key={workspace.id}>
      <td><strong>{workspace.project_name}</strong></td>
      <td>{workspace.server_name}</td>
      <td className="fluid-text-cell"><code className="truncate-code" title={workspace.path}>{workspace.path}</code></td>
      <td><span className="inline"><GitBranch size={13} />{workspace.branch || labels.detached}</span></td>
      <td><code>{formatCommit(workspace.commit_sha)}</code></td>
      <td><Status value={workspace.status && workspace.status !== "ready" ? workspace.status : workspace.dirty ? "dirty" : "clean"} /></td>
      {renderActions && <td><div className="row-actions">{renderActions(workspace)}</div></td>}
    </tr>)}
  </DataTable>;
}
