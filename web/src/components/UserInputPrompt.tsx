import { useId, useState } from "react";
import { Check, LoaderCircle, MessageCircleQuestion, Send } from "lucide-react";
import { post } from "../api";
import { useI18n } from "../i18n";
import type { Approval } from "../types";

export const isUserInputRequest = (item: Approval) => item.kind === "item/tool/requestUserInput";
export type Question = { id: string; header: string; question: string; multiSelect?: boolean; isOther?: boolean; isSecret?: boolean; options?: { label: string; description: string }[] | null };

export function UserInputPrompt({ item, onDecided }: { item: Approval; onDecided: () => void }) {
  const questions = ((item.detail as { questions?: Question[] })?.questions ?? []);
  return <QuestionForm questions={questions} onSubmit={async values => {
    await post(`/approvals/${item.id}/decision`, { decision: "answered", answers: Object.fromEntries(questions.map(question => [question.id, { answers: Array.isArray(values[question.id]) ? values[question.id] : [values[question.id]] }])) });
    onDecided();
  }} />;
}

export type QuestionValue = string | string[];

export function QuestionForm({ questions, onSubmit, answers = {}, readOnly = false, heading }: { questions: Question[]; onSubmit: (values: Record<string, QuestionValue>) => Promise<void>; answers?: Record<string, QuestionValue>; readOnly?: boolean; heading?: string }) {
  const { t } = useI18n();
  const instanceID = useId();
  const [values, setValues] = useState<Record<string, QuestionValue>>({});
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState("");
  const answered = sent || (questions.length > 0 && questions.every(question => answers[question.id] !== undefined));
  const disabled = busy || answered || readOnly;
  const questionLabel = heading ?? t("codex.userQuestion");
  const valueFor = (id: string) => answers[id] ?? values[id] ?? "";
  const valid = questions.length > 0 && questions.every(question => { const value=valueFor(question.id); return Array.isArray(value) ? value.length > 0 && value.every(item => item.trim()) : value.trim(); });
  const selected = (question: Question, label: string) => { const value=valueFor(question.id); return Array.isArray(value) ? value.includes(label) : value === label; };
  const choose = (question: Question, label: string) => setValues(current => { const value=current[question.id]; const choices=Array.isArray(value) ? value : []; return {...current,[question.id]:question.multiSelect ? choices.includes(label) ? choices.filter(item=>item!==label) : [...choices,label] : label}; });
  const submit = async () => {
    if (!valid || disabled) return;
    setBusy(true); setError("");
    try {
      await onSubmit(values);
      setSent(true);
    } catch (failure) { setError(failure instanceof Error ? failure.message : t("codex.answerFailed")); }
    finally { setBusy(false); }
  };
  return <section className={`user-input-prompt${answered ? " answered" : ""}`} aria-label={questionLabel}>
    <div className="question-heading"><MessageCircleQuestion size={15} aria-hidden="true" /><strong>{answered ? t("codex.questionAnswered") : questionLabel}</strong></div>
    {questions.map(question => <fieldset key={question.id} disabled={disabled}>
      <legend className={questions.length === 1 && /^\d+$/.test(question.header) ? "question-number-only" : undefined}>{question.header}</legend><p className="question-text">{question.question}</p>
      {!!question.options?.length && <div className="question-options">{question.options.map(option => <label className={`user-input-option${selected(question, option.label) ? " selected" : ""}`} key={option.label}>
        <input type={question.multiSelect ? "checkbox" : "radio"} name={`${instanceID}-${question.id}`} checked={selected(question, option.label)} onChange={() => choose(question, option.label)} />
        <span><strong>{option.label}</strong>{option.description && <small>{option.description}</small>}</span>
      </label>)}</div>}
      {(!question.options?.length || question.isOther) && <label className="question-custom-answer"><span>{t("codex.customAnswer")}</span>
        <input type={question.isSecret ? "password" : "text"} autoComplete="off" value={Array.isArray(valueFor(question.id)) ? (valueFor(question.id) as string[]).filter(value => !question.options?.some(option=>option.label===value)).join(", ") : valueFor(question.id) as string} onChange={event => { const text=event.target.value; setValues(current => ({ ...current, [question.id]: question.multiSelect ? [...(Array.isArray(current[question.id]) ? current[question.id] as string[] : []).filter(value=>question.options?.some(option=>option.label===value)), ...(text.trim() ? [text] : [])] : text })); }} />
      </label>}
    </fieldset>)}
    {error && <p className="question-error" role="alert">{error}</p>}
    <div className="question-actions"><button type="button" className={`${answered ? "question-sent" : "primary-button small"}`} disabled={!valid || disabled} onClick={() => void submit()}>{answered ? <Check size={14} aria-hidden="true" /> : busy ? <LoaderCircle className="spin" size={14} aria-hidden="true" /> : <Send size={13} aria-hidden="true" />}{t(answered ? "codex.answerSent" : "codex.submitAnswer")}</button></div>
  </section>;
}
