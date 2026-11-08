import type { Session } from "./types";

let csrfToken = "";
let unauthorizedHandler: (() => void) | null = null;
// These public endpoints answer 401 for rejected credentials, not for an expired session.
const sessionlessPaths = new Set(["/auth/login", "/setup", "/setup/status"]);

export class APIError extends Error {
  status: number;
  code: string;
  constructor(status: number, message: string, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

/** `ignoreUnauthorized` keeps a 401 local to the caller, e.g. the initial session probe. */
export type APIOptions = RequestInit & { ignoreUnauthorized?: boolean };

export function setSession(session: Session | null) {
  csrfToken = session?.csrf_token ?? "";
}

/** Registers the local sign-out that runs when the server rejects the session. Returns an unregister function. */
export function onUnauthorized(handler: () => void) {
  unauthorizedHandler = handler;
  return () => {
    if (unauthorizedHandler === handler) unauthorizedHandler = null;
  };
}

function requestError(path: string, status: number, body: { error?: string; code?: string } | null, ignoreUnauthorized = false) {
  if (status === 401 && !ignoreUnauthorized && !sessionlessPaths.has(path.split("?", 1)[0])) unauthorizedHandler?.();
  return new APIError(status, body?.error ?? `Request failed (${status})`, body?.code ?? "");
}

export async function api<T>(path: string, options: APIOptions = {}): Promise<T> {
  const { ignoreUnauthorized, ...init } = options;
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (init.method && !["GET", "HEAD"].includes(init.method.toUpperCase()) && csrfToken) {
    headers.set("X-CSRF-Token", csrfToken);
  }
  const response = await fetch(`/api${path}`, { ...init, headers, credentials: "same-origin" });
  const text = await response.text();
  let body: any = null;
  if (text) {
    try { body = JSON.parse(text); } catch { body = null; }
  }
  if (!response.ok) throw requestError(path, response.status, body, ignoreUnauthorized);
  return body as T;
}

export function post<T>(path: string, body: unknown, options: APIOptions = {}): Promise<T> {
  return api<T>(path, { ...options, method: "POST", body: JSON.stringify(body) });
}

export function patch<T>(path: string, body: unknown): Promise<T> {
  return api<T>(path, { method: "PATCH", body: JSON.stringify(body) });
}

export function put<T>(path: string, body: unknown): Promise<T> {
  return api<T>(path, { method: "PUT", body: JSON.stringify(body) });
}

export async function postStream<T>(path: string, body: unknown, onEvent: (event: T) => void): Promise<void> {
  const headers = new Headers({ "Content-Type": "application/json" });
  if (csrfToken) headers.set("X-CSRF-Token", csrfToken);
  const response = await fetch(`/api${path}`, {
    method: "POST",
    body: JSON.stringify(body),
    headers,
    credentials: "same-origin"
  });
  if (!response.ok) {
    const text = await response.text();
    let errorBody: { error?: string; code?: string } | null = null;
    try { errorBody = text ? JSON.parse(text) : null; } catch { errorBody = null; }
    throw requestError(path, response.status, errorBody);
  }
  if (!response.body) throw new APIError(502, "Streaming response unavailable", "stream_unavailable");

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let pending = "";
  while (true) {
    const { done, value } = await reader.read();
    pending += decoder.decode(value, { stream: !done });
    const lines = pending.split("\n");
    pending = lines.pop() ?? "";
    for (const line of lines) {
      if (line.trim()) onEvent(JSON.parse(line) as T);
    }
    if (done) break;
  }
  if (pending.trim()) onEvent(JSON.parse(pending) as T);
}

export function remove<T>(path: string): Promise<T> {
  return api<T>(path, { method: "DELETE" });
}

export function socketURL() {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${location.host}/api/ws`;
}
