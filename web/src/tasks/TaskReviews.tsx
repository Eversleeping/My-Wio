import { useState } from "react";
import { Field } from "../components/FormUI";
import { RefreshWarning } from "../components/PageUI";
import { useResource } from "../data/resources";
import { reviews, taskCommand, type ReviewCheck, type TaskDetail, type TaskReview } from "../data/tasks";
import { useI18n } from "../i18n";
import { TaskForm } from "./TaskForm";
import { TaskRunForm } from "./TaskRunForms";

export function TaskReviews({ detail }: { detail: TaskDetail }) {
  const { t } = useI18n();
  const { task } = detail;
  const data = useResource(reviews.list(task.id, task.project_id));
  const [decision, setDecision] = useState<TaskReview | null>(null);
  const [recovery, setRecovery] = useState<{ review: TaskReview; action: "rework" | "repeat_review" } | null>(null);
  return <section aria-label={t("tasks.reviews")}>
    <h3>{t("tasks.reviews")}</h3>
    {data.error && <RefreshWarning error={data.error} reload={data.reload} />}
    {data.data?.items.length === 0 && <p>{t("tasks.review.empty")}</p>}
    <ul className="task-step-list">{data.data?.items.map(review => {
      const current = detail.steps.some(step => step.current_attempt_id === review.attempt_id);
      const attempt = detail.attempts.find(item => item.id === review.attempt_id);
      const active = task.state === "active" && detail.run?.state !== "cancelling" && current;
      return <li className="task-step" key={review.attempt_id}>
        <h4>{(detail.run_plan ?? detail.plan)?.steps.find(step => step.id === review.step_id)?.title ?? review.step_id}</h4>
        <span>{t(`tasks.review.${review.verdict ?? "pending"}`)}</span>
        <small>{t("tasks.artifact.digest")} <code>{review.artifact_digest}</code></small>
        {review.reason && <p className="task-text">{review.reason}</p>}
        {review.checks?.map(check => <div key={check.criterion_id}>
          <strong>{review.criteria.find(c => c.id === check.criterion_id)?.text}</strong> · {t(`tasks.review.check.${check.state}`)}
          {check.evidence && <p className="task-text">{check.evidence}</p>}
        </div>)}
        {active && !review.verdict && review.evidence_hash && <button className="secondary-button small" onClick={() => setDecision(review)}>{t("tasks.review.record")}</button>}
        {active && attempt && (review.verdict === "rejected" || review.verdict === "inconclusive") && <button className="secondary-button small"
          onClick={() => setRecovery({ review, action: review.verdict === "rejected" ? "rework" : "repeat_review" })}>
          {t(review.verdict === "rejected" ? "tasks.action.rework" : "tasks.action.repeat_review")}
        </button>}
      </li>;
    })}</ul>
    {decision && <ReviewForm detail={detail} review={decision} onClose={() => { setDecision(null); data.reload(); }} />}
    {recovery && <TaskRunForm detail={detail}
      dialog={{action: recovery.action, attempt: detail.attempts.find(item => item.id === recovery.review.attempt_id)}}
      onClose={() => { setRecovery(null); data.reload(); }} />}
  </section>;
}

function ReviewForm({ detail, review, onClose }: { detail: TaskDetail; review: TaskReview; onClose: () => void }) {
  const { t } = useI18n();
  const [verdict, setVerdict] = useState("inconclusive");
  const [reason, setReason] = useState("");
  const [checks, setChecks] = useState<ReviewCheck[]>(() => review.criteria.map(c => ({ criterion_id: c.id, state: "not_run", evidence: "" })));
  const change = (key: string, patch: Partial<ReviewCheck>) => setChecks(items => items.map(item => item.criterion_id === key ? { ...item, ...patch } : item));
  return <TaskForm title={t("tasks.review.record")} submitLabel="common.save" onClose={onClose}
    disabled={verdict === "approved" && checks.some(check => check.state !== "passed")}
    prepare={() => {
      const run = taskCommand(reviews.decide(detail.task.id, detail.task.project_id), detail.task.project_id, {
        expected_version: detail.task.version, attempt_id: review.attempt_id, artifact_id: review.artifact_id,
        evidence_hash: review.evidence_hash, verdict, reason, checks
      });
      return async () => { await run(); };
    }}>
    {checks.map(check => <fieldset className="task-form-card" key={check.criterion_id}>
      <legend>{review.criteria.find(c => c.id === check.criterion_id)?.text}</legend>
      <Field label={t("column.status")}><select value={check.state} onChange={event => change(check.criterion_id, { state: event.target.value as ReviewCheck["state"] })}>
        {["passed", "failed", "unknown", "not_run"].map(state => <option value={state} key={state}>{t(`tasks.review.check.${state}`)}</option>)}
      </select></Field>
      <Field label={t("tasks.evidence")}><textarea required={check.state === "passed" || check.state === "failed"} maxLength={4096}
        value={check.evidence} onChange={event => change(check.criterion_id, { evidence: event.target.value })} /></Field>
    </fieldset>)}
    <Field label={t("tasks.review.verdict")}><select value={verdict} onChange={event => setVerdict(event.target.value)}>
      {["approved", "rejected", "inconclusive"].map(value => <option value={value} key={value}>{t(`tasks.review.${value}`)}</option>)}
    </select></Field>
    <Field label={t("tasks.reason")}><textarea required maxLength={4096} value={reason} onChange={event => setReason(event.target.value)} /></Field>
  </TaskForm>;
}
