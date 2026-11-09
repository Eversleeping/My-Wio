import { useEffect, useState } from "react";
import { api } from "./api";

export type GeminiModels = { models: { id: string; name: string }[]; configured: boolean; credential_revision?: string; default_model?: string };

export function useGeminiModels(workspaceID: string, revision = "") {
  const key = `${workspaceID}:${revision}`;
  const [result, setResult] = useState<{ key: string; data: GeminiModels | null; error: string }>({ key: "", data: null, error: "" });
  useEffect(() => {
    if (!workspaceID) return;
    const controller = new AbortController();
    // Defer the refresh until StrictMode's mount replay has settled.
    const timer = window.setTimeout(() => {
      api<GeminiModels>(`/workspaces/${workspaceID}/gemini/models`, { signal: controller.signal })
        .then(data => { if (!controller.signal.aborted) setResult({ key, data, error: "" }); })
        .catch(error => { if (!controller.signal.aborted) setResult({ key, data: null, error: error instanceof Error ? error.message : "Request failed" }); });
    }, 0);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [workspaceID, key]);
  return result.key === key ? { ...result, loading: false } : { data: null, error: "", loading: Boolean(workspaceID) };
}

