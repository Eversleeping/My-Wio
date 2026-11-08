export type FilePreviewSelection = { path: string; line?: number; mode?: "file" | "diff" };

export function workspaceFileLink(href: string | undefined, workspaceRoot: string): FilePreviewSelection | null {
  if (!href || href.startsWith("#") || isExternalLink(href)) return null;
  let value = href;
  try { value = decodeURIComponent(value); } catch { return null; }
  value = value.replace(/^file:\/\//i, "");
  let line: number | undefined;
  const hashIndex = value.indexOf("#");
  if (hashIndex >= 0) {
    const match = value.slice(hashIndex).match(/^#L?(\d+)/i);
    if (match) line = Number(match[1]);
    value = value.slice(0, hashIndex);
  }
  value = value.split("?", 1)[0];
  const lineMatch = value.match(/:(\d+)(?::\d+)?$/);
  if (lineMatch) {
    line = Number(lineMatch[1]);
    value = value.slice(0, lineMatch.index);
  }
  const root = workspaceRoot.replaceAll("\\", "/").replace(/\/$/, "");
  value = value.replaceAll("\\", "/");
  if (value.startsWith(root + "/")) value = value.slice(root.length + 1);
  else if (value.startsWith("/")) return null;
  const parts: string[] = [];
  for (const part of value.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      if (parts.length === 0) return null;
      parts.pop();
    } else parts.push(part);
  }
  return parts.length > 0 ? { path: parts.join("/"), line } : null;
}

export function isExternalLink(href: string | undefined) {
  if (!href) return false;
  if (href.startsWith("//")) return true;
  const scheme = href.match(/^([a-z][a-z0-9+.-]*):/i)?.[1]?.toLowerCase();
  return Boolean(scheme && scheme !== "file");
}

const inlineImageData = /^data:image\/(?:png|jpeg|webp|gif);base64,[a-z0-9+/=\s]+$/i;

function imageURL(source: string) {
  try {
    return new URL(source);
  } catch {
    return null;
  }
}

function isWebURL(url: URL) {
  return url.protocol === "https:" || url.protocol === "http:";
}

/**
 * Returns an image source that may load without user action: embedded raster
 * data, local blobs, or this origin. Remote URLs are excluded because loading
 * them from model output would leak data to the remote host.
 */
export function safeImageSource(value: unknown): string {
  if (typeof value !== "string") return "";
  const source = value.trim();
  if (inlineImageData.test(source)) return source;
  const url = imageURL(source);
  if (!url) return "";
  if (url.protocol === "blob:") return url.href;
  return isWebURL(url) && url.origin === location.origin ? url.href : "";
}

/** Returns a remote image URL that may only be opened on request, never loaded inline. */
export function remoteImageURL(value: unknown): string {
  if (typeof value !== "string") return "";
  const url = imageURL(value.trim());
  return url && isWebURL(url) && url.origin !== location.origin ? url.href : "";
}
