import { createContext, useContext, useEffect, useRef, useState } from "react";
import { AlertTriangle, LoaderCircle } from "lucide-react";
import { api } from "../api";
import { previewImageSource, type ImagePreviewData } from "../imageContent";
import { useI18n } from "../i18n";
import { ImageThumbnail } from "./ImageViewer";

export const WorkspaceImageContext = createContext({ workspaceID: "", workspaceRoot: "" });

// Share only in-flight reads. Settled images live with their thumbnail so a
// later screenshot at the same path can be read again and memory stays bounded.
const pending = new Map<string, Promise<string>>();
async function readImage(workspaceID: string, path: string): Promise<string> {
  const key = JSON.stringify([workspaceID, path]);
  const existing = pending.get(key);
  if (existing) return existing;
  const request = (async () => {
    const signal = AbortSignal.timeout(20_000);
    const queued = await api<{ operation_id: string }>(`/workspaces/${workspaceID}/image-preview`, { method: "POST", body: JSON.stringify({ path }), signal });
    const deadline = Date.now() + 20_000;
    for (let attempt = 0; ; attempt++) {
      const result = await api<{ status: string; result: string; result_data: string }>(`/operations/${encodeURIComponent(queued.operation_id)}`, { signal });
      if (result.status === "succeeded") {
        const data = JSON.parse(result.result_data) as ImagePreviewData;
        const source = data.path === path ? previewImageSource(data) : "";
        if (!source) throw new Error("invalid_image_preview");
        return source;
      }
      if (["failed", "cancelled"].includes(result.status)) throw new Error(result.result);
      if (Date.now() >= deadline) throw new Error("image_preview_timed_out");
      await new Promise(resolve => setTimeout(resolve, Math.min(750 * 2 ** attempt, 3000)));
    }
  })();
  pending.set(key, request);
  try { return await request; } finally { pending.delete(key); }
}

export function WorkspaceImage({ path, alt }: { path: string; alt: string }) {
  const { t } = useI18n();
  const { workspaceID } = useContext(WorkspaceImageContext);
  const ref = useRef<HTMLSpanElement>(null);
  const [visible, setVisible] = useState(false);
  const [nonce, setNonce] = useState(0);
  const [state, setState] = useState({ source: "", error: "" });
  useEffect(() => {
    if (!ref.current || typeof IntersectionObserver === "undefined") { setVisible(true); return; }
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) { setVisible(true); observer.disconnect(); }
    }, { rootMargin: "200px" });
    observer.observe(ref.current);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (!visible || !workspaceID) return;
    let active = true;
    setState({ source: "", error: "" });
    void readImage(workspaceID, path).then(source => { if (active) setState({ source, error: "" }); }, error => { if (active) setState({ source: "", error: error instanceof Error ? error.message : "invalid_image_preview" }); });
    return () => { active = false; };
  }, [workspaceID, path, visible, nonce]);
  const error = state.error === "image preview exceeds the 4 MB limit" ? t("codex.imagePreviewTooLarge") : state.error === "image_preview_timed_out" ? t("codex.previewTimedOut") : state.error === "invalid_image_preview" ? t("codex.imageLoadFailed") : state.error;
  return <span ref={ref} className="workspace-image">
    {state.source ? <ImageThumbnail source={state.source} alt={alt} /> : error || !workspaceID ? <span className="workspace-image-error" role="status"><AlertTriangle size={16} /><span>{alt}: {error || t("codex.linkUnavailable")}</span>{workspaceID && <button type="button" className="text-button" onClick={() => setNonce(value => value + 1)}>{t("common.retry")}</button>}</span> : <span className="workspace-image-loading" role="status"><LoaderCircle size={16} className="spin" />{alt}</span>}
  </span>;
}
