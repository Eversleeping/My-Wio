import { useCallback, useEffect, useState } from "react";
import { api } from "./api";

export type ClaudeSkill = { name: string; description: string; argument_hint?: string };
type Discovery = { status: string; error?: string; skills?: ClaudeSkill[] };
export const validClaudeSkillName = (name: string) => name.length <= 128 && /^[A-Za-z0-9_][A-Za-z0-9_.:/-]*$/.test(name);

// Discovery initializes the CLI only. It never sends a prompt or starts a turn.
export function useClaudeSkills(workspaceID: string, sessionID: string, enabled: boolean) {
  const key = `${workspaceID}:${sessionID}`;
  const [revision, setRevision] = useState(0);
  const [state, setState] = useState<{ key: string; skills: ClaudeSkill[] | null; loading: boolean; error: string }>({ key: "", skills: null, loading: false, error: "" });
  const reload = useCallback(() => setRevision(value => value + 1), []);
  useEffect(() => {
    if (!enabled || !workspaceID) return;
    const controller = new AbortController();
    let poll: number | undefined;
    const timeout = window.setTimeout(() => {
      setState(current => ({ ...current, key, loading: false, error: "Claude skill discovery timed out" }));
      controller.abort();
      window.clearTimeout(poll);
    }, 60000);
    const timer = window.setTimeout(() => {
      setState(current => ({ key, skills: current.key === key ? current.skills : null, loading: true, error: "" }));
      void (async () => {
        const operation = await api<{ operation_id: string }>(`/workspaces/${workspaceID}/claude/skills`, { method: "POST", body: "{}", signal: controller.signal });
        if (!operation.operation_id) throw new Error("Invalid Claude skill discovery operation");
        const check = async () => {
          try {
            const result = await api<Discovery>(`/workspaces/${workspaceID}/claude/skills/${operation.operation_id}`, { signal: controller.signal });
            if (controller.signal.aborted) return;
            if (result.status === "succeeded") {
              if (!Array.isArray(result.skills)) throw new Error("Invalid Claude skills response");
              setState({ key, skills: result.skills.filter(skill => typeof skill.name === "string" && validClaudeSkillName(skill.name)), loading: false, error: "" });
              window.clearTimeout(timeout);
            } else if (["failed", "interrupted", "cancelled", "canceled"].includes(result.status)) {
              throw new Error(result.error || "Claude skill discovery failed");
            } else {
              poll = window.setTimeout(() => void check(), 750);
            }
          } catch (error) { fail(error); }
        };
        await check();
      })().catch(fail);
    }, 0);
    function fail(error: unknown) {
      if (controller.signal.aborted) return;
      window.clearTimeout(timeout);
      setState(current => ({ ...current, key, loading: false, error: error instanceof Error ? error.message : "Claude skill discovery failed" }));
    }
    return () => { controller.abort(); window.clearTimeout(timer); window.clearTimeout(poll); window.clearTimeout(timeout); };
  }, [workspaceID, key, enabled, revision]);
  const current = state.key === key ? state : { skills: null, loading: enabled, error: "" };
  return { ...current, loading: enabled && current.loading, reload };
}
