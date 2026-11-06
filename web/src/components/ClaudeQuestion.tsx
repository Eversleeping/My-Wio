import { useEffect, useRef } from "react";
import { api } from "../api";
import type { Operation } from "../types";
import type { ClaudeDisplayItem } from "../claudeContent";
import { QuestionForm } from "./UserInputPrompt";
import { useI18n } from "../i18n";

export function ClaudeQuestion({ item, threadID, readOnly, onAnswered }: { item: ClaudeDisplayItem; threadID: string; readOnly: boolean; onAnswered: () => void }) {
  const { t } = useI18n();
  const pending = useRef<AbortController>();
  useEffect(() => () => pending.current?.abort(), []);
  return <div className="claude-question">{(item.cancelled || (readOnly && !item.answers)) && <p className="muted">{t("claude.questionExpired")}</p>}<QuestionForm heading={t("claude.userQuestion")} questions={item.questions ?? []} answers={item.answers} readOnly={readOnly} onSubmit={async values => {
    const controller = new AbortController(); pending.current = controller;
    const deadline = Date.now() + 30_000;
    const timeout = setTimeout(() => controller.abort(), 30_000);
    try {
      const result = await api<{operation_id:string}>(`/claude/threads/${threadID}/questions/${item.id}/answer`, { method: "POST", signal: controller.signal, body: JSON.stringify({ answers: Object.fromEntries(Object.entries(values).map(([id, answer]) => [id, { answers: Array.isArray(answer) ? answer : [answer] }])) }) });
      while (!controller.signal.aborted) {
        const operation = await api<Operation>(`/operations/${result.operation_id}`, { signal: controller.signal });
        if (operation.status === "succeeded") { onAnswered(); return; }
        if (["failed", "cancelled"].includes(operation.status)) throw new Error(operation.result || t("codex.answerFailed"));
        if (Date.now() >= deadline) throw new Error(t("claude.answerTimeout"));
        await new Promise<void>(resolve => {
          const finish = () => { clearTimeout(timer); controller.signal.removeEventListener("abort", finish); resolve(); };
          const timer = setTimeout(finish, 350); controller.signal.addEventListener("abort", finish, { once: true });
        });
      }
      throw new Error(t("codex.answerFailed"));
    } catch (error) { if (Date.now() >= deadline) throw new Error(t("claude.answerTimeout")); throw error; }
    finally { clearTimeout(timeout); controller.abort(); if (pending.current === controller) pending.current = undefined; }
  }} /></div>;
}
