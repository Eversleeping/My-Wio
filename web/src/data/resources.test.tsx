import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { ReactNode } from "react";
import { DataProvider, clearResourceMemory, mutation, query, useResource, writeResource } from "./resources";

afterEach(() => { vi.unstubAllGlobals(); clearResourceMemory(); });

test("reads only the endpoint's realtime scopes and ignores unrelated changes", async () => {
  const fetcher = vi.fn(async () => new Response(JSON.stringify(["value"])));
  vi.stubGlobal("fetch", fetcher);
  let revision = { monitoring: 0, servers: 0 };
  const wrapper = ({ children }: { children: ReactNode }) => <DataProvider revisions={revision}>{children}</DataProvider>;
  const hook = renderHook(() => useResource(query<string[]>("/scoped", ["monitoring"])), {wrapper});
  await waitFor(() => expect(hook.result.current.data).toEqual(["value"]));
  revision = {monitoring: 0, servers: 1}; hook.rerender();
  expect(fetcher).toHaveBeenCalledTimes(1);
  revision = {monitoring: 1, servers: 1}; hook.rerender();
  await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
});

test("successful writes refresh every active reader once while rejected writes retain the loaded data", async () => {
  let reads = 0;
  vi.stubGlobal("fetch", vi.fn(async (_input: unknown, init: RequestInit) => {
    if (init.method) return new Response(JSON.stringify({error: "rejected"}), {status: init.body === '"reject"' ? 409 : 200});
    return new Response(JSON.stringify([++reads]));
  }));
  const endpoint = query<number[]>("/mutation-readers", ["monitoring"]);
  const first = renderHook(() => useResource(endpoint));
  const second = renderHook(() => useResource(endpoint));
  await waitFor(() => expect(second.result.current.data).toEqual([1]));
  await expect(writeResource(mutation("/change", "POST", ["monitoring"]), "reject")).rejects.toMatchObject({status:409});
  expect(reads).toBe(1);
  expect(first.result.current.data).toEqual([1]);
  await act(async () => { await writeResource(mutation("/change", "POST", ["monitoring"]), "accept"); });
  await waitFor(() => expect(first.result.current.data).toEqual([2]));
  expect(second.result.current.data).toEqual([2]);
  expect(reads).toBe(2);
});

test("keeps cache policy on the endpoint and clears cached account data on logout", async () => {
  let reads = 0;
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify([++reads]))));
  const endpoint = query<number[]>("/account-resource", ["settings"], true);
  const first = renderHook(() => useResource(endpoint));
  await waitFor(() => expect(first.result.current.data).toEqual([1])); first.unmount();
  const second = renderHook(() => useResource(endpoint));
  expect(second.result.current.data).toEqual([1]); expect(reads).toBe(1); second.unmount();
  clearResourceMemory();
  const nextAccount = renderHook(() => useResource(endpoint));
  await waitFor(() => expect(nextAccount.result.current.data).toEqual([2]));
});
