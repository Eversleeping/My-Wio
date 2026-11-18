import { useState } from "react";
import { Field } from "../components/FormUI";
import { RefreshWarning } from "../components/PageUI";
import { useResource } from "../data/resources";
import { artifacts, taskCommand, type Artifact, type TaskDetail } from "../data/tasks";
import { useI18n } from "../i18n";
import type { Workspace } from "../types";
import { TaskForm } from "./TaskForm";
import { workspaceLabel } from "./TaskPlanForm";

export function TaskArtifacts({ detail, workspaces }: { detail: TaskDetail; workspaces: Workspace[] }) {
  const { t } = useI18n();
  const [dialog, setDialog] = useState<"capture" | Artifact | null>(null);
  const { task } = detail;
  const data = useResource(artifacts.list(task.id, task.project_id));
  const candidates = detail.attempts.filter(attempt => attempt.state === "succeeded" && attempt.outcome_certainty === "confirmed" &&
    detail.steps.some(step => step.current_attempt_id === attempt.id));
  return <section aria-label={t("tasks.artifacts")}>
    <div className="task-inline"><h3>{t("tasks.artifacts")}</h3>
      {!task.archived_at && candidates.length > 0 && <button className="secondary-button small" onClick={() => setDialog("capture")}>{t("tasks.artifact.capture")}</button>}
    </div>
    {data.error && <RefreshWarning error={data.error} reload={data.reload} />}
    {data.data?.items.length === 0 && <p>{t("tasks.artifact.empty")}</p>}
    <ul className="task-step-list">{data.data?.items.map(artifact => <li className="task-step" key={artifact.id}>
      <div className="task-inline">
        <h4>{t("tasks.artifact.version", { count: artifact.revision })}</h4>
        <span className="task-status">{t(`tasks.state.${artifact.state}`)}</span>
      </div>
      {artifact.error && <p className="task-notice">{artifact.error}</p>}
      {artifact.state === "ready" && <>
        <small>{t("tasks.artifact.baseline")} <code>{artifact.base_sha}</code></small>
        <small>{t("tasks.artifact.digest")} <code>{artifact.digest}</code></small>
        <ul>{artifact.files?.map(file => <li key={file.path}><code>{file.path}</code> · {t(!file.before_hash ? "tasks.artifact.added" : !file.after_hash ? "tasks.artifact.deleted" : "tasks.artifact.modified")}</li>)}</ul>
        {!task.archived_at && <button className="secondary-button small" onClick={() => setDialog(artifact)}>{t("tasks.artifact.prepare")}</button>}
      </>}
      {data.data?.handoffs.filter(handoff => handoff.artifact_id === artifact.id).map(handoff => <div key={handoff.id} className="task-attempt">
        <span>{t(`tasks.artifact.${handoff.action}`)} · {workspaces.find(w => w.id === handoff.workspace_id)?.display_name || workspaces.find(w => w.id === handoff.workspace_id)?.path || handoff.workspace_id} · {t(`tasks.state.${handoff.state}`)}</span>
        {handoff.error && <p className="task-notice">{handoff.error}</p>}
      </div>)}
    </li>)}</ul>
    {dialog && <ArtifactForm detail={detail} artifact={dialog === "capture" ? undefined : dialog} workspaces={workspaces}
      candidateIDs={candidates.map(a => a.id)} onClose={() => { setDialog(null); data.reload(); }} />}
  </section>;
}

function ArtifactForm({ detail, artifact, workspaces, candidateIDs, onClose }: {
  detail: TaskDetail; artifact?: Artifact; workspaces: Workspace[]; candidateIDs: string[]; onClose: () => void;
}) {
  const { t } = useI18n();
  const [base] = useState(detail);
  const [attempt, setAttempt] = useState(candidateIDs[0] ?? "");
  const [paths, setPaths] = useState("");
  const [workspace, setWorkspace] = useState("");
  const [action, setAction] = useState("preflight");
  const targets = workspaces.filter(w => w.status === "ready" && (action === "preflight" || w.id !== artifact?.workspace_id && w.kind === "worktree" && w.management_mode === "managed"));
  const label = artifact ? "tasks.artifact.prepare" : "tasks.artifact.capture";
  return <TaskForm title={t(label)} submitLabel={label} onClose={onClose} prepare={() => {
    const { id, project_id, version } = detail.task;
    const run = artifact
      ? taskCommand(artifacts.handoff(id, project_id, artifact.id), project_id, { expected_version: version, digest: artifact.digest, workspace_id: workspace, action })
      : taskCommand(artifacts.capture(id, project_id), project_id, { expected_version: version, attempt_id: attempt, paths: paths.split("\n").map(p => p.trim()).filter(Boolean) });
    return async () => { await run(); };
  }}>
    {artifact ? <>
      <Field label={t("tasks.artifact.action")}><select value={action} onChange={event => { setAction(event.target.value); setWorkspace(""); }}>
        <option value="preflight">{t("tasks.artifact.preflight")}</option>
        <option value="apply">{t("tasks.artifact.apply")}</option>
      </select></Field>
      <Field label={t("tasks.workspace")}><select required value={workspace} onChange={event => setWorkspace(event.target.value)}>
        <option value="">{t("tasks.chooseWorkspace")}</option>
        {targets.map(w => <option key={w.id} value={w.id}>{workspaceLabel(w)}</option>)}
      </select></Field>
      {action === "apply" && <p>{t("tasks.artifact.separate")}</p>}
    </> : <>
      <Field label={t("tasks.artifact.attempt")}><select required value={attempt} onChange={event => setAttempt(event.target.value)}>
        {base.attempts.filter(a => candidateIDs.includes(a.id)).map(a => <option key={a.id} value={a.id}>{base.plan?.steps.find(s => s.id === a.step_id)?.title ?? a.step_id} · {t("tasks.attempt", { count: a.number })}</option>)}
      </select></Field>
      <Field label={t("tasks.artifact.paths")}><textarea required rows={5} value={paths} onChange={event => setPaths(event.target.value)} /></Field>
    </>}
  </TaskForm>;
}
