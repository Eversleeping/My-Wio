import type { Workspace } from "../types";
import type { SessionGroup } from "./groups";
import { sessionKey, type AISession, type SessionGroupSummary } from "./types";

export function workspaceTitle(workspace: Workspace) {
  const project = workspace.project_display_name ?? workspace.project_name;
  const identity = [workspace.display_name?.trim(), workspace.branch?.trim(), workspace.path.split(/[\\/]/).filter(Boolean).at(-1), workspace.id].find(value => value && value !== project);
  return identity ? `${project} · ${identity}` : project || workspace.id;
}

export function unifiedGroups(items: AISession[], workspaces: Workspace[], summaries: SessionGroupSummary[], showAll: boolean, search = "") {
  const byID = new Map(workspaces.map(item => [item.id, item]));
  const counts = new Map(summaries.map(group => [group.workspace_id, group]));
  const groups = new Map<string, SessionGroup<AISession>>();
  const query = search.trim().toLocaleLowerCase();
  const add = (id: string, item?: AISession) => {
    const workspace = byID.get(id);
    if (workspace?.project_hidden_at || item?.project_hidden_at) return;
    if (!workspace && !item) return;
    const summary = counts.get(id);
    const group = groups.get(id) ?? {
      id, name: workspace ? workspaceTitle(workspace) : item!.project_name,
      path: workspace?.path ?? item!.path, serverName: workspace?.server_name ?? item!.server_name,
      pinnedAt: workspace?.project_pinned_at ?? summary?.project_pinned_at ?? item?.project_pinned_at,
      count: summary?.matched_count ?? 0, threads: [],
    };
    if (item) { group.context ??= item; group.threads.push(item); group.count = Math.max(group.count, group.threads.length); }
    groups.set(id, group);
  };
  // A group can have matches beyond the loaded cursor window.
  for (const summary of summaries) add(summary.workspace_id);
  for (const item of items) add(item.workspace_id, item);
  if (showAll) for (const workspace of workspaces) {
    if (!query || [workspaceTitle(workspace), workspace.path, workspace.server_name].join(" ").toLocaleLowerCase().includes(query)) add(workspace.id);
  }
  for (const group of groups.values()) group.threads.sort((a, b) => Number(!a.pinned_at) - Number(!b.pinned_at) || (b.pinned_at ?? "").localeCompare(a.pinned_at ?? "") || b.updated_at.localeCompare(a.updated_at) || b.engine.localeCompare(a.engine) || b.id.localeCompare(a.id));
  return [...groups.values()].sort((a, b) => Number(!a.pinnedAt) - Number(!b.pinnedAt) || (b.pinnedAt ?? "").localeCompare(a.pinnedAt ?? "") || (byID.get(a.id)?.project_display_name ?? byID.get(a.id)?.project_name ?? a.context?.project_name ?? "").localeCompare(byID.get(b.id)?.project_display_name ?? byID.get(b.id)?.project_name ?? b.context?.project_name ?? "") || a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
}

export function includeSelection(items: AISession[], selected: AISession | null, archived: boolean, engine: string, search: string, workspaces: Workspace[]) {
  if (!selected || (engine && selected.engine !== engine) || Boolean(selected.archived_at) !== archived || selected.project_hidden_at || workspaces.find(w => w.id === selected.workspace_id)?.project_hidden_at || items.some(item => sessionKey(item) === sessionKey(selected))) return items;
  const workspace = workspaces.find(item => item.id === selected.workspace_id);
  const fields = [selected.title, selected.project_name, selected.server_name, selected.path, selected.id, selected.native_id, workspace && workspaceTitle(workspace)].join(" ").toLocaleLowerCase();
  return !search.trim() || fields.includes(search.trim().toLocaleLowerCase()) ? [...items, selected] : items;
}
