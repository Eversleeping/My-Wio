import { afterEach, expect, test, vi } from "vitest";
import { api, APIError, onUnauthorized, post, postStream } from "./api";

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => { vi.unstubAllGlobals(); });

test("signs out through the registered handler when a request reports an expired session", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ error: "authentication required" }, 401)));
  const expired = vi.fn();
  const unregister = onUnauthorized(expired);

  const request = api("/threads?limit=50");
  await expect(request).rejects.toBeInstanceOf(APIError);
  await expect(request).rejects.toMatchObject({ status: 401, message: "authentication required" });
  await expect(postStream("/servers/server-1/cli/install", {}, vi.fn())).rejects.toMatchObject({ status: 401 });
  expect(expired).toHaveBeenCalledTimes(2);

  unregister();
  await expect(api("/summary")).rejects.toMatchObject({ status: 401 });
  expect(expired).toHaveBeenCalledTimes(2);
});

test("keeps credential failures on sign-in and setup endpoints local", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ error: "invalid credentials" }, 401)));
  const expired = vi.fn();
  const unregister = onUnauthorized(expired);

  await expect(post("/auth/login", { username: "admin" })).rejects.toMatchObject({ status: 401, message: "invalid credentials" });
  await expect(post("/setup", { username: "admin" })).rejects.toMatchObject({ status: 401 });
  await expect(api("/setup/status")).rejects.toMatchObject({ status: 401 });
  await expect(api("/auth/session", { ignoreUnauthorized: true })).rejects.toMatchObject({ status: 401 });
  expect(expired).not.toHaveBeenCalled();
  unregister();
});

test("does not sign out for other failures and forwards abort signals", async () => {
  const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({ error: "invalid CSRF token" }, 403));
  vi.stubGlobal("fetch", fetchMock);
  const expired = vi.fn();
  const unregister = onUnauthorized(expired);
  const controller = new AbortController();

  await expect(post("/threads/thread-1/codex/status/refresh", {}, { signal: controller.signal })).rejects.toMatchObject({ status: 403 });
  expect(expired).not.toHaveBeenCalled();
  expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "POST", signal: controller.signal });
  expect(fetchMock.mock.calls[0][1]).not.toHaveProperty("ignoreUnauthorized");
  unregister();
});
