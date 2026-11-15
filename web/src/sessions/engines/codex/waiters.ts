import { api, APIError } from "../../../api";
import { waitForBackoff, waitForResult } from "../../../data/waitFor";
import type { CodexSnapshot, Thread, Workspace } from "../../../types";
import { normalizeGoal } from "./conversation";
export { waitForBackoff } from "../../../data/waitFor";

export async function waitForGoalSnapshot(threadID: string, expected?: {objective?: string; status?: string; cleared?: boolean}, signal?: AbortSignal) {
  let latest: CodexSnapshot<unknown> | undefined;
  return waitForResult<CodexSnapshot<unknown> | undefined>(async () => {
    latest = await api<CodexSnapshot<unknown>>(`/sessions/codex/${threadID}/goal`, {signal});
    if (latest.status === "failed" || latest.status === "unsupported") return latest;
    if (latest.status !== "loading") {
      if (!expected) return latest;
      const goal = normalizeGoal(latest.data);
      if (expected.cleared ? !goal : Boolean(goal && goal.objective === expected.objective && (!expected.status || goal.status === expected.status))) return latest;
    }
  }, {attempts: 120, pause: attempt => waitForBackoff(attempt, undefined, undefined, signal), onTimeout: () => latest});
}

export async function waitForThread(threadID: string, timeoutMessage: string, operationID?: string) {
  return waitForResult<Thread>(async () => {
    if (operationID) {
      const operation = await api<{status: string; result: string}>(`/operations/${encodeURIComponent(operationID)}`);
      if (["failed", "cancelled"].includes(operation.status)) throw new Error(operation.result || timeoutMessage);
      if (operation.status === "succeeded") return api<Thread>(`/sessions/codex/${encodeURIComponent(threadID)}`);
      return;
    }
    try { return await api<Thread>(`/sessions/codex/${encodeURIComponent(threadID)}`); }
    catch (error) { if (!(error instanceof APIError) || error.status !== 404) throw error; }
  }, {attempts: 120, pause: attempt => waitForBackoff(attempt), onTimeout: () => { throw new Error(timeoutMessage); }});
}

export async function waitForWorkspace(workspaceID: string, timeoutMessage: string) {
  return waitForResult<Workspace>(async () => {
    try {
      const workspaces = await api<Workspace[]>("/workspaces");
      const workspace = workspaces.find(item => item.id === workspaceID);
      if (workspace) return workspace;
    } catch (error) {
      if (error instanceof APIError && error.status >= 400 && error.status < 500) throw error;
    }
  }, {attempts: 40, pause: attempt => waitForBackoff(attempt, 600, 3000), onTimeout: () => { throw new Error(timeoutMessage); }});
}
