import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { ExternalLink, Image as ImageIcon } from "lucide-react";
import { remoteImageURL, safeImageSource } from "../codexContent";
import { useI18n } from "../i18n";
import { Dialog } from "./Dialog";

/** Placeholder for a remote image: shows the host and opens the URL in a new tab, never loading it inline. */
export function RemoteImageLink({ href, label }: { href: string; label?: string }) {
  const { t } = useI18n();
  const host = new URL(href).host;
  return <a className="remote-image-link" href={href} target="_blank" rel="noopener noreferrer" title={t("codex.openRemoteImage", { host })}><ImageIcon size={16} /><span><strong>{label || t("codex.remoteImage")}</strong><small>{host}</small></span><ExternalLink size={13} /></a>;
}

export function MessageImages({ sources }: { sources: string[] }) {
  const { t } = useI18n();
  const [active, setActive] = useState("");
  useEffect(() => {
    if (!active) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = previous; };
  }, [active]);
  return <>
    <div className={`message-images ${sources.length === 1 ? "single" : ""}`}>
      {sources.map((value, index) => {
        const key = `${value.slice(0, 80)}:${index}`;
        const source = safeImageSource(value);
        if (source) return <button type="button" key={key} title={t("codex.openImage")} onClick={() => setActive(source)}><img src={source} alt={t("codex.messageImage")} loading="lazy" referrerPolicy="no-referrer" /></button>;
        const remote = remoteImageURL(value);
        return remote ? <RemoteImageLink key={key} href={remote} /> : null;
      })}
    </div>
    {active && createPortal(<Dialog open title={t("codex.messageImage")} closeLabel={t("common.close")} className="image-preview-dialog" onClose={() => setActive("")}>
      <div className="image-preview-body"><img src={active} alt={t("codex.messageImage")} referrerPolicy="no-referrer" /></div>
    </Dialog>, document.body)}
  </>;
}
