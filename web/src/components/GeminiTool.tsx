import { ChevronRight, CircleCheck, CircleX, FilePenLine, FileText, LoaderCircle, Search, SquareTerminal, Wrench } from "lucide-react";
import type { GeminiMessage } from "../geminiContent";
import { useI18n } from "../i18n";

export function GeminiTool({ item }: { item: GeminiMessage }) {
  const { t } = useI18n();
  const name = item.toolName ?? "";
  const Icon = /^(read_file|read_many_files|list_directory)(?:__|$)/.test(name) ? FileText
    : /^(grep_search|search_file_content|glob|google_web_search)(?:__|$)/.test(name) ? Search
    : /^(write_file|replace|edit_file)(?:__|$)/.test(name) ? FilePenLine
    : /^run_shell_command(?:__|$)/.test(name) ? SquareTerminal : Wrench;
  const failed = ["failed", "error"].includes(item.toolStatus ?? "");
  const complete = ["completed", "success", "succeeded"].includes(item.toolStatus ?? "");
  const status = failed ? "failed" : complete ? "completed" : "running";
  const StatusIcon = failed ? CircleX : complete ? CircleCheck : LoaderCircle;
  return <details className={`gemini-tool ${failed ? "failed" : ""}`}>
    <summary><ChevronRight className="gemini-tool-chevron" size={14} /><Icon size={15} /><span className="gemini-tool-title">{item.title || t("gemini.tool")}</span><span className={`gemini-tool-status ${status}`}><StatusIcon size={13} className={status === "running" ? "spin" : undefined} />{t(`status.${status}`)}</span></summary>
    {item.toolInput && <section><strong>{t("gemini.toolInput")}</strong><pre>{item.toolInput}</pre></section>}
    {item.toolOutput !== undefined && <section><strong>{t("gemini.toolOutput")}</strong><pre>{item.toolOutput || t("codex.noCommandOutput")}</pre></section>}
  </details>;
}
