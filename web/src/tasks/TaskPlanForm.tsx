import { useState } from "react";
import { Field } from "../components/FormUI";
import { roles, stepKinds, tasks, taskCommand, type Member, type Step, type StepInput, type TaskDetail } from "../data/tasks";
import { useI18n } from "../i18n";
import type { Engine } from "../sessions/types";
import type { Workspace } from "../types";
import { TaskGraph, graphLayers } from "./TaskGraph";
import { TaskForm } from "./TaskForm";

export const engineLabels: Record<Engine, string> = { codex: "Codex", claude: "Claude", gemini: "Gemini" };
const modes: Record<Engine, string[]> = {
  codex: ["on-request", "untrusted", "never"],
  claude: ["default", "acceptEdits", "plan", "bypassPermissions"],
  gemini: ["default", "auto_edit", "plan", "yolo"]
};
const efforts: Record<Engine, string[]> = {
  codex: ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"],
  claude: ["low", "medium", "high", "xhigh", "max"],
  gemini: ["minimal", "low", "medium", "high"]
};

export function workspaceLabel(workspace: Workspace) {
  return [workspace.display_name || workspace.path, workspace.server_name].filter(Boolean).join(" · ");
}

function newMember(workspaces: Workspace[]): Member {
  return { id: crypto.randomUUID(), role: "developer", engine: "codex", mode: "on-request", model: "", effort: "",
    directory_mode: "shared", workspace_id: workspaces.find(item => item.status === "ready")?.id ?? "" };
}
function newStep(memberID: string): Step {
  return { id: crypto.randomUUID(), member_id: memberID, kind: "develop", title: "", instructions: "",
    input_refs: [], criterion_ids: [], depends_on: [], required: true, completion_policy: "execution" };
}
function toggle(values: string[] | null, key: string) {
  return values?.includes(key) ? values.filter(value => value !== key) : [...(values ?? []), key];
}
function predecessors(step: Step, steps: Step[]) {
  const found = new Set<string>();
  const walk = (ids: string[] | null) => {
    for (const id of ids ?? []) {
      if (id === step.id || found.has(id)) continue;
      found.add(id);
      walk(steps.find(candidate => candidate.id === id)?.depends_on ?? null);
    }
  };
  walk(step.depends_on);
  return steps.filter(candidate => found.has(candidate.id));
}

export function TaskPlanForm({ detail, workspaces, onClose }: {
  detail: TaskDetail; workspaces: Workspace[]; onClose: () => void;
}) {
  const { t } = useI18n();
  const [base] = useState(detail);
  const [members, setMembers] = useState(() => detail.plan?.members ?? [newMember(workspaces)]);
  const [steps, setSteps] = useState(() => detail.plan?.steps ?? [newStep(members[0].id)]);
  const [reason, setReason] = useState("");
  const updateMember = (id: string, change: Partial<Member>) => setMembers(items => items.map(item => item.id === id ? { ...item, ...change } : item));
  const updateStep = (id: string, change: Partial<Step>) => setSteps(items => items.map(item => item.id === id ? { ...item, ...change } : item));
  const ready = workspaces.filter(item => item.status === "ready");
  const memberLabel = (member: Member, index: number) => t("tasks.memberLabel", { count: index + 1, role: t(`tasks.role.${member.role}`), engine: engineLabels[member.engine] });
  const invalid = graphLayers(steps).invalid;
  const stepLabel = (step: Step) => step.title.trim() || t("tasks.stepLabel", { count: steps.indexOf(step) + 1 });

  return <TaskForm title={t("tasks.editPlan")} submitLabel="tasks.savePlan" onClose={onClose} disabled={!ready.length || invalid.size > 0}
    prepare={() => {
      const run = taskCommand(tasks.plan(base.task.id, base.task.project_id), base.task.project_id, {
        expected_version: base.task.version, parent_id: base.plan?.id, reason: reason.trim(), members,
        steps: steps.map(step => ({
          ...step,
          criterion_ids: (step.criterion_ids ?? []).filter(id => base.task.criteria.some(criterion => criterion.id === id))
        }))
      });
      return async () => { await run(); };
    }}>
    {!ready.length && <p role="status">{t("tasks.noReadyWorkspace")}</p>}
    <h3>{t("tasks.members")}</h3>
    {members.map((member, index) => <fieldset key={member.id} className="task-form-card">
      <legend>{memberLabel(member, index)}</legend>
      <div className="task-form-grid">
        <Field label={t("tasks.role")}>
          <select value={member.role} onChange={e => updateMember(member.id, { role: e.target.value as Member["role"] })}>
            {roles.map(role => <option key={role} value={role}>{t(`tasks.role.${role}`)}</option>)}
          </select>
        </Field>
        <Field label={t("tasks.engine")}>
          <select value={member.engine} onChange={e => {
            const engine = e.target.value as Engine;
            updateMember(member.id, { engine, mode: modes[engine][0], model: "", effort: "" });
          }}>
            {Object.entries(engineLabels).map(([id, label]) => <option key={id} value={id}>{label}</option>)}
          </select>
        </Field>
        <Field label={t("tasks.workspace")}>
          <select required value={ready.some(item => item.id === member.workspace_id) ? member.workspace_id : ""} onChange={e => {
            const workspace = ready.find(item => item.id === e.target.value);
            updateMember(member.id, { workspace_id: e.target.value, directory_mode: workspace?.kind === "worktree" ? member.directory_mode : "shared" });
          }}>
            <option value="">{t("tasks.chooseWorkspace")}</option>
            {ready.map(item => <option key={item.id} value={item.id}>{workspaceLabel(item)}</option>)}
          </select>
        </Field>
        <Field label={t("tasks.directoryMode")}>
          <select value={member.directory_mode} onChange={e => updateMember(member.id, { directory_mode: e.target.value as Member["directory_mode"] })}>
            <option value="shared">{t("tasks.shared")}</option>
            {ready.find(item => item.id === member.workspace_id)?.kind === "worktree" && <option value="existing_worktree">{t("tasks.existingWorktree")}</option>}
          </select>
        </Field>
        <Field label={t("tasks.mode")}><select value={member.mode} onChange={e => updateMember(member.id, { mode: e.target.value })}>
          {modes[member.engine].map(mode => <option key={mode} value={mode}>{t(`tasks.mode.${mode}`)}</option>)}
        </select></Field>
        <Field label={t("tasks.model")}><input maxLength={128} value={member.model} onChange={e => updateMember(member.id, { model: e.target.value })} placeholder={t("tasks.default")} /></Field>
        <Field label={t("tasks.effort")}><select value={member.effort} onChange={e => updateMember(member.id, { effort: e.target.value })}>
          <option value="">{t("tasks.default")}</option>
          {efforts[member.engine].map(effort => <option key={effort} value={effort}>{effort}</option>)}
        </select></Field>
      </div>
      <button type="button" className="secondary-button small" disabled={members.length === 1 || steps.some(step => step.member_id === member.id)}
        onClick={() => setMembers(items => items.filter(item => item.id !== member.id))}>{t("tasks.removeMember")}</button>
    </fieldset>)}
    <button type="button" className="secondary-button small" disabled={members.length >= 8}
      onClick={() => setMembers(items => [...items, newMember(ready)])}>{t("tasks.addMember")}</button>
    <h3>{t("tasks.steps")}</h3>
    <TaskGraph steps={steps} members={members} onSelect={id => document.getElementById(`edit-task-step-${id}`)?.scrollIntoView({block: "center"})} />
    {steps.map((step, index) => <fieldset key={step.id} id={`edit-task-step-${step.id}`} className="task-form-card">
      <legend>{t("tasks.stepLabel", { count: index + 1 })}</legend>
      {invalid.has(step.id) && <p className="form-error" role="status">{t("tasks.graph.invalid")}</p>}
      <div className="task-form-grid">
        <Field label={t("tasks.title")}><input required maxLength={200} value={step.title} onChange={e => updateStep(step.id, { title: e.target.value })} /></Field>
        <Field label={t("tasks.member")}><select value={step.member_id} onChange={e => updateStep(step.id, { member_id: e.target.value })}>
          {members.map((member, i) => <option key={member.id} value={member.id}>{memberLabel(member, i)}</option>)}
        </select></Field>
        <Field label={t("tasks.kind")}><select value={step.kind} onChange={e => updateStep(step.id, { kind: e.target.value as Step["kind"], completion_policy: "execution", input_refs: step.input_refs?.filter(input => input.kind !== "artifact") ?? [] })}>
          {stepKinds.map(kind => <option key={kind} value={kind}>{t(`tasks.kind.${kind}`)}</option>)}
        </select></Field>
        <Field label={t("tasks.policy")}>
          <select value={step.completion_policy} onChange={e => updateStep(step.id, {
            completion_policy: e.target.value as Step["completion_policy"],
            input_refs: e.target.value === "review" ? step.input_refs : step.input_refs?.filter(input => input.kind !== "artifact") ?? []
          })}>
            <option value="execution">{t("tasks.policy.execution")}</option>
            {step.kind === "develop" && <option value="artifact">{t("tasks.policy.artifact")}</option>}
            {step.kind === "review" && <option value="review">{t("tasks.policy.review")}</option>}
          </select>
        </Field>
      </div>
      <Field label={t("tasks.instructions")}><textarea required rows={3} value={step.instructions} onChange={e => updateStep(step.id, { instructions: e.target.value })} /></Field>
      {steps.length > 1 && <fieldset className="task-checks"><legend>{t("tasks.dependencies")}</legend>
        {steps.filter(other => other.id !== step.id).map(other => <label className="toggle-row" key={other.id}>
          <input type="checkbox" checked={step.depends_on?.includes(other.id) ?? false}
            onChange={() => updateStep(step.id, { depends_on: toggle(step.depends_on, other.id) })} />{stepLabel(other)}
        </label>)}
      </fieldset>}
      <fieldset className="task-checks"><legend>{t("tasks.criteria")}</legend>
        {base.task.criteria.map(criterion => <label className="toggle-row" key={criterion.id}>
          <input type="checkbox" checked={step.criterion_ids?.includes(criterion.id) ?? false}
            onChange={() => updateStep(step.id, { criterion_ids: toggle(step.criterion_ids, criterion.id) })} />{criterion.text}
        </label>)}
      </fieldset>
      <StepInputs step={step} ancestors={predecessors(step, steps)} stepLabel={stepLabel} onChange={inputs => updateStep(step.id, { input_refs: inputs })} />
      <button type="button" className="secondary-button small" disabled={steps.length === 1}
        onClick={() => setSteps(items => items.filter(item => item.id !== step.id).map(item => ({ ...item,
          depends_on: item.depends_on?.filter(id => id !== step.id) ?? [],
          input_refs: item.input_refs?.filter(input => input.kind === "text" || input.step_id !== step.id) ?? []
        })))}>{t("tasks.removeStep")}</button>
    </fieldset>)}
    <button type="button" className="secondary-button small" disabled={steps.length >= 50}
      onClick={() => setSteps(items => [...items, newStep(members[0].id)])}>{t("tasks.addStep")}</button>
    <Field label={t("tasks.reason")}><textarea required maxLength={4096} value={reason} onChange={e => setReason(e.target.value)} /></Field>
  </TaskForm>;
}

function StepInputs({ step, ancestors, stepLabel, onChange }: {
  step: Step; ancestors: Step[]; stepLabel: (step: Step) => string; onChange: (inputs: StepInput[]) => void;
}) {
  const { t } = useI18n();
  const inputs = step.input_refs ?? [];
  const text = inputs.filter(input => input.kind === "text");
  const artifacts = inputs.filter(input => input.kind === "artifact");
  const results = inputs.filter(input => input.kind === "step_result");
  return <fieldset className="task-checks"><legend>{t("tasks.inputs")}</legend>
    {step.completion_policy === "review" && <Field label={t("tasks.review.source")}>
      <select required value={artifacts[0]?.step_id ?? ""} onChange={e => onChange([...text, ...results, ...(e.target.value ? [{ kind: "artifact" as const, step_id: e.target.value }] : [])])}>
        <option value="">{t("tasks.review.chooseSource")}</option>
        {ancestors.filter(item => item.completion_policy === "artifact").map(item => <option value={item.id} key={item.id}>{stepLabel(item)}</option>)}
      </select>
    </Field>}
    {text.map((input, index) => <div className="task-inline" key={index}>
      <Field label={t("tasks.textInput", { count: index + 1 })}><textarea required value={input.text}
        onChange={e => onChange([...artifacts, ...text.map((item, i) => i === index ? { ...item, text: e.target.value } : item), ...results])} /></Field>
      <button type="button" className="secondary-button small"
        onClick={() => onChange([...artifacts, ...text.filter((_, i) => i !== index), ...results])}>{t("tasks.removeInput")}</button>
    </div>)}
    {Array.from(new Set([...ancestors.map(item => item.id), ...results.map(item => item.step_id)])).map(id => {
      const ancestor = ancestors.find(item => item.id === id);
      return <label className="toggle-row" key={id}>
        <input type="checkbox" checked={results.some(item => item.step_id === id)}
          onChange={() => onChange([...artifacts, ...text, ...toggle(results.map(item => item.step_id), id).map(step_id => ({ kind: "step_result" as const, step_id }))])} />
        {ancestor ? t("tasks.useStepResult", { title: stepLabel(ancestor) }) : t("tasks.missingAncestor")}
      </label>;
    })}
    <button type="button" className="secondary-button small" disabled={inputs.length >= 100}
      onClick={() => onChange([...artifacts, ...text, { kind: "text", text: "" }, ...results])}>{t("tasks.addInput")}</button>
  </fieldset>;
}
