import { clearCodexSessionMemory, useThreadEvents } from "../sessions/engines/codex/useThreadEvents";
import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { ReactNode } from "react";
import { DataProvider } from "./resources";
import { useData } from "./useData";

afterEach(() => { clearCodexSessionMemory(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });
const settle = async () => { await act(async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); }); };

test("shares one disconnected fallback, pauses while hidden and refreshes once on reconnect", async () => {
  vi.useFakeTimers();
  let hidden = false;
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  const fetcher = vi.fn(async () => new Response(JSON.stringify({value: fetcher.mock.calls.length})));
  vi.stubGlobal("fetch", fetcher);
  let connected = true;
  const wrapper = ({children}: {children: ReactNode}) => <DataProvider revisions={{}} connected={connected}>{children}</DataProvider>;
  const hooks = renderHook(() => [useData<{value: number}>("/first", 0), useData<{value: number}>("/second", 0), useData("/inactive", 0)], {wrapper});
  await settle(); expect(fetcher).toHaveBeenCalledTimes(3);
  await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
  expect(fetcher).toHaveBeenCalledTimes(3);
  connected = false; hooks.rerender();
  expect(vi.getTimerCount()).toBe(1);
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  expect(fetcher).toHaveBeenCalledTimes(6);
  hidden = true; act(() => document.dispatchEvent(new Event("visibilitychange")));
  expect(vi.getTimerCount()).toBe(0);
  await act(async () => { await vi.advanceTimersByTimeAsync(20000); });
  expect(fetcher).toHaveBeenCalledTimes(6);
  hidden = false; act(() => document.dispatchEvent(new Event("visibilitychange"))); await settle();
  expect(fetcher).toHaveBeenCalledTimes(9);
  connected = true; hooks.rerender(); await settle();
  expect(fetcher).toHaveBeenCalledTimes(12);
  expect(vi.getTimerCount()).toBe(0);
  await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
  expect(fetcher).toHaveBeenCalledTimes(12);
  hooks.unmount(); expect(vi.getTimerCount()).toBe(0);
});

test("does not refresh inactive or unmounted readers", async () => {
  vi.useFakeTimers();
  const fetcher = vi.fn(async () => new Response("{}")); vi.stubGlobal("fetch", fetcher);
  let path: string | null = "/active";
  const wrapper = ({children}: {children: ReactNode}) => <DataProvider revisions={{}} connected={false}>{children}</DataProvider>;
  const hook = renderHook(() => useData(path, 0), {wrapper}); await settle();
  path = null; hook.rerender();
  await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
  expect(fetcher).toHaveBeenCalledTimes(1);
  hook.unmount(); expect(vi.getTimerCount()).toBe(0);
});


test("revalidates the native Codex window after a missed rewrite rather than only appending", async () => {
  vi.useFakeTimers();
  let text = "discarded reply";
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async input => {
    requests.push(String(input));
    return new Response(JSON.stringify([{event_id: text, stream_id: "rewound", sequence: 1, kind: "codex.message", occurred_at: "2026-11-15T00:00:00Z", payload: {text}}]));
  }));
  const wrapper = ({children}: {children: ReactNode}) => <DataProvider revisions={{}} connected={false}>{children}</DataProvider>;
  const hook = renderHook(() => useThreadEvents("rewound", false, 0), {wrapper}); await settle();
  expect(hook.result.current.data?.[0].event_id).toBe("discarded reply");
  text = "rewritten reply";
  await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
  expect(hook.result.current.data?.map(event => event.event_id)).toEqual(["rewritten reply"]);
  expect(requests.every(path => !path.includes("&after="))).toBe(true);
});
