import { createContext, useContext, useSyncExternalStore, type ReactNode } from "react";
import type { RealtimeRevisions, RealtimeScope } from "../realtime/scopes";
import { api, type APIOptions } from "./api";
import { clearDataCacheMatching, useData, useThrottledValue } from "./useData";

export type Query<T> = { path: string; scopes: readonly RealtimeScope[]; cache: boolean; throttle: number; response?: T };
export function query<T>(path: string, scopes: readonly RealtimeScope[], cache = false, throttle = 0): Query<T> { return { path, scopes, cache, throttle }; }
export type Mutation<T> = { path: string; method: "POST" | "PATCH" | "PUT" | "DELETE"; scopes: readonly RealtimeScope[]; response?: T };
export function mutation<T = unknown>(path: string, method: Mutation<T>["method"], scopes: readonly RealtimeScope[]): Mutation<T> { return {path, method, scopes}; }

const Revisions = createContext<Partial<RealtimeRevisions>>({});
const localRevisions = new Map<RealtimeScope, number>();
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };
export function DataProvider({ revisions, children }: { revisions: Partial<RealtimeRevisions>; children: ReactNode }) { return <Revisions.Provider value={revisions}>{children}</Revisions.Provider>; }

export function invalidateResources(scopes: readonly RealtimeScope[]) {
  for (const scope of new Set(scopes)) localRevisions.set(scope, (localRevisions.get(scope) ?? 0) + 1);
  for (const listener of listeners) listener();
}
export function clearResourceMemory() { clearDataCacheMatching(() => true); }
export function useResourceRevision(scopes: readonly RealtimeScope[]) {
  const realtime = useContext(Revisions);
  const local = useSyncExternalStore(subscribe, () => scopes.map(scope => localRevisions.get(scope) ?? 0).join(":"));
  return `${scopes.map(scope => realtime[scope] ?? 0).join(":")}|${local}`;
}
export function useResource<T>(endpoint: Query<T> | null) {
  const revision = useResourceRevision(endpoint?.scopes ?? []);
  const throttled = useThrottledValue(revision, endpoint?.throttle ?? 0);
  return useData<T>(endpoint?.path ?? null, throttled, endpoint?.cache ?? false);
}
export function readResource<T>(endpoint: Query<T>, options?: APIOptions) { return api<T>(endpoint.path, options); }
export async function writeResource<T>(endpoint: Mutation<T>, body?: unknown, options?: APIOptions) {
  const result = await api<T>(endpoint.path, { ...options, method: endpoint.method, ...(body === undefined ? {} : {body: JSON.stringify(body)}) });
  invalidateResources(endpoint.scopes);
  return result;
}
