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

  useEffect(() => {
    if (!path) { setSettledPath(null); return; }
    const cached = cache ? dataCache.get(path) : undefined;
    if (version === 0 && cached && Object.is(cached.dependency, dependency)) {
      setFailure({ path, text: "" });
      setSettledPath(path);
      return;
    }
    const controller = new AbortController();
    api<T>(path, { signal: controller.signal }).then(value => {
      if (cache) dataCache.set(path, { data: value, dependency });
      setResult({ path, data: value });
      setFailure({ path, text: "" });
    }).catch(error => {
      if (error instanceof DOMException && error.name === "AbortError") return;
      setFailure({ path, text: error instanceof Error ? error.message : "Request failed" });
    }).finally(() => { if (!controller.signal.aborted) setSettledPath(path); });
    return () => controller.abort();
  }, [path, dependency, version, cache]);

  const cached = cache && path ? dataCache.get(path) : undefined;
  const cachedData = cached && Object.is(cached.dependency, dependency) ? cached.data as T : null;
  const data = result.path === path ? result.data : cachedData;
  const error = failure.path === path ? failure.text : "";
  return { data, error, loading: Boolean(path) && data === null && settledPath !== path, reload };
}
