import { ArrowUpRight, GitBranch, RefreshCw } from "lucide-react";
import { useState } from "react";
import { type Attempt, type TaskDetail as Snapshot } from "../data/tasks";
import { useI18n } from "../i18n";
import { locationFor } from "../navigation";
import type { Workspace } from "../types";
import { engineLabels, TaskPlanForm, workspaceLabel } from "./TaskPlanForm";
import { TaskGrantForm, TaskRunForm, type TaskDialog } from "./TaskRunForms";
import { TaskTeam } from "./TaskTeam";
import { TaskPlanHistory } from "./TaskPlanHistory";
import { TaskReviews } from "./TaskReviews";
import { TaskArtifacts } from "./TaskArtifacts";
import { TaskSpecForm } from "./TaskSpecForm";

export function TaskStatus({ state }: { state: string }) {
  const { t } = useI18n();
  return <span className={`task-status task-status-${state}`}>{t(`tasks.state.${state}`)}</span>;
}

function AttemptDetails({ attempt }: { attempt: Attempt }) {
  const { t } = useI18n();
  return <div className="task-attempt">
    <div className="task-inline">
      <span>{t("tasks.attempt", { count: attempt.number })} · {engineLabels[attempt.engine]}</span>
      <TaskStatus state={attempt.state} />
      <a className="text-button" href={locationFor("sessions", attempt.session_id, attempt.engine)}>{t("tasks.openSession")}<ArrowUpRight size={14} /></a>
    </div>
    {attempt.operation_id && <small>{t("tasks.operation")} <code>{attempt.operation_id}</code></small>}
    {attempt.result_summary && <details><summary>{t("tasks.evidence")}</summary><pre>{attempt.result_summary}</pre></details>}
  </div>;
}

export default function TaskDetail({ detail, workspaces, reload }: {
  detail: Snapshot; workspaces: Workspace[]; reload: () => void;
}) {
  const { t } = useI18n();
  const [dialog, setDialog] = useState<TaskDialog | null>(null);
  const { task, plan, run, grant, capabilities } = detail;
  const archived = Boolean(task.archived_at);
  const active = task.state === "active";
  const awaiting = run?.state === "awaiting_acceptance";
  const canManageAttempts = active && run?.state !== "cancelling";
  const executionPlan = detail.run_plan ?? plan;
  const currentPlan = plan?.spec_version === task.spec_version;
  const unavailableWorkspace = plan?.members.some(member => workspaces.some(item => item.id === member.workspace_id && item.status !== "ready"));
  const exhausted = Boolean(grant && (task.counters.runs >= grant.budgets.runs || task.counters.attempts >= grant.budgets.attempts));
  const close = () => { setDialog(null); reload(); };
  const action = (key: TaskDialog, label: string, primary = false) =>
    <button className={primary ? "primary-button small" : "secondary-button small"} onClick={() => setDialog(key)}>{t(label)}</button>;

  return <section className="section task-detail" aria-label={task.title}>
    <header className="task-detail-header">
      <div><h2>{task.title}</h2><div className="task-inline"><TaskStatus state={run?.state ?? task.state} />{archived && <TaskStatus state="archived" />}</div></div>
      <button className="icon-button" aria-label={t("tasks.refresh")} title={t("tasks.refresh")} onClick={reload}><RefreshCw size={16} /></button>
    </header>
    <div className="task-actions">
      {capabilities.edit_plan && action("spec", "tasks.editSpec")}
      {capabilities.edit_plan && action("plan", "tasks.editPlan")}
      {!archived && plan && run?.state !== "cancelling" && action("grant", "tasks.authorization")}
      {capabilities.start_run && !unavailableWorkspace && action("start", "tasks.action.start", true)}
      {(run?.state === "running" || run?.state === "blocked") && action({ action: "pause" }, "tasks.action.pause")}
      {run?.state === "paused" && action({ action: "resume" }, "tasks.action.resume")}
      {(active || awaiting) && run?.state !== "cancelling" && action({ action: "cancel" }, "tasks.action.cancel")}
      {!active && !awaiting && action("archive", archived ? "tasks.action.restore" : "tasks.action.archive")}
    </div>
    {!archived && plan && !currentPlan && <p className="task-notice">{t("tasks.planOutdated")}</p>}
    {!archived && currentPlan && !active && !awaiting && !capabilities.start_run && <p className="task-notice">
      {t(exhausted ? "tasks.block.attempt_budget" : "tasks.block.authorization_required")}
    </p>}
    {!archived && !active && !awaiting && unavailableWorkspace && <p className="task-notice">{t("tasks.block.workspace_not_ready")}</p>}
    {awaiting && <div className="task-notice">
      <p>{t("tasks.awaitingNotice")}</p>
      {detail.acceptance_evidence_hash ? <div className="task-actions">
        {action({ action: "accepted" }, "tasks.action.accepted", true)}
        {action({ action: "rejected" }, "tasks.action.rejected")}
      </div> : <p>{t("tasks.evidenceUnavailable")}</p>}
    </div>}
    {run?.acceptance_reason && <p className="task-notice">{run.acceptance_reason}</p>}
    <section aria-label={t("tasks.requirement")}>
      <div className="task-inline"><h3>{t("tasks.requirement")}</h3><small>{t("tasks.specVersion", { count: task.spec_version })}</small></div>
      <p className="task-text">{task.requirement}</p>
      <h3>{t("tasks.criteria")}</h3>
      <ul>{task.criteria.map(item => <li className="task-text" key={item.id}>{item.text}</li>)}</ul>
    </section>
    {executionPlan && capabilities.graph && <TaskPlanHistory detail={detail} />}
    {executionPlan && <section aria-label={t("tasks.steps")}>
      <div className="task-inline"><h3>{t("tasks.steps")}</h3><small>{t("tasks.planVersion", { count: executionPlan.revision })}</small></div>
      <ul className="task-step-list">{executionPlan.steps.map(step => {
        const projection = detail.steps.find(item => item.step_id === step.id);
        const history = detail.attempts.filter(item => item.step_id === step.id);
        const attempt = history.find(item => item.id === projection?.current_attempt_id);
        const member = executionPlan.members.find(item => item.id === step.member_id);
        const workspace = workspaces.find(item => item.id === member?.workspace_id);
        return <li className="task-step" id={`task-step-${step.id}`} key={step.id}>
          <div className="task-inline"><h4>{step.title}</h4><TaskStatus state={projection?.state ?? "pending"} /></div>
          {member && <small>{engineLabels[member.engine]} · {t(`tasks.role.${member.role}`)} · {workspace ? workspaceLabel(workspace) : t("tasks.workspaceUnavailable")}</small>}
          {!!step.depends_on?.length && <div className="task-dependencies"><GitBranch size={14} />{t("tasks.dependencies")} {step.depends_on.map(id => executionPlan.steps.find(item => item.id === id)?.title ?? id).join(" · ")}</div>}
          <p className="task-text">{step.instructions}</p>
          {!!step.input_refs?.length && <details>
            <summary>{t("tasks.inputs")}</summary>
            <ul>{step.input_refs.map((input, index) => <li className="task-text" key={index}>{input.kind === "text" ? input.text
              : t(input.kind === "artifact" ? "tasks.useArtifact" : "tasks.useStepResult", { title: executionPlan.steps.find(item => item.id === input.step_id)?.title ?? input.step_id })}</li>)}</ul>
          </details>}
          {projection?.block_code && <p className="task-notice">{t(`tasks.block.${projection.block_code}`)}</p>}
          {attempt && <AttemptDetails attempt={attempt} />}
          {canManageAttempts && attempt && ["failed", "cancelled", "succeeded"].includes(attempt.state) && <div className="task-actions">
            {projection?.state === "blocked" && action({ action: "retry_step", stepID: step.id }, "tasks.action.retry_step")}
            {action({ action: "takeover", attempt }, "tasks.action.takeover")}
          </div>}
          {active && attempt?.state === "unknown" && action({ action: "resolve_unknown", attempt }, "tasks.action.resolve_unknown")}
          {history.some(item => item.id !== attempt?.id) && <details>
            <summary>{t("tasks.attemptHistory")}</summary>
            {history.filter(item => item.id !== attempt?.id).map(item => <AttemptDetails attempt={item} key={item.id} />)}
          </details>}
        </li>;
      })}</ul>
    </section>}
    {capabilities.artifacts && <TaskArtifacts detail={detail} workspaces={workspaces} />}
    {capabilities.reviews && <TaskReviews detail={detail} />}
    {capabilities.collaboration && <TaskTeam detail={detail} />}
    {dialog === "spec" && <TaskSpecForm task={task} projectID={task.project_id} onClose={close} onSaved={close} />}
    {dialog === "plan" && <TaskPlanForm detail={detail} workspaces={workspaces} onClose={close} />}
    {dialog === "grant" && <TaskGrantForm detail={detail} workspaces={workspaces} onClose={close} />}
    {dialog && dialog !== "spec" && dialog !== "plan" && dialog !== "grant" && <TaskRunForm detail={detail} dialog={dialog} onClose={close} />}
  </section>;
}
