import { useState } from "react";
import { Field } from "../components/FormUI";
import { defaultBudgets, maxBudgets, taskCommand, tasks, type Attempt, type Budgets, type TaskAction, type TaskDetail } from "../data/tasks";
import { useI18n } from "../i18n";
import type { Workspace } from "../types";
import { TaskForm } from "./TaskForm";
import { engineLabels, workspaceLabel } from "./TaskPlanForm";

export type TaskDialog = "spec" | "plan" | "grant" | "start" | "archive" |
  { action: TaskAction | "accepted" | "rejected"; attempt?: Attempt; stepID?: string };

export function TaskGrantForm({ detail, workspaces, onClose }: {
  detail: TaskDetail; workspaces: Workspace[]; onClose: () => void;
}) {
  const { t } = useI18n();
  const [base] = useState(detail);
  const [taskTools, setTaskTools] = useState(base.grant?.task_tools ?? false);
  const [enabled, setEnabled] = useState(true);
  const [values, setValues] = useState<Budgets>({...defaultBudgets,...base.grant?.budgets});
  const members = base.plan?.members ?? [];
  return <TaskForm title={t("tasks.authorization")} submitLabel={enabled ? "tasks.authorize" : "tasks.revoke"}
    onClose={onClose} prepare={() => {
      if (base.plan?.id !== detail.plan?.id || base.grant?.revision !== detail.grant?.revision) throw new Error(t("tasks.draftOutdated"));
      const { id, project_id, version } = detail.task;
      const run = taskCommand(tasks.grant(id, project_id), project_id, {
        expected_version: version, enabled, task_tools: taskTools, budgets: values,
        allowed_members: members.map(member => member.id),
        workspace_ids: [...new Set(members.map(member => member.workspace_id))],
        execution_modes: Object.fromEntries(members.map(member => [member.id, { engine: member.engine, mode: member.mode }]))
      });
      return async () => { await run(); };
    }}>
    <p>{t("tasks.planVersion", { count: base.plan?.revision ?? 0 })}</p>
    <ul>{members.map(member => {
      const workspace = workspaces.find(item => item.id === member.workspace_id);
      return <li key={member.id}>{t(`tasks.role.${member.role}`)} · {engineLabels[member.engine]} · {t(`tasks.mode.${member.mode}`)} · {workspace ? workspaceLabel(workspace) : t("tasks.workspaceUnavailable")} · {t(member.directory_mode === "shared" ? "tasks.shared" : "tasks.existingWorktree")}</li>;
    })}</ul>
    {members.some(member => member.directory_mode === "shared") && <p>{t("tasks.sharedNotice")}</p>}
    {base.grant?.enabled && <label className="toggle-row">
      <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} />{t("tasks.allowExecution")}
    </label>}
    {base.capabilities.collaboration && <label className="toggle-row"><input type="checkbox" checked={taskTools}
      onChange={event => setTaskTools(event.target.checked)} />{t("tasks.team.enable")}</label>}
    {!enabled && <p>{t("tasks.revokeNotice")}</p>}
    <div className="task-form-grid">{(Object.keys(defaultBudgets) as (keyof Budgets)[]).map(key =>
      <Field key={key} label={t(`tasks.budget.${key}`)}>
        <input required type="number" min={key === "messages" || key === "reworks" ? 0 : 1} max={maxBudgets[key]} value={values[key]}
          onChange={e => setValues({ ...values, [key]: Number(e.target.value) })} />
      </Field>
    )}</div>
  </TaskForm>;
}

export function TaskRunForm({ detail, dialog, onClose }: {
  detail: TaskDetail; dialog: Exclude<TaskDialog, "spec" | "plan" | "grant">; onClose: () => void;
}) {
  const { t } = useI18n();
  const [base] = useState(detail);
  const [reason, setReason] = useState("");
  const [evidence, setEvidence] = useState("");
  const [stoppedAt, setStoppedAt] = useState("");
  const [resolution, setResolution] = useState("cancelled");
  const { task, run, plan, grant } = base;
  const action = typeof dialog === "object" ? dialog.action : dialog === "archive" && task.archived_at ? "restore" : dialog;
  const prepare = () => {
    const { id, project_id: project, version } = detail.task;
    if (dialog === "start" && plan && grant) {
      if (plan.id !== detail.plan?.id || grant.revision !== detail.grant?.revision) throw new Error(t("tasks.draftOutdated"));
      return taskCommand(tasks.start(id, project), project, { expected_version: version, plan_id: plan.id, grant_revision: grant.revision });
    }
    if (dialog === "archive") {
      if (task.archived_at !== detail.task.archived_at) throw new Error(t("tasks.draftOutdated"));
      return taskCommand(tasks.archive(id, project), project, { expected_version: version, archived: !task.archived_at });
    }
    if (!run || typeof dialog !== "object") throw new Error(t("tasks.runUnavailable"));
    if (run.id !== detail.run?.id) throw new Error(t("tasks.draftOutdated"));
    const meta = { expected_version: detail.run.version, reason: reason.trim() };
    if (dialog.action === "accepted" || dialog.action === "rejected") {
      return taskCommand(tasks.accept(id, project, run.id), project, { ...meta, decision: dialog.action, evidence_hash: base.acceptance_evidence_hash });
    }
    return taskCommand(tasks.action(id, project, run.id), project, {
      ...meta, action: dialog.action, step_id: dialog.stepID, attempt_id: dialog.attempt?.id,
      ...(dialog.action === "resolve_unknown" ? { stopped_at: new Date(stoppedAt).toISOString(), evidence: evidence.trim(), resolution } : {})
    });
  };
  return <TaskForm title={t(`tasks.action.${action}`)} submitLabel={`tasks.action.${action}`} onClose={onClose}
    prepare={() => { const execute = prepare(); return async () => { await execute(); }; }}>
    {action === "start" && <p>{t("tasks.confirmStart", { plan: plan?.revision ?? 0, grant: grant?.revision ?? 0 })}</p>}
    {action === "archive" && <p>{t("tasks.archiveNotice")}</p>}
    {action === "restore" && <p>{t("tasks.restoreNotice")}</p>}
    {action === "cancel" && <p>{t("tasks.cancelNotice")}</p>}
    {action === "takeover" && <p>{t("tasks.takeoverNotice")}</p>}
    {action === "resolve_unknown" && <>
      <p>{t("tasks.resolveNotice")}</p>
      <Field label={t("tasks.stoppedAt")}><input required type="datetime-local" value={stoppedAt} onChange={e => setStoppedAt(e.target.value)} /></Field>
      <Field label={t("tasks.stopEvidence")}><textarea required minLength={10} rows={3} value={evidence} onChange={e => setEvidence(e.target.value)} /></Field>
      <Field label={t("tasks.resolution")}><select value={resolution} onChange={e => setResolution(e.target.value)}>
        <option value="cancelled">{t("tasks.state.cancelled")}</option><option value="failed">{t("tasks.state.failed")}</option>
      </select></Field>
    </>}
    {typeof dialog === "object" && <Field label={t("tasks.reason")}><textarea required maxLength={4096} rows={3} value={reason} onChange={e => setReason(e.target.value)} /></Field>}
  </TaskForm>;
}
