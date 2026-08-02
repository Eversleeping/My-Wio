import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { isExternalLink, safeImageSource, workspaceFileLink, type FilePreviewSelection } from "./codexContent";
import { useI18n } from "./i18n";

export default function MarkdownContent({ text, workspaceRoot, onOpenFile }: { text: string; workspaceRoot: string; onOpenFile: (selection: FilePreviewSelection) => void }) {
  const { t } = useI18n();
  return <div className="message-content markdown-content"><ReactMarkdown remarkPlugins={[remarkGfm]} urlTransform={(url, key) => key === "src" ? safeImageSource(url) : defaultUrlTransform(url)} components={{
    a: ({ href, children, node: _node, ...props }) => { const selection = workspaceFileLink(href, workspaceRoot); if (selection) return <a {...props} href={href} onClick={event => { event.preventDefault(); onOpenFile(selection); }}>{children}</a>; if (isExternalLink(href)) return <a {...props} href={href} target="_blank" rel="noreferrer">{children}</a>; if (href?.startsWith("#")) return <a {...props} href={href}>{children}</a>; return <a {...props} className="unavailable-link" href={href} aria-disabled="true" title={t("codex.linkUnavailable")} onClick={event => event.preventDefault()}>{children}</a>; },
    img: ({ src, alt }) => { const source = safeImageSource(src); return source ? <a className="markdown-image" href={source} target="_blank" rel="noreferrer" title={t("codex.openImage")}><img src={source} alt={alt || t("codex.messageImage")} loading="lazy" referrerPolicy="no-referrer" /></a> : null; }
  }}>{text}</ReactMarkdown></div>;
}
