import { DialogActions } from "../components/Dialog";
import { Dialog,Field } from "../components/FormUI";
import { useI18n } from "../i18n";

export function RenameSessionDialog({ open, busy, title, setTitle, onClose, onSave, autoFocus = false }: { open: boolean; busy: boolean; title: string; setTitle: (value: string) => void; onClose: () => void; onSave: () => void; autoFocus?: boolean }) {
  const { t } = useI18n();
  return <Dialog open={open} title={t("gemini.rename")} onClose={() => { if (!busy) onClose(); }}><form onSubmit={event => { event.preventDefault(); onSave(); }}><Field label={t("gemini.title")}><input autoFocus={autoFocus} required maxLength={180} value={title} onChange={event => setTitle(event.target.value)} /></Field><DialogActions><button className="primary-button" disabled={busy || !title.trim()}>{t("common.save")}</button></DialogActions></form></Dialog>;
}
