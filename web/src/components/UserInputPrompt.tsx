import { useId, useState } from "react";
import { post } from "../api";
import { useI18n } from "../i18n";
import type { Approval } from "../types";

export const isUserInputRequest = (item: Approval) => item.kind === "item/tool/requestUserInput";
type Question = { id: string; header: string; question: string; isOther?: boolean; isSecret?: boolean; options?: { label: string; description: string }[] | null };

export function UserInputPrompt({ item, onDecided }: { item: Approval; onDecided: () => void }) {
  const { t } = useI18n();
  const instanceID = useId();
  const questions = ((item.detail as { questions?: Question[] })?.questions ?? []);
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState("");
  const valid = questions.length > 0 && questions.every(question => values[question.id]?.trim());
  const submit = async () => {
    if (!valid || busy || sent) return;
    setBusy(true); setError("");
    try {
      await post(`/approvals/${item.id}/decision`, { decision: "answered", answers: Object.fromEntries(questions.map(question => [question.id, { answers: [values[question.id]] }])) });
      setSent(true); onDecided();
    } catch (failure) { setError(failure instanceof Error ? failure.message : t("codex.answerFailed")); }
    finally { setBusy(false); }
  };
  return <section className="user-input-prompt" aria-label={t("codex.userQuestion")}>
    <strong>{t("codex.userQuestion")}</strong>
    {questions.map(question => <fieldset key={question.id} disabled={busy || sent}>
      <legend>{question.header}</legend><p>{question.question}</p>
      {question.options?.map(option => <label className="user-input-option" key={option.label}>
        <input type="radio" name={`${instanceID}-${question.id}`} checked={values[question.id] === option.label} onChange={() => setValues(current => ({ ...current, [question.id]: option.label }))} />
        <span><strong>{option.label}</strong><small>{option.description}</small></span>
      </label>)}
      {(!question.options?.length || question.isOther) && <label>{t("codex.customAnswer")}
        <input type={question.isSecret ? "password" : "text"} autoComplete="off" value={values[question.id] ?? ""} onChange={event => setValues(current => ({ ...current, [question.id]: event.target.value }))} />
      </label>}
    </fieldset>)}
    {error && <p role="alert">{error}</p>}
    <button type="button" className="primary-button" disabled={!valid || busy || sent} onClick={() => void submit()}>{t(sent ? "codex.answerSent" : "codex.submitAnswer")}</button>
  </section>;
}
