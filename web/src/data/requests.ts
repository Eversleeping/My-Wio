import { api } from "./api";

type Request = { promise: Promise<unknown>; controller: AbortController; consumers: number };
const requests = new Map<string, Request>();

// Each view owns a lease; navigating one view must not cancel another view's
// request. The last release cancels abandoned work, including StrictMode probes.
export function acquireDataRequest<T>(path: string) {
  let request = requests.get(path);
  if (!request) {
    const controller = new AbortController();
    const entry: Request = { controller, consumers: 0, promise: Promise.resolve() };
    entry.promise = api(path, { signal: controller.signal }).finally(() => { if (requests.get(path) === entry) requests.delete(path); });
    request = entry;
    requests.set(path, entry);
  }
  request.consumers++;
  const owned = request;
  let released = false;
  return { promise: owned.promise as Promise<T>, release: () => {
    if (released) return;
    released = true;
    if (--owned.consumers === 0) {
      owned.controller.abort();
      if (requests.get(path) === owned) requests.delete(path);
    }
  } };
}
