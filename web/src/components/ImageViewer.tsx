import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { AlertTriangle, Maximize2, Minimize2 } from "lucide-react";
import { useI18n } from "../i18n";
import { Dialog } from "./Dialog";

export function ImageThumbnail({ source, alt }: { source: string; alt: string }) {
  const { t } = useI18n();
  const [active, setActive] = useState(false);
  const [failed, setFailed] = useState(false);
  useEffect(() => { setFailed(false); setActive(false); }, [source]);
  return <>
    {failed ? <span className="workspace-image-error" role="status"><AlertTriangle size={16} />{alt}: {t("codex.imageLoadFailed")}</span> : <button type="button" className="message-image" title={t("codex.openImage")} onClick={() => setActive(true)}><img src={source} alt={alt} loading="lazy" referrerPolicy="no-referrer" onError={() => setFailed(true)} /></button>}
    {active && <ImagePreviewDialog src={source} alt={alt} onClose={() => setActive(false)} />}
  </>;
}

export function ImageViewer({ src, alt }: { src: string; alt: string }) {
  const { t } = useI18n();
  const [original, setOriginal] = useState(false);
  const [failed, setFailed] = useState(false);
  const [dimensions, setDimensions] = useState("");
  useEffect(() => { setOriginal(false); setFailed(false); setDimensions(""); }, [src]);
  return <div className="image-viewer">
    <div className="image-viewer-toolbar"><button type="button" className="secondary-button small" aria-pressed={original} onClick={() => setOriginal(value => !value)}>{original ? <Minimize2 size={14} /> : <Maximize2 size={14} />}{t(original ? "codex.fitImage" : "codex.originalImageSize")}</button>{dimensions && <small>{dimensions}</small>}</div>
    <div className={`image-viewer-viewport ${original ? "original" : "fit"}`}>
      {failed || !src ? <div className="file-preview-error" role="alert"><AlertTriangle size={22} /><span>{t("codex.imageLoadFailed")}</span></div> : <img src={src} alt={alt} referrerPolicy="no-referrer" onLoad={event => setDimensions(`${event.currentTarget.naturalWidth} × ${event.currentTarget.naturalHeight}`)} onError={() => setFailed(true)} />}
    </div>
  </div>;
}

export function ImagePreviewDialog({ src, alt, onClose }: { src: string; alt: string; onClose: () => void }) {
  const { t } = useI18n();
  useEffect(() => {
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = previous; };
  }, []);
  return createPortal(<Dialog open title={alt} closeLabel={t("common.close")} className="image-preview-dialog" onClose={onClose}>
    <ImageViewer src={src} alt={alt} />
  </Dialog>, document.body);
}
