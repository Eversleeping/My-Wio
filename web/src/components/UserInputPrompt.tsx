import { useId, useState } from "react";
import { post } from "../api";
import { useI18n } from "../i18n";
import type { Approval } from "../types";

export const isUserInputRequest = (item: Approval) => item.kind === "item/tool/requestUserInput";
export type Question = { id: string; header: string; question: string; isOther?: boolean; isSecret?: boolean; options?: { label: string; description: string }[] | null };

export function UserInputPrompt({ item, onDecided }: { item: Approval; onDecided: () => void }) {
  const questions = ((item.detail as { questions?: Question[] })?.questions ?? []);
  return <QuestionForm questions={questions} onSubmit={async values => {
    await post(`/approvals/${item.id}/decision`, { decision: "answered", answers: Object.fromEntries(questions.map(question => [question.id, { answers: [values[question.id]] }])) });
    onDecided();
  }} />;
}

export function QuestionForm({ questions, onSubmit, answers = {}, readOnly = false }: { questions: Question[]; onSubmit: (values: Record<string, string>) => Promise<void>; answers?: Record<string, string>; readOnly?: boolean }) {
  const { t } = useI18n();
  const instanceID = useId();
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState("");
  const answered = sent || (questions.length > 0 && questions.every(question => answers[question.id] !== undefined));
  const disabled = busy || answered || readOnly;
  const valueFor = (id: string) => answers[id] ?? values[id] ?? "";
  const valid = questions.length > 0 && questions.every(question => valueFor(question.id).trim());
  const submit = async () => {
    if (!valid || disabled) return;
    setBusy(true); setError("");
    try {
      await onSubmit(values);
      setSent(true);
    } catch (failure) { setError(failure instanceof Error ? failure.message : t("codex.answerFailed")); }
    finally { setBusy(false); }
  };
  return <section className="user-input-prompt" aria-label={t("codex.userQuestion")}>
    <strong>{t("codex.userQuestion")}</strong>
    {questions.map(question => <fieldset key={question.id} disabled={disabled}>
      <legend>{question.header}</legend><p>{question.question}</p>
      {question.options?.map(option => <label className="user-input-option" key={option.label}>
        <input type="radio" name={`${instanceID}-${question.id}`} checked={valueFor(question.id) === option.label} onChange={() => setValues(current => ({ ...current, [question.id]: option.label }))} />
        <span><strong>{option.label}</strong><small>{option.description}</small></span>
      </label>)}
      {(!question.options?.length || question.isOther) && <label>{t("codex.customAnswer")}
        <input type={question.isSecret ? "password" : "text"} autoComplete="off" value={valueFor(question.id)} onChange={event => setValues(current => ({ ...current, [question.id]: event.target.value }))} />
      </label>}
    </fieldset>)}
    {error && <p role="alert">{error}</p>}
    <button type="button" className="primary-button" disabled={!valid || disabled} onClick={() => void submit()}>{t(answered ? "codex.answerSent" : "codex.submitAnswer")}</button>
  </section>;
}
