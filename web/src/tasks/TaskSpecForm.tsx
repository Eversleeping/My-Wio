import { useRef, useState } from "react";
import { Field } from "../components/FormUI";
import { tasks, taskCommand, type Task } from "../data/tasks";
import { useI18n } from "../i18n";
import { TaskForm } from "./TaskForm";

export function TaskSpecForm({ projectID, task, onSaved, onClose }: {
  projectID: string; task?: Task; onSaved: (task: Task) => void; onClose: () => void;
}) {
  const { t } = useI18n();
  const saved = useRef<Task | null>(null);
  const [base] = useState(task);
  const [title, setTitle] = useState(task?.title ?? "");
  const [requirement, setRequirement] = useState(task?.requirement ?? "");
  const [criteria, setCriteria] = useState(() => task?.criteria ?? [{ id: crypto.randomUUID(), text: "" }]);
  return <TaskForm title={t(base ? "tasks.editSpec" : "tasks.new")} submitLabel={base ? "common.save" : "tasks.createDraft"} onClose={onClose}
    onSuccess={() => { if (saved.current) onSaved(saved.current); }}
    prepare={() => {
      const spec = { title: title.trim(), requirement: requirement.trim(), criteria: criteria.map(item => ({ ...item, text: item.text.trim() })) };
      const run = base
        ? taskCommand(tasks.update(base.id, projectID), projectID, {
          expected_version: base.version,
          ...(base.title !== spec.title ? { title: spec.title } : {}),
          ...(base.requirement !== spec.requirement ? { requirement: spec.requirement } : {}),
          ...(JSON.stringify(base.criteria) !== JSON.stringify(spec.criteria) ? { criteria: spec.criteria } : {})
        })
        : taskCommand(tasks.create(), projectID, { project_id: projectID, ...spec });
      return async () => { saved.current = (await run()).task; };
    }}>
    <Field label={t("tasks.title")}><input autoFocus required maxLength={200} value={title} onChange={event => setTitle(event.target.value)} /></Field>
    <Field label={t("tasks.requirement")}><textarea required rows={4} value={requirement} onChange={event => setRequirement(event.target.value)} /></Field>
    <h3>{t("tasks.criteria")}</h3>
    {criteria.map((item, index) => <div key={item.id} className="task-inline">
      <Field label={t("tasks.criterion", { count: index + 1 })}><textarea required rows={2} value={item.text}
        onChange={event => setCriteria(criteria.map(current => current.id === item.id ? { ...current, text: event.target.value } : current))} /></Field>
      <button type="button" className="secondary-button small" disabled={criteria.length === 1}
        onClick={() => setCriteria(criteria.filter(other => other.id !== item.id))}>{t("tasks.removeCriterion", { count: index + 1 })}</button>
    </div>)}
    <button type="button" className="secondary-button small" disabled={criteria.length >= 50}
      onClick={() => setCriteria([...criteria, { id: crypto.randomUUID(), text: "" }])}>{t("tasks.addCriterion")}</button>
  </TaskForm>;
}
