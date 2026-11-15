import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { message } from "../text";
import type { AuditEntry } from "../types";
import { readResource, useResource } from "./resources";
import { auditListPageSize, settings, type AuditListPage } from "./settings";

function mergeAuditEntries(...pages: AuditEntry[][]) {
  const merged: AuditEntry[] = [];
  const seen = new Set<string>();
  for (const page of pages) for (const entry of page) {
    if (seen.has(entry.id)) continue;
    seen.add(entry.id);
    merged.push(entry);
  }
  return merged;
}

export function useAuditList() {
  const firstPage = useResource(settings.audit());
  const [tail, setTail] = useState<AuditEntry[]>([]);
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
  const items = useMemo(() => mergeAuditEntries(firstPage.data?.items ?? [], tail), [firstPage.data?.items, tail]);
  const next = tailLoaded ? tailNext : firstPage.data?.next ?? null;
  const hasMore = tailLoaded ? tailNext !== null : Boolean(firstPage.data?.has_more);
  const loadMore = useCallback(async () => {
    if (loadingMore || next === null) return;
    const request = ++requestRef.current;
    setLoadingMore(true);
    setLoadError("");
    try {
      const page = await readResource(settings.audit(next));
      if (request !== requestRef.current) return;
      setTail(current => mergeAuditEntries(current, page.items));
      setTailLoaded(true);
      setTailNext(page.next);
    } catch (error) {
      if (request === requestRef.current) setLoadError(message(error));
    } finally {
      if (request === requestRef.current) setLoadingMore(false);
    }
  }, [loadingMore, next]);
  useEffect(() => {
    if (!firstPage.data || !tailLoadedRef.current) return;
    const loadedTailCount = tailRef.current.length;
    if (loadedTailCount === 0) return;
    const request = ++requestRef.current;
    setLoadingMore(true);
    setLoadError("");
    void (async () => {
      try {
        const refreshed: AuditEntry[] = [];
        let offset: number | null = auditListPageSize;
        while (offset !== null && refreshed.length < loadedTailCount) {
          const page: AuditListPage = await readResource(settings.audit(offset));
          if (request !== requestRef.current) return;
          refreshed.push(...page.items);
          offset = page.next;
        }
        if (request !== requestRef.current) return;
        setTail(mergeAuditEntries(refreshed));
        setTailNext(offset);
      } catch (error) {
        if (request === requestRef.current) setLoadError(message(error));
      } finally {
        if (request === requestRef.current) setLoadingMore(false);
      }
    })();
  }, [firstPage.data]);
  const reload = useCallback(() => firstPage.reload(), [firstPage.reload]);
  return { data: firstPage.data ? items : null, error: firstPage.error, loading: firstPage.loading, reload, hasMore, loadingMore, loadError, loadMore };
}



