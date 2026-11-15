import { api, patch, post, remove, type APIOptions } from "../api";
import type { Engine, SessionFilter, SessionListPage, AISession } from "./types";

export function sessionPath(engine: Engine, id?: string, action?: string) {
  return `/sessions/${engine}${id ? `/${encodeURIComponent(id)}` : ""}${action ? `/${action}` : ""}`;
}

export function sessionListPath(filter: SessionFilter = {}) {
  const query = new URLSearchParams();
  if (filter.engine) query.set("engine", filter.engine);
  query.set("archived", String(filter.archived ?? false));
  if (filter.q?.trim()) query.set("q", filter.q.trim());
  query.set("limit", String(filter.limit ?? 50));
  if (filter.cursor) query.set("cursor", filter.cursor);
  return `/sessions?${query}`;
}

export const sessionAPI = {
  list: <T = AISession>(filter: SessionFilter, options?: APIOptions) => api<SessionListPage<T>>(sessionListPath(filter), options),
  detail: <E extends Engine>(engine: E, id: string, options?: APIOptions) => api<AISession<E>>(sessionPath(engine, id), options),
  create: <E extends Engine>(engine: E, input: unknown) => post<AISession<E>>(sessionPath(engine), input),
  update: <E extends Engine>(engine: E, id: string, input: unknown) => patch<AISession<E>>(sessionPath(engine, id), input),
  remove: (engine: Engine, id: string) => remove(sessionPath(engine, id)),
  action: <T>(engine: Engine, id: string, action: string, input: unknown, options?: APIOptions) => post<T>(sessionPath(engine, id, action), input, options),
};
