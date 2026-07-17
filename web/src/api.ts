import type { Session } from "./types";

let csrfToken = "";

export class APIError extends Error {
  status: number;
  code: string;
  constructor(status: number, message: string, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export function setSession(session: Session | null) {
  csrfToken = session?.csrf_token ?? "";
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  if (options.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (options.method && !["GET", "HEAD"].includes(options.method.toUpperCase()) && csrfToken) {
    headers.set("X-CSRF-Token", csrfToken);
  }
  const response = await fetch(`/api${path}`, { ...options, headers, credentials: "same-origin" });
  const text = await response.text();
  let body: any = null;
  if (text) {
    try { body = JSON.parse(text); } catch { body = null; }
  }
  if (!response.ok) throw new APIError(response.status, body?.error ?? `Request failed (${response.status})`, body?.code ?? "");
  return body as T;
}

export function post<T>(path: string, body: unknown): Promise<T> {
  return api<T>(path, { method: "POST", body: JSON.stringify(body) });
}

export function remove<T>(path: string): Promise<T> {
  return api<T>(path, { method: "DELETE" });
}

export function socketURL() {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${location.host}/api/ws`;
}
