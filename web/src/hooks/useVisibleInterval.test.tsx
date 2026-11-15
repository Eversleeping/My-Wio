import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { useVisibleClock } from "./useVisibleInterval";

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });
test("pauses the activity clock while inactive or hidden and catches up on return", () => {
  vi.useFakeTimers();
  let hidden = false;
  vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
  const clock = renderHook(({active}) => useVisibleClock(active, 15000), {initialProps: {active: true}});
  const start = clock.result.current;
  act(() => { vi.advanceTimersByTime(15000); });
  expect(clock.result.current).toBe(start + 15000);
  hidden = true; act(() => document.dispatchEvent(new Event("visibilitychange")));
  expect(vi.getTimerCount()).toBe(0);
  act(() => { vi.advanceTimersByTime(30000); });
  expect(clock.result.current).toBe(start + 15000);
  hidden = false; act(() => document.dispatchEvent(new Event("visibilitychange")));
  expect(clock.result.current).toBe(start + 45000);
  clock.rerender({active: false}); expect(vi.getTimerCount()).toBe(0);
  clock.unmount(); expect(vi.getTimerCount()).toBe(0);
});
