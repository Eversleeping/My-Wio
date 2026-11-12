import { Ban, ChevronRight, CircleCheck, CircleHelp, CircleX, FilePenLine, FileText, LoaderCircle, Search, SquareTerminal, Wrench } from "lucide-react";
import { useI18n } from "../i18n";
import { claudeToolPresentation, type ClaudeToolItem } from "../claudeContent";
import { formatDate, formatTime } from "../format";
import { MessageImages } from "./MessageImages";

export function ClaudeToolGroup({ tools, activeOperationID }: { tools: ClaudeToolItem[]; activeOperationID?: string }) {
  const { t } = useI18n();
  return <details className="command-event-group gemini-tool-group">
    <summary><SquareTerminal size={16} /><strong>{t("codex.commandsRun", { count: tools.length })}</strong><ChevronRight className="command-group-chevron" size={16} /></summary>
    <div className="command-event-group-items">{tools.map(tool => {
      const display = claudeToolPresentation(tool);
      const Icon = display.kind === "shell" ? SquareTerminal : display.kind === "read" ? FileText : display.kind === "edit" ? FilePenLine : display.kind === "search" ? Search : Wrench;
      const status = tool.status === "running" ? activeOperationID && tool.operationID === activeOperationID ? "running" : "unknown" : tool.status ?? "unknown";
      const StatusIcon = status === "completed" ? CircleCheck : status === "failed" ? CircleX : status === "interrupted" ? Ban : status === "running" ? LoaderCircle : CircleHelp;
      return <details className={`gemini-tool claude-tool${status === "failed" ? " failed" : ""}`} key={tool.id}>
        <summary><ChevronRight className="gemini-tool-chevron" size={14} /><Icon size={15} /><span className="gemini-tool-title" title={display.title}>{display.title || t("claude.toolResult")}</span><span className={`gemini-tool-status ${status}`}><StatusIcon size={13} className={status === "running" ? "spin" : undefined} />{t(`status.${status}`)}</span>{tool.occurredAt && <time dateTime={tool.occurredAt} title={formatDate(tool.occurredAt)}>{formatTime(tool.occurredAt)}</time>}</summary>
        {display.input !== undefined && <section><strong>{t("gemini.toolInput")}</strong><pre>{display.input}</pre></section>}
        {display.output !== undefined && <section><strong>{t("gemini.toolOutput")}</strong><pre>{display.output || t("claude.toolOutputEmpty")}</pre></section>}
        {!!tool.images?.length && <MessageImages sources={tool.images} />}
      </details>;
    })}</div>
  </details>;
}
