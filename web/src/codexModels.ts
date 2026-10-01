import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import type { CodexSnapshot } from "./types";

export interface CodexModel { id: string; model: string; displayName: string; description: string; isDefault: boolean }

export function useCodexModels(workspaceID?: string) {
  const [result, setResult] = useState<{ workspaceID: string; models: CodexModel[]; error: string }>({ workspaceID: "", models: [], error: "" });
  const [loading, setLoading] = useState(false);
  const [nonce, setNonce] = useState(0);
  const refresh = useCallback(() => setNonce(value => value + 1), []);
  useEffect(() => {
    if (!workspaceID) return;
    const controller = new AbortController();
    const path = `/workspaces/${workspaceID}/codex/models`;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const request = async () => {
      setLoading(true);
      setResult(current => ({ workspaceID, models: current.workspaceID === workspaceID ? current.models : [], error: "" }));
      try {
        // Always refresh when entering a workspace, even if its persisted snapshot
        // came from an older CLI. Never reuse another server's model catalogue.
        await api(`${path}/refresh`, { method: "POST", body: "{}", signal: controller.signal });
        const deadline = Date.now() + 30_000;
        const poll = async (): Promise<void> => {
          const snapshot = await api<CodexSnapshot<CodexModel[]>>(path, { signal: controller.signal });
          if (controller.signal.aborted) return;
          if (snapshot.status === "refreshing" || snapshot.status === "loading" || snapshot.status === "idle") {
            if (Date.now() >= deadline) throw new Error("Model list refresh timed out");
            timer = setTimeout(() => void poll().catch(fail), 500);
            return;
          }
          if (snapshot.status === "failed") throw new Error(snapshot.error || "Model list refresh failed");
          if (!snapshot.supported) throw new Error(snapshot.reason || "model/list is unsupported");
          if (!Array.isArray(snapshot.data)) throw new Error("Invalid model list");
          setResult({ workspaceID, models: snapshot.data, error: "" });
          setLoading(false);
        };
        await poll();
      } catch (error) { fail(error); }
    };
    function fail(error: unknown) {
      if (controller.signal.aborted) return;
      setResult({ workspaceID: workspaceID!, models: [], error: error instanceof Error ? error.message : "Model list refresh failed" });
      setLoading(false);
    }
    void request();
    return () => { controller.abort(); if (timer) clearTimeout(timer); };
  }, [workspaceID, nonce]);
  return { models: result.workspaceID === workspaceID ? result.models : [], error: result.workspaceID === workspaceID ? result.error : "", loading: Boolean(workspaceID) && loading, refresh };
}
