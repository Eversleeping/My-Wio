import { type CodexSnapshot, type Thread, type Workspace } from "../types";
import { api, APIError } from "../api";
import { normalizeGoal } from "./conversation";

export async function waitForGoalSnapshot(threadID: string, expected?: { objective?: string; status?: string; cleared?: boolean }, signal?: AbortSignal) {
  let latest: CodexSnapshot<unknown> | undefined;
  for (let attempt = 0; attempt < 120; attempt++) {
    latest = await api<CodexSnapshot<unknown>>(`/threads/${threadID}/goal`, { signal });
    if (latest.status === "failed" || latest.status === "unsupported") return latest;
    if (latest.status !== "loading") {
      if (!expected) return latest;
      const goal = normalizeGoal(latest.data);
      if (expected.cleared ? !goal : Boolean(goal && goal.objective === expected.objective && (!expected.status || goal.status === expected.status))) return latest;
    }
    await waitForBackoff(attempt, undefined, undefined, signal);
  }
  return latest;
}

export async function waitForThread(threadID: string, timeoutMessage: string, operationID?: string) {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    if (operationID) {
      const operation = await api<{ status: string; result: string }>(`/operations/${encodeURIComponent(operationID)}`);
      if (["failed", "cancelled"].includes(operation.status)) throw new Error(operation.result || timeoutMessage);
      if (operation.status === "succeeded") return api<Thread>(`/threads/${encodeURIComponent(threadID)}`);
      await waitForBackoff(attempt);
      continue;
    }
    const threads = await api<Thread[]>("/threads");
    const thread = threads.find(item => item.id === threadID);
    if (thread) return thread;
    await waitForBackoff(attempt);
  }
  throw new Error(timeoutMessage);
}

export async function waitForWorkspace(workspaceID: string, timeoutMessage: string) {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    try {
      const workspaces = await api<Workspace[]>("/workspaces");
      const workspace = workspaces.find(item => item.id === workspaceID);
      if (workspace) return workspace;
    } catch (error) {
      if (error instanceof APIError && error.status >= 400 && error.status < 500) throw error;
    }
    await waitForBackoff(attempt, 600, 3_000);
  }
  throw new Error(timeoutMessage);
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
