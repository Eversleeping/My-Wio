import { useState } from "react";
import { Field } from "../components/FormUI";
import { ErrorState } from "../components/PageUI";
import { useResource } from "../data/resources";
import { taskHistory, type TaskDetail } from "../data/tasks";
import { useI18n } from "../i18n";
import { TaskGraph } from "./TaskGraph";

export function TaskPlanHistory({ detail }: { detail: TaskDetail }) {
  const { t } = useI18n();
  const { task } = detail;
  const current = detail.run_plan ?? detail.plan;
  const [selection, setSelection] = useState("");
  const plans = useResource(taskHistory.plans(task.id, task.project_id));
  const old = useResource(selection ? taskHistory.plan(task.id, task.project_id, selection) : null);
  const chosen = selection ? old.data : current;
  return <section aria-label={t("tasks.graph")}>
    <div className="task-inline">
      <h3>{t("tasks.graph")}</h3>
      <Field label={t("tasks.graph.plan")}><select value={selection} onChange={event => setSelection(event.target.value)}>
        <option value="">{t("tasks.graph.current")}</option>
        {plans.data?.items.filter(plan => plan.id !== current?.id).map(plan => <option key={plan.id} value={plan.id}>
          {t("tasks.planVersion", {count: plan.revision})}
        </option>)}
      </select></Field>
    </div>
    {old.error && <ErrorState error={old.error} reload={old.reload} />}
    {chosen && <TaskGraph steps={chosen.steps} members={chosen.members} projection={chosen.id === current?.id ? detail.steps : []}
      onSelect={chosen.id === current?.id ? id => document.getElementById(`task-step-${id}`)?.scrollIntoView({block: "nearest"}) : undefined} />}
    {selection && chosen && <ul>{chosen.steps.map(step => <li className="task-text" key={step.id}><strong>{step.title}</strong><p>{step.instructions}</p></li>)}</ul>}
  </section>;
}
