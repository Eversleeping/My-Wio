import { AlertTriangle, Ban, LoaderCircle, Minimize2 } from "lucide-react";
import type { ClaudeDisplayItem } from "../claudeContent";
import { formatDate, formatTime } from "../format";
import { useI18n } from "../i18n";

export function ClaudeCompactionEvent({ item, activeOperationID }: { item: ClaudeDisplayItem; activeOperationID?: string }) {
  const { t, language } = useI18n();
  const compaction = item.compaction!;
  const status = compaction.status === "running" && (!activeOperationID || item.operationID !== activeOperationID) ? "unknown" : compaction.status;
  const title = status === "running" ? "codex.compacting" : status === "completed" ? compaction.trigger === "auto" ? "claude.compactedAuto" : compaction.trigger === "manual" ? "claude.compactedManual" : "codex.compacted" : status === "failed" ? "claude.compactionFailed" : status === "interrupted" ? "claude.compactionInterrupted" : "claude.compactionUnknown";
  const detail = status === "running" ? "claude.compactingDetail" : status === "completed" ? "codex.compactedDetail" : "claude.compactionUnconfirmedDetail";
  const Icon = status === "running" ? LoaderCircle : status === "completed" ? Minimize2 : status === "interrupted" ? Ban : AlertTriangle;
  const count = (value: number) => value.toLocaleString(language);
  return <article className={`context-compaction-event claude-compaction-event ${status}`} role={status === "running" ? "status" : undefined}>
    <header><Icon size={15} className={status === "running" ? "spin" : undefined} /><strong>{t(title)}</strong>{item.occurredAt && <time dateTime={item.occurredAt} title={formatDate(item.occurredAt)}>{formatTime(item.occurredAt)}</time>}</header>
    <div className="message-content">{t(detail)}</div>
    {status === "completed" && (compaction.preTokens !== undefined || compaction.postTokens !== undefined) && <div className="claude-compaction-tokens">
      {compaction.preTokens !== undefined && <span>{t("claude.compactionBefore", { value: count(compaction.preTokens) })}</span>}
      {compaction.postTokens !== undefined && <span>{t("claude.compactionAfter", { value: count(compaction.postTokens) })}</span>}
    </div>}
  </article>;
}
