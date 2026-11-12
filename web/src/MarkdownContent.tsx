import { createContext, useContext } from "react";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { isExternalLink, remoteImageURL, safeImageSource, workspaceFileLink, type FilePreviewSelection } from "./codexContent";
import { localImageSource } from "./imageContent";
import { MessageImage } from "./components/MessageImages";
import { useI18n } from "./i18n";

const MarkdownImageContext = createContext<{ workspaceRoot: string; filePath?: string }>({ workspaceRoot: "" });
// Keep the renderer's identity stable. Conversation updates and virtual-list
// measurements must not remount thumbnails or close an open image dialog.
function MarkdownImage({ src, alt }: { src?: string; alt?: string }) {
  const { workspaceRoot, filePath } = useContext(MarkdownImageContext);
  return src ? <span className="markdown-image"><MessageImage source={src} alt={alt} workspaceRoot={workspaceRoot} filePath={filePath} /></span> : null;
}

export default function MarkdownContent({ text, workspaceRoot, onOpenFile, filePath }: { text: string; workspaceRoot: string; onOpenFile?: (selection: FilePreviewSelection) => void; filePath?: string }) {
  const { t } = useI18n();
  return <MarkdownImageContext.Provider value={{ workspaceRoot, filePath }}><div className="message-content markdown-content"><ReactMarkdown remarkPlugins={[remarkGfm]} urlTransform={(url, key) => key === "src" ? safeImageSource(url) || remoteImageURL(url) || localImageSource(url) : defaultUrlTransform(url)} components={{
    a: ({ href, children, node: _node, ...props }) => {
      const directory = filePath?.includes("/") ? filePath.slice(0, filePath.lastIndexOf("/") + 1) : "";
      const relative = href && !/^(?:[a-z][a-z0-9+.-]*:|\/|#)/i.test(href);
      const selection = workspaceFileLink(relative ? directory + href : href, workspaceRoot);
      if (selection && onOpenFile) return <a {...props} href={href} onClick={event => { event.preventDefault(); onOpenFile(selection); }}>{children}</a>;
      if (isExternalLink(href)) return <a {...props} href={href} target="_blank" rel="noreferrer">{children}</a>;
      if (href?.startsWith("#")) return <a {...props} href={href}>{children}</a>;
      return <a {...props} className="unavailable-link" href={href} aria-disabled="true" title={t("codex.linkUnavailable")} onClick={event => event.preventDefault()}>{children}</a>;
    },
    img: MarkdownImage
  }}>{text}</ReactMarkdown></div></MarkdownImageContext.Provider>;
}
