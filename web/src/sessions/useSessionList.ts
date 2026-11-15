import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { message } from "../text";
import { useData } from "../useData";
import { sessionListPath } from "./api";
import type { AISession, SessionFilter, SessionListPage, SessionSummary } from "./types";

export function mergeSessions<T extends SessionSummary>(...pages: T[][]) {
  const seen = new Set<string>();
  return pages.flat().filter(item => {
    const key = `${item.engine ?? ""}:${item.id}`;
    if (seen.has(key)) return false;
    seen.add(key); return true;
  });
}

// Refresh every loaded window from the new head cursor. An old tail cursor is
// never attached to a refreshed head or reused after changing the filter.
export function useSessionList<T extends SessionSummary = AISession>(filter: Omit<SessionFilter, "cursor">, revision: unknown) {
  const path = sessionListPath(filter);
  const firstPage = useData<SessionListPage<T>>(path, revision);
  const [tail, setTail] = useState<{ path: string; items: T[]; loaded: boolean; next: string | null }>({ path, items: [], loaded: false, next: null });
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadError, setLoadError] = useState("");
  const requestRef = useRef(0);
  const tailRef = useRef(tail); tailRef.current = tail;
  const filterRef = useRef(filter); filterRef.current = filter;
  useEffect(() => {
    setLoadingMore(false); setLoadError("");
    return () => { requestRef.current += 1; };
  }, [path]);
  const currentTail = tail.path === path ? tail : null;
  const items = useMemo(() => mergeSessions(firstPage.data?.items ?? [], currentTail?.items ?? []), [firstPage.data, currentTail]);
  const next = currentTail?.loaded ? currentTail.next : firstPage.data?.next_cursor ?? null;
  const loadMore = useCallback(async () => {
    if (loadingMore || !next) return;
    const request = ++requestRef.current;
    setLoadingMore(true); setLoadError("");
    try {
      const page = await api<SessionListPage<T>>(sessionListPath({ ...filterRef.current, cursor: next }));
      if (request !== requestRef.current) return;
      setTail(current => ({ path, items: mergeSessions(current.path === path ? current.items : [], page.items), loaded: true, next: page.next_cursor }));
    } catch (error) {
      if (request === requestRef.current) setLoadError(message(error));
    } finally { if (request === requestRef.current) setLoadingMore(false); }
  }, [loadingMore, next, path]);
  useEffect(() => {
    const previous = tailRef.current;
    if (!firstPage.data || previous.path !== path || !previous.loaded) return;
    const request = ++requestRef.current;
    setLoadingMore(true); setLoadError("");
    void (async () => {
      try {
        const refreshed: T[] = [];
        let cursor = firstPage.data!.next_cursor;
        while (cursor && refreshed.length < previous.items.length) {
          const page = await api<SessionListPage<T>>(sessionListPath({ ...filterRef.current, cursor }));
          if (request !== requestRef.current) return;
          refreshed.push(...page.items);
          cursor = page.next_cursor;
        }
        if (request === requestRef.current) setTail({ path, items: mergeSessions(refreshed), loaded: true, next: cursor });
      } catch (error) {
        if (request === requestRef.current) setLoadError(message(error));
      } finally { if (request === requestRef.current) setLoadingMore(false); }
    })();
  }, [firstPage.data, path]);
  return { data: firstPage.data ? items : null, groups: firstPage.data?.groups ?? [], error: firstPage.error, loading: firstPage.loading, reload: firstPage.reload, hasMore: next !== null, loadingMore, loadError, loadMore };
}
