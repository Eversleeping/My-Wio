import { useId, useState } from "react";
import { Check, LoaderCircle, MessageCircleQuestion, Send } from "lucide-react";
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
  return <section className={`user-input-prompt${answered ? " answered" : ""}`} aria-label={t("codex.userQuestion")}>
    <div className="question-heading"><MessageCircleQuestion size={15} aria-hidden="true" /><strong>{t(answered ? "codex.questionAnswered" : "codex.userQuestion")}</strong></div>
    {questions.map(question => <fieldset key={question.id} disabled={disabled}>
      <legend className={questions.length === 1 && /^\d+$/.test(question.header) ? "question-number-only" : undefined}>{question.header}</legend><p className="question-text">{question.question}</p>
      {!!question.options?.length && <div className="question-options">{question.options.map(option => <label className={`user-input-option${valueFor(question.id) === option.label ? " selected" : ""}`} key={option.label}>
        <input type="radio" name={`${instanceID}-${question.id}`} checked={valueFor(question.id) === option.label} onChange={() => setValues(current => ({ ...current, [question.id]: option.label }))} />
        <span><strong>{option.label}</strong>{option.description && <small>{option.description}</small>}</span>
      </label>)}</div>}
      {(!question.options?.length || question.isOther) && <label className="question-custom-answer"><span>{t("codex.customAnswer")}</span>
        <input type={question.isSecret ? "password" : "text"} autoComplete="off" value={valueFor(question.id)} onChange={event => setValues(current => ({ ...current, [question.id]: event.target.value }))} />
      </label>}
    </fieldset>)}
    {error && <p className="question-error" role="alert">{error}</p>}
    <div className="question-actions"><button type="button" className={`${answered ? "question-sent" : "primary-button small"}`} disabled={!valid || disabled} onClick={() => void submit()}>{answered ? <Check size={14} aria-hidden="true" /> : busy ? <LoaderCircle className="spin" size={14} aria-hidden="true" /> : <Send size={13} aria-hidden="true" />}{t(answered ? "codex.answerSent" : "codex.submitAnswer")}</button></div>
  </section>;
}
