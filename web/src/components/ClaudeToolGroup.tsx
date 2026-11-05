import { ChevronRight, SquareTerminal } from "lucide-react";
import { useI18n } from "../i18n";
import type { ClaudeToolItem } from "../claudeContent";

export function ClaudeToolGroup({ tools }: { tools: ClaudeToolItem[] }) {
  const { t } = useI18n();
  return <details className="command-event-group gemini-tool-group">
    <summary><SquareTerminal size={16} /><strong>{t("codex.commandsRun", { count: tools.length })}</strong><ChevronRight className="command-group-chevron" size={16} /></summary>
    <div className="command-event-group-items">{tools.map(tool => <details className={`gemini-tool${tool.failed ? " failed" : ""}`} key={tool.id}>
      <summary><ChevronRight className="gemini-tool-chevron" size={14} /><span className="gemini-tool-title">{tool.name ? `${t("claude.tool")}: ${tool.name}` : t("claude.toolResult")}</span>{tool.failed && <span className="gemini-tool-status failed">{t("status.failed")}</span>}</summary>
      {tool.input !== undefined && <section><strong>{t("gemini.toolInput")}</strong><pre>{tool.input}</pre></section>}
      {tool.output !== undefined && <section><strong>{t("gemini.toolOutput")}</strong><pre>{tool.output}</pre></section>}
    </details>)}</div>
  </details>;
}
