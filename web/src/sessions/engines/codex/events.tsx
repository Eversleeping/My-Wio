import {
AlertTriangle,
Ban,
Bot,
Braces,
ChevronRight,
Copy,
GitBranch,
GitFork,
Image as ImageIcon,
Minimize2,
MoreHorizontal,
Pencil,
SquareTerminal,
UserRound,
Users,
Wrench
} from "lucide-react";
import { asyncQuestions,readableQuestionReply } from "../../../asyncUserInput";
import { type FilePreviewSelection } from "../../../codexContent";
import { MarkdownContent } from "../../../components/LazyMarkdown";
import { MessageImages } from "../../../components/MessageImages";
import { Status } from "../../../components/PageUI";
import { QuestionForm } from "../../../components/UserInputPrompt";
import { ContextMenu } from "../../../ContextMenu";
import { formatDateTime } from "../../../format";
import { useI18n } from "../../../i18n";
import { extractImageSources } from "../../../imageContent";
import { copyText,message,pretty } from "../../../text";
import { type StreamEvent } from "../../../types";
import { asRecord,errorText,extractText,readableKind,toolDetail,toolSummary } from "./conversation";

export function ConversationEventItem({ event, onEdit, onFork, forkDisabled = false, notify, workspaceRoot, onOpenFile, questionAnswers = {}, onAnswer }: { event: StreamEvent; onEdit?: (eventID: string, text: string) => void; onFork?: (eventID: string) => Promise<void>; forkDisabled?: boolean; notify: (text: string) => void; workspaceRoot: string; onOpenFile: (selection: FilePreviewSelection) => void; questionAnswers?: Record<string, string>; onAnswer?: (values: Record<string, string>) => Promise<void> }) {
  const { t } = useI18n();
  const kind = event.kind;
  const payload = asRecord(event.payload);
  if (kind === "user.message") {
    const originalText = String(payload?.text ?? "");
    const text = readableQuestionReply(originalText);

    const images = extractImageSources(payload?.images);
    const imageCount = Math.max(images.length, Number(payload?.image_count ?? 0));
    const copyMessage = async () => { try { await copyText(text); notify(t("codex.messageCopied")); } catch (error) { notify(message(error)); } };
    return <article className="message user"><header><UserRound size={15} /><strong>{t(payload?.steer ? "codex.youSteering" : "codex.you")}</strong><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></header>{text && <MarkdownContent text={text} workspaceRoot={workspaceRoot} onOpenFile={onOpenFile} />}{images.length > 0 ? <MessageImages sources={images} /> : imageCount > 0 && <span className="message-image-count"><ImageIcon size={14} />{imageCount}</span>}<div className="message-actions"><button type="button" className="message-action" disabled={!text} title={t("codex.copyMessage")} aria-label={t("codex.copyMessage")} onClick={() => void copyMessage()}><Copy size={14} /></button>{onEdit && text === originalText && !payload?.steer && <button type="button" className="message-action" disabled={!text} title={t("codex.editMessage")} aria-label={t("codex.editMessage")} onClick={() => onEdit(event.event_id, text)}><Pencil size={14} /></button>}</div></article>;
  }
  if (kind === "codex.turn.cancelled") return <article className="message interrupted"><header><Ban size={15} /><strong>{t("codex.turnInterrupted")}</strong><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></header><div className="message-content">{t("codex.turnInterruptedDetail")}</div></article>;
  if (kind === "codex.error" && payload?.willRetry === true) return null;
  if (kind === "codex.error" || kind === "codex.turn.failed" || kind === "codex.interrupt.failed" || kind === "codex.approval.failed" || (kind === "codex.compact.failed" || kind === "codex.steer.failed")) return <article className="message error"><header><AlertTriangle size={15} /><strong>{t(kind === "codex.turn.failed" || kind === "codex.error" ? "codex.turnFailed" : "codex.actionFailed")}</strong><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></header><div className="message-content">{errorText(payload) || t("codex.unknownError")}</div></article>;
  if (kind === "codex.turn.completed") {
    const turn = asRecord(payload?.turn);
    const status = String(turn?.status ?? "failed");
    if (status === "interrupted") return <article className="message interrupted"><header><Ban size={15} /><strong>{t("codex.turnInterrupted")}</strong><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></header><div className="message-content">{t("codex.turnInterruptedDetail")}</div></article>;
    return <article className="message error"><header><AlertTriangle size={15} /><strong>{t("codex.turnFailed")}</strong><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></header><div className="message-content">{errorText(turn) || t("codex.unknownError")}</div></article>;
  }
  const item = asRecord(payload?.item);
  const itemImages = extractImageSources(item);
  if (item?.type === "contextCompaction") return <article className="context-compaction-event"><header><Minimize2 size={15} /><strong>{t(kind === "codex.item.started" ? "codex.compacting" : "codex.compacted")}</strong><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></header><div className="message-content">{t(kind === "codex.item.started" ? "codex.compactingDetail" : "codex.compactedDetail")}</div></article>;
  if (item?.type === "collabToolCall" || item?.type === "collabAgentToolCall" || item?.type === "subAgentActivity") return <SubagentEvent event={event} item={item} />;
  if (item?.type === "agentMessage" || item?.type === "plan" || itemImages.length > 0) {
    const text = extractText(item);
    const questions = asyncQuestions(item);
    const copyValue = questions.length ? questions.map(question => question.question).join("\n\n") : text;
    const actions = <div className="message-actions"><button type="button" className="message-action" disabled={!copyValue} title={t("codex.copyMessage")} aria-label={t("codex.copyMessage")} onClick={() => void copyText(copyValue).then(() => notify(t("codex.messageCopied"))).catch(error => notify(message(error)))}><Copy size={14} /></button>{onFork && <ContextMenu className="message-menu" label={t("codex.messageMenu")} triggerIcon={MoreHorizontal} actions={[{ id: "fork", label: t("codex.branchToNewChat"), icon: GitFork, disabled: forkDisabled, disabledReason: t("codex.forkWaitForTurn"), onSelect: () => onFork(event.event_id) }]}>{null}</ContextMenu>}</div>;
    if (questions.length) return <article className="message assistant"><header><Bot size={15} /><strong>Codex</strong><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></header><QuestionForm questions={questions} answers={questionAnswers} readOnly={!onAnswer} onSubmit={async values => { await onAnswer?.(Object.fromEntries(Object.entries(values).map(([id, value]) => [id, Array.isArray(value) ? value.join(", ") : value]))); }} />{actions}</article>;
    return <article className="message assistant"><header><Bot size={15} /><strong>Codex</strong><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></header>{text && <MarkdownContent text={text} workspaceRoot={workspaceRoot} onOpenFile={onOpenFile} />}{itemImages.length > 0 && <MessageImages sources={itemImages} />}{actions}</article>;
  }
  return <ToolEvent event={event} item={item} />;
}

function SubagentEvent({ event, item }: { event: StreamEvent; item: Record<string, unknown> }) {
  const { t } = useI18n();
  const activity = item.type === "subAgentActivity";
  const agentStatus = asRecord(item.agentStatus);
  const status = activity ? String(item.kind ?? item.status ?? "started") : String(agentStatus?.status ?? (typeof item.agentStatus === "string" ? item.agentStatus : item.status ?? "inProgress"));
  const receiverIDs = Array.isArray(item.receiverThreadIds) ? item.receiverThreadIds.map(String) : [item.receiverThreadId, item.newThreadId].filter(value => typeof value === "string" && value).map(String);
  const states = asRecord(item.agentsStates) ?? {};
  const summary = activity ? String(item.agentPath ?? item.agentThreadId ?? "") : `${String(item.tool ?? "spawnAgent")} · ${receiverIDs.length || Object.keys(states).length} ${t("codex.subagentThreads")}`;
  return <article className="subagent-event"><header><Users size={15} /><strong>{t(activity ? "codex.subagentActivity" : "codex.subagentToolCall")}</strong><Status value={status} /><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></header><div className="message-content">{summary}</div>{typeof item.prompt === "string" && item.prompt && <p>{item.prompt}</p>}{receiverIDs.length > 0 && <code>{receiverIDs.join(", ")}</code>}</article>;
}

function ToolEvent({ event, item }: { event: StreamEvent; item: Record<string, unknown> | null }) {
  const { t } = useI18n();
  const type = String(item?.type ?? "tool");
  const title = type === "commandExecution" ? t("codex.command") : type === "fileChange" ? t("codex.changes") : type === "webSearch" ? t("codex.webSearch") : type === "mcpToolCall" ? `${String(item?.server ?? "MCP")} / ${String(item?.tool ?? t("codex.toolCall"))}` : String(item?.tool ?? t("codex.toolCall"));
  const summary = toolSummary(item, t);
  const detail = toolDetail(item, t);
  return <details className={`tool-event ${type === "fileChange" ? "change" : ""}`}><summary><span>{type === "fileChange" ? <GitBranch size={15} /> : type === "commandExecution" ? <SquareTerminal size={15} /> : <Wrench size={15} />}<strong>{title}</strong>{summary && <small>{summary}</small>}</span><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></summary>{detail && <pre>{detail}</pre>}</details>;
}

export function CommandEventGroup({ events }: { events: StreamEvent[] }) {
  const { t } = useI18n();
  return <details className="command-event-group"><summary><SquareTerminal size={16} /><strong>{t("codex.commandsRun", { count: events.length })}</strong><ChevronRight className="command-group-chevron" size={16} /></summary><div className="command-event-group-items">{events.map(event => { const payload = asRecord(event.payload); return <ToolEvent key={event.event_id} event={event} item={asRecord(payload?.item)} />; })}</div></details>;
}

export function RawEventItem({ event }: { event: StreamEvent }) {
  return <details className="raw-event"><summary><span><Braces size={14} /><strong>{readableKind(event.kind)}</strong></span><time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time></summary><pre>{pretty(event.payload)}</pre></details>;
}

export function WorkingIndicator({ events }: { events: StreamEvent[] }) {
  const { t } = useI18n();
  let label = "codex.working";
  for (let index = events.length - 1; index >= 0; index--) {
    const event = events[index], payload = asRecord(event.payload), item = asRecord(payload?.item);
    if (event.kind === "user.message" || event.kind === "codex.turn.started") break;
    if (event.kind === "codex.error" && payload?.willRetry === true) { label = "codex.reconnectingModel"; break; }
    if (event.kind === "codex.item.completed" && item?.type === "agentMessage" && item.phase === "final_answer" && item.delivery !== "async") { label = "codex.awaitingCompletion"; break; }
  }
  return <div className="working-indicator" role="status"><Bot size={15} /><span>{t(label)}</span><i /><i /><i /></div>;
}
