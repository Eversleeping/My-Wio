import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useI18n } from "../i18n";
import { Dialog } from "./Dialog";

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
      {sources.map((source, index) => <button type="button" key={`${source.slice(0, 80)}:${index}`} title={t("codex.openImage")} onClick={() => setActive(source)}><img src={source} alt={t("codex.messageImage")} loading="lazy" referrerPolicy="no-referrer" /></button>)}
    </div>
    {active && createPortal(<Dialog open title={t("codex.messageImage")} closeLabel={t("common.close")} className="image-preview-dialog" onClose={() => setActive("")}>
      <div className="image-preview-body"><img src={active} alt={t("codex.messageImage")} referrerPolicy="no-referrer" /></div>
    </Dialog>, document.body)}
  </>;
}
