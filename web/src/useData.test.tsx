import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { clearDataCacheMatching, useData } from "./useData";

function requests() {
  const pending: { signal: AbortSignal; resolve: (value: unknown) => void; reject: (error: Error) => void }[] = [];
  vi.stubGlobal("fetch", vi.fn((_url: string, init: RequestInit) => new Promise<Response>((resolve, reject) => {
    pending.push({ signal: init.signal as AbortSignal, resolve: value => resolve(new Response(JSON.stringify(value))), reject });
  })));
  return pending;
}

afterEach(() => { vi.unstubAllGlobals(); clearDataCacheMatching(() => true); });

test("renders a slow first response during an event burst and coalesces catch-up requests", async () => {
  const pending = requests();
  const hook = renderHook(({ revision }) => useData<string[]>("/threads", revision), { initialProps: { revision: 0 } });
  for (let revision = 1; revision <= 20; revision++) hook.rerender({ revision });
  expect(pending).toHaveLength(1);
  expect(pending[0].signal.aborted).toBe(false);
  await act(async () => pending[0].resolve(["first"]));
  expect(hook.result.current.data).toEqual(["first"]);
  expect(hook.result.current.loading).toBe(false);
  expect(pending).toHaveLength(2);
  await act(async () => pending[1].resolve(["latest"]));
  expect(hook.result.current.data).toEqual(["latest"]);
  expect(pending).toHaveLength(2);
});

test("aborts on navigation and ignores late responses from the previous path", async () => {
  const pending = requests();
  const hook = renderHook(({ path }) => useData<string[]>(path, 0), { initialProps: { path: "/first" } });
  hook.rerender({ path: "/second" });
  expect(pending[0].signal.aborted).toBe(true);
  await act(async () => pending[1].resolve(["second"]));
  await act(async () => pending[0].resolve(["stale"]));
  expect(hook.result.current.data).toEqual(["second"]);
  hook.unmount();
  expect(pending[1].signal.aborted).toBe(true);
});

test("cancels pending catch-up work on unmount or a null path", async () => {
  const pending = requests();
  const hook = renderHook(({ path, revision }: { path: string | null; revision: number }) => useData(path, revision), { initialProps: { path: "/threads" as string | null, revision: 0 } });
  hook.rerender({ path: "/threads", revision: 1 });
  hook.rerender({ path: null, revision: 1 });
  expect(pending[0].signal.aborted).toBe(true);
  await act(async () => pending[0].resolve(["old"]));
  expect(pending).toHaveLength(1);
  expect(hook.result.current.data).toBeNull();
  expect(hook.result.current.loading).toBe(false);
});

test("recovers after a failed in-flight request using a queued invalidation", async () => {
  const pending = requests();
  const hook = renderHook(({ revision }) => useData("/threads", revision), { initialProps: { revision: 0 } });
  hook.rerender({ revision: 1 });
  await act(async () => pending[0].reject(new Error("network error")));
  expect(hook.result.current.error).toBe("network error");
  expect(pending).toHaveLength(2);
  await act(async () => pending[1].resolve(["recovered"]));
  expect(hook.result.current.data).toEqual(["recovered"]);
  expect(hook.result.current.error).toBe("");
});

test("keeps the latest cache revision and allows a forced reload", async () => {
  const pending = requests();
  const hook = renderHook(({ revision }) => useData<string[]>("/threads", revision, true), { initialProps: { revision: 0 } });
  hook.rerender({ revision: 1 });
  await act(async () => pending[0].resolve(["first"]));
  await act(async () => pending[1].resolve(["latest"]));
  hook.unmount();
  const cached = renderHook(() => useData<string[]>("/threads", 1, true));
  expect(cached.result.current.data).toEqual(["latest"]);
  expect(pending).toHaveLength(2);
  act(() => cached.result.current.reload());
  expect(pending).toHaveLength(3);
  await act(async () => pending[2].resolve(["manual"]));
  expect(cached.result.current.data).toEqual(["manual"]);
});

test("deduplicates simultaneous readers and keeps the remaining reader alive after navigation", async () => {
  const pending = requests();
  const first = renderHook(() => useData<string[]>("/shared", 0));
  const second = renderHook(() => useData<string[]>("/shared", 0));
  expect(pending).toHaveLength(1);
  first.unmount();
  expect(pending[0].signal.aborted).toBe(false);
  await act(async () => pending[0].resolve(["shared value"]));
  expect(second.result.current.data).toEqual(["shared value"]);
  second.unmount();
  expect(pending[0].signal.aborted).toBe(true);
});

test("cancels a shared request only after its last reader leaves and does not reuse an aborted request", async () => {
  const pending = requests();
  const first = renderHook(() => useData("/shared-cancel", 0));
  const second = renderHook(() => useData("/shared-cancel", 0));
  first.unmount(); second.unmount();
  expect(pending[0].signal.aborted).toBe(true);
  const replacement = renderHook(() => useData("/shared-cancel", 0));
  expect(pending).toHaveLength(2);
  await act(async () => pending[0].resolve(["abandoned"]));
  await act(async () => pending[1].resolve(["fresh"]));
  expect(replacement.result.current.data).toEqual(["fresh"]);
});
