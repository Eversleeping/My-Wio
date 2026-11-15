import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { sessionListPath, sessionPath } from "./api";
import { mergeSessions, useSessionList } from "./useSessionList";
import type { SessionFilter, SessionSummary } from "./types";

const row = (id: string, engine = "claude"): SessionSummary => ({ id, engine: engine as "claude", workspace_id: "w", project_id: "p", project_name: "Project", server_id: "s", server_name: "Server", path: "/project", title: id, status: "idle", created_at: "", updated_at: "", archived_at: null });
const groups = [{ workspace_id: "w", matched_count: 4, latest_updated_at: "now", project_pinned_at: null }];
function requests() {
  const pending: { path: string; resolve: (items: SessionSummary[], cursor: string | null) => void; reject: (e: Error) => void }[] = [];
  vi.stubGlobal("fetch", vi.fn((path: string) => new Promise<Response>((resolve, reject) => {
    pending.push({ path, resolve: (items, next_cursor) => resolve(new Response(JSON.stringify({ items, next_cursor, groups }))), reject });
  })));
  return pending;
}
afterEach(() => vi.unstubAllGlobals());

test("encodes opaque cursors and IDs without losing mixed-engine identity", () => {
  const path = sessionListPath({ q: " ID /% ", archived: "all", cursor: "a+/b=" });
  const query = new URL(path, "http://localhost").searchParams;
  expect(query.get("q")).toBe("ID /%");
  expect(query.get("cursor")).toBe("a+/b=");
  expect(query.has("engine")).toBe(false);
  expect(sessionPath("claude", "a/b")).toBe("/sessions/claude/a%2Fb");
  expect(mergeSessions([row("same", "claude"), row("same", "codex")], [row("same", "claude")])).toHaveLength(2);
});

test("keeps complete groups while refreshing every loaded page from the new head cursor", async () => {
  const pending = requests();
  const hook = renderHook(({ revision }) => useSessionList({ engine: "claude" }, revision), { initialProps: { revision: 0 } });
  await act(async () => pending[0].resolve([row("a")], "old-tail"));
  act(() => { void hook.result.current.loadMore(); });
  expect(new URL(pending[1].path, "http://localhost").searchParams.get("cursor")).toBe("old-tail");
  await act(async () => pending[1].resolve([row("b")], "old-next"));
  expect(hook.result.current.data?.map(i => i.id)).toEqual(["a", "b"]);
  expect(hook.result.current.groups).toEqual(groups);
  hook.rerender({ revision: 1 });
  await act(async () => pending[2].resolve([row("new")], "new-tail"));
  expect(new URL(pending[3].path, "http://localhost").searchParams.get("cursor")).toBe("new-tail");
  await act(async () => pending[3].resolve([row("a")], "next"));
  expect(hook.result.current.data?.map(i => i.id)).toEqual(["new", "a"]);
  expect(hook.result.current.groups).toEqual(groups);
});

test("discards an in-flight old tail on engine/archive/search changes", async () => {
  const pending = requests();
  const hook = renderHook(({ filter }: { filter: SessionFilter }) => useSessionList(filter, 0), { initialProps: { filter: { engine: "claude" } } });
  await act(async () => pending[0].resolve([row("a")], "old"));
  act(() => { void hook.result.current.loadMore(); });
  hook.rerender({ filter: { engine: "gemini", archived: true, q: "find" } });
  expect(hook.result.current.data).toBeNull();
  await act(async () => pending[2].resolve([row("found", "gemini")], null));
  await act(async () => pending[1].resolve([row("stale")], "stale-next"));
  expect(hook.result.current.data?.map(i => i.id)).toEqual(["found"]);
  expect(hook.result.current.hasMore).toBe(false);
});

test("preserves loaded sessions after a tail failure and permits retry", async () => {
  const pending = requests();
  const hook = renderHook(() => useSessionList({ engine: "claude" }, 0));
  await act(async () => pending[0].resolve([row("a")], "tail"));
  act(() => { void hook.result.current.loadMore(); });
  await act(async () => pending[1].reject(new Error("offline")));
  expect(hook.result.current.data?.map(i => i.id)).toEqual(["a"]);
  expect(hook.result.current.loadError).toBe("offline");
  act(() => { void hook.result.current.loadMore(); });
  await act(async () => pending[2].resolve([row("b")], null));
  expect(hook.result.current.loadError).toBe("");
  expect(hook.result.current.data?.map(i => i.id)).toEqual(["a", "b"]);
});
