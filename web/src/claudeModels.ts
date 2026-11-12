import { useEffect, useState } from "react";
import { api } from "./api";

export type ClaudeModel = { id: string; name: string };
type ClaudeModels = { models: ClaudeModel[]; configured: boolean; credential_revision?: string; default_model?: string };

// A session entry triggers one fresh request. Realtime events and the normal
// thread polling must not repeatedly call the upstream Models API.
export function useClaudeModels(workspaceID: string, sessionID: string) {
  const key = `${workspaceID}:${sessionID}`;
  const [result, setResult] = useState<{ key: string; data: ClaudeModels | null; error: string }>({ key: "", data: null, error: "" });
  useEffect(() => {
    if (!workspaceID) { setResult({ key: "", data: null, error: "" }); return; }
    const controller = new AbortController();
    // Avoid duplicate upstream calls during StrictMode's mount replay.
    const timer = window.setTimeout(() => {
      api<ClaudeModels>(`/workspaces/${workspaceID}/claude/models`, { signal: controller.signal })
        .then(data => { if (!controller.signal.aborted) setResult({ key, data, error: "" }); })
        .catch(error => { if (!controller.signal.aborted) setResult({ key, data: null, error: error instanceof Error ? error.message : "Request failed" }); });
    }, 0);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [workspaceID, key]);
  return result.key === key ? { models: result.data?.models ?? [], data: result.data, error: result.error, loading: false } : { models: [], data: null, error: "", loading: Boolean(workspaceID) };
}
