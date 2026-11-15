// A bounded wait for one operation; cadence and timeout behavior stay with
// the caller. Undefined means the result is not ready yet.
export async function waitForResult<T>(probe: () => Promise<T | undefined>, options: {attempts: number; pause: (attempt: number) => Promise<void>; onTimeout: () => T}): Promise<T> {
  for (let attempt = 0; attempt < options.attempts; attempt++) {
    const result = await probe();
    if (result !== undefined) return result;
    await options.pause(attempt);
  }
  return options.onTimeout();
}

export function waitForBackoff(attempt: number, base = 400, maximum = 2_500, signal?: AbortSignal) {
  const delay = Math.min(maximum, Math.round(base * Math.pow(1.55, Math.min(attempt, 6))));
  return new Promise<void>((resolve, reject) => {
    const aborted = () => { window.clearTimeout(timer); reject(new DOMException("Polling cancelled", "AbortError")); };
    const timer = window.setTimeout(() => { signal?.removeEventListener("abort", aborted); resolve(); }, delay);
    if (signal?.aborted) aborted();
    else signal?.addEventListener("abort", aborted, { once: true });
  });
}
