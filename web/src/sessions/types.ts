import type { ClaudeThread, GeminiThread, Thread } from "../types";

export type Engine = "codex" | "gemini" | "claude";
export type SessionRef = { engine: Engine; id: string };
export type SessionCapabilities = Record<"turns" | "images" | "steer" | "steer_images" | "rewrite" | "fork" | "fork_messages" | "fork_worktree" | "questions" | "approvals" | "goal" | "plan" | "compact" | "skills" | "skills_manage" | "effort" | "thinking" | "yolo" | "confirm_stopped" | "collaboration", boolean>;
export type SessionSummary = {
  id: string; engine?: Engine; native_id?: string; workspace_id: string;
  project_id: string; project_name: string; server_id: string; server_name: string;
  path: string; title: string; status: string; created_at: string; updated_at: string;
  pinned_at?: string | null; archived_at: string | null;
  project_pinned_at?: string | null; project_hidden_at?: string | null;
};
export type NativeSessions = { codex: Thread; gemini: GeminiThread; claude: ClaudeThread };
export type AISession<E extends Engine = Engine> = NativeSessions[E] & {
  engine: E; native_id: string; capabilities: SessionCapabilities;
  server_status: string; model: string; mode: string; effort: string; operation_id: string;
};
export type SessionGroupSummary = { workspace_id: string; matched_count: number; latest_updated_at: string; project_pinned_at: string | null };
export type SessionListPage<T = AISession> = { items: T[]; next_cursor: string | null; groups: SessionGroupSummary[] };
export type SessionFilter = { engine?: Engine; archived?: boolean | "all"; q?: string; limit?: number; cursor?: string | null };

export function sessionKey(ref: SessionRef) { return `${ref.engine}:${ref.id}`; }
