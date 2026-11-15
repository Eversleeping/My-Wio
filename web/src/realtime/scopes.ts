import { type View } from "../navigation";
import { type StreamEvent } from "../types";

export type RealtimeScope = Exclude<View, "sessions"> | "approvals";

export type RealtimeRevisions = Record<RealtimeScope, number>;

export const allRealtimeScopes: RealtimeScope[] = ["dashboard", "servers", "projects", "codex", "gemini", "claude", "deployments", "operations", "monitoring", "settings", "approvals"];

export const initialRealtimeRevisions = (): RealtimeRevisions => ({ dashboard: 0, servers: 0, projects: 0, codex: 0, gemini: 0, claude: 0, deployments: 0, operations: 0, monitoring: 0, settings: 0, approvals: 0 });

export function realtimeScopesForEvent(event: Pick<Partial<StreamEvent>, "kind">): RealtimeScope[] {
  const kind = event.kind ?? "";
  if (kind.startsWith("gemini.")) return ["gemini", "operations"];
  if (kind.startsWith("claude.")) return ["claude", "operations"];
  if (kind === "inventory.updated") return ["dashboard", "servers", "projects", "deployments", "operations", "monitoring"];
  if (kind.startsWith("deployment.")) return ["dashboard", "deployments", "operations", "monitoring"];
  if (kind.startsWith("agent.")) return ["dashboard", "servers", "projects", "deployments", "operations", "monitoring"];
  if (kind.startsWith("approval.") || kind.startsWith("codex.approval.") || kind === "codex.serverRequest.resolved") return ["codex", "approvals"];
  if (["codex.turn.completed", "codex.turn.failed", "codex.turn.cancelled"].includes(kind)) return ["dashboard", "codex", "approvals"];
  if (kind === "user.message" || kind.startsWith("codex.") || kind.startsWith("thread.")) return ["dashboard", "codex"];
  return allRealtimeScopes;
}
