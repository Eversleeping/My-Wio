import { useState } from "react";
import { Field } from "../components/FormUI";
import { RefreshWarning } from "../components/PageUI";
import { useResource } from "../data/resources";
import { taskCommand, taskHistory, tasks, type TaskDetail, type TaskTeamRequest } from "../data/tasks";
import { useI18n } from "../i18n";
import { TaskForm } from "./TaskForm";
import { TaskGraph } from "./TaskGraph";

export function TaskTeam({ detail }: { detail: TaskDetail }) {
  const { t } = useI18n();
  const { task } = detail;
  const data = useResource(taskHistory.team(task.id, task.project_id));
  const [proposal, setProposal] = useState<TaskTeamRequest | null>(null);
  return <section aria-label={t("tasks.team")}>
    <h3>{t("tasks.team")}</h3>
    {data.error && <RefreshWarning error={data.error} reload={data.reload} />}
    {data.data?.items.length === 0 && <p>{t("tasks.team.empty")}</p>}
    <ul className="task-step-list">{data.data?.items.map(item => <li className="task-step" key={item.id}>
      <div className="task-inline">
        <h4>{t(`tasks.team.kind.${item.kind}`)}</h4>
        <span className="task-status">{t(`tasks.team.state.${item.state}`)}</span>
      </div>
      <small>{item.source_member_id}{item.target_member_id && ` → ${item.target_member_id}`}</small>
      <p className="task-text">{item.reason}</p>
      {item.requires_authorization && <p className="task-notice">{t("tasks.team.expanded")}</p>}
      {item.proposal && <button className="secondary-button small" onClick={() => setProposal(item)}>{t("tasks.team.inspect")}</button>}
    </li>)}</ul>
    {proposal && <ProposalForm detail={detail} item={proposal} onClose={() => { setProposal(null); data.reload(); }} />}
  </section>;
}

function ProposalForm({ detail, item, onClose }: { detail: TaskDetail; item: TaskTeamRequest; onClose: () => void }) {
  const { t } = useI18n();
  const [base] = useState(detail);
  const [reason, setReason] = useState("");
  const plan = item.proposal!;
  const canAdopt = detail.capabilities.edit_plan && item.state === "pending" && detail.plan?.id === item.plan_id;
  return <TaskForm title={t("tasks.team.inspect")} submitLabel="tasks.team.adopt" onClose={onClose} disabled={!canAdopt}
    prepare={() => {
      const run = taskCommand(tasks.plan(base.task.id, base.task.project_id), base.task.project_id, {
        expected_version: base.task.version, parent_id: base.plan?.id, proposal_id: item.id, reason, ...plan
      });
      return async () => { await run(); };
    }}>
    <p className="task-text">{item.reason}</p>
    <TaskGraph steps={plan.steps} members={plan.members} />
    <ul>{plan.members.map(member => <li key={member.id}>{member.id} · {member.engine} · {member.mode} · {member.workspace_id}</li>)}</ul>
    <ul>{plan.steps.map(step => <li key={step.id}><strong>{step.title}</strong><p className="task-text">{step.instructions}</p></li>)}</ul>
    {!canAdopt && <p>{t("tasks.team.wait")}</p>}
    {canAdopt && <Field label={t("tasks.reason")}><textarea required value={reason} onChange={e => setReason(e.target.value)} /></Field>}
  </TaskForm>;
}
