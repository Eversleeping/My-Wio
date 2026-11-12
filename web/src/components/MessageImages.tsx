import { useContext } from "react";
import { ExternalLink, Image as ImageIcon } from "lucide-react";
import { remoteImageURL, safeImageSource } from "../codexContent";
import { useI18n } from "../i18n";
import { localImageSource, workspaceImagePath } from "../imageContent";
import { ImageThumbnail } from "./ImageViewer";
import { WorkspaceImage, WorkspaceImageContext } from "./WorkspaceImage";

/** Placeholder for a remote image: shows the host and opens the URL in a new tab, never loading it inline. */
export function RemoteImageLink({ href, label }: { href: string; label?: string }) {
  const { t } = useI18n();
  const host = new URL(href).host;
  return <a className="remote-image-link" href={href} target="_blank" rel="noopener noreferrer" title={t("codex.openRemoteImage", { host })}><ImageIcon size={16} /><span><strong>{label || t("codex.remoteImage")}</strong><small>{host}</small></span><ExternalLink size={13} /></a>;
}

export function MessageImage({ source: value, alt, workspaceRoot, filePath }: { source: string; alt?: string; workspaceRoot?: string; filePath?: string }) {
  const { t } = useI18n();
  const context = useContext(WorkspaceImageContext);
  const label = alt || t("codex.messageImage");
  const source = safeImageSource(value);
  if (source) return <ImageThumbnail source={source} alt={label} />;
  const remote = remoteImageURL(value);
  if (remote) return <RemoteImageLink href={remote} label={alt} />;
  const path = workspaceImagePath(value, workspaceRoot ?? context.workspaceRoot, filePath);
  if (path) return <WorkspaceImage key={`${context.workspaceID}:${path}`} path={path} alt={label} />;
  return localImageSource(value) ? <span className="workspace-image-error">{label}: {t("codex.linkUnavailable")}</span> : null;
}

export function MessageImages({ sources }: { sources: string[] }) {
  return <div className={`message-images ${sources.length === 1 ? "single" : ""}`}>
    {sources.map((source, index) => <MessageImage key={`${source.slice(0, 80)}:${index}`} source={source} />)}
  </div>;
}
