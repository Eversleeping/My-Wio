import type { Question } from "./components/UserInputPrompt";

type QuestionReply = { questionItemId: string; question: string; answer: string };
const replyStart = "<send_user_message_question_reply>";
const replyEnd = "</send_user_message_question_reply>";

// Async questions are agentMessage items, rather than pending server RPCs.
export function asyncQuestions(item: Record<string, unknown> | null): Question[] {
  if (item?.type !== "agentMessage" || item.delivery !== "async" || typeof item.id !== "string" || !Array.isArray(item.questions)) return [];
  return item.questions.flatMap((value: unknown, index: number) => {
    if (!value || typeof value !== "object" || !("title" in value) || typeof value.title !== "string" || !value.title.trim()) return [];
    const options = "options" in value && Array.isArray(value.options) ? value.options.filter((option): option is string => typeof option === "string") : [];
    return [{ id: JSON.stringify(["request_user_input_async", item.id, index]), header: String(index + 1), question: value.title, isOther: true, options: options.map(label => ({ label, description: "" })) }];
  });
}

export function questionReply(questions: Question[], answers: Record<string, string>): string {
  return `${replyStart}\n${JSON.stringify(questions.map(question => ({ questionItemId: question.id, question: question.question, answer: answers[question.id] })))}\n${replyEnd}`;
}

export function parseQuestionReply(text: string): QuestionReply[] {
  const trimmed = text.trim();
  if (!trimmed.startsWith(replyStart) || !trimmed.endsWith(replyEnd)) return [];
  try {
    const value: unknown = JSON.parse(trimmed.slice(replyStart.length, -replyEnd.length));
    return Array.isArray(value) ? value.filter((reply): reply is QuestionReply => Boolean(reply && typeof reply.questionItemId === "string" && typeof reply.question === "string" && typeof reply.answer === "string")) : [];
  } catch { return []; }
}

export function readableQuestionReply(text: string): string {
  const replies = parseQuestionReply(text);
  return replies.length ? replies.map(reply => `${reply.question}\n${reply.answer}`).join("\n\n") : text;
}
