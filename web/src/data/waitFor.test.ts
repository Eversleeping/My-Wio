import { afterEach, expect, test, vi } from "vitest";
import { waitForBackoff, waitForResult } from "./waitFor";

afterEach(() => vi.useRealTimers());
test("bounds a wait and preserves the caller's backoff cadence", async () => {
  vi.useFakeTimers();
  const probe = vi.fn().mockResolvedValueOnce(undefined).mockResolvedValueOnce(undefined).mockResolvedValue(0);
  const result = waitForResult(probe, {attempts: 3, pause: attempt => waitForBackoff(attempt), onTimeout: () => { throw new Error("timeout"); }});
  await vi.advanceTimersByTimeAsync(399); expect(probe).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(1); expect(probe).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(619); expect(probe).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(1); expect(await result).toBe(0); expect(probe).toHaveBeenCalledTimes(3);
  expect(vi.getTimerCount()).toBe(0);
});
test("keeps the configured exhaustion behavior and stops immediately on a failed probe", async () => {
  const pause = vi.fn(async () => {});
  const probe = vi.fn(async () => undefined);
  expect(await waitForResult(probe, {attempts: 2, pause, onTimeout: () => "last snapshot"})).toBe("last snapshot");
  expect(probe).toHaveBeenCalledTimes(2); expect(pause).toHaveBeenCalledTimes(2);
  const failure = new Error("operation failed");
  await expect(waitForResult(async () => { throw failure; }, {attempts: 120, pause, onTimeout: () => "timeout"})).rejects.toBe(failure);
  expect(pause).toHaveBeenCalledTimes(2);
});
test("aborts a pending backoff without leaving a timer", async () => {
  vi.useFakeTimers();
  const controller = new AbortController();
  const rejected = expect(waitForBackoff(0, undefined, undefined, controller.signal)).rejects.toMatchObject({name: "AbortError"});
  controller.abort(); await rejected;
  expect(vi.getTimerCount()).toBe(0);
});
