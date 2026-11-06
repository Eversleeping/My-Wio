import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";

type DataCacheEntry = { data: unknown; dependency: unknown };

const dataCache = new Map<string, DataCacheEntry>();

export function useThrottledValue<T>(value: T, minimumInterval: number) {
  const [throttled, setThrottled] = useState(value);
  const lastAppliedAt = useRef(Date.now());
  useEffect(() => {
    if (Object.is(value, throttled)) return;
    const remaining = minimumInterval - (Date.now() - lastAppliedAt.current);
    const apply = () => {
      lastAppliedAt.current = Date.now();
      setThrottled(value);
    };
    if (remaining <= 0) { apply(); return; }
    const timer = window.setTimeout(apply, remaining);
    return () => window.clearTimeout(timer);
  }, [minimumInterval, throttled, value]);
  return throttled;
}

export function clearDataCacheMatching(matches: (path: string) => boolean) {
  for (const path of dataCache.keys()) {
    if (matches(path)) dataCache.delete(path);
  }
}

export function useData<T>(path: string | null, dependency: unknown, cache = false) {
  const [result, setResult] = useState<{ path: string | null; data: T | null }>({ path: null, data: null });
  const [failure, setFailure] = useState<{ path: string | null; text: string }>({ path: null, text: "" });
  const [settledPath, setSettledPath] = useState<string | null>(null);
  const [version, setVersion] = useState(0);
  const reload = useCallback(() => setVersion(value => value + 1), []);
  const request = useRef<((dependency: unknown, force: boolean) => void) | null>(null);

  useEffect(() => {
    if (!path) { setSettledPath(null); return; }
    let stopped = false;
    let controller: AbortController | null = null;
    let running = false;
    let pending: { dependency: unknown } | null = null;
    const refresh = (dependency: unknown, force: boolean) => {
      // Let the current response paint, then catch up once with the latest
      // invalidation. Repeated events must not starve a slow initial load.
      if (running) { pending = { dependency }; return; }
      const cached = cache ? dataCache.get(path) : undefined;
      if (!force && cached && Object.is(cached.dependency, dependency)) {
        setFailure({ path, text: "" });
        setSettledPath(path);
        return;
      }
      running = true;
      controller = new AbortController();
      api<T>(path, { signal: controller.signal }).then(value => {
        if (stopped) return;
        if (cache) dataCache.set(path, { data: value, dependency });
        setResult({ path, data: value });
        setFailure({ path, text: "" });
      }).catch(error => {
        if (stopped) return;
        setFailure({ path, text: error instanceof Error ? error.message : "Request failed" });
      }).finally(() => {
        if (stopped) return;
        running = false;
        setSettledPath(path);
        if (pending) {
          const next = pending;
          pending = null;
          refresh(next.dependency, true);
        }
      });
    };
    request.current = refresh;
    return () => {
      stopped = true;
      controller?.abort();
      request.current = null;
    };
  }, [path, cache]);

  useEffect(() => { if (path) request.current?.(dependency, version !== 0); }, [path, dependency, version, cache]);

  const cached = cache && path ? dataCache.get(path) : undefined;
  const cachedData = cached && Object.is(cached.dependency, dependency) ? cached.data as T : null;
  const data = result.path === path ? result.data : cachedData;
  const error = failure.path === path ? failure.text : "";
  return { data, error, loading: Boolean(path) && data === null && settledPath !== path, reload };
}
