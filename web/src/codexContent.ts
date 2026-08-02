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

export function safeImageSource(value: unknown): string {
  if (typeof value !== "string") return "";
  const source = value.trim();
  if (/^data:image\/(?:png|jpeg|webp|gif);base64,[a-z0-9+/=\s]+$/i.test(source)) return source;
  try {
    const url = new URL(source);
    return url.protocol === "https:" || url.protocol === "http:" ? url.toString() : "";
  } catch {
    return "";
  }
}
