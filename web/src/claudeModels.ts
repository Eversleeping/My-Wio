import { useEffect, useState } from "react";
import { api } from "./api";

export type ClaudeModel = { id: string; name: string };
type ClaudeModels = { models: ClaudeModel[]; configured: boolean };

// A session entry triggers one fresh request. Realtime events and the normal
// thread polling must not repeatedly call the upstream Models API.
export function useClaudeModels(workspaceID: string, sessionID: string) {
  const key = `${workspaceID}:${sessionID}`;
  const [result, setResult] = useState<{ key: string; models: ClaudeModel[]; error: string }>({ key: "", models: [], error: "" });
  useEffect(() => {
    if (!workspaceID) return;
    const controller = new AbortController();
    // Avoid duplicate upstream calls during StrictMode's mount replay.
    const timer = window.setTimeout(() => {
      api<ClaudeModels>(`/workspaces/${workspaceID}/claude/models`, { signal: controller.signal })
        .then(data => { if (!controller.signal.aborted) setResult({ key, models: data.models ?? [], error: "" }); })
        .catch(error => { if (!controller.signal.aborted) setResult({ key, models: [], error: error instanceof Error ? error.message : "Request failed" }); });
    }, 0);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [workspaceID, key]);
  return result.key === key ? { models: result.models, error: result.error } : { models: [], error: "" };
}
