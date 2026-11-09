import { APIError, post } from "./api";

type PendingRequest = { fingerprint: string; path: string; body: Record<string, unknown> };

/** Keep both the key and original endpoint after an ambiguous network failure.
 * A steer retry can happen after the UI has already observed turn completion.
 */
export class CodexRequests {
  private pending = new Map<string, PendingRequest>();

  async send(channel: string, draft: unknown, path: string, body: Record<string, unknown>) {
    const fingerprint = JSON.stringify(draft);
    let request = this.pending.get(channel);
    if (!request || request.fingerprint !== fingerprint) {
      request = { fingerprint, path, body: { ...body, request_id: crypto.randomUUID() } };
      this.pending.set(channel, request);
    }
    try {
      const result = await post(request.path, request.body);
      if (this.pending.get(channel) === request) this.pending.delete(channel);
      return result;
    } catch (error) {
      // Explicit input/state rejections did not queue anything. Keep the key
      // for timeouts and server failures, which may follow a committed write.
      if (error instanceof APIError && error.status >= 400 && error.status < 500 && error.status !== 408) {
        if (this.pending.get(channel) === request) this.pending.delete(channel);
      }
      throw error;
    }
  }
}
