import type { Workspace } from "../types";
import type { SessionGroupSummary, SessionSummary } from "./types";

export type SessionGroup<T> = {
  id: string; name: string; path: string; serverName: string;
  pinnedAt?: string | null; count: number; context?: T; threads: T[];
};

export function groupSessions<T extends SessionSummary>(threads: T[], workspaces: Workspace[], summaries: SessionGroupSummary[] = []) {
  const byID = new Map(workspaces.map(item => [item.id, item]));
  const result = new Map<string, SessionGroup<T>>();
  const groupFor = (id: string, item?: T) => {
    const existing = result.get(id); if (existing) return existing;
    const workspace = byID.get(id);
    if (!workspace && !item) return null;
    const path = workspace?.path || item!.path;
    const project = item?.project_name ?? workspace!.project_name;
    const identity = [workspace?.display_name?.trim(), workspace?.branch?.trim(), path.split(/[\\/]/).filter(Boolean).at(-1)].find(value => value && value !== project);
    const summary = summaries.find(group => group.workspace_id === id);
    const group: SessionGroup<T> = { id, name: identity ? `${project} · ${identity}` : project, path, serverName: item?.server_name ?? workspace!.server_name, pinnedAt: summary?.project_pinned_at ?? item?.project_pinned_at, count: summary?.matched_count ?? 0, threads: [] };
    result.set(id, group); return group;
  };
  // Summaries include matches beyond the current page. Do not present them as
  // empty workspaces while their sessions have not yet been loaded.
  for (const summary of summaries) groupFor(summary.workspace_id);
  for (const item of threads) {
    if (item.project_hidden_at) { result.delete(item.workspace_id); continue; }
    const group = groupFor(item.workspace_id, item)!;
    group.context ??= item; group.threads.push(item);
    if (!summaries.some(summary => summary.workspace_id === item.workspace_id)) group.count = group.threads.length;
  }
  for (const group of result.values()) group.threads.sort((a, b) => Number(!a.pinned_at) - Number(!b.pinned_at) || (b.pinned_at ?? "").localeCompare(a.pinned_at ?? ""));
  return [...result.values()].sort((a, b) => Number(!a.pinnedAt) - Number(!b.pinnedAt) || (b.pinnedAt ?? "").localeCompare(a.pinnedAt ?? ""));
}

// A direct link may point outside the loaded cursor window. Keep its context
// visible without leaking a selection into another archive/search view.
export function withSelectedSession<T extends SessionSummary>(items: T[], selected: T | null, archived: boolean, search: string): T[] {
  if (!selected || items.some(item => item.id === selected.id && item.engine === selected.engine) || Boolean(selected.archived_at) !== archived || selected.project_hidden_at) return items;
  const query = search.trim().toLocaleLowerCase();
  if (query && ![selected.title, selected.project_name, selected.server_name, selected.path, selected.id, selected.native_id].join(" ").toLocaleLowerCase().includes(query)) return items;
  return [...items, selected];
}
