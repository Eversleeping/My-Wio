import { isExternalLink, remoteImageURL, safeImageSource, workspaceFileLink } from "./codexContent";

export type ImagePreviewData = { path: string; content: string; mime_type?: string; encoding?: string; truncated?: boolean };

export function previewImageSource(data: ImagePreviewData): string {
  if (data.encoding !== "base64" || data.truncated) return "";
  return safeImageSource(`data:${data.mime_type};base64,${data.content}`);
}

export function localImageSource(value: unknown): string {
  if (typeof value !== "string" || value.length > 4096 || /[\r\n]/.test(value) || isExternalLink(value)) return "";
  return /\.(?:jpe?g|png|gif|webp)(?:[?#].*)?$/i.test(value) ? value : "";
}

export function workspaceImagePath(source: string, root: string, filePath?: string): string {
  if (!localImageSource(source)) return "";
  const directory = filePath?.includes("/") ? filePath.slice(0, filePath.lastIndexOf("/") + 1) : "";
  const relative = !/^(?:file:|\/)/i.test(source);
  return workspaceFileLink(relative ? directory + source : source, root)?.path ?? "";
}

// Render native text/image content arrays without printing base64 image bytes.
export function imageContentText(value: unknown): string | undefined {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) {
    const parts = value.map(imageContentText);
    return parts.every(part => part !== undefined) ? parts.filter(Boolean).join("\n") : undefined;
  }
  if (!value || typeof value !== "object") return undefined;
  const block = value as Record<string, unknown>;
  if (block.type === "text" && typeof block.text === "string") return block.text;
  if (block.type === "image" || block.type === "image_url") return "";
  if (block.type === "content") return imageContentText(block.content);
  return undefined;
}

// Screenshot tools return several native content shapes: Codex image URLs,
// Claude base64 sources, and Gemini/ACP MIME-typed image blocks.
export function extractImageSources(payload: unknown): string[] {
  const sources = new Set<string>();
  const seen = new Set<object>();
  const add = (value: unknown) => {
    const source = safeImageSource(value) || remoteImageURL(value) || localImageSource(value);
    if (source && sources.size < 8) sources.add(source);
  };
  const visit = (value: unknown) => {
    if (typeof value === "string") { add(value); return; }
    if (!value || typeof value !== "object" || seen.has(value) || sources.size >= 8) return;
    seen.add(value);
    if (Array.isArray(value)) { value.forEach(visit); return; }
    const record = value as Record<string, unknown>;
    const type = String(record.type ?? "").toLowerCase();
    const image = ["image", "input_image", "output_image", "image_url", "local_image", "imageview", "image_view"].includes(type);
    if (image) {
      const mime = record.mimeType ?? record.mime_type ?? record.media_type;
      if (typeof mime === "string" && typeof record.data === "string") add(`data:${mime};base64,${record.data}`);
      for (const key of ["url", "path", "file_path", "data_url", "image_url", "data"]) {
        const candidate = record[key];
        add(candidate && typeof candidate === "object" ? (candidate as Record<string, unknown>).url : candidate);
      }
      // Claude's image.source has type=base64 rather than type=image.
      const source = record.source as Record<string, unknown> | undefined;
      if (source?.type === "base64" && typeof source.media_type === "string" && typeof source.data === "string") add(`data:${source.media_type};base64,${source.data}`);
    }
    for (const [key, child] of Object.entries(record)) {
      if (key === "data_url" || key === "image_url") visit(child);
      else if (child && typeof child === "object") visit(child);
    }
  };
  visit(payload);
  return [...sources];
}
