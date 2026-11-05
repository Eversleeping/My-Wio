import { StrictMode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { useClaudeModels } from "./claudeModels";

afterEach(() => { vi.unstubAllGlobals(); });

test("refreshes models once per session entry, including returning to the same workspace", async () => {
  const fetcher = vi.fn(async (_url: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({ configured: true, models: [{ id: "custom-model", name: "Custom" }] }), { status: 200 }));
  vi.stubGlobal("fetch", fetcher);
  const { result, rerender, unmount } = renderHook(({ session }) => useClaudeModels("workspace-1", session), { initialProps: { session: "one" }, wrapper: StrictMode });
  await waitFor(() => expect(result.current.models[0]?.id).toBe("custom-model"));
  expect(fetcher).toHaveBeenCalledTimes(1);
  rerender({ session: "one" });
  await act(() => new Promise(resolve => setTimeout(resolve, 10)));
  expect(fetcher).toHaveBeenCalledTimes(1);
  rerender({ session: "two" });
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
  rerender({ session: "one" });
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3));
  unmount();
  renderHook(() => useClaudeModels("workspace-1", "one"));
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(4));
  expect(fetcher.mock.calls.every(([url]) => url === "/api/workspaces/workspace-1/claude/models")).toBe(true);
});

test("ignores a late response after switching workspaces and reports refresh failure", async () => {
  let completeOld: (response: Response) => void = () => {};
  const fetcher = vi.fn((url: string) => url.includes("workspace-1") ? new Promise<Response>(resolve => { completeOld = resolve; }) : Promise.resolve(new Response(JSON.stringify({ error: "Upstream unavailable" }), { status: 502 })));
  vi.stubGlobal("fetch", fetcher);
  const { result, rerender } = renderHook(({ workspace }) => useClaudeModels(workspace, "one"), { initialProps: { workspace: "workspace-1" } });
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
  rerender({ workspace: "workspace-2" });
  await waitFor(() => expect(result.current.error).toContain("Upstream unavailable"));
  await act(async () => { completeOld(new Response(JSON.stringify({ models: [{ id: "old-model", name: "Old" }] }))); });
  expect(result.current.models).toEqual([]);
  expect(result.current.error).toContain("Upstream unavailable");
});
