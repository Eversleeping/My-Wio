import { type Thread, type Workspace } from "../types";
import { useData } from "../useData";
import { useState, useRef, useEffect, useMemo, useCallback } from "react";
import { api } from "../api";
import { message } from "../text";

type ThreadListPage = { items: Thread[]; has_more: boolean; next: number | null };

const threadListPageSize = 50;

export function mergeThreads(...pages: Thread[][]) {
  const merged: Thread[] = [];
  const seen = new Set<string>();
  for (const page of pages) for (const thread of page) {
    if (seen.has(thread.id)) continue;
    seen.add(thread.id);
    merged.push(thread);
  }
  return merged;
}

export function useThreadList(archived: boolean, realtime: unknown) {
  const firstPath = archived ? `/threads?archived=true&limit=${threadListPageSize}` : `/threads?limit=${threadListPageSize}`;
  const firstPage = useData<ThreadListPage>(firstPath, realtime);
  const [tail, setTail] = useState<Thread[]>([]);
  const [tailLoaded, setTailLoaded] = useState(false);
  const [tailNext, setTailNext] = useState<number | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadError, setLoadError] = useState("");
  const requestRef = useRef(0);
  const tailRef = useRef(tail);
  const tailLoadedRef = useRef(tailLoaded);
  tailRef.current = tail;
  tailLoadedRef.current = tailLoaded;
  useEffect(() => () => { requestRef.current += 1; }, []);
  const items = useMemo(() => mergeThreads(firstPage.data?.items ?? [], tail), [firstPage.data?.items, tail]);
  const next = tailLoaded ? tailNext : firstPage.data?.next ?? null;
  const hasMore = tailLoaded ? tailNext !== null : Boolean(firstPage.data?.has_more);
  const loadMore = useCallback(async () => {
    if (loadingMore || next === null) return;
    const request = ++requestRef.current;
    setLoadingMore(true);
    setLoadError("");
    try {
      const prefix = archived ? "/threads?archived=true&" : "/threads?";
      const page = await api<ThreadListPage>(`${prefix}limit=${threadListPageSize}&offset=${next}`);
      if (request !== requestRef.current) return;
      setTail(current => mergeThreads(current, page.items));
      setTailLoaded(true);
      setTailNext(page.next);
    } catch (error) {
      if (request === requestRef.current) setLoadError(message(error));
    } finally {
      if (request === requestRef.current) setLoadingMore(false);
    }
  }, [archived, loadingMore, next]);
  useEffect(() => {
    if (!firstPage.data || !tailLoadedRef.current) return;
    const loadedTailCount = tailRef.current.length;
    if (loadedTailCount === 0) return;
    const request = ++requestRef.current;
    setLoadingMore(true);
    setLoadError("");
    void (async () => {
      try {
        const refreshed: Thread[] = [];
        let offset: number | null = threadListPageSize;
        while (offset !== null && refreshed.length < loadedTailCount) {
          const prefix = archived ? "/threads?archived=true&" : "/threads?";
          const page: ThreadListPage = await api<ThreadListPage>(`${prefix}limit=${threadListPageSize}&offset=${offset}`);
          if (request !== requestRef.current) return;
          refreshed.push(...page.items);
          offset = page.next;
        }
        if (request !== requestRef.current) return;
        setTail(mergeThreads(refreshed));
        setTailNext(offset);
      } catch (error) {
        if (request === requestRef.current) setLoadError(message(error));
      } finally {
        if (request === requestRef.current) setLoadingMore(false);
      }
    })();
  }, [archived, firstPage.data]);
  const reload = useCallback(() => firstPage.reload(), [firstPage.reload]);
  return { data: firstPage.data ? items : null, error: firstPage.error, loading: firstPage.loading, reload, hasMore, loadingMore, loadError, loadMore };
}

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
