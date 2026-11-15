import { type Thread,type Workspace } from "../../../types";
export { mergeSessions as mergeThreads } from "../../useSessionList";
export function groupThreadsByWorkspace(threads: Thread[], workspaces: Workspace[]) {
  const workspacesByID = new Map(workspaces.map(workspace => [workspace.id, workspace]));
  const groups = new Map<string, ThreadGroup>();
  for (const thread of threads) {
    const workspace = workspacesByID.get(thread.workspace_id);
    const path = workspace?.path || thread.path;
    const pathName = path.split(/[\\/]/).filter(Boolean).at(-1) || path;
    const workspaceName = workspace?.display_name?.trim();
    const branch = workspace?.branch?.trim();
    const identity = [workspaceName, branch, pathName].find(value => value && value !== thread.project_name) || pathName || thread.workspace_id;
    const group = groups.get(thread.workspace_id) ?? { workspaceID: thread.workspace_id, projectID: thread.project_id, serverID: thread.server_id, serverName: thread.server_name, projectName: thread.project_name, label: `${thread.project_name} · ${identity}`, path, pinnedAt: thread.project_pinned_at, threads: [] };
    group.threads.push(thread);
    groups.set(thread.workspace_id, group);
  }
  const pinnedFirst = (left: string | null, right: string | null) => left && !right ? -1 : !left && right ? 1 : left && right ? right.localeCompare(left) : 0;
  for (const group of groups.values()) group.threads.sort((left, right) => pinnedFirst(left.pinned_at, right.pinned_at) || right.updated_at.localeCompare(left.updated_at));
  return Array.from(groups.values()).sort((left, right) => pinnedFirst(left.pinnedAt, right.pinnedAt) || left.projectName.localeCompare(right.projectName) || left.label.localeCompare(right.label));
}

export type ThreadGroup = { workspaceID: string; projectID: string; serverID: string; serverName: string; projectName: string; label: string; path: string; pinnedAt: string | null; threads: Thread[] };
