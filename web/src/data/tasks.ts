import type { Engine } from "../sessions/types";
import { APIError } from "./api";
import { invalidateResources, mutation, query, readResource, writeResource, type Mutation } from "./resources";

export type TaskState = "draft" | "active" | "awaiting_acceptance" | "accepted" | "rejected" | "failed" | "cancelled";
export type RunState = Exclude<TaskState, "draft" | "active"> | "running" | "blocked" | "paused" | "cancelling";
export type Criterion = { id: string; text: string };
export type TaskSpec = { title: string; requirement: string; criteria: Criterion[] };
export type Task = TaskSpec & {
  id: string;
  project_id: string;
  state: TaskState;
  version: number;
  spec_version: number;
  archived_at: string | null;
  updated_at: string;
  counters: { runs: number; plans: number; attempts: number };
};
export const roles = ["coordinator", "developer", "researcher", "reviewer"] as const;
export const stepKinds = ["plan", "develop", "research", "review", "summarize"] as const;
export type Member = {
  id: string;
  role: typeof roles[number];
  engine: Engine;
  workspace_id: string;
  model: string;
  mode: string;
  effort: string;
  directory_mode: "shared" | "existing_worktree";
};
export type StepInput = { kind: "text"; text: string } | { kind: "step_result"; step_id: string } | { kind: "artifact"; step_id: string };
export type Step = {
  id: string;
  member_id: string;
  kind: typeof stepKinds[number];
  title: string;
  instructions: string;
  input_refs: StepInput[] | null;
  depends_on: string[] | null;
  criterion_ids: string[] | null;
  required: true;
  completion_policy: "execution" | "artifact" | "review";
};
export type PlanInput = { members: Member[]; steps: Step[]; reason: string; parent_id?: string };
export type Plan = PlanInput & { id: string; revision: number; spec_version: number };
export type Run = {
  id: string;
  state: RunState;
  version: number;
  deadline_at: string | null;
  accepted_at?: string;
  acceptance_reason?: string;
};
export type StepRun = { step_id: string; state: string; current_attempt_id: string | null; block_code: string | null; artifact_id?: string; reworks?: number };
export type Attempt = {
  id: string; step_id: string; engine: Engine; session_id: string; operation_id: string | null;
  number: number; state: string; result_summary: string | null; outcome_certainty: string;
};
export const defaultBudgets = {
  concurrency: 2, messages: 100, reworks: 2, attempts: 100, step_attempts: 3, runs: 10, plan_revisions: 10, step_seconds: 3600, run_seconds: 86400
};
export const maxBudgets: typeof defaultBudgets = {
  concurrency: 4, messages: 500, reworks: 4, attempts: 500, step_attempts: 5, runs: 50, plan_revisions: 50, step_seconds: 86400, run_seconds: 604800
};
export type Budgets = typeof defaultBudgets;
export type Grant = { enabled: boolean; task_tools?: boolean; revision: number; budgets: Budgets };
export type TaskDetail = {
  task: Task; plan: Plan | null; run_plan?: Plan; run: Run | null; steps: StepRun[]; attempts: Attempt[]; grant: Grant | null;
  blockers: string[]; capabilities: { edit_plan: boolean; start_run: boolean; artifacts?: boolean; reviews?: boolean; collaboration?: boolean; graph?: boolean }; acceptance_evidence_hash?: string;
};
export type TaskPage = { items: Task[]; next_cursor: string | null };
export type RecentTask = Pick<Task, "id" | "title" | "project_id"> & { project_name: string; state: string };
export type TaskAction = "pause" | "resume" | "cancel" | "retry_step" | "takeover" | "resolve_unknown" | "rework" | "repeat_review";

const scope = ["tasks"] as const;
const segment = encodeURIComponent;
const base = (id: string) => `/tasks/${segment(id)}`;
const scoped = (path: string, projectID: string) => `${path}?${new URLSearchParams({ project_id: projectID })}`;

export const tasks = {
  recent: () => query<{ items: RecentTask[] }>("/tasks/recent", scope),
  locate: (id: string) => query<{ project_id: string }>(`${base(id)}/location`, scope),
  list: (projectID: string, state: string, cursor: string) => {
    const params = new URLSearchParams({ project_id: projectID, limit: "20" });
    if (state) params.set("state", state);
    if (cursor) params.set("cursor", cursor);
    return query<TaskPage>(`/tasks?${params}`, scope);
  },
  detail: (id: string, projectID: string) => query<TaskDetail>(scoped(base(id), projectID), scope),
  create: () => mutation<{ task: Task }>("/tasks", "POST", scope),
  update: (id: string, projectID: string) => mutation<{ task: Task }>(scoped(base(id), projectID), "PATCH", scope),
  plan: (id: string, projectID: string) => mutation(scoped(`${base(id)}/plans`, projectID), "POST", scope),
  grant: (id: string, projectID: string) => mutation(scoped(`${base(id)}/grant`, projectID), "PUT", scope),
  start: (id: string, projectID: string) => mutation(scoped(`${base(id)}/runs`, projectID), "POST", scope),
  action: (id: string, projectID: string, run: string) => mutation(scoped(`${base(id)}/runs/${segment(run)}/actions`, projectID), "POST", scope),
  accept: (id: string, projectID: string, run: string) => mutation(scoped(`${base(id)}/runs/${segment(run)}/acceptance`, projectID), "POST", scope),
  archive: (id: string, projectID: string) => mutation(scoped(`${base(id)}/archive`, projectID), "POST", scope)
};

export function taskCommand<T>(endpoint: Mutation<T>, projectID: string, body: object) {
  const requestID = crypto.randomUUID();
  const input = { ...body, request_id: requestID };
  return async () => {
    try {
      return await writeResource(endpoint, input, { signal: AbortSignal.timeout(15000) });
    } catch (error) {
      if (error instanceof APIError && error.status < 500 && error.status !== 408) {
        if (error.status === 409) invalidateResources(scope);
        throw error;
      }
      try {
        const found = await readResource(query<{ response: T }>(scoped(`/task-requests/${requestID}`, projectID), []), { signal: AbortSignal.timeout(5000) });
        invalidateResources(scope);
        return found.response;
      } catch {
        throw new TaskRequestPending(requestID);
      }
    }
  };
}

export class TaskRequestPending extends Error {
  constructor(readonly requestID: string) { super(requestID); }
}

export type Artifact = {
  id: string; attempt_id: string; workspace_id: string; revision: number; operation_id: string;
  state: string; digest: string; base_sha: string; error: string;
  files: { path: string; before_hash?: string; after_hash?: string }[] | null;
};
export type ArtifactHandoff = {
  id: string; artifact_id: string; workspace_id: string; operation_id: string; action: "preflight" | "apply"; state: string; error: string;
};
export const artifacts = {
  list: (id: string, project: string) => query<{ items: Artifact[]; handoffs: ArtifactHandoff[] }>(scoped(`${base(id)}/artifacts`, project), scope),
  capture: (id: string, project: string) => mutation<Artifact>(scoped(`${base(id)}/artifacts`, project), "POST", scope),
  handoff: (id: string, project: string, artifact: string) =>
    mutation<ArtifactHandoff>(scoped(`${base(id)}/artifacts/${segment(artifact)}/handoffs`, project), "POST", scope)
};

export type ReviewCheck = { criterion_id: string; state: "passed" | "failed" | "unknown" | "not_run"; evidence: string };
export type TaskReview = {
  attempt_id: string; artifact_id: string; artifact_digest: string; step_id: string; verdict: string | null;
  criteria: Criterion[]; checks: ReviewCheck[] | null; evidence_hash: string | null; reason: string | null;
};
export const reviews = {
  list: (id: string, project: string) => query<{ items: TaskReview[] }>(scoped(`${base(id)}/reviews`, project), scope),
  decide: (id: string, project: string) => mutation<TaskReview>(scoped(`${base(id)}/reviews`, project), "POST", scope)
};

export type TaskTeamRequest = {
  id: string; plan_id: string; run_id: string; kind: string; source_member_id: string;
  target_member_id: string | null; target_step_id: string | null; artifact_id: string | null;
  reason: string; state: string; requires_authorization: boolean; proposal?: Pick<PlanInput, "members" | "steps">;
};
export const taskHistory = {
  team: (id: string, project: string) => query<{ items: TaskTeamRequest[] }>(scoped(`${base(id)}/team`, project), scope),
  plans: (id: string, project: string) => query<{ items: { id: string; revision: number }[] }>(scoped(`${base(id)}/plans`, project), scope),
  plan: (id: string, project: string, plan: string) => query<Plan>(scoped(`${base(id)}/plans/${segment(plan)}`, project), scope)
};
