import { useCallback,useEffect,useRef,useState } from "react";
import { api } from "../../../api";
import { message } from "../../../text";
import { type StreamEvent } from "../../../types";
import { clearDataCacheMatching } from "../../../useData";
import { clearFileDrafts } from "../../../workspaceFileEditing";
import { registerSessionMemory } from "../../memory";
import { codexScrollPositions } from "./scroll";

// invalidatedFrom is the lowest sequence invalidated since the entry was stored:
// undefined when nothing changed and null when the position is unknown.
type ThreadEventsCacheEntry = { events: StreamEvent[]; dependency: unknown; globalRevision: number; streamRevision: number; hasEarlier: boolean; invalidatedFrom?: number | null };

const threadEventsCache = new Map<string, ThreadEventsCacheEntry>();

const threadEventsCacheThreadLimit = 8;

const threadEventsPageSize = 500;

export function clearCodexSessionMemory(threadID?: string) {
  if (!threadID) clearFileDrafts();
  const eventPrefix = threadID ? `/sessions/codex/${threadID}/events?` : "/sessions/codex/";
  clearDataCacheMatching(path => path.startsWith(eventPrefix) && path.includes("/events?"));
  for (const path of threadEventsCache.keys()) {
    if (!threadID || path.startsWith(`/sessions/codex/${threadID}/events?`)) threadEventsCache.delete(path);
  }
  for (const key of codexScrollPositions.keys()) {
    if (!threadID || key.startsWith(`${threadID}:`)) codexScrollPositions.delete(key);
  }
}

function lowestInvalidation(current: number | null | undefined, next: number | null | undefined) {
  if (current === undefined) return next;
  if (next === undefined) return current;
  return current === null || next === null ? null : Math.min(current, next);
}

/**
 * Keeps a running minimum of realtime invalidations on every cached view of a
 * thread until that view reloads. A rewrite reinserts events at lower sequences,
 * so a view opened later must not trust only the latest batch.
 */
export function invalidateThreadEvents(threadID: string, sequence: number | null) {
  const prefix = `/sessions/codex/${threadID}/events?`;
  for (const [path, entry] of threadEventsCache) {
    if (path.startsWith(prefix)) entry.invalidatedFrom = lowestInvalidation(entry.invalidatedFrom, sequence);
  }
}

function threadEventsCacheThread(path: string) {
  return path.slice(0, path.indexOf("?") + 1);
}

// Marks every cached view of the thread as most recently used.
function touchThreadEvents(path: string) {
  const thread = threadEventsCacheThread(path);
  for (const [key, entry] of Array.from(threadEventsCache)) {
    if (threadEventsCacheThread(key) !== thread) continue;
    threadEventsCache.delete(key);
    threadEventsCache.set(key, entry);
  }
}

// Stores a view and evicts the least recently used threads beyond the limit.
function storeThreadEvents(path: string, entry: ThreadEventsCacheEntry) {
  threadEventsCache.set(path, entry);
  touchThreadEvents(path);
  const threads = Array.from(new Set(Array.from(threadEventsCache.keys(), threadEventsCacheThread)));
  const evicted = new Set(threads.slice(0, Math.max(0, threads.length - threadEventsCacheThreadLimit)));
  for (const key of Array.from(threadEventsCache.keys())) {
    if (evicted.has(threadEventsCacheThread(key))) threadEventsCache.delete(key);
  }
}

function mergeThreadEvents(existing: StreamEvent[], incoming: StreamEvent[]) {
  const eventIDs = new Set<string>();
  const sequences = new Set<number>();
  const merged: StreamEvent[] = [];
  for (const event of [...existing, ...incoming]) {
    if (eventIDs.has(event.event_id) || sequences.has(event.sequence)) continue;
    eventIDs.add(event.event_id);
    sequences.add(event.sequence);
    merged.push(event);
  }
  return merged.sort((left, right) => left.sequence - right.sequence);
}

function latestEventSequence(events: StreamEvent[]) {
  return events.reduce((latest, event) => Math.max(latest, event.sequence), 0);
}

export function useThreadEvents(threadID: string, rawEvents: boolean, realtime: unknown, globalRevision = 0, streamRevision = 0, invalidationSequence?: number | null) {
  const view = rawEvents ? "raw" : "conversation";
  const path = `/sessions/codex/${threadID}/events?view=${view}`;
  const [state, setState] = useState<{ path: string; data: StreamEvent[] | null; error: string; loading: boolean; loadingEarlier: boolean }>({ path: "", data: null, error: "", loading: false, loadingEarlier: false });
  const [reloadNonce, setReloadNonce] = useState(0);
  const appliedReloadNonceRef = useRef(0);
  const loadingEarlierRef = useRef(false);
  const reload = useCallback(() => setReloadNonce(value => value + 1), []);

  useEffect(() => {
    const cached = threadEventsCache.get(path);
    const manuallyReloaded = reloadNonce !== appliedReloadNonceRef.current;
    appliedReloadNonceRef.current = reloadNonce;
    const initialLoad = !cached;
    const currentMaximumSequence = latestEventSequence(cached?.events ?? []);
    const globalResync = !initialLoad && cached.globalRevision !== globalRevision;
    const streamChanged = !initialLoad && cached.streamRevision !== streamRevision;
    const latestInvalidation = typeof invalidationSequence === "number" && Number.isInteger(invalidationSequence) && invalidationSequence > 0 ? invalidationSequence : null;
    // The latest batch alone is not enough: batches recorded while this view was
    // hidden may have rewound the stream below the cached tail.
    const invalidatedFrom = lowestInvalidation(cached?.invalidatedFrom, streamChanged ? latestInvalidation : undefined);
    const rewound = invalidatedFrom === null || (invalidatedFrom !== undefined && invalidatedFrom <= currentMaximumSequence);
    const windowReload = !initialLoad && (globalResync || rewound);
    const incrementalLoad = !initialLoad && !windowReload && (manuallyReloaded || streamChanged || invalidatedFrom !== undefined || !Object.is(cached.dependency, realtime));
    if (!initialLoad && !windowReload && !incrementalLoad) {
      touchThreadEvents(path);
      setState({ path, data: cached.events, error: "", loading: false, loadingEarlier: false });
      return;
    }

    const controller = new AbortController();
    let active = true;
    let settled = false;
    // This load consumes the recorded invalidations. Restore them unless it
    // completes, so an aborted or failed request cannot hide an earlier rewrite.
    if (cached) cached.invalidatedFrom = undefined;
    const restoreInvalidation = () => {
      const latest = threadEventsCache.get(path);
      if (latest) latest.invalidatedFrom = lowestInvalidation(latest.invalidatedFrom, invalidatedFrom);
    };
    setState({ path, data: cached?.events ?? null, error: "", loading: !cached, loadingEarlier: false });
    const load = async () => {
      let events = initialLoad || windowReload ? [] : cached?.events ?? [];
      let after = latestEventSequence(events);
      try {
        let page: StreamEvent[];
        do {
          const requestPath = initialLoad || windowReload ? path : `${path}&after=${after}&limit=1000`;
          page = await api<StreamEvent[]>(requestPath, { signal: controller.signal });
          if (!active) return;
          events = mergeThreadEvents(events, page);
          const nextAfter = latestEventSequence(page);
          if (initialLoad || windowReload || page.length < 1000 || nextAfter <= after) break;
          after = nextAfter;
        } while (true);
        if (!active) return;
        const hasEarlier = initialLoad || windowReload ? page.length >= threadEventsPageSize : cached?.hasEarlier ?? false;
        settled = true;
        // Invalidations recorded while the request was in flight stay pending.
        storeThreadEvents(path, { events, dependency: realtime, globalRevision, streamRevision, hasEarlier, invalidatedFrom: threadEventsCache.get(path)?.invalidatedFrom });
        setState({ path, data: events, error: "", loading: false, loadingEarlier: false });
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        settled = true;
        restoreInvalidation();
        setState({ path, data: cached?.events ?? null, error: message(error), loading: false, loadingEarlier: false });
      }
    };
    void load();
    return () => {
      active = false;
      controller.abort();
      if (!settled) restoreInvalidation();
    };
  }, [path, realtime, globalRevision, streamRevision, invalidationSequence, reloadNonce]);

  const loadEarlier = useCallback(async () => {
    const entry = threadEventsCache.get(path);
    const earliestSequence = entry?.events[0]?.sequence ?? 0;
    if (!entry?.hasEarlier || earliestSequence <= 0 || loadingEarlierRef.current) return false;
    loadingEarlierRef.current = true;
    setState(current => current.path === path ? { ...current, error: "", loadingEarlier: true } : current);
    try {
      const page = await api<StreamEvent[]>(`${path}&before=${earliestSequence}&limit=${threadEventsPageSize}`);
      const current = threadEventsCache.get(path);
      if (!current || current.globalRevision !== entry.globalRevision || current.streamRevision !== entry.streamRevision) return false;
      const events = mergeThreadEvents(page, current.events);
      const next = { ...current, events, hasEarlier: page.length >= threadEventsPageSize };
      storeThreadEvents(path, next);
      setState(currentState => currentState.path === path ? { path, data: events, error: "", loading: false, loadingEarlier: false } : currentState);
      return true;
    } catch (error) {
      setState(current => current.path === path ? { ...current, error: message(error), loadingEarlier: false } : current);
      return false;
    } finally {
      loadingEarlierRef.current = false;
    }
  }, [path]);

  const cached = threadEventsCache.get(path);
  const current = state.path === path ? state : { path, data: cached?.events ?? null, error: "", loading: !cached, loadingEarlier: false };
  return { data: current.data, error: current.error, loading: current.loading, loadingEarlier: current.loadingEarlier, hasEarlier: cached?.hasEarlier ?? false, loadEarlier, reload };
}

registerSessionMemory("codex", { clear: clearCodexSessionMemory, invalidate: invalidateThreadEvents });
